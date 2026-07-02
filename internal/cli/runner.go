package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

// tunnelSpec is the fully-resolved description of a tunnel, produced by the
// parent process (which handles discovery, prompts, and port resolution) and
// executed by the detached runner child. All fields must be concrete — the
// child never prompts.
type tunnelSpec struct {
	Type       string `json:"type"` // tunnel.TunnelType
	Engine     string `json:"engine,omitempty"`
	Target     string `json:"target"` // human-readable name for state/display
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	JumpHostID string `json:"jump_host_id,omitempty"`
	Direct     bool   `json:"direct,omitempty"`
	TargetID   string `json:"target_id,omitempty"` // for direct SSM
	Profile    string `json:"profile,omitempty"`

	// OpenSearch: the SSM tunnel runs on LocalPort (internal) and the signing
	// proxy listens on ProxyPort (user-facing).
	DomainEndpoint string `json:"domain_endpoint,omitempty"`
	ProxyPort      int    `json:"proxy_port,omitempty"`

	// ECS auto-stop: task the parent started that the child must stop on close.
	AutoStopCluster string `json:"auto_stop_cluster,omitempty"`
	AutoStopTaskARN string `json:"auto_stop_task_arn,omitempty"`
}

// stateID returns the tunnel's state-file ID. For OpenSearch the user-facing
// proxy port names the tunnel; everything else matches manager's type-port ID.
func (s tunnelSpec) stateID() string {
	if s.Type == string(tunnel.TunnelTypeOpenSearch) && s.ProxyPort != 0 {
		return fmt.Sprintf("%s-%d", s.Type, s.ProxyPort)
	}
	return fmt.Sprintf("%s-%d", s.Type, s.LocalPort)
}

func (s tunnelSpec) userPort() int {
	if s.ProxyPort != 0 {
		return s.ProxyPort
	}
	return s.LocalPort
}

var tunnelRunnerCmd = &cobra.Command{
	Use:    "__tunnel-runner",
	Hidden: true,
	Short:  "Internal: run a detached tunnel from a spec on stdin",
	RunE:   runTunnelRunner,
}

func init() {
	rootCmd.AddCommand(tunnelRunnerCmd)
}

func runTunnelRunner(cmd *cobra.Command, args []string) error {
	var spec tunnelSpec
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		return fmt.Errorf("read tunnel spec: %w", err)
	}

	ctx := context.Background()
	pm := aws.NewProfileManager()
	if err := pm.LoadProfile(ctx, spec.Profile); err != nil {
		return err
	}

	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	t, err := tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       tunnel.TunnelType(spec.Type),
		Engine:     spec.Engine,
		LocalPort:  spec.LocalPort,
		RemoteHost: spec.RemoteHost,
		RemotePort: spec.RemotePort,
		JumpHostID: spec.JumpHostID,
		Direct:     spec.Direct,
		TargetID:   spec.TargetID,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	if spec.AutoStopTaskARN != "" && spec.AutoStopCluster != "" {
		discovery := aws.NewDiscovery(pm.GetConfig())
		cluster, taskARN := spec.AutoStopCluster, spec.AutoStopTaskARN
		t.AddCloseHook(func() error {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fmt.Fprintf(os.Stderr, "► Stopping ECS task in %s...\n", cluster)
			return discovery.StopTask(stopCtx, cluster, taskARN, "tunnelboy: tunnel closed")
		})
	}

	var proxy *tunnel.OpenSearchProxy
	if spec.Type == string(tunnel.TunnelTypeOpenSearch) {
		proxy, err = tunnel.NewOpenSearchProxy(tunnel.OpenSearchProxyConfig{
			AWSConfig:      pm.GetConfig(),
			Region:         pm.GetConfig().Region,
			ProfileName:    pm.GetCurrentProfile(),
			Endpoint:       fmt.Sprintf("localhost:%d", t.LocalPort),
			DomainEndpoint: spec.DomainEndpoint,
			LocalPort:      spec.ProxyPort,
			UseTunnel:      true,
		})
		if err == nil {
			err = proxy.Start(ctx)
		}
		if err != nil {
			_ = tunnelMgr.CloseAll()
			return fmt.Errorf("failed to start signing proxy: %w", err)
		}
	}

	st := state.TunnelState{
		ID:         spec.stateID(),
		PID:        os.Getpid(),
		Type:       spec.Type,
		Target:     spec.Target,
		LocalPort:  spec.userPort(),
		RemoteHost: spec.RemoteHost,
		RemotePort: spec.RemotePort,
		JumpHost:   spec.JumpHostID,
		Profile:    pm.GetCurrentProfile(),
		Detached:   true,
		StartedAt:  time.Now(),
	}
	if dir, err := state.LogDir(); err == nil {
		st.LogFile = filepath.Join(dir, st.ID+".log")
	}
	if err := state.Write(st); err != nil {
		if proxy != nil {
			_ = proxy.Stop()
		}
		_ = tunnelMgr.CloseAll()
		return fmt.Errorf("write state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "tunnel %s ready on localhost:%d (pid %d)\n", st.ID, st.LocalPort, st.PID)

	waitForInterrupt()

	_ = state.Remove(st.ID)
	if proxy != nil {
		_ = proxy.Stop()
	}
	_ = tunnelMgr.CloseAll()
	return nil
}

// detachReadyTimeout bounds how long the parent waits for the child to report
// ready. The child's own listener wait is 30s; SSO profile load and session
// start add more, so allow generous headroom.
const detachReadyTimeout = 2 * time.Minute

// spawnDetached launches the runner child for a fully-resolved spec and waits
// until its state file appears (tunnel ready) or the child dies.
func spawnDetached(spec tunnelSpec) (*state.TunnelState, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}

	id := spec.stateID()
	if existing, err := state.Get(id); err == nil && state.IsAlive(existing.PID) {
		return nil, fmt.Errorf("tunnel %s already running (pid %d)", id, existing.PID)
	}
	_ = state.Remove(id)

	logDir, err := state.LogDir()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(logDir, id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path built from sanitized internal ID under ~/.tunnelboy
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "--- %s starting %s ---\n", time.Now().Format(time.RFC3339), id)

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(exe, "__tunnel-runner") // #nosec G204 -- re-exec of our own binary
	cmd.Stdin = strings.NewReader(string(specJSON))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// New session: survives the parent's terminal closing, ignores its Ctrl+C.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn tunnel runner: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(detachReadyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return nil, fmt.Errorf("tunnel process exited before becoming ready: %s", lastLogLines(logPath, 5))
		case <-time.After(300 * time.Millisecond):
		}
		st, err := state.Get(id)
		if err == nil && st.PID == cmd.Process.Pid {
			return st, nil
		}
	}

	// Timed out: kill the child so we don't leak a half-started tunnel.
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("tunnel did not become ready within %s: %s", detachReadyTimeout, lastLogLines(logPath, 5))
}

// lastLogLines returns the tail of a log file for error messages.
func lastLogLines(path string, n int) string {
	data, err := os.ReadFile(path) // #nosec G304 -- internal log path under ~/.tunnelboy
	if err != nil {
		return "(no log available)"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
