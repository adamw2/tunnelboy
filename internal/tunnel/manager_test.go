package tunnel

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestWaitForListener(t *testing.T) {
	session := &SSMSession{done: make(chan struct{})}

	// Listener that comes up after a short delay, like the plugin does
	port, err := FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return
		}
		defer l.Close()
		conn, _ := l.Accept()
		if conn != nil {
			conn.Close()
		}
	}()

	if err := waitForListener(context.Background(), session, port, 5*time.Second); err != nil {
		t.Errorf("expected listener to become ready: %v", err)
	}
}

func TestWaitForListenerTimeout(t *testing.T) {
	session := &SSMSession{done: make(chan struct{})}
	port, err := FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForListener(context.Background(), session, port, 700*time.Millisecond); err == nil {
		t.Error("expected timeout error for port that never opens")
	}
}

func TestWaitForListenerSessionDied(t *testing.T) {
	session := &SSMSession{done: make(chan struct{})}
	close(session.done)
	port, err := FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForListener(context.Background(), session, port, 5*time.Second); err == nil {
		t.Error("expected error when session is already dead")
	}
}
