package cli

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var tunnelsCmd = &cobra.Command{
	Use:   "tunnels",
	Short: "List active tunnels",
	Long:  "Show all currently active tunnels (foreground and detached) managed by TunnelBoy",
	RunE:  runTunnels,
}

var disconnectCmd = &cobra.Command{
	Use:   "disconnect [tunnel-id]",
	Short: "Close a tunnel",
	Long:  "Close a specific tunnel or all tunnels",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDisconnect,
}

var disconnectAll bool

func init() {
	rootCmd.AddCommand(tunnelsCmd)
	rootCmd.AddCommand(disconnectCmd)

	disconnectCmd.Flags().BoolVar(&disconnectAll, "all", false, "disconnect all tunnels")
	disconnectCmd.ValidArgsFunction = completeTunnelIDs
}

func runTunnels(cmd *cobra.Command, args []string) error {
	tunnels, err := state.List()
	if err != nil {
		return fmt.Errorf("read tunnel state: %w", err)
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(tunnels)
	case "quiet":
		for _, t := range tunnels {
			fmt.Println(t.ID)
		}
		return nil
	}

	fmt.Println(tui.TitleStyle.Render("Active Tunnels"))
	if len(tunnels) == 0 {
		fmt.Println(tui.DimStyle.Render("No active tunnels. Start one with: tunnelboy connect <type> --detach"))
		return nil
	}
	fmt.Println()

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"ID", "Type", "Target", "Local Port", "Profile", "Mode", "Status", "Uptime"})
	table.SetBorder(false)
	setGreenTableColors(table, 8)

	for _, t := range tunnels {
		mode := "foreground"
		if t.Detached {
			mode = "background"
		}
		status := t.Status
		if status == "" {
			status = "active"
		}
		uptime := time.Since(t.StartedAt).Round(time.Second).String()
		table.Append([]string{
			t.ID, t.Type, t.Target,
			fmt.Sprintf("%d", t.LocalPort),
			t.Profile, mode, status, uptime,
		})
	}
	table.Render()
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Close with: tunnelboy disconnect <id>  (or --all)"))
	return nil
}

// disconnectTimeout bounds how long we wait for a signalled tunnel process to
// clean up before escalating to SIGKILL.
const disconnectTimeout = 10 * time.Second

func runDisconnect(cmd *cobra.Command, args []string) error {
	if !disconnectAll && len(args) == 0 {
		return fmt.Errorf("specify a tunnel ID or use --all (see: tunnelboy tunnels)")
	}

	tunnels, err := state.List()
	if err != nil {
		return fmt.Errorf("read tunnel state: %w", err)
	}

	var targets []state.TunnelState
	if disconnectAll {
		targets = tunnels
	} else {
		for _, t := range tunnels {
			if t.ID == args[0] {
				targets = append(targets, t)
				break
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("tunnel %q not found (see: tunnelboy tunnels)", args[0])
		}
	}

	if len(targets) == 0 {
		fmt.Println(tui.DimStyle.Render("No active tunnels"))
		return nil
	}

	var failed int
	for i := range targets {
		if err := disconnectTunnel(&targets[i]); err != nil {
			failed++
			fmt.Printf("%s %s: %v\n", tui.ErrorStyle.Render("✗"), targets[i].ID, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d tunnel(s) failed to disconnect", failed)
	}
	return nil
}

// stopResult describes how a tunnel process was brought down.
type stopResult int

const (
	stopClean stopResult = iota // exited on SIGTERM, close hooks ran
	stopStale                   // process was already dead, record removed
	stopKilled                  // SIGKILL escalation, close hooks may not have run
)

// stopTunnelProcess sends SIGTERM to the owning process (triggering its normal
// close path: SSM teardown, ECS auto-stop hooks, state removal) and escalates
// to SIGKILL if it doesn't clean up in time. Silent — callers render the result.
func stopTunnelProcess(t *state.TunnelState) stopResult {
	if err := state.Signal(t, syscall.SIGTERM); err != nil {
		_ = state.Remove(t.ID)
		return stopStale
	}

	deadline := time.Now().Add(disconnectTimeout)
	for time.Now().Before(deadline) {
		if !state.IsAlive(t.PID) {
			_ = state.Remove(t.ID) // in case the process died before its cleanup
			return stopClean
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = state.Signal(t, syscall.SIGKILL)
	_ = state.Remove(t.ID)
	return stopKilled
}

func disconnectTunnel(t *state.TunnelState) error {
	fmt.Printf("%s Closing tunnel %s (pid %d)...\n", tui.DimStyle.Render("►"), t.ID, t.PID)

	switch stopTunnelProcess(t) {
	case stopStale:
		fmt.Printf("%s Tunnel %s was already dead, removed stale record\n", tui.WarningStyle.Render("⚠"), t.ID)
	case stopKilled:
		fmt.Printf("%s Tunnel %s force-killed after %s (close hooks may not have run)\n",
			tui.WarningStyle.Render("⚠"), t.ID, disconnectTimeout)
	default:
		fmt.Printf("%s Tunnel %s closed\n", tui.SuccessStyle.Render("✓"), t.ID)
	}
	return nil
}

func completeTunnelIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	tunnels, err := state.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var completions []string
	for _, t := range tunnels {
		completions = append(completions, fmt.Sprintf("%s\t%s → localhost:%d", t.ID, t.Target, t.LocalPort))
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}
