package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/adamw2/tunnelboy/internal/tui"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(tui.BorderStyle.Render(tui.RenderHeader(versionInfo.Version)))
		fmt.Printf("  %s %s\n", tui.DimStyle.Render("Commit: "), tui.TextStyle.Render(versionInfo.Commit))
		fmt.Printf("  %s %s\n", tui.DimStyle.Render("Built:  "), tui.TextStyle.Render(versionInfo.Date))
		fmt.Printf("  %s %s\n", tui.DimStyle.Render("Go:     "), tui.TextStyle.Render(runtime.Version()))
		fmt.Printf("  %s %s\n", tui.DimStyle.Render("OS/Arch:"), tui.TextStyle.Render(runtime.GOOS+"/"+runtime.GOARCH))
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
