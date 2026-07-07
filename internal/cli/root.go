package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/tui"
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
  tunnelboy                          # Open the live dashboard
  tunnelboy connect rds              # Interactive RDS selection
  tunnelboy connect opensearch       # Interactive OpenSearch selection
  tunnelboy list all                 # List all discoverable resources`,
	Args:         cobra.NoArgs,
	RunE:         runDash,
	SilenceUsage: true,
}

// Execute runs the root command
func Execute() error {
	err := rootCmd.Execute()
	if err != nil && aws.IsCredentialError(err) {
		profile := viper.GetString("profile")
		if profile == "" {
			profile = os.Getenv("AWS_PROFILE")
		}
		if profile == "" {
			profile = viper.GetString("default_profile")
		}
		fmt.Fprintf(os.Stderr, "\n%s\n", tui.WarningStyle.Render(aws.CredentialHint(profile)))
	}
	return err
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

// loadedConfigFiles records which config files were read, for `doctor` and
// debugging. Order: home config first, project config (merged on top) second.
var loadedConfigFiles []string

func initConfig() {
	// Read environment variables
	viper.SetEnvPrefix("TUNNELBOY")
	viper.AutomaticEnv()

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
		if err := viper.ReadInConfig(); err == nil {
			loadedConfigFiles = append(loadedConfigFiles, viper.ConfigFileUsed())
		}
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not find home directory:", err)
		return
	}

	// Home config first (ignore if not found)
	viper.AddConfigPath(home)
	viper.SetConfigType("yaml")
	viper.SetConfigName(".tunnelboy")
	if err := viper.ReadInConfig(); err == nil {
		loadedConfigFiles = append(loadedConfigFiles, viper.ConfigFileUsed())
	}

	// Project-local config: nearest .tunnelboy.yaml walking up from the
	// working directory, merged over the home config so teams can commit
	// shared presets while users keep personal defaults.
	if projectCfg := findProjectConfig(home); projectCfg != "" {
		viper.SetConfigFile(projectCfg)
		if err := viper.MergeInConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read project config %s: %v\n", projectCfg, err)
		} else {
			loadedConfigFiles = append(loadedConfigFiles, projectCfg)
		}
	}
}

// findProjectConfig walks up from the working directory looking for
// .tunnelboy.yaml, stopping at the home directory (whose config is already
// loaded) or the filesystem root.
func findProjectConfig(home string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if dir == home {
			return ""
		}
		candidate := filepath.Join(dir, ".tunnelboy.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
