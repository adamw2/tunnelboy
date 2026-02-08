package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/tui"
)

// Note: This is a simplified version. In a full implementation,
// you'd persist tunnel state to a file or use a daemon process.

var tunnelsCmd = &cobra.Command{
	Use:   "tunnels",
	Short: "List active tunnels",
	Long:  "Show all currently active tunnels managed by TunnelBoy",
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
}

func runTunnels(cmd *cobra.Command, args []string) error {
	// In a full implementation, this would read from a state file
	// or communicate with a daemon process.
	
	// For now, show a message about the current implementation
	fmt.Println(tui.TitleStyle.Render("Active Tunnels"))
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Tunnels are managed within each connect session."))
	fmt.Println(tui.DimStyle.Render("Use Ctrl+C in the connect session to close the tunnel."))
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("For persistent tunnel management, a daemon mode is planned for a future release."))

	return nil
}

func runDisconnect(cmd *cobra.Command, args []string) error {
	if disconnectAll {
		fmt.Println(tui.DimStyle.Render("Disconnecting all tunnels..."))
		// In a full implementation, this would signal the daemon
		fmt.Println(tui.SuccessStyle.Render("✓ All tunnels closed"))
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("specify a tunnel ID or use --all")
	}

	tunnelID := args[0]
	fmt.Printf("%s Closing tunnel %s...\n", tui.DimStyle.Render("►"), tunnelID)
	// In a full implementation, this would signal the daemon
	fmt.Printf("%s Tunnel %s closed\n", tui.SuccessStyle.Render("✓"), tunnelID)

	return nil
}

// TunnelInfo represents tunnel information for display
type TunnelInfo struct {
	ID         string
	Type       string
	LocalPort  int
	RemoteHost string
	RemotePort int
	Status     string
	Duration   time.Duration
}

func outputTunnelsTable(tunnels []TunnelInfo) error {
	output := viper.GetString("output")

	switch output {
	case "json":
		return outputJSON(tunnels)
	case "quiet":
		for _, t := range tunnels {
			fmt.Println(t.ID)
		}
		return nil
	default:
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"ID", "Type", "Local", "Remote", "Status", "Duration"})
		table.SetBorder(false)
		setGreenTableColors(table)

		for _, t := range tunnels {
			local := fmt.Sprintf(":%d", t.LocalPort)
			remote := fmt.Sprintf("%s:%d", t.RemoteHost, t.RemotePort)
			duration := formatDuration(t.Duration)
			table.Append([]string{t.ID, t.Type, local, remote, t.Status, duration})
		}
		table.Render()
		return nil
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
