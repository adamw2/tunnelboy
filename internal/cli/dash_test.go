package cli

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/viper"

	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

func TestConnectionString(t *testing.T) {
	cases := []struct {
		name string
		st   state.TunnelState
		want string
	}{
		{"rds postgres", state.TunnelState{Type: "rds", Engine: "aurora-postgresql", LocalPort: 15432},
			"postgresql://localhost:15432/"},
		{"rds mysql", state.TunnelState{Type: "rds", Engine: "mysql", LocalPort: 13306},
			"mysql://localhost:13306/"},
		{"rds unknown engine (pre-engine state file)", state.TunnelState{Type: "rds", LocalPort: 15432},
			"localhost:15432"},
		{"opensearch", state.TunnelState{Type: "opensearch", LocalPort: 9250},
			"http://localhost:9250"},
		{"elasticache redis", state.TunnelState{Type: "elasticache", Engine: "redis", LocalPort: 16379},
			"redis://localhost:16379"},
		{"elasticache valkey", state.TunnelState{Type: "elasticache", Engine: "valkey", LocalPort: 16379},
			"redis://localhost:16379"},
		{"elasticache memcached", state.TunnelState{Type: "elasticache", Engine: "memcached", LocalPort: 11211},
			"localhost:11211"},
		{"docdb", state.TunnelState{Type: "docdb", LocalPort: 27017},
			"mongodb://localhost:27017/?tls=true&tlsAllowInvalidHostnames=true&directConnection=true&retryWrites=false"},
		{"ec2", state.TunnelState{Type: "ec2", LocalPort: 12222},
			"localhost:12222"},
		{"msk", state.TunnelState{Type: "msk", LocalPort: 19092},
			"localhost:19092"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := connectionString(c.st); got != c.want {
				t.Errorf("connectionString(%+v) = %q, want %q", c.st, got, c.want)
			}
		})
	}
}

func TestPresetDBUser(t *testing.T) {
	cfg := &config.Config{Connections: map[string]config.Connection{
		"prod":   {Type: "rds", Identifier: "prod-db", DBUser: "readonly"},
		"nouser": {Type: "rds", Identifier: "staging-db"},
	}}

	if got := presetDBUser(cfg, "prod-db"); got != "readonly" {
		t.Errorf("matching preset: got %q, want %q", got, "readonly")
	}
	if got := presetDBUser(cfg, "staging-db"); got != "" {
		t.Errorf("preset without db_user: got %q, want empty", got)
	}
	if got := presetDBUser(cfg, "unknown-db"); got != "" {
		t.Errorf("unmatched target: got %q, want empty", got)
	}
	if got := presetDBUser(nil, "prod-db"); got != "" {
		t.Errorf("nil config: got %q, want empty", got)
	}
}

// key builds a KeyMsg the way bubbletea delivers one, so handleKey sees the
// same msg.String() values it does at runtime.
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestDashTokenKey(t *testing.T) {
	cfg := &config.Config{Connections: map[string]config.Connection{
		"prod": {Type: "rds", Identifier: "prod-db", DBUser: "readonly"},
	}}

	t.Run("rds tunnel prefills the preset user", func(t *testing.T) {
		m := dashModel{
			cfg:      cfg,
			progress: &startProgress{},
			tunnels:  []state.TunnelState{{ID: "rds-15432", Type: "rds", Target: "prod-db"}},
		}
		got, _ := m.handleKey(key("t"))
		dm := got.(dashModel)
		if dm.mode != modeDBUserInput {
			t.Fatalf("mode = %v, want modeDBUserInput", dm.mode)
		}
		if dm.dbUserBuf != "readonly" {
			t.Errorf("dbUserBuf = %q, want %q", dm.dbUserBuf, "readonly")
		}
		if dm.tokenTunnel.ID != "rds-15432" {
			t.Errorf("tokenTunnel.ID = %q, want %q", dm.tokenTunnel.ID, "rds-15432")
		}
	})

	t.Run("non-rds tunnel is rejected", func(t *testing.T) {
		m := dashModel{
			cfg:      cfg,
			progress: &startProgress{},
			tunnels:  []state.TunnelState{{ID: "ec2-12222", Type: "ec2", Target: "i-abc"}},
		}
		got, cmd := m.handleKey(key("t"))
		dm := got.(dashModel)
		if dm.mode != modeList {
			t.Errorf("mode = %v, want modeList", dm.mode)
		}
		if cmd != nil {
			t.Error("expected no command for a non-RDS tunnel")
		}
		if dm.message == "" {
			t.Error("expected an explanatory message")
		}
	})

	t.Run("no tunnels is a no-op", func(t *testing.T) {
		m := dashModel{cfg: cfg, progress: &startProgress{}}
		got, cmd := m.handleKey(key("t"))
		if dm := got.(dashModel); dm.mode != modeList {
			t.Errorf("mode = %v, want modeList", dm.mode)
		}
		if cmd != nil {
			t.Error("expected no command with no tunnels")
		}
	})
}

func TestDashDBUserInput(t *testing.T) {
	base := dashModel{
		mode:        modeDBUserInput,
		progress:    &startProgress{},
		tokenTunnel: state.TunnelState{ID: "rds-15432", Type: "rds", Target: "prod-db"},
	}

	t.Run("typing and backspace edit the buffer", func(t *testing.T) {
		m := base
		for _, k := range []string{"a", "p", "p", "_", "1", "backspace"} {
			got, _ := m.handleKey(key(k))
			m = got.(dashModel)
		}
		if m.dbUserBuf != "app_" {
			t.Errorf("dbUserBuf = %q, want %q", m.dbUserBuf, "app_")
		}
	})

	t.Run("spaces are ignored", func(t *testing.T) {
		m := base
		got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeySpace})
		if dm := got.(dashModel); dm.dbUserBuf != "" {
			t.Errorf("dbUserBuf = %q, want empty", dm.dbUserBuf)
		}
	})

	t.Run("empty user is rejected", func(t *testing.T) {
		m := base
		got, cmd := m.handleKey(key("enter"))
		dm := got.(dashModel)
		if dm.mode != modeDBUserInput {
			t.Errorf("mode = %v, want to stay in modeDBUserInput", dm.mode)
		}
		if cmd != nil {
			t.Error("expected no command for an empty user")
		}
		if dm.message == "" {
			t.Error("expected a validation message")
		}
	})

	t.Run("enter starts generation", func(t *testing.T) {
		m := base
		m.dbUserBuf = "readonly"
		got, cmd := m.handleKey(key("enter"))
		if dm := got.(dashModel); dm.mode != modeTokenGen {
			t.Errorf("mode = %v, want modeTokenGen", dm.mode)
		}
		if cmd == nil {
			t.Error("expected a token command")
		}
	})

	t.Run("esc returns to the list", func(t *testing.T) {
		m := base
		got, _ := m.handleKey(key("esc"))
		if dm := got.(dashModel); dm.mode != modeList {
			t.Errorf("mode = %v, want modeList", dm.mode)
		}
	})
}

func TestDefaultLocalPort(t *testing.T) {
	cfg := &config.Config{DefaultLocalPorts: map[string]int{"rds": 3307}}

	t.Run("configured default wins", func(t *testing.T) {
		spec := tunnelSpec{Type: "rds", RemotePort: 5432}
		if got := defaultLocalPort(spec, cfg); got != 3307 {
			t.Errorf("got %d, want 3307", got)
		}
	})

	t.Run("falls back to remote port when unconfigured", func(t *testing.T) {
		spec := tunnelSpec{Type: "elasticache", RemotePort: 6379}
		if got := defaultLocalPort(spec, cfg); got != 6379 {
			t.Errorf("got %d, want 6379", got)
		}
	})

	t.Run("opensearch falls back to 9250, not the remote HTTPS port", func(t *testing.T) {
		spec := tunnelSpec{Type: string(tunnel.TunnelTypeOpenSearch), RemotePort: 443}
		if got := defaultLocalPort(spec, cfg); got != 9250 {
			t.Errorf("got %d, want 9250", got)
		}
	})

	t.Run("nil config still falls back", func(t *testing.T) {
		spec := tunnelSpec{Type: "rds", RemotePort: 5432}
		if got := defaultLocalPort(spec, nil); got != 5432 {
			t.Errorf("got %d, want 5432", got)
		}
	})
}

func TestDashTargetPickPrefillsLocalPort(t *testing.T) {
	cfg := &config.Config{DefaultLocalPorts: map[string]int{"rds": 3307}}

	t.Run("non-EC2 target goes straight to the local-port prompt", func(t *testing.T) {
		m := dashModel{
			mode:     modeTargetPick,
			cfg:      cfg,
			progress: &startProgress{},
			targets: []dashTarget{
				{label: "prod-db", spec: tunnelSpec{Type: "rds", Target: "prod-db", RemotePort: 5432}},
			},
		}
		got, cmd := m.handleKey(key("enter"))
		dm := got.(dashModel)
		if dm.mode != modeLocalPortInput {
			t.Fatalf("mode = %v, want modeLocalPortInput", dm.mode)
		}
		if dm.localPortBuf != "3307" {
			t.Errorf("localPortBuf = %q, want %q", dm.localPortBuf, "3307")
		}
		if cmd != nil {
			t.Error("expected no command yet — still prompting")
		}
	})

	t.Run("EC2 target asks for remote port first", func(t *testing.T) {
		m := dashModel{
			mode:     modeTargetPick,
			progress: &startProgress{},
			targets: []dashTarget{
				{label: "i-abc", needsPort: true, spec: tunnelSpec{Type: "ec2", Target: "i-abc"}},
			},
		}
		got, _ := m.handleKey(key("enter"))
		if dm := got.(dashModel); dm.mode != modePortInput {
			t.Fatalf("mode = %v, want modePortInput", dm.mode)
		}
	})
}

func TestDashLocalPortInput(t *testing.T) {
	base := dashModel{
		mode:        modeLocalPortInput,
		cfg:         &config.Config{},
		progress:    &startProgress{},
		pendingSpec: tunnelSpec{Type: "rds", Target: "prod-db", RemotePort: 5432},
	}

	t.Run("typing and backspace edit the buffer", func(t *testing.T) {
		m := base
		for _, k := range []string{"3", "3", "0", "8", "backspace"} {
			got, _ := m.handleKey(key(k))
			m = got.(dashModel)
		}
		if m.localPortBuf != "330" {
			t.Errorf("localPortBuf = %q, want %q", m.localPortBuf, "330")
		}
	})

	t.Run("non-numeric port is rejected", func(t *testing.T) {
		m := base
		m.localPortBuf = ""
		got, cmd := m.handleKey(key("enter"))
		dm := got.(dashModel)
		if dm.mode != modeLocalPortInput {
			t.Errorf("mode = %v, want to stay in modeLocalPortInput", dm.mode)
		}
		if cmd != nil {
			t.Error("expected no command for an invalid port")
		}
		if dm.message == "" {
			t.Error("expected a validation message")
		}
	})

	t.Run("a busy port is rejected with a retry", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		busyPort := l.Addr().(*net.TCPAddr).Port

		m := base
		m.localPortBuf = ""
		for _, r := range []rune(strconv.Itoa(busyPort)) {
			got, _ := m.handleKey(key(string(r)))
			m = got.(dashModel)
		}
		got, cmd := m.handleKey(key("enter"))
		dm := got.(dashModel)
		if dm.mode != modeLocalPortInput {
			t.Errorf("mode = %v, want to stay in modeLocalPortInput", dm.mode)
		}
		if cmd != nil {
			t.Error("expected no command for a busy port")
		}
		if dm.message == "" {
			t.Error("expected a validation message")
		}
	})

	t.Run("enter with a free port starts the launch", func(t *testing.T) {
		freePort, err := tunnel.FindFreePort()
		if err != nil {
			t.Fatal(err)
		}
		m := base
		m.localPortBuf = strconv.Itoa(freePort)
		got, cmd := m.handleKey(key("enter"))
		dm := got.(dashModel)
		if dm.mode != modeStarting {
			t.Errorf("mode = %v, want modeStarting", dm.mode)
		}
		if cmd == nil {
			t.Error("expected a launch command")
		}
	})

	t.Run("esc on a non-EC2 target returns to target pick", func(t *testing.T) {
		m := base
		got, _ := m.handleKey(key("esc"))
		if dm := got.(dashModel); dm.mode != modeTargetPick {
			t.Errorf("mode = %v, want modeTargetPick", dm.mode)
		}
	})

	t.Run("esc on an EC2 target returns to the remote-port prompt", func(t *testing.T) {
		m := base
		m.pendingSpec = tunnelSpec{Type: "ec2", Target: "i-abc", RemotePort: 8080}
		got, _ := m.handleKey(key("esc"))
		dm := got.(dashModel)
		if dm.mode != modePortInput {
			t.Errorf("mode = %v, want modePortInput", dm.mode)
		}
		if dm.portBuf != "8080" {
			t.Errorf("portBuf = %q, want %q", dm.portBuf, "8080")
		}
	})
}

func TestDashPortInputPrefillsLocalPort(t *testing.T) {
	cfg := &config.Config{DefaultLocalPorts: map[string]int{"ec2": 2222}}
	m := dashModel{
		mode:        modePortInput,
		cfg:         cfg,
		progress:    &startProgress{},
		pendingSpec: tunnelSpec{Type: "ec2", Target: "i-abc"},
	}
	for _, k := range []string{"8", "0", "8", "0"} {
		got, _ := m.handleKey(key(k))
		m = got.(dashModel)
	}
	got, cmd := m.handleKey(key("enter"))
	dm := got.(dashModel)
	if dm.mode != modeLocalPortInput {
		t.Fatalf("mode = %v, want modeLocalPortInput", dm.mode)
	}
	if dm.localPortBuf != "2222" {
		t.Errorf("localPortBuf = %q, want %q", dm.localPortBuf, "2222")
	}
	if dm.pendingSpec.RemotePort != 8080 {
		t.Errorf("pendingSpec.RemotePort = %d, want 8080", dm.pendingSpec.RemotePort)
	}
	if cmd != nil {
		t.Error("expected no command yet — still prompting for the local port")
	}
}

func TestTokenCmdWithoutEndpoint(t *testing.T) {
	// A pre-RemoteHost state file must fail fast rather than reach AWS.
	msg := tokenCmd(state.TunnelState{ID: "rds-15432", Type: "rds"}, "readonly")()
	done, ok := msg.(tokenDoneMsg)
	if !ok {
		t.Fatalf("got %T, want tokenDoneMsg", msg)
	}
	if done.err == nil {
		t.Error("expected an error for a tunnel with no recorded endpoint")
	}
}

func TestDashUnmanagedNavigation(t *testing.T) {
	base := dashModel{
		mode:     modeList,
		progress: &startProgress{},
		tunnels:  []state.TunnelState{{ID: "rds-15432", Type: "rds", Target: "prod-db"}},
		unmanaged: []tunnel.UnmanagedSession{
			{PID: 4242, LocalPort: 3307, Detail: "prod-db.rds.amazonaws.com:3306 (profile latest)"},
		},
	}

	// "down" from within the valid range also calls reload(), which re-reads
	// real state via state.List()/lsof — not hermetic, so it's not exercised
	// here; only the boundary (no-op) case is, since that branch never calls
	// reload().
	t.Run("down does not overrun the combined list", func(t *testing.T) {
		m := base
		m.cursor = 1
		got, _ := m.handleKey(key("down"))
		if dm := got.(dashModel); dm.cursor != 1 {
			t.Errorf("cursor = %d, want to stay at 1", dm.cursor)
		}
	})

	t.Run("d on an unmanaged row enters confirm", func(t *testing.T) {
		m := base
		m.cursor = 1
		got, _ := m.handleKey(key("d"))
		if dm := got.(dashModel); dm.mode != modeConfirm {
			t.Errorf("mode = %v, want modeConfirm", dm.mode)
		}
	})

	t.Run("confirmLabel describes the unmanaged session", func(t *testing.T) {
		m := base
		m.cursor = 1
		got := m.confirmLabel()
		want := "unmanaged session on :3307 (pid 4242)"
		if got != want {
			t.Errorf("confirmLabel() = %q, want %q", got, want)
		}
	})

	t.Run("confirming y on an unmanaged row kills it, not a tracked tunnel", func(t *testing.T) {
		m := base
		m.cursor = 1
		m.mode = modeConfirm
		got, cmd := m.handleKey(key("y"))
		dm := got.(dashModel)
		if dm.mode != modeList {
			t.Errorf("mode = %v, want modeList", dm.mode)
		}
		if cmd == nil {
			t.Fatal("expected a kill command")
		}
		msg, ok := cmd().(stopDoneMsg)
		if !ok {
			t.Fatalf("got %T, want stopDoneMsg", msg)
		}
		if msg.id != "pid 4242 (:3307)" {
			t.Errorf("stopDoneMsg.id = %q, want %q", msg.id, "pid 4242 (:3307)")
		}
	})
}

func TestKillUnmanagedSession(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to spawn test process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	// Reap it as soon as it exits, exactly as a real unmanaged session's own
	// (non-TunnelBoy) parent would — otherwise it lingers as a zombie, whose
	// PID still answers signal 0, making IsAlive report it as alive until
	// something reaps it.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	result := killUnmanagedSession(cmd.Process.Pid)
	if result != stopClean {
		t.Errorf("killUnmanagedSession() = %v, want stopClean", result)
	}
	if err := <-waited; err == nil {
		t.Error("expected the process to have been signalled")
	}
}

func TestReloadDetectsVanishedTunnel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dead := state.TunnelState{ID: "rds-9999", PID: 99999999, Type: "rds", Target: "prod-db"}
	if err := state.Write(dead); err != nil {
		t.Fatal(err)
	}

	m := &dashModel{mode: modeList, progress: &startProgress{}, tunnels: []state.TunnelState{dead}}
	m.reload()

	if len(m.tunnels) != 0 {
		t.Fatalf("expected the dead tunnel to be pruned, got %+v", m.tunnels)
	}
	if !containsAction(m.actions, "rds-9999") || !containsAction(m.actions, "vanished") {
		t.Errorf("actions = %v, want a vanish notice for rds-9999", m.actions)
	}
	if m.failureLabel != "rds-9999" {
		t.Errorf("failureLabel = %q, want %q", m.failureLabel, "rds-9999")
	}
	if len(m.failureLog) == 0 {
		t.Error("expected a failureLog entry (even a placeholder, since this tunnel had no LogFile recorded)")
	}
}

func TestReloadSkipsVanishNoticeForExpectedRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dead := state.TunnelState{ID: "rds-9998", PID: 99999999, Type: "rds", Target: "prod-db"}
	if err := state.Write(dead); err != nil {
		t.Fatal(err)
	}

	m := &dashModel{
		mode:            modeList,
		progress:        &startProgress{},
		tunnels:         []state.TunnelState{dead},
		pendingRemovals: map[string]bool{"rds-9998": true},
	}
	m.reload()

	if len(m.actions) != 0 {
		t.Errorf("actions = %v, want none — this removal was expected", m.actions)
	}
	if m.pendingRemovals["rds-9998"] {
		t.Error("pendingRemovals entry should be cleared once the removal is observed")
	}
}

func TestRecordActionCapsHistory(t *testing.T) {
	m := &dashModel{}
	for i := 0; i < dashActionHistory+2; i++ {
		m.recordAction(strconv.Itoa(i))
	}
	if len(m.actions) != dashActionHistory {
		t.Fatalf("actions len = %d, want %d", len(m.actions), dashActionHistory)
	}
	// Most recent first.
	if want := strconv.Itoa(dashActionHistory + 1); m.actions[0] != want {
		t.Errorf("actions[0] = %q, want %q", m.actions[0], want)
	}
}

func containsAction(actions []string, substr string) bool {
	for _, a := range actions {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

func TestLaunchDoneMsgWarnsWhenTunnelAlreadyGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Nothing in state — simulates the detached runner having died in the
	// instant between writing its state file and this reload.
	m := dashModel{mode: modeLaunching, progress: &startProgress{}}
	got, _ := m.Update(launchDoneMsg{name: "latestadmin", output: "tunnel ready\n"})
	dm := got.(dashModel)

	if len(dm.tunnels) != 0 {
		t.Fatalf("expected no tunnels, got %+v", dm.tunnels)
	}
	if !containsAction(dm.actions, "latestadmin") || !containsAction(dm.actions, "reported started") {
		t.Errorf("actions = %v, want a warning that latestadmin reported started but isn't active", dm.actions)
	}
}

func TestLaunchDoneMsgNoWarningWhenTunnelIsActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	live := state.TunnelState{ID: "rds-3307", PID: os.Getpid(), Type: "rds", Target: "prod-db", StartedAt: time.Now()}
	if err := state.Write(live); err != nil {
		t.Fatal(err)
	}

	m := dashModel{mode: modeLaunching, progress: &startProgress{}}
	got, _ := m.Update(launchDoneMsg{name: "latestadmin", output: "tunnel ready\n"})
	dm := got.(dashModel)

	if len(dm.tunnels) != 1 {
		t.Fatalf("expected the live tunnel to be picked up, got %+v", dm.tunnels)
	}
	if containsAction(dm.actions, "reported started") {
		t.Errorf("actions = %v, want no false-alarm warning — the tunnel is actually active", dm.actions)
	}
}

func TestStartDoneMsgWarnsWhenTunnelAlreadyGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := state.TunnelState{ID: "rds-3308", LocalPort: 3308}
	m := dashModel{mode: modeStarting, progress: &startProgress{}}
	got, _ := m.Update(startDoneMsg{st: &st})
	dm := got.(dashModel)

	if !containsAction(dm.actions, "rds-3308") || !containsAction(dm.actions, "already gone") {
		t.Errorf("actions = %v, want a warning that rds-3308 is already gone", dm.actions)
	}
}

func TestStartDoneMsgNoWarningWhenTunnelIsActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	live := state.TunnelState{ID: "rds-3308", PID: os.Getpid(), Type: "rds", Target: "prod-db", LocalPort: 3308, StartedAt: time.Now()}
	if err := state.Write(live); err != nil {
		t.Fatal(err)
	}

	m := dashModel{mode: modeStarting, progress: &startProgress{}}
	got, _ := m.Update(startDoneMsg{st: &live})
	dm := got.(dashModel)

	if containsAction(dm.actions, "already gone") {
		t.Errorf("actions = %v, want no false-alarm warning — the tunnel is actually active", dm.actions)
	}
}

func TestCurrentProfileIndex(t *testing.T) {
	orig := viper.GetString("profile")
	t.Cleanup(func() { viper.Set("profile", orig) })

	profiles := []aws.ProfileInfo{{Name: "default"}, {Name: "dev-admin"}, {Name: "latest"}}

	t.Run("matches the current profile", func(t *testing.T) {
		viper.Set("profile", "dev-admin")
		if got := currentProfileIndex(profiles); got != 1 {
			t.Errorf("currentProfileIndex() = %d, want 1", got)
		}
	})

	t.Run("empty viper profile falls back to default", func(t *testing.T) {
		viper.Set("profile", "")
		if got := currentProfileIndex(profiles); got != 0 {
			t.Errorf("currentProfileIndex() = %d, want 0 (default)", got)
		}
	})

	t.Run("unknown profile falls back to index 0", func(t *testing.T) {
		viper.Set("profile", "nonexistent")
		if got := currentProfileIndex(profiles); got != 0 {
			t.Errorf("currentProfileIndex() = %d, want 0", got)
		}
	})
}

func TestStartProfilePickOrDiscover(t *testing.T) {
	t.Run("zero or one profile skips the picker", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // no ~/.aws/config at all

		m := dashModel{progress: &startProgress{}}
		got, cmd := m.startProfilePickOrDiscover("rds")
		if got.mode != modeDiscovering {
			t.Errorf("mode = %v, want modeDiscovering", got.mode)
		}
		if got.service != "rds" {
			t.Errorf("service = %q, want %q", got.service, "rds")
		}
		if cmd == nil {
			t.Error("expected a discover command to run immediately")
		}
	})

	t.Run("multiple profiles opens the picker, sorted, with the current one pre-selected", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		awsDir := filepath.Join(home, ".aws")
		if err := os.MkdirAll(awsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfgContent := "[profile latest]\nregion = us-east-1\n\n[profile dev-admin]\nregion = us-east-1\nsso_start_url = https://example.awsapps.com/start\n"
		if err := os.WriteFile(filepath.Join(awsDir, "config"), []byte(cfgContent), 0o600); err != nil {
			t.Fatal(err)
		}

		orig := viper.GetString("profile")
		t.Cleanup(func() { viper.Set("profile", orig) })
		viper.Set("profile", "latest")

		m := dashModel{progress: &startProgress{}}
		got, cmd := m.startProfilePickOrDiscover("rds")
		if got.mode != modeProfilePick {
			t.Fatalf("mode = %v, want modeProfilePick", got.mode)
		}
		if cmd != nil {
			t.Error("expected no command yet — waiting on the picker")
		}
		if got.pendingService != "rds" {
			t.Errorf("pendingService = %q, want %q", got.pendingService, "rds")
		}
		if len(got.profiles) != 2 {
			t.Fatalf("profiles = %+v, want 2", got.profiles)
		}
		if got.profiles[0].Name != "dev-admin" || got.profiles[1].Name != "latest" {
			t.Errorf("profiles not sorted alphabetically: %+v", got.profiles)
		}
		if got.profileCursor != 1 { // "latest" sorts to index 1
			t.Errorf("profileCursor = %d, want 1 (pre-selecting the current profile)", got.profileCursor)
		}
	})
}
