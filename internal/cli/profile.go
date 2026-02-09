package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage AWS profiles",
	Long:  "List, switch, and view AWS profile information",
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available AWS profiles",
	RunE:  runProfileList,
}

var profileUseCmd = &cobra.Command{
	Use:   "use <profile-name>",
	Short: "Switch to a different AWS profile",
	Args:  cobra.ExactArgs(1),
	RunE:  runProfileUse,
}

var profileCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show current AWS identity",
	RunE:  runProfileCurrent,
}

func init() {
	rootCmd.AddCommand(profileCmd)
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileUseCmd)
	profileCmd.AddCommand(profileCurrentCmd)
}

func runProfileList(cmd *cobra.Command, args []string) error {
	profiles, err := aws.ListProfiles()
	if err != nil {
		return fmt.Errorf("failed to list profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println(tui.WarningStyle.Render("No AWS profiles found in ~/.aws/config or ~/.aws/credentials"))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(profiles)
	case "quiet":
		for _, p := range profiles {
			fmt.Println(p.Name)
		}
		return nil
	default:
		return outputProfilesTable(profiles)
	}
}

func outputProfilesTable(profiles []aws.ProfileInfo) error {
	// Get current profile for highlighting
	currentProfile := os.Getenv("AWS_PROFILE")
	if currentProfile == "" {
		currentProfile = "default"
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"", "Profile", "Region", "Type"})
	table.SetBorder(false)
	setGreenTableColors(table, 4)

	for _, p := range profiles {
		marker := " "
		if p.Name == currentProfile {
			marker = "►"
		}

		profileType := "credentials"
		if p.IsSSO {
			profileType = "SSO"
		}

		region := p.Region
		if region == "" {
			region = "-"
		}

		table.Append([]string{marker, p.Name, region, profileType})
	}

	table.Render()
	return nil
}

func runProfileUse(cmd *cobra.Command, args []string) error {
	profileName := args[0]

	// Verify profile exists
	profiles, err := aws.ListProfiles()
	if err != nil {
		return err
	}

	found := false
	for _, p := range profiles {
		if p.Name == profileName {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("profile %q not found", profileName)
	}

	// Set the profile
	if err := aws.SetProfile(profileName); err != nil {
		return err
	}

	fmt.Printf("%s Switched to profile: %s\n", 
		tui.SuccessStyle.Render("✓"),
		tui.TextStyle.Render(profileName))
	
	// Note about shell persistence
	fmt.Println(tui.DimStyle.Render("Note: To persist across shell sessions, run:"))
	fmt.Printf(tui.DimStyle.Render("  export AWS_PROFILE=%s\n"), profileName)

	return nil
}

func runProfileCurrent(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	pm := aws.NewProfileManager()
	
	// Get profile from flag or environment
	profileName := viper.GetString("profile")
	
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return err
	}

	identity, err := pm.GetIdentity(ctx)
	if err != nil {
		// Check if it's an SSO expiration issue
		if strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "SSO") {
			fmt.Println(tui.ErrorStyle.Render("SSO session expired. Run: aws sso login"))
			return nil
		}
		return fmt.Errorf("failed to get identity: %w", err)
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(identity)
	case "quiet":
		fmt.Println(identity.AccountID)
		return nil
	default:
		fmt.Println(tui.TitleStyle.Render("Current AWS Identity"))
		fmt.Println()
		fmt.Printf("  %s  %s\n", tui.DimStyle.Render("Profile:"), tui.TextStyle.Render(identity.Name))
		fmt.Printf("  %s  %s\n", tui.DimStyle.Render("Account:"), tui.TextStyle.Render(identity.AccountID))
		fmt.Printf("  %s  %s\n", tui.DimStyle.Render("Region: "), tui.TextStyle.Render(identity.Region))
		fmt.Printf("  %s  %s\n", tui.DimStyle.Render("ARN:    "), tui.DimStyle.Render(identity.Arn))
		return nil
	}
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
