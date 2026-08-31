package cli

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/state"
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
