package main

import (
	"fmt"
	"os"

	"github.com/adamw2/tunnelboy/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Set version info from build flags
	cli.SetVersionInfo(version, commit, date)

	// Execute the CLI
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
