package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// TunnelType represents the type of tunnel
type TunnelType string

const (
	TunnelTypeRDS        TunnelType = "rds"
	TunnelTypeOpenSearch TunnelType = "opensearch"
	TunnelTypeEC2        TunnelType = "ec2"
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
	session      *SSMSession
	cancel       context.CancelFunc
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

// CreateTunnel creates a new tunnel
func (m *Manager) CreateTunnel(ctx context.Context, cfg TunnelConfig) (*Tunnel, error) {
	// Auto-assign local port if not specified
	localPort := cfg.LocalPort
	if localPort == 0 {
		var err error
		localPort, err = findFreePort()
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
		cancel:     cancel,
	}

	// Start SSM session
	var session *SSMSession
	var err error

	if cfg.Direct {
		// Direct port forwarding to SSM-enabled target
		session, err = m.ssmMgr.StartPortForward(tunnelCtx, SSMPortForwardConfig{
			TargetID:  cfg.TargetID,
			LocalPort: localPort,
			RemotePort: cfg.RemotePort,
		})
	} else {
		// Port forwarding through jump host to remote host
		session, err = m.ssmMgr.StartRemotePortForward(tunnelCtx, SSMRemotePortForwardConfig{
			JumpHostID: cfg.JumpHostID,
			LocalPort:  localPort,
			RemoteHost: cfg.RemoteHost,
			RemotePort: cfg.RemotePort,
		})
	}

	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start SSM session: %w", err)
	}

	tunnel.session = session
	tunnel.Status = "active"

	// Store tunnel
	m.mu.Lock()
	m.tunnels[id] = tunnel
	m.mu.Unlock()

	// Monitor tunnel in background
	go m.monitorTunnel(tunnel)

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

	// Cancel the tunnel context
	if tunnel.cancel != nil {
		tunnel.cancel()
	}

	// Terminate SSM session
	if tunnel.session != nil {
		tunnel.session.Close()
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
		m.CloseTunnel(id)
	}
	return nil
}

// monitorTunnel monitors a tunnel for disconnection
func (m *Manager) monitorTunnel(tunnel *Tunnel) {
	if tunnel.session == nil {
		return
	}

	// Wait for session to end
	<-tunnel.session.Done()

	// Update status
	m.mu.Lock()
	if t, ok := m.tunnels[tunnel.ID]; ok {
		t.Status = "disconnected"
	}
	m.mu.Unlock()
}

// findFreePort finds an available port
func findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	
	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
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
	default:
		return fmt.Sprintf("localhost:%d", t.LocalPort)
	}
}
