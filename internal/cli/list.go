package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List AWS resources",
	Long:  "Discover and list AWS resources like RDS instances, OpenSearch domains, and EC2 instances",
}

var listJumpHostsCmd = &cobra.Command{
	Use:     "jump-hosts",
	Aliases: []string{"bastions", "jh"},
	Short:   "List discovered jump hosts",
	RunE:    runListJumpHosts,
}

var listRDSCmd = &cobra.Command{
	Use:   "rds",
	Short: "List RDS instances",
	RunE:  runListRDS,
}

var listOpenSearchCmd = &cobra.Command{
	Use:     "opensearch",
	Aliases: []string{"os", "es"},
	Short:   "List OpenSearch domains",
	RunE:    runListOpenSearch,
}

var listEC2Cmd = &cobra.Command{
	Use:   "ec2",
	Short: "List EC2 instances",
	RunE:  runListEC2,
}

var listAllCmd = &cobra.Command{
	Use:   "all",
	Short: "List all resources",
	RunE:  runListAll,
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.AddCommand(listJumpHostsCmd)
	listCmd.AddCommand(listRDSCmd)
	listCmd.AddCommand(listOpenSearchCmd)
	listCmd.AddCommand(listEC2Cmd)
	listCmd.AddCommand(listAllCmd)
}

func getDiscovery(ctx context.Context) (*aws.Discovery, error) {
	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return nil, err
	}

	return aws.NewDiscovery(pm.GetConfig()), nil
}

func runListJumpHosts(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	discovery, err := getDiscovery(ctx)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fmt.Println(tui.DimStyle.Render("Discovering jump hosts..."))

	// Discover jump hosts (EC2 and ECS)
	jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to discover jump hosts: %w", err)
	}

	if len(jumpHosts) == 0 {
		fmt.Println(tui.WarningStyle.Render("No jump hosts found"))
		fmt.Println(tui.DimStyle.Render("Configure jump_hosts in ~/.tunnelboy.yaml"))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(jumpHosts)
	case "quiet":
		for _, h := range jumpHosts {
			fmt.Println(h.ID)
		}
		return nil
	default:
		return outputJumpHostsTable(jumpHosts, nil)
	}
}

func outputJumpHostsTable(jumpHosts []aws.JumpHost, _ []aws.ECSTask) error {
	if len(jumpHosts) > 0 {
		fmt.Println(tui.TitleStyle.Render("Jump Hosts"))
		
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Type", "ID/Name", "Private IP", "SSM"})
		table.SetBorder(false)
		setGreenTableColors(table, 4)

		for _, jh := range jumpHosts {
			ssmStatus := tui.FormatSSMStatus(jh.SSMEnabled)
			idName := jh.Name
			if jh.Type == "ecs" {
				idName = fmt.Sprintf("%s/%s", jh.ClusterName, jh.Name)
			}
			table.Append([]string{strings.ToUpper(jh.Type), idName, jh.PrivateIP, ssmStatus})
		}
		table.Render()
		fmt.Println()
	}

	return nil
}

func runListRDS(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	discovery, err := getDiscovery(ctx)
	if err != nil {
		return err
	}

	fmt.Println(tui.DimStyle.Render("Discovering RDS instances..."))

	instances, err := discovery.DiscoverRDSInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover RDS instances: %w", err)
	}

	if len(instances) == 0 {
		fmt.Println(tui.WarningStyle.Render("No RDS instances found"))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(instances)
	case "quiet":
		for _, i := range instances {
			fmt.Println(i.Identifier)
		}
		return nil
	default:
		return outputRDSTable(instances)
	}
}

func outputRDSTable(instances []aws.RDSInstance) error {
	fmt.Println(tui.TitleStyle.Render("RDS Instances"))

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Identifier", "Engine", "Version", "Class", "Status"})
	table.SetBorder(false)
	setGreenTableColors(table, 5)

	for _, i := range instances {
		engine := fmt.Sprintf("%s", i.Engine)
		table.Append([]string{i.Identifier, engine, i.EngineVersion, i.InstanceClass, i.Status})
	}
	table.Render()
	return nil
}

func runListOpenSearch(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	discovery, err := getDiscovery(ctx)
	if err != nil {
		return err
	}

	fmt.Println(tui.DimStyle.Render("Discovering OpenSearch domains..."))

	domains, err := discovery.DiscoverOpenSearchDomains(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover OpenSearch domains: %w", err)
	}

	if len(domains) == 0 {
		fmt.Println(tui.WarningStyle.Render("No OpenSearch domains found"))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(domains)
	case "quiet":
		for _, d := range domains {
			fmt.Println(d.DomainName)
		}
		return nil
	default:
		return outputOpenSearchTable(domains)
	}
}

func outputOpenSearchTable(domains []aws.OpenSearchDomain) error {
	fmt.Println(tui.TitleStyle.Render("OpenSearch Domains"))

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Domain", "Version", "Instance Type", "Nodes"})
	table.SetBorder(false)
	setGreenTableColors(table, 4)

	for _, d := range domains {
		nodes := fmt.Sprintf("%d", d.InstanceCount)
		table.Append([]string{d.DomainName, d.EngineVersion, d.InstanceType, nodes})
	}
	table.Render()
	return nil
}

func runListEC2(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	discovery, err := getDiscovery(ctx)
	if err != nil {
		return err
	}

	fmt.Println(tui.DimStyle.Render("Discovering EC2 instances..."))

	instances, err := discovery.DiscoverEC2Instances(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover EC2 instances: %w", err)
	}

	if len(instances) == 0 {
		fmt.Println(tui.WarningStyle.Render("No EC2 instances found"))
		return nil
	}

	output := viper.GetString("output")
	switch output {
	case "json":
		return outputJSON(instances)
	case "quiet":
		for _, i := range instances {
			fmt.Println(i.InstanceID)
		}
		return nil
	default:
		return outputEC2Table(instances)
	}
}

func outputEC2Table(instances []aws.EC2Instance) error {
	fmt.Println(tui.TitleStyle.Render("EC2 Instances"))

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Instance ID", "Name", "Type", "Private IP", "SSM"})
	table.SetBorder(false)
	setGreenTableColors(table, 5)

	for _, i := range instances {
		ssmStatus := tui.FormatSSMStatus(i.SSMEnabled)
		table.Append([]string{i.InstanceID, i.Name, i.InstanceType, i.PrivateIP, ssmStatus})
	}
	table.Render()
	return nil
}

func runListAll(cmd *cobra.Command, args []string) error {
	// Run all list commands
	fmt.Println()
	if err := runListJumpHosts(cmd, args); err != nil {
		fmt.Println(tui.ErrorStyle.Render("Jump hosts: " + err.Error()))
	}
	fmt.Println()
	if err := runListRDS(cmd, args); err != nil {
		fmt.Println(tui.ErrorStyle.Render("RDS: " + err.Error()))
	}
	fmt.Println()
	if err := runListOpenSearch(cmd, args); err != nil {
		fmt.Println(tui.ErrorStyle.Render("OpenSearch: " + err.Error()))
	}
	fmt.Println()
	if err := runListEC2(cmd, args); err != nil {
		fmt.Println(tui.ErrorStyle.Render("EC2: " + err.Error()))
	}
	fmt.Println()
	if err := runListEndpoints("ElastiCache", elastiCacheKind.discover); err != nil {
		fmt.Println(tui.ErrorStyle.Render("ElastiCache: " + err.Error()))
	}
	fmt.Println()
	if err := runListEndpoints("DocumentDB", docDBKind.discover); err != nil {
		fmt.Println(tui.ErrorStyle.Render("DocumentDB: " + err.Error()))
	}
	fmt.Println()
	if err := runListEndpoints("MSK", mskKind.discover); err != nil {
		fmt.Println(tui.ErrorStyle.Render("MSK: " + err.Error()))
	}
	return nil
}

func setGreenTableColors(table *tablewriter.Table, numColumns int) {
	headerColors := make([]tablewriter.Colors, numColumns)
	columnColors := make([]tablewriter.Colors, numColumns)
	
	for i := 0; i < numColumns; i++ {
		headerColors[i] = tablewriter.Colors{tablewriter.Bold, tablewriter.FgGreenColor}
		columnColors[i] = tablewriter.Colors{tablewriter.FgGreenColor}
	}
	
	table.SetHeaderColor(headerColors...)
	table.SetColumnColor(columnColors...)
}
