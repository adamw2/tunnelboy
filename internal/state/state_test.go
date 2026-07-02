package state

import (
	"os"
	"testing"
	"time"
)

func TestWriteListRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := TunnelState{
		ID:        "rds-3306",
		PID:       os.Getpid(), // definitely alive
		Type:      "rds",
		Target:    "my-db",
		LocalPort: 3306,
		StartedAt: time.Now(),
	}
	if err := Write(st); err != nil {
		t.Fatal(err)
	}

	got, err := Get("rds-3306")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "my-db" || got.LocalPort != 3306 {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(list))
	}

	if err := Remove("rds-3306"); err != nil {
		t.Fatal(err)
	}
	list, _ = List()
	if len(list) != 0 {
		t.Errorf("expected empty list after remove, got %d", len(list))
	}

	// Removing again is not an error
	if err := Remove("rds-3306"); err != nil {
		t.Errorf("double remove should be nil: %v", err)
	}
}

func TestListCleansStaleRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dead := TunnelState{ID: "ec2-9999", PID: 99999999, Type: "ec2", StartedAt: time.Now()}
	if err := Write(dead); err != nil {
		t.Fatal(err)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("dead-PID record should be cleaned, got %d entries", len(list))
	}
	if _, err := Get("ec2-9999"); err == nil {
		t.Error("stale file should have been removed from disk")
	}
}

func TestIsAlive(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Error("own PID should be alive")
	}
	if IsAlive(0) || IsAlive(-1) || IsAlive(99999999) {
		t.Error("bogus PIDs should not be alive")
	}
}
