package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string

	// Version info set by main
	versionInfo struct {
		Version string
		Commit  string
		Date    string
	}
)

// rootCmd is the base command
var rootCmd = &cobra.Command{
	Use:   "tunnelboy",
	Short: "AWS VPC tunneling CLI",
	Long: `TunnelBoy - AWS VPC Tunneling CLI with Pip-Boy theming

Securely connect to RDS databases, OpenSearch clusters, and EC2 
instances through SSM Session Manager.

Example:
  tunnelboy connect rds              # Interactive RDS selection
  tunnelboy connect opensearch       # Interactive OpenSearch selection  
  tunnelboy list all                 # List all discoverable resources`,
	SilenceUsage: true,
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

// SetVersionInfo sets version information from build flags
func SetVersionInfo(version, commit, date string) {
	versionInfo.Version = version
	versionInfo.Commit = commit
	versionInfo.Date = date
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.tunnelboy.yaml)")
	rootCmd.PersistentFlags().String("profile", "", "AWS profile to use")
	rootCmd.PersistentFlags().StringP("output", "o", "table", "output format: table, json, quiet")

	// Bind flags to viper
	viper.BindPFlag("profile", rootCmd.PersistentFlags().Lookup("profile"))
	viper.BindPFlag("output", rootCmd.PersistentFlags().Lookup("output"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Warning: could not find home directory:", err)
			return
		}

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".tunnelboy")
	}

	// Read environment variables
	viper.SetEnvPrefix("TUNNELBOY")
	viper.AutomaticEnv()

	// Read config file (ignore if not found)
	if err := viper.ReadInConfig(); err == nil {
		// Config loaded successfully
	}
}
