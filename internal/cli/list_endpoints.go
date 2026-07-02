package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var listRedisCmd = &cobra.Command{
	Use:     "redis",
	Aliases: []string{"elasticache", "cache", "valkey"},
	Short:   "List ElastiCache clusters",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runListEndpoints("ElastiCache", elastiCacheKind.discover)
	},
}

var listDocDBCmd = &cobra.Command{
	Use:     "docdb",
	Aliases: []string{"documentdb"},
	Short:   "List DocumentDB clusters",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runListEndpoints("DocumentDB", docDBKind.discover)
	},
}

var listMSKCmd = &cobra.Command{
	Use:     "msk",
	Aliases: []string{"kafka"},
	Short:   "List MSK clusters",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runListEndpoints("MSK", mskKind.discover)
	},
}

func init() {
	listCmd.AddCommand(listRedisCmd)
	listCmd.AddCommand(listDocDBCmd)
	listCmd.AddCommand(listMSKCmd)
}

func runListEndpoints(label string, discover func(context.Context, *aws.Discovery) ([]aws.EndpointTarget, error)) error {
	ctx := context.Background()

	discovery, err := getDiscovery(ctx)
	if err != nil {
		return err
	}

	fmt.Println(tui.DimStyle.Render(fmt.Sprintf("Discovering %s targets...", label)))

	targets, err := discover(ctx, discovery)
	if err != nil {
		return fmt.Errorf("failed to discover %s targets: %w", label, err)
	}

	if len(targets) == 0 {
		fmt.Println(tui.WarningStyle.Render(fmt.Sprintf("No %s targets found", label)))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(targets)
	case "quiet":
		for _, t := range targets {
			fmt.Println(t.Name)
		}
		return nil
	default:
		fmt.Println(tui.TitleStyle.Render(label))

		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Name", "Engine", "Endpoint", "Port"})
		table.SetBorder(false)
		setGreenTableColors(table, 4)

		for _, t := range targets {
			table.Append([]string{t.Name, t.Engine, t.Endpoint, fmt.Sprintf("%d", t.Port)})
		}
		table.Render()
		return nil
	}
}
