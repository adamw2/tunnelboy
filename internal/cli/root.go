package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
  tunnelboy connect rds              # Interactive RDS selection
  tunnelboy connect opensearch       # Interactive OpenSearch selection  
  tunnelboy list all                 # List all discoverable resources`,
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
// debugging, in merge order (lowest precedence first).
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

	// Layered config, lowest precedence first: synced shared repos, then
	// files listed under `includes:` in the home config, then the home
	// config itself, then the nearest project-local config. Later layers
	// override earlier ones, so personal settings beat shared ones and
	// project settings beat both.
	homeCfg := homeConfigFile(home)
	var layers []string
	layers = append(layers, sharedConfigs(home)...)
	layers = append(layers, includedConfigs(homeCfg, home)...)
	if homeCfg != "" {
		layers = append(layers, homeCfg)
	}
	if projectCfg := findProjectConfig(home); projectCfg != "" {
		layers = append(layers, projectCfg)
	}

	viper.SetConfigType("yaml")
	for _, f := range layers {
		viper.SetConfigFile(f)
		if err := viper.MergeInConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read config %s: %v\n", f, err)
			continue
		}
		loadedConfigFiles = append(loadedConfigFiles, f)
	}
}

// homeConfigFile returns the user's home config path, or "" if none exists.
func homeConfigFile(home string) string {
	for _, name := range []string{".tunnelboy.yaml", ".tunnelboy.yml"} {
		p := filepath.Join(home, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// sharedConfigs returns the .tunnelboy.yaml of every repo synced into
// ~/.tunnelboy/shared/ (see `tunnelboy config sync`), sorted by repo name.
func sharedConfigs(home string) []string {
	matches, err := filepath.Glob(filepath.Join(home, ".tunnelboy", "shared", "*", ".tunnelboy.yaml"))
	if err != nil {
		return nil
	}
	return matches
}

// includedConfigs returns existing files listed under `includes:` in the
// home config. Entries support ~ expansion; relative paths resolve from
// the home directory.
func includedConfigs(homeCfg, home string) []string {
	if homeCfg == "" {
		return nil
	}
	v := viper.New()
	v.SetConfigFile(homeCfg)
	if err := v.ReadInConfig(); err != nil {
		return nil
	}
	var out []string
	for _, p := range v.GetStringSlice("includes") {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		} else if !filepath.IsAbs(p) {
			p = filepath.Join(home, p)
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: included config %s not found\n", p)
		}
	}
	return out
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
