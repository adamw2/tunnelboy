package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/tui"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

// endpointKind describes a tunnelable service that only needs host:port
// forwarding (no IAM tokens, no signing proxy).
type endpointKind struct {
	kind       string
	title      string // TUI selector title
	tunnelType tunnel.TunnelType
	discover   func(context.Context, *aws.Discovery) ([]aws.EndpointTarget, error)
	printHints func(t *tunnel.Tunnel, target *aws.EndpointTarget)
}

var elastiCacheKind = endpointKind{
	kind:       "elasticache",
	title:      "SELECT ELASTICACHE CLUSTER",
	tunnelType: tunnel.TunnelTypeElastiCache,
	discover: func(ctx context.Context, d *aws.Discovery) ([]aws.EndpointTarget, error) {
		return d.DiscoverElastiCache(ctx)
	},
	printHints: func(t *tunnel.Tunnel, target *aws.EndpointTarget) {
		fmt.Println(tui.TitleStyle.Render("Client"))
		fmt.Printf("  %s\n", tui.TextStyle.Render(fmt.Sprintf("redis-cli -h localhost -p %d", t.LocalPort)))
		fmt.Println(tui.DimStyle.Render("  If in-transit encryption is enabled, add: --tls --insecure"))
		fmt.Println(tui.DimStyle.Render("  (--insecure because the cert is for the AWS endpoint, not localhost)"))
	},
}

var docDBKind = endpointKind{
	kind:       "docdb",
	title:      "SELECT DOCUMENTDB CLUSTER",
	tunnelType: tunnel.TunnelTypeDocDB,
	discover: func(ctx context.Context, d *aws.Discovery) ([]aws.EndpointTarget, error) {
		return d.DiscoverDocDBClusters(ctx)
	},
	printHints: func(t *tunnel.Tunnel, target *aws.EndpointTarget) {
		fmt.Println(tui.TitleStyle.Render("Client"))
		fmt.Printf("  %s\n", tui.TextStyle.Render(fmt.Sprintf(
			"mongosh \"mongodb://<user>:<password>@localhost:%d/?tls=true&tlsAllowInvalidHostnames=true&directConnection=true&retryWrites=false\" --tlsCAFile global-bundle.pem",
			t.LocalPort)))
		fmt.Println(tui.DimStyle.Render("  CA bundle: https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem"))
	},
}

var mskKind = endpointKind{
	kind:       "msk",
	title:      "SELECT MSK CLUSTER",
	tunnelType: tunnel.TunnelTypeMSK,
	discover: func(ctx context.Context, d *aws.Discovery) ([]aws.EndpointTarget, error) {
		return d.DiscoverMSKClusters(ctx)
	},
	printHints: func(t *tunnel.Tunnel, target *aws.EndpointTarget) {
		fmt.Printf("  %s localhost:%d (first bootstrap broker)\n", tui.DimStyle.Render("Bootstrap:"), t.LocalPort)
		fmt.Println()
		fmt.Println(tui.WarningStyle.Render("⚠ Kafka clients bootstrap here but are then redirected to the brokers'"))
		fmt.Println(tui.WarningStyle.Render("  advertised listeners, which are not reachable through this tunnel."))
		fmt.Println(tui.WarningStyle.Render("  Good for connectivity checks and single-broker admin calls only."))
	},
}

var connectRedisCmd = &cobra.Command{
	Use:     "redis [name]",
	Aliases: []string{"elasticache", "cache", "valkey"},
	Short:   "Connect to an ElastiCache cluster (Redis/Valkey/Memcached)",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConnectEndpoint(elastiCacheKind, args)
	},
}

var connectDocDBCmd = &cobra.Command{
	Use:     "docdb [name]",
	Aliases: []string{"documentdb", "mongo"},
	Short:   "Connect to a DocumentDB cluster",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConnectEndpoint(docDBKind, args)
	},
}

var connectMSKCmd = &cobra.Command{
	Use:     "msk [name]",
	Aliases: []string{"kafka"},
	Short:   "Connect to an MSK cluster (first bootstrap broker)",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConnectEndpoint(mskKind, args)
	},
}

func init() {
	connectCmd.AddCommand(connectRedisCmd)
	connectCmd.AddCommand(connectDocDBCmd)
	connectCmd.AddCommand(connectMSKCmd)

	for _, c := range []*cobra.Command{connectRedisCmd, connectDocDBCmd, connectMSKCmd} {
		c.Flags().IntVar(&connectLocalPort, "local-port", 0, "local port (default: same as remote)")
		c.Flags().StringVar(&connectVia, "via", "", "jump host instance ID")
		c.Flags().BoolVar(&connectDetach, "detach", false, "run the tunnel in the background")
	}
}

// chooseJumpHost resolves the jump host to tunnel through: --via wins,
// otherwise discover and (if needed) prompt.
func chooseJumpHost(ctx context.Context, discovery *aws.Discovery, cfg *config.Config) (*aws.JumpHost, string, error) {
	if connectVia != "" {
		return nil, connectVia, nil
	}
	jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("jump host discovery failed: %w", err)
	}
	if len(jumpHosts) == 0 {
		return nil, "", fmt.Errorf("no jump host found. Configure jump_hosts in ~/.tunnelboy.yaml or use --via")
	}
	if len(jumpHosts) == 1 {
		return &jumpHosts[0], jumpHosts[0].ID, nil
	}
	selected, err := tui.SelectJumpHost(jumpHosts)
	if err != nil {
		return nil, "", err
	}
	return selected, selected.ID, nil
}

func runConnectEndpoint(kind endpointKind, args []string) error {
	ctx := context.Background()

	if err := tunnel.CheckSessionManagerPlugin(); err != nil {
		return err
	}

	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return err
	}

	discovery := aws.NewDiscovery(pm.GetConfig())
	enableECSAutoStart(discovery)
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	targets, err := kind.discover(ctx, discovery)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no %s targets found", kind.kind)
	}

	var target *aws.EndpointTarget
	if len(args) > 0 {
		for i := range targets {
			if targets[i].Name == args[0] {
				target = &targets[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("%s target %q not found", kind.kind, args[0])
		}
	} else if len(targets) == 1 {
		target = &targets[0]
	} else {
		target, err = tui.SelectEndpoint(kind.title, targets)
		if err != nil {
			return err
		}
	}

	selectedHost, jumpHost, err := chooseJumpHost(ctx, discovery, cfg)
	if err != nil {
		return err
	}

	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	localPort, err := resolveLocalPort(connectLocalPort, int(target.Port))
	if err != nil {
		return err
	}

	if connectDetach {
		spec := tunnelSpec{
			Type:       string(kind.tunnelType),
			Engine:     target.Engine,
			Target:     target.Name,
			LocalPort:  localPort,
			RemoteHost: target.Endpoint,
			RemotePort: int(target.Port),
			JumpHostID: jumpHost,
			Profile:    pm.GetCurrentProfile(),
		}
		applyAutoStop(&spec, selectedHost, cfg)
		fmt.Printf("%s Starting background tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(target.Name))
		st, err := spawnDetached(spec)
		if err != nil {
			return err
		}
		printDetached(st)
		return nil
	}

	fmt.Printf("%s Creating tunnel to %s...\n",
		tui.DimStyle.Render("►"),
		tui.TextStyle.Render(target.Name))

	t, err := tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       kind.tunnelType,
		Engine:     target.Engine,
		LocalPort:  localPort,
		RemoteHost: target.Endpoint,
		RemotePort: int(target.Port),
		JumpHostID: jumpHost,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	registerAutoStopIfECS(t, selectedHost, cfg, discovery)

	fmt.Println()
	fmt.Printf("%s Tunnel active\n", tui.SuccessStyle.Render("✓"))
	fmt.Println()
	fmt.Println(tui.TitleStyle.Render("Connection Details"))
	fmt.Printf("  %s localhost\n", tui.DimStyle.Render("Host:      "))
	fmt.Printf("  %s %d\n", tui.DimStyle.Render("Port:      "), t.LocalPort)
	fmt.Printf("  %s %s (%s)\n", tui.DimStyle.Render("Target:    "), target.Name, target.Engine)
	fmt.Println()
	if kind.printHints != nil {
		kind.printHints(t, target)
		fmt.Println()
	}
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	holdTunnel(tunnelMgr, tunnelStateFor(t, target.Name, pm.GetCurrentProfile()))

	return nil
}
