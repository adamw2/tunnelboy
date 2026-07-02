package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose common setup problems",
	Long:  "Check prerequisites, AWS profiles, credentials, and TunnelBoy configuration",
	RunE:  runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func checkPass(msg string)  { fmt.Printf("  %s %s\n", tui.SuccessStyle.Render("✓"), msg) }
func checkWarn(msg string)  { fmt.Printf("  %s %s\n", tui.WarningStyle.Render("⚠"), msg) }
func checkFail(msg string)  { fmt.Printf("  %s %s\n", tui.ErrorStyle.Render("✗"), msg) }

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	failures := 0

	fmt.Println(tui.TitleStyle.Render("TunnelBoy Doctor"))
	fmt.Println()

	// 1. session-manager-plugin
	if path, err := exec.LookPath("session-manager-plugin"); err == nil {
		checkPass(fmt.Sprintf("session-manager-plugin found (%s)", path))
	} else {
		checkFail("session-manager-plugin not found. Install: https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html")
		failures++
	}

	// 2. AWS profiles
	profiles, err := aws.ListProfiles()
	switch {
	case err != nil:
		checkFail(fmt.Sprintf("could not read AWS profiles: %v", err))
		failures++
	case len(profiles) == 0:
		checkFail("no AWS profiles found in ~/.aws/config")
		failures++
	default:
		checkPass(fmt.Sprintf("%d AWS profile(s) configured", len(profiles)))
	}

	// 3. TunnelBoy config
	if len(loadedConfigFiles) == 0 {
		home, _ := os.UserHomeDir()
		checkWarn(fmt.Sprintf("no config file loaded (create %s)", filepath.Join(home, ".tunnelboy.yaml")))
	} else {
		for _, f := range loadedConfigFiles {
			checkPass(fmt.Sprintf("config loaded: %s", f))
		}
	}

	cfg, err := config.Load()
	if err != nil {
		checkFail(fmt.Sprintf("config does not parse: %v", err))
		failures++
		cfg = nil
	} else {
		checkPass(fmt.Sprintf("%d connection preset(s) defined", len(cfg.Connections)))
	}

	// 4. Credentials
	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	credsOK := false
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		checkFail(fmt.Sprintf("could not load AWS config: %v", err))
		failures++
	} else {
		idCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		identity, err := pm.GetIdentity(idCtx)
		cancel()
		switch {
		case err == nil:
			checkPass(fmt.Sprintf("credentials valid: %s (%s, %s)", identity.Arn, identity.AccountID, identity.Region))
			credsOK = true
		case aws.IsCredentialError(err):
			checkFail(fmt.Sprintf("credentials expired or missing for profile %q", pm.GetCurrentProfile()))
			fmt.Printf("    %s\n", tui.DimStyle.Render(fmt.Sprintf("run: assume %s", pm.GetCurrentProfile())))
			failures++
		default:
			checkFail(fmt.Sprintf("identity check failed: %v", err))
			failures++
		}
	}

	// 5. Jump host discovery (only meaningful with valid credentials)
	if credsOK && cfg != nil {
		discovery := aws.NewDiscovery(pm.GetConfig())
		jhCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		jumpHosts, err := discovery.DiscoverJumpHosts(jhCtx, cfg)
		cancel()
		switch {
		case err != nil:
			checkWarn(fmt.Sprintf("jump host discovery failed: %v", err))
		case len(jumpHosts) == 0:
			checkWarn("no jump hosts found (configure jump_hosts, or rely on --direct/--via)")
		default:
			checkPass(fmt.Sprintf("%d jump host(s) discovered", len(jumpHosts)))
		}
	}

	// 6. Optional DB clients (for connect rds --exec)
	for _, client := range []string{"psql", "mysql"} {
		if _, err := exec.LookPath(client); err == nil {
			checkPass(fmt.Sprintf("%s found (for connect rds --exec)", client))
		} else {
			checkWarn(fmt.Sprintf("%s not found (only needed for connect rds --exec)", client))
		}
	}

	fmt.Println()
	if failures > 0 {
		return fmt.Errorf("%d check(s) failed", failures)
	}
	fmt.Println(tui.SuccessStyle.Render("All checks passed"))
	return nil
}
