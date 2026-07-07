// Package state persists per-tunnel state files under ~/.tunnelboy/tunnels so
// `tunnelboy tunnels` and `tunnelboy disconnect` can see and control tunnels
// owned by other processes (foreground or detached). One JSON file per tunnel,
// written by the owning process after the tunnel is ready and removed on close.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// TunnelState is the on-disk record for one live tunnel.
type TunnelState struct {
	ID         string    `json:"id"`
	PID        int       `json:"pid"`
	Type       string    `json:"type"`
	Engine     string    `json:"engine,omitempty"` // rds/elasticache engine (postgres, mysql, redis...)
	Target     string    `json:"target"`           // human-readable (RDS identifier, domain, instance ID...)
	LocalPort  int       `json:"local_port"`
	RemoteHost string    `json:"remote_host,omitempty"`
	RemotePort int       `json:"remote_port,omitempty"`
	JumpHost   string    `json:"jump_host,omitempty"`
	Profile    string    `json:"profile,omitempty"`
	Detached   bool      `json:"detached"`
	Status     string    `json:"status,omitempty"` // active, reconnecting, disconnected
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	LogFile    string    `json:"log_file,omitempty"`
}

// Dir returns the state directory, creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".tunnelboy", "tunnels")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LogDir returns the log directory for detached tunnels, creating it if needed.
func LogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".tunnelboy", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func statePath(dir, id string) string {
	// IDs are generated internally (type-port) but sanitize anyway.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(dir, safe+".json")
}

// Write persists a tunnel state file.
func Write(st TunnelState) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(dir, st.ID), data, 0o600)
}

// Remove deletes a tunnel state file. Missing files are not an error.
func Remove(id string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	err = os.Remove(statePath(dir, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Get returns the state for one tunnel ID.
func Get(id string) (*TunnelState, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(statePath(dir, id))
	if err != nil {
		return nil, err
	}
	var st TunnelState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// List returns all live tunnel states, silently removing records whose owning
// process is gone (e.g. kill -9 skipped cleanup).
func List() ([]TunnelState, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []TunnelState
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- path is a directory entry of our own 0700 state dir
		if err != nil {
			continue
		}
		var st TunnelState
		if err := json.Unmarshal(data, &st); err != nil {
			_ = os.Remove(path) // corrupt record
			continue
		}
		if !IsAlive(st.PID) {
			_ = os.Remove(path) // stale record
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// IsAlive reports whether a process with the given PID exists.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// Signal sends sig to the tunnel's owning process.
func Signal(st *TunnelState, sig syscall.Signal) error {
	if st.PID <= 0 {
		return fmt.Errorf("tunnel %s has no PID", st.ID)
	}
	return syscall.Kill(st.PID, sig)
}
