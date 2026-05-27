package cli

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

// enableECSAutoStart opts a Discovery into ECS auto-start and wires up a
// terminal-friendly progress callback that overwrites a single line until the
// task is ready.
func enableECSAutoStart(d *aws.Discovery) {
	var (
		mu       sync.Mutex
		printed  bool
		finished bool
	)
	// \r returns to column 0; \033[K clears to end of line so a shorter status
	// never leaves leftover characters from a longer previous one.
	const clearLine = "\r\033[K"
	d.EnableAutoStart(func(elapsed time.Duration, status string) {
		mu.Lock()
		defer mu.Unlock()
		if finished {
			return
		}
		if status == "READY" {
			finished = true
			prefix := ""
			if printed {
				prefix = clearLine
			}
			fmt.Fprintf(os.Stderr, "%s► ECS task ready (%s)\n", prefix, elapsed)
			return
		}
		printed = true
		fmt.Fprintf(os.Stderr, "%s► ECS auto-start: %s (%s)", clearLine, status, elapsed)
	})
}

// registerAutoStopIfECS adds a close hook that stops the ephemeral task
// tunnelboy started, when ecs_auto_stop is enabled. No-ops for non-ECS hosts or
// tasks tunnelboy didn't start (StartedTaskARN empty) — we never stop a task we
// merely discovered, only ones we launched.
func registerAutoStopIfECS(t *tunnel.Tunnel, host *aws.JumpHost, cfg *config.Config, discovery *aws.Discovery) {
	if t == nil || host == nil {
		return
	}
	if host.Type != "ecs" || host.StartedTaskARN == "" || host.ClusterName == "" {
		return
	}
	if !cfg.JumpHosts.ECSAutoStop {
		return
	}
	cluster := host.ClusterName
	taskARN := host.StartedTaskARN
	t.AddCloseHook(func() error {
		// Fresh context — the tunnel's context is already cancelled by close.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fmt.Fprintf(os.Stderr, "► Stopping ECS task in %s...\n", cluster)
		if err := discovery.StopTask(ctx, cluster, taskARN, "tunnelboy: tunnel closed"); err != nil {
			return fmt.Errorf("auto_stop: %w", err)
		}
		return nil
	})
}
