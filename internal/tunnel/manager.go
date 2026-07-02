package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/adamw2/tunnelboy/internal/aws"
)

// TunnelType represents the type of tunnel
type TunnelType string

const (
	TunnelTypeRDS         TunnelType = "rds"
	TunnelTypeOpenSearch  TunnelType = "opensearch"
	TunnelTypeEC2         TunnelType = "ec2"
	TunnelTypeElastiCache TunnelType = "elasticache"
	TunnelTypeDocDB       TunnelType = "docdb"
	TunnelTypeMSK         TunnelType = "msk"
)

// Tunnel represents an active tunnel
type Tunnel struct {
	ID           string
	Type         TunnelType
	Engine       string // Database engine (mysql, postgres, etc.) for RDS
	LocalPort    int
	RemoteHost   string
	RemotePort   int
	JumpHost     string // Instance ID or ECS task ARN
	Direct       bool   // Direct SSM connection (no jump host)
	Status       string
	StartedAt    time.Time
	cfg          TunnelConfig
	session      *SSMSession
	cancel       context.CancelFunc
	closed       bool
	stateMu      sync.Mutex
	closeHooks   []func() error
	hooksMu      sync.Mutex
}

// AddCloseHook registers a callback to run when this tunnel is closed. Hooks
// run in registration order after the SSM session is terminated. Errors are
// logged but do not abort subsequent hooks.
func (t *Tunnel) AddCloseHook(fn func() error) {
	t.hooksMu.Lock()
	defer t.hooksMu.Unlock()
	t.closeHooks = append(t.closeHooks, fn)
}

// markClosed flags the tunnel as intentionally closed and returns its current
// session so the caller can terminate it.
func (t *Tunnel) markClosed() *SSMSession {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	t.closed = true
	return t.session
}

func (t *Tunnel) isClosed() bool {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.closed
}

func (t *Tunnel) currentSession() *SSMSession {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.session
}

func (t *Tunnel) setSession(s *SSMSession) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	t.session = s
}

// Manager manages active tunnels
type Manager struct {
	tunnels map[string]*Tunnel
	mu      sync.RWMutex
	ssmMgr  *SSMManager
}

// NewManager creates a new tunnel manager
func NewManager(ssmMgr *SSMManager) *Manager {
	return &Manager{
		tunnels: make(map[string]*Tunnel),
		ssmMgr:  ssmMgr,
	}
}

// TunnelConfig contains configuration for creating a tunnel
type TunnelConfig struct {
	Type        TunnelType
	Engine      string // Database engine (mysql, postgres, etc.) for RDS
	LocalPort   int    // 0 for auto-assign
	RemoteHost  string // For port forwarding to remote host (RDS endpoint, etc.)
	RemotePort  int
	JumpHostID  string // EC2 instance ID or ECS task ARN
	Direct      bool   // Direct SSM connection (target is SSM-enabled)
	TargetID    string // Target instance ID for direct connections
}

// startSession starts the underlying SSM session for a tunnel config on the
// given local port. Used both for initial creation and reconnects.
func (m *Manager) startSession(ctx context.Context, cfg TunnelConfig, localPort int) (*SSMSession, error) {
	if cfg.Direct {
		return m.ssmMgr.StartPortForward(ctx, SSMPortForwardConfig{
			TargetID:   cfg.TargetID,
			LocalPort:  localPort,
			RemotePort: cfg.RemotePort,
		})
	}
	return m.ssmMgr.StartRemotePortForward(ctx, SSMRemotePortForwardConfig{
		JumpHostID: cfg.JumpHostID,
		LocalPort:  localPort,
		RemoteHost: cfg.RemoteHost,
		RemotePort: cfg.RemotePort,
	})
}

// CreateTunnel creates a new tunnel
func (m *Manager) CreateTunnel(ctx context.Context, cfg TunnelConfig) (*Tunnel, error) {
	// Auto-assign local port if not specified
	localPort := cfg.LocalPort
	if localPort == 0 {
		var err error
		localPort, err = FindFreePort()
		if err != nil {
			return nil, fmt.Errorf("failed to find free port: %w", err)
		}
	}

	// Generate tunnel ID
	id := fmt.Sprintf("%s-%d", cfg.Type, localPort)

	// Create cancellable context
	tunnelCtx, cancel := context.WithCancel(ctx)

	tunnel := &Tunnel{
		ID:         id,
		Type:       cfg.Type,
		Engine:     cfg.Engine,
		LocalPort:  localPort,
		RemoteHost: cfg.RemoteHost,
		RemotePort: cfg.RemotePort,
		JumpHost:   cfg.JumpHostID,
		Direct:     cfg.Direct,
		Status:     "starting",
		StartedAt:  time.Now(),
		cfg:        cfg,
		cancel:     cancel,
	}

	session, err := m.startSession(tunnelCtx, cfg, localPort)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start SSM session: %w", err)
	}

	// The plugin binds the local listener asynchronously after the session
	// banner; anything connecting immediately (e.g. --exec) would get
	// connection refused without this.
	if err := waitForListener(tunnelCtx, session, localPort, listenerReadyTimeout); err != nil {
		_ = session.Close()
		cancel()
		return nil, fmt.Errorf("tunnel failed to become ready: %w", err)
	}

	tunnel.setSession(session)
	tunnel.Status = "active"

	// Store tunnel
	m.mu.Lock()
	m.tunnels[id] = tunnel
	m.mu.Unlock()

	// Monitor tunnel in background
	go m.monitorTunnel(tunnelCtx, tunnel)

	return tunnel, nil
}

// GetTunnel returns a tunnel by ID
func (m *Manager) GetTunnel(id string) (*Tunnel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tunnels[id]
	return t, ok
}

// ListTunnels returns all active tunnels
func (m *Manager) ListTunnels() []*Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tunnels := make([]*Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		tunnels = append(tunnels, t)
	}
	return tunnels
}

// CloseTunnel closes a specific tunnel
func (m *Manager) CloseTunnel(id string) error {
	m.mu.Lock()
	tunnel, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel %s not found", id)
	}
	delete(m.tunnels, id)
	m.mu.Unlock()

	// Mark closed first so the monitor goroutine doesn't treat the session
	// death as a failure and try to reconnect.
	session := tunnel.markClosed()

	// Cancel the tunnel context
	if tunnel.cancel != nil {
		tunnel.cancel()
	}

	// Terminate SSM session
	if session != nil {
		_ = session.Close() // process may already be gone
	}

	// Run close hooks (e.g. ECS auto_stop scaling service back to 0).
	tunnel.hooksMu.Lock()
	hooks := tunnel.closeHooks
	tunnel.closeHooks = nil
	tunnel.hooksMu.Unlock()
	for _, fn := range hooks {
		if err := fn(); err != nil {
			fmt.Fprintf(os.Stderr, "tunnel %s close hook: %v\n", id, err)
		}
	}

	tunnel.Status = "closed"
	return nil
}

// CloseAll closes all tunnels
func (m *Manager) CloseAll() error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tunnels))
	for id := range m.tunnels {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		_ = m.CloseTunnel(id) // best-effort; only fails for unknown IDs
	}
	return nil
}

const (
	maxReconnectAttempts = 5
	reconnectInitialWait = 2 * time.Second
	reconnectMaxWait     = 30 * time.Second
	listenerReadyTimeout = 30 * time.Second
)

// waitForListener polls the local port until the session-manager-plugin has
// bound its listener, failing fast if the plugin process exits first.
func waitForListener(ctx context.Context, session *SSMSession, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		select {
		case <-session.Done():
			return fmt.Errorf("SSM session ended before the local listener was ready")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("local listener on %s not ready after %s", addr, timeout)
}

// monitorTunnel watches the tunnel's SSM session and reconnects with backoff
// when it dies unexpectedly (idle timeout, max session duration, network
// change). Intentional closes and context cancellation end the monitor.
func (m *Manager) monitorTunnel(ctx context.Context, tunnel *Tunnel) {
	for {
		session := tunnel.currentSession()
		if session == nil {
			return
		}

		select {
		case <-session.Done():
		case <-ctx.Done():
			return
		}

		// Give an in-flight CloseTunnel/CloseAll (e.g. Ctrl+C, which also
		// kills the plugin process) a moment to mark the tunnel closed.
		time.Sleep(1 * time.Second)
		if tunnel.isClosed() || ctx.Err() != nil {
			return
		}

		tunnel.Status = "reconnecting"
		fmt.Fprintf(os.Stderr, "\n⚠ Tunnel %s disconnected, reconnecting...\n", tunnel.ID)

		wait := reconnectInitialWait
		reconnected := false
		for attempt := 1; attempt <= maxReconnectAttempts; attempt++ {
			newSession, err := m.startSession(ctx, tunnel.cfg, tunnel.LocalPort)
			if err == nil {
				err = waitForListener(ctx, newSession, tunnel.LocalPort, listenerReadyTimeout)
				if err != nil {
					_ = newSession.Close()
				}
			}
			if err == nil {
				tunnel.setSession(newSession)
				tunnel.Status = "active"
				fmt.Fprintf(os.Stderr, "✓ Tunnel %s reconnected (localhost:%d)\n", tunnel.ID, tunnel.LocalPort)
				reconnected = true
				break
			}

			if aws.IsCredentialError(err) {
				fmt.Fprintf(os.Stderr, "✗ Tunnel %s cannot reconnect: %s\n", tunnel.ID, aws.CredentialHint(m.ssmMgr.Profile()))
				tunnel.Status = "disconnected"
				return
			}

			if attempt < maxReconnectAttempts {
				fmt.Fprintf(os.Stderr, "  reconnect attempt %d/%d failed: %v (retrying in %s)\n",
					attempt, maxReconnectAttempts, err, wait)
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return
				}
				wait *= 2
				if wait > reconnectMaxWait {
					wait = reconnectMaxWait
				}
			} else {
				fmt.Fprintf(os.Stderr, "✗ Tunnel %s: giving up after %d reconnect attempts: %v\n",
					tunnel.ID, maxReconnectAttempts, err)
			}
		}

		if !reconnected {
			tunnel.Status = "disconnected"
			return
		}
	}
}

// FindFreePort finds an available local port
func FindFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
}

// PortAvailable reports whether a local TCP port can be bound
func PortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// ConnectionString returns a connection string for the tunnel
func (t *Tunnel) ConnectionString(dbUser string) string {
	switch t.Type {
	case TunnelTypeRDS:
		// Determine connection string based on engine
		engine := t.Engine
		if dbUser == "" {
			// Default user based on engine
			if engine == "mysql" || engine == "mariadb" {
				dbUser = "admin"
			} else {
				dbUser = "postgres"
			}
		}

		// Generate appropriate connection string
		if engine == "mysql" || engine == "mariadb" {
			return fmt.Sprintf("mysql://%s@localhost:%d", dbUser, t.LocalPort)
		} else if engine == "postgres" || engine == "aurora-postgresql" {
			return fmt.Sprintf("postgresql://%s@localhost:%d", dbUser, t.LocalPort)
		} else {
			// Generic format for other engines
			return fmt.Sprintf("%s://%s@localhost:%d", engine, dbUser, t.LocalPort)
		}
	case TunnelTypeOpenSearch:
		return fmt.Sprintf("http://localhost:%d", t.LocalPort)
	case TunnelTypeEC2:
		return fmt.Sprintf("ssh -p %d localhost", t.LocalPort)
	case TunnelTypeElastiCache:
		return fmt.Sprintf("redis://localhost:%d", t.LocalPort)
	case TunnelTypeDocDB:
		return fmt.Sprintf("mongodb://%s@localhost:%d", dbUser, t.LocalPort)
	case TunnelTypeMSK:
		return fmt.Sprintf("localhost:%d", t.LocalPort)
	default:
		return fmt.Sprintf("localhost:%d", t.LocalPort)
	}
}
