package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/adamw2/tunnelboy/internal/tui"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage tunnelboy configuration",
}

var configSyncCmd = &cobra.Command{
	Use:   "sync [git-url]",
	Short: "Clone or update shared config repos",
	Long: `Sync shared team configuration.

With a git URL, clones the repo into ~/.tunnelboy/shared/<name>/ — its
.tunnelboy.yaml is loaded on every run from then on, below your personal
~/.tunnelboy.yaml so your own settings always win.

With no arguments, pulls the latest changes for every synced repo.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir := filepath.Join(home, ".tunnelboy", "shared")
		if len(args) == 1 {
			return syncRepo(dir, args[0])
		}
		return syncAll(dir)
	},
}

var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// repoNameFromURL derives a directory name from a git URL, e.g.
// git@github.com:truecontext/tunnelboy-config.git → tunnelboy-config.
func repoNameFromURL(url string) (string, error) {
	name := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." || !repoNameRE.MatchString(name) {
		return "", fmt.Errorf("cannot derive a repo name from %q", url)
	}
	return name, nil
}

func syncRepo(dir, url string) error {
	name, err := repoNameFromURL(url)
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, name)

	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		fmt.Printf("%s Updating %s...\n", tui.DimStyle.Render("►"), name)
		if err := runGit("-C", dest, "pull", "--ff-only"); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		fmt.Printf("%s Cloning %s...\n", tui.DimStyle.Render("►"), url)
		if err := runGit("clone", "--depth", "1", "--", url, dest); err != nil {
			return err
		}
	}
	reportSharedConfig(dest, name)
	return nil
}

func syncAll(dir string) error {
	repos, _ := filepath.Glob(filepath.Join(dir, "*", ".git"))
	if len(repos) == 0 {
		fmt.Println(tui.DimStyle.Render("No shared config repos yet. Add one with: tunnelboy config sync <git-url>"))
		return nil
	}
	for _, g := range repos {
		dest := filepath.Dir(g)
		name := filepath.Base(dest)
		fmt.Printf("%s Updating %s...\n", tui.DimStyle.Render("►"), name)
		if err := runGit("-C", dest, "pull", "--ff-only"); err != nil {
			return fmt.Errorf("updating %s: %w", name, err)
		}
		reportSharedConfig(dest, name)
	}
	return nil
}

func runGit(args ...string) error {
	cmd := exec.Command("git", args...) // #nosec G204 -- args are git subcommands plus a user-supplied repo URL/path, passed after "--"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// reportSharedConfig says what a synced repo contributes (or warns if it
// has no config at its root).
func reportSharedConfig(dest, name string) {
	cfgPath := filepath.Join(dest, ".tunnelboy.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Printf("%s %s has no .tunnelboy.yaml at its root — nothing will be loaded from it\n",
			tui.WarningStyle.Render("⚠"), name)
		return
	}
	v := viper.New()
	v.SetConfigFile(cfgPath)
	if err := v.ReadInConfig(); err != nil {
		fmt.Printf("%s %s/.tunnelboy.yaml is not valid YAML: %v\n", tui.WarningStyle.Render("⚠"), name, err)
		return
	}
	var presets []string
	for p := range v.GetStringMap("connections") {
		presets = append(presets, p)
	}
	fmt.Printf("%s %s synced", tui.SuccessStyle.Render("✓"), name)
	if len(presets) > 0 {
		fmt.Printf(" — presets: %s", strings.Join(presets, ", "))
	}
	fmt.Println()
}

func init() {
	configCmd.AddCommand(configSyncCmd)
	rootCmd.AddCommand(configCmd)
}
