package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tui"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

var (
	connectLocalPort    int
	connectRemotePort   int
	connectVia          string
	connectDirect       bool
	connectDBUser       string
	connectDBName       string
	connectExec         bool
	connectKibanaPort   int
	connectPrintToken   bool
	connectShell        bool
	connectPortForward  bool
	connectDetach       bool
)

var connectCmd = &cobra.Command{
	Use:   "connect [preset-name]",
	Short: "Connect to AWS resources",
	Long: `Create tunnels to AWS resources like RDS databases, OpenSearch domains, and EC2 instances.

Examples:
  tunnelboy connect rds                    # Interactive RDS selection
  tunnelboy connect rds my-database        # Connect to specific RDS
  tunnelboy connect opensearch my-domain   # Connect to OpenSearch
  tunnelboy connect ec2 i-0abc123          # Connect to EC2 instance
  tunnelboy connect latest-readonly        # Use connection preset from config`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConnectPreset,
}

var connectRDSCmd = &cobra.Command{
	Use:   "rds [identifier]",
	Short: "Connect to an RDS instance",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConnectRDS,
}

var connectOpenSearchCmd = &cobra.Command{
	Use:     "opensearch [domain]",
	Aliases: []string{"os", "es"},
	Short:   "Connect to an OpenSearch domain",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runConnectOpenSearch,
}

var connectEC2Cmd = &cobra.Command{
	Use:   "ec2 [instance-id]",
	Short: "Connect to an EC2 instance",
	Long: `Connect to an EC2 instance via SSM interactive shell or port forwarding.

Examples:
  tunnelboy connect ec2 i-0abc123              # Interactive shell (default)
  tunnelboy connect ec2 i-0abc123 --port-forward --remote-port 8080  # Port forward
  tunnelboy connect ec2                        # Interactive selection`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConnectEC2,
}

func init() {
	rootCmd.AddCommand(connectCmd)
	connectCmd.AddCommand(connectRDSCmd)
	connectCmd.AddCommand(connectOpenSearchCmd)
	connectCmd.AddCommand(connectEC2Cmd)

	// Add shell completion functions
	connectCmd.ValidArgsFunction = completeConnectionPresets
	connectRDSCmd.ValidArgsFunction = completeRDSInstances
	connectOpenSearchCmd.ValidArgsFunction = completeOpenSearchDomains
	connectEC2Cmd.ValidArgsFunction = completeEC2Instances

	// RDS flags
	connectRDSCmd.Flags().IntVar(&connectLocalPort, "local-port", 0, "local port (default: auto)")
	connectRDSCmd.Flags().StringVar(&connectVia, "via", "", "jump host instance ID")
	connectRDSCmd.Flags().StringVar(&connectDBUser, "db-user", "", "database user for IAM auth")
	connectRDSCmd.Flags().BoolVar(&connectPrintToken, "print-token", false, "print only the IAM token and exit")
	connectRDSCmd.Flags().BoolVar(&connectExec, "exec", false, "launch psql/mysql through the tunnel with the IAM token")
	connectRDSCmd.Flags().StringVar(&connectDBName, "db-name", "", "database name (for --exec)")
	connectRDSCmd.Flags().BoolVar(&connectDetach, "detach", false, "run the tunnel in the background")
	connectCmd.Flags().BoolVar(&connectDetach, "detach", false, "run the tunnel in the background (for presets)")

	connectOpenSearchCmd.Flags().BoolVar(&connectDetach, "detach", false, "run the tunnel in the background")
	connectEC2Cmd.Flags().BoolVar(&connectDetach, "detach", false, "run the tunnel in the background (port-forward mode only)")

	// OpenSearch flags
	connectOpenSearchCmd.Flags().IntVar(&connectLocalPort, "local-port", 0, "local port for API (default 9250; Chrome blocks 9200)")
	connectOpenSearchCmd.Flags().IntVar(&connectKibanaPort, "kibana-port", 5601, "local port for Kibana")
	connectOpenSearchCmd.Flags().StringVar(&connectVia, "via", "", "jump host instance ID")

	// EC2 flags
	connectEC2Cmd.Flags().IntVar(&connectLocalPort, "local-port", 0, "local port (default: auto)")
	connectEC2Cmd.Flags().IntVar(&connectRemotePort, "remote-port", 22, "remote port")
	connectEC2Cmd.Flags().StringVar(&connectVia, "via", "", "jump host instance ID")
	connectEC2Cmd.Flags().BoolVar(&connectDirect, "direct", false, "connect directly via SSM (no jump host)")
	connectEC2Cmd.Flags().BoolVar(&connectShell, "shell", false, "open interactive shell (default behavior)")
	connectEC2Cmd.Flags().BoolVar(&connectPortForward, "port-forward", false, "use port forwarding instead of shell")
}

func runConnectRDS(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Check for session manager plugin
	if err := tunnel.CheckSessionManagerPlugin(); err != nil {
		return err
	}

	// Load profile
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

	// Get RDS instance
	var rdsInstance *aws.RDSInstance

	if len(args) > 0 {
		// Direct identifier provided
		instances, err := discovery.DiscoverRDSInstances(ctx)
		if err != nil {
			return err
		}
		for _, inst := range instances {
			if inst.Identifier == args[0] {
				rdsInstance = &inst
				break
			}
		}
		if rdsInstance == nil {
			return fmt.Errorf("RDS instance %q not found", args[0])
		}
	} else {
		// Interactive selection
		instances, err := discovery.DiscoverRDSInstances(ctx)
		if err != nil {
			return err
		}
		if len(instances) == 0 {
			return fmt.Errorf("no RDS instances found")
		}

		selected, err := tui.SelectRDS(instances)
		if err != nil {
			return err
		}
		rdsInstance = selected
	}

	// Get database user. A detached launch without one is fine — the tunnel
	// doesn't need it, only the IAM token does — so don't block on a prompt
	// (the dashboard launches with no terminal to prompt on).
	dbUser := connectDBUser
	if dbUser == "" && !connectDetach {
		if connectPrintToken {
			return fmt.Errorf("--db-user is required when using --print-token")
		}
		dbUser, err = tui.PromptInput("Database user", "")
		if err != nil {
			return err
		}
	}

	// If --print-token flag is set, just generate and print the token
	if connectPrintToken {
		token, err := aws.GenerateRDSAuthToken(
			ctx,
			pm.GetConfig(),
			rdsInstance.Endpoint,
			int(rdsInstance.Port),
			pm.GetConfig().Region,
			dbUser,
		)
		if err != nil {
			return fmt.Errorf("failed to generate IAM token: %w", err)
		}
		fmt.Println(token)
		return nil
	}

	// Get jump host
	var selectedHost *aws.JumpHost
	jumpHost := connectVia
	if jumpHost == "" {
		jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
		if err != nil || len(jumpHosts) == 0 {
			return fmt.Errorf("no jump host found. Configure jump_hosts in ~/.tunnelboy.yaml or use --via")
		}
		if len(jumpHosts) == 1 {
			selectedHost = &jumpHosts[0]
		} else {
			selectedHost, err = tui.SelectJumpHost(jumpHosts)
			if err != nil {
				return err
			}
		}
		jumpHost = selectedHost.ID
	}

	// Create tunnel
	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	localPort, err := resolveLocalPort(connectLocalPort, int(rdsInstance.Port))
	if err != nil {
		return err
	}

	if connectDetach {
		if connectExec {
			return fmt.Errorf("--detach cannot be combined with --exec (the client is interactive)")
		}
		spec := tunnelSpec{
			Type:       string(tunnel.TunnelTypeRDS),
			Engine:     rdsInstance.Engine,
			Target:     rdsInstance.Identifier,
			LocalPort:  localPort,
			RemoteHost: rdsInstance.Endpoint,
			RemotePort: int(rdsInstance.Port),
			JumpHostID: jumpHost,
			Profile:    pm.GetCurrentProfile(),
		}
		applyAutoStop(&spec, selectedHost, cfg)

		fmt.Printf("%s Starting background tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(rdsInstance.Identifier))
		st, err := spawnDetached(spec)
		if err != nil {
			return err
		}
		printDetached(st)

		if dbUser == "" {
			fmt.Println()
			fmt.Println(tui.DimStyle.Render(fmt.Sprintf(
				"IAM token: tunnelboy connect rds %s --print-token --db-user <user>", rdsInstance.Identifier)))
			return nil
		}
		token, err := aws.GenerateRDSAuthToken(ctx, pm.GetConfig(), rdsInstance.Endpoint,
			int(rdsInstance.Port), pm.GetConfig().Region, dbUser)
		if err != nil {
			fmt.Printf("%s Could not generate IAM token: %v\n", tui.WarningStyle.Render("⚠"), err)
			return nil
		}
		fmt.Println()
		fmt.Println(tui.TitleStyle.Render("IAM Authentication Token"))
		fmt.Println(tui.DimStyle.Render("Use as password (valid 15 minutes; rerun with --print-token for a fresh one):"))
		fmt.Println(tui.TextStyle.Render(token))
		return nil
	}

	fmt.Printf("%s Creating tunnel to %s...\n",
		tui.DimStyle.Render("►"),
		tui.TextStyle.Render(rdsInstance.Identifier))

	t, err := tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       tunnel.TunnelTypeRDS,
		Engine:     rdsInstance.Engine,
		LocalPort:  localPort,
		RemoteHost: rdsInstance.Endpoint,
		RemotePort: int(rdsInstance.Port),
		JumpHostID: jumpHost,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	registerAutoStopIfECS(t, selectedHost, cfg, discovery)

	// Generate IAM auth token
	fmt.Println()
	fmt.Printf("%s Generating IAM authentication token...\n", tui.DimStyle.Render("►"))
	
	token, err := aws.GenerateRDSAuthToken(
		ctx,
		pm.GetConfig(),
		rdsInstance.Endpoint,
		int(rdsInstance.Port),
		pm.GetConfig().Region,
		dbUser,
	)
	if err != nil {
		fmt.Printf("%s Warning: Failed to generate IAM token: %v\n", tui.WarningStyle.Render("⚠"), err)
		fmt.Println(tui.DimStyle.Render("You may need to use master password authentication instead"))
		token = "<failed-to-generate>"
	}

	fmt.Println()
	fmt.Printf("%s Tunnel active\n", tui.SuccessStyle.Render("✓"))
	fmt.Println()

	// Connection details
	fmt.Println(tui.TitleStyle.Render("Connection Details"))
	fmt.Printf("  %s localhost\n", tui.DimStyle.Render("Host:      "))
	fmt.Printf("  %s %d\n", tui.DimStyle.Render("Port:      "), t.LocalPort)
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("Database:  "), rdsInstance.Identifier)
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("User:      "), tui.TextStyle.Render(dbUser))
	fmt.Println()

	// --exec: hand the session to the DB client; tunnel closes when it exits
	if connectExec {
		if token == "<failed-to-generate>" {
			tunnelMgr.CloseAll()
			return fmt.Errorf("cannot use --exec: IAM token generation failed")
		}
		st := tunnelStateFor(t, rdsInstance.Identifier, pm.GetCurrentProfile())
		_ = state.Write(st)
		clientErr := runDBClient(rdsInstance.Engine, dbUser, connectDBName, t.LocalPort, token)
		_ = state.Remove(st.ID)
		tunnelMgr.CloseAll()
		fmt.Println(tui.DimStyle.Render("\nTunnel closed"))
		return clientErr
	}

	// IAM Token
	fmt.Println(tui.TitleStyle.Render("IAM Authentication Token"))
	fmt.Println(tui.DimStyle.Render("Use this as your password (valid for 15 minutes):"))
	fmt.Println()
	fmt.Println(tui.TextStyle.Render(token))
	fmt.Println()
	
	// Connection string
	fmt.Println(tui.TitleStyle.Render("Connection String"))
	fmt.Printf("  %s\n", tui.TextStyle.Render(t.ConnectionString(dbUser)))
	fmt.Println()
	
	// Instructions
	fmt.Println(tui.TitleStyle.Render("MySQL Workbench / GUI Tools"))
	fmt.Println(tui.DimStyle.Render("1. Host: localhost"))
	fmt.Printf(tui.DimStyle.Render("2. Port: %d\n"), t.LocalPort)
	fmt.Printf(tui.DimStyle.Render("3. Username: %s\n"), dbUser)
	fmt.Println(tui.DimStyle.Render("4. Password: <copy token above>"))
	fmt.Println()
	
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	holdTunnel(tunnelMgr, tunnelStateFor(t, rdsInstance.Identifier, pm.GetCurrentProfile()))

	return nil
}

func runConnectOpenSearch(cmd *cobra.Command, args []string) error {
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

	// Get OpenSearch domain
	var domain *aws.OpenSearchDomain

	if len(args) > 0 {
		domains, err := discovery.DiscoverOpenSearchDomains(ctx)
		if err != nil {
			return err
		}
		for _, d := range domains {
			if d.DomainName == args[0] {
				domain = &d
				break
			}
		}
		if domain == nil {
			return fmt.Errorf("OpenSearch domain %q not found", args[0])
		}
	} else {
		domains, err := discovery.DiscoverOpenSearchDomains(ctx)
		if err != nil {
			return err
		}
		if len(domains) == 0 {
			return fmt.Errorf("no OpenSearch domains found")
		}

		selected, err := tui.SelectOpenSearch(domains)
		if err != nil {
			return err
		}
		domain = selected
	}

	// Get jump host
	var selectedHost *aws.JumpHost
	jumpHost := connectVia
	if jumpHost == "" {
		jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
		if err != nil || len(jumpHosts) == 0 {
			return fmt.Errorf("no jump host found")
		}
		if len(jumpHosts) == 1 {
			selectedHost = &jumpHosts[0]
		} else {
			selectedHost, err = tui.SelectJumpHost(jumpHosts)
			if err != nil {
				return err
			}
		}
		jumpHost = selectedHost.ID
	}

	// Create tunnel manager
	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	// User-facing proxy port (default 9250 — Chrome blocks 9200)
	localPort, err := resolveLocalPort(connectLocalPort, 9250)
	if err != nil {
		return err
	}

	// Internal tunnel port behind the proxy
	tunnelPort := localPort + 50
	if !tunnel.PortAvailable(tunnelPort) {
		tunnelPort, err = tunnel.FindFreePort()
		if err != nil {
			return fmt.Errorf("failed to find free port: %w", err)
		}
	}

	if connectDetach {
		spec := tunnelSpec{
			Type:           string(tunnel.TunnelTypeOpenSearch),
			Target:         domain.DomainName,
			LocalPort:      tunnelPort,
			RemoteHost:     domain.Endpoint,
			RemotePort:     443,
			JumpHostID:     jumpHost,
			Profile:        pm.GetCurrentProfile(),
			DomainEndpoint: domain.Endpoint,
			ProxyPort:      localPort,
		}
		applyAutoStop(&spec, selectedHost, cfg)

		fmt.Printf("%s Starting background tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(domain.DomainName))
		st, err := spawnDetached(spec)
		if err != nil {
			return err
		}
		printDetached(st)
		fmt.Println()
		fmt.Printf("  %s http://localhost:%d\n", tui.DimStyle.Render("API:       "), st.LocalPort)
		fmt.Printf("  %s http://localhost:%d/_dashboards\n", tui.DimStyle.Render("Dashboards:"), st.LocalPort)
		return nil
	}

	fmt.Printf("%s Creating tunnel to %s...\n",
		tui.DimStyle.Render("►"),
		tui.TextStyle.Render(domain.DomainName))

	// Create SSM tunnel to OpenSearch HTTPS port on internal port
	osTunnel, err := tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       tunnel.TunnelTypeOpenSearch,
		LocalPort:  tunnelPort,
		RemoteHost: domain.Endpoint,
		RemotePort: 443,
		JumpHostID: jumpHost,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	registerAutoStopIfECS(osTunnel, selectedHost, cfg, discovery)

	fmt.Printf("%s Starting signing proxy...\n", tui.DimStyle.Render("►"))

	// Start signing proxy on user-facing port
	proxy, err := tunnel.NewOpenSearchProxy(tunnel.OpenSearchProxyConfig{
		AWSConfig:      pm.GetConfig(),
		Region:         pm.GetConfig().Region,
		ProfileName:    pm.GetCurrentProfile(),
		Endpoint:       fmt.Sprintf("localhost:%d", tunnelPort),
		DomainEndpoint: domain.Endpoint,
		LocalPort:      localPort,
		UseTunnel:      true,
	})
	if err != nil {
		tunnelMgr.CloseAll()
		return fmt.Errorf("failed to create signing proxy: %w", err)
	}

	if err := proxy.Start(ctx); err != nil {
		tunnelMgr.CloseAll()
		return fmt.Errorf("failed to start signing proxy: %w", err)
	}

	fmt.Println()
	fmt.Printf("%s Signing proxy active\n", tui.SuccessStyle.Render("✓"))
	fmt.Println()
	fmt.Println(tui.TitleStyle.Render("Connection Details"))
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("API:       "), proxy.LocalURL())
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("Dashboards:"), proxy.KibanaURL())
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("Domain:    "), domain.DomainName)
	fmt.Println()
	fmt.Println(tui.TitleStyle.Render("Browser Access"))
	fmt.Printf("  Open %s in your browser\n", tui.SuccessStyle.Render(proxy.KibanaURL()))
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("The proxy automatically signs all requests with AWS SigV4."))
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	// Register under the user-facing proxy port, not the internal tunnel port
	st := tunnelStateFor(osTunnel, domain.DomainName, pm.GetCurrentProfile())
	st.ID = fmt.Sprintf("%s-%d", tunnel.TunnelTypeOpenSearch, localPort)
	st.LocalPort = localPort
	_ = state.Write(st)

	waitForInterrupt()

	// Clean up
	_ = state.Remove(st.ID)
	proxy.Stop()
	tunnelMgr.CloseAll()
	fmt.Println(tui.DimStyle.Render("\nConnection closed"))

	return nil
}

func runConnectEC2(cmd *cobra.Command, args []string) error {
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

	// Get EC2 instance
	var instance *aws.EC2Instance

	if len(args) > 0 {
		instances, err := discovery.DiscoverEC2Instances(ctx)
		if err != nil {
			return err
		}
		for _, inst := range instances {
			if inst.InstanceID == args[0] {
				instance = &inst
				break
			}
		}
		if instance == nil {
			return fmt.Errorf("EC2 instance %q not found", args[0])
		}
	} else {
		instances, err := discovery.DiscoverEC2Instances(ctx)
		if err != nil {
			return err
		}
		if len(instances) == 0 {
			return fmt.Errorf("no EC2 instances found")
		}

		selected, err := tui.SelectEC2(instances)
		if err != nil {
			return err
		}
		instance = selected
	}

	return connectEC2Instance(ctx, pm, discovery, cfg, instance)
}

func runConnectEC2ByName(cmd *cobra.Command, namePattern string) error {
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

	// Discover all EC2 instances
	instances, err := discovery.DiscoverEC2Instances(ctx)
	if err != nil {
		return err
	}

	// Filter by name pattern (case-insensitive substring match)
	var matches []aws.EC2Instance
	for _, inst := range instances {
		if matchesNamePattern(inst.Name, namePattern) {
			matches = append(matches, inst)
		}
	}

	if len(matches) == 0 {
		return fmt.Errorf("no EC2 instances found matching pattern %q", namePattern)
	}

	// If single match, use it directly
	var instance *aws.EC2Instance
	if len(matches) == 1 {
		instance = &matches[0]
	} else {
		// Multiple matches - let user select
		selected, err := tui.SelectEC2(matches)
		if err != nil {
			return err
		}
		instance = selected
	}

	// Continue with the selected instance using existing EC2 logic
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	return connectEC2Instance(ctx, pm, discovery, cfg, instance)
}

// connectEC2Instance handles the shared EC2 connection flow (shell by default,
// port forwarding with --port-forward, optionally detached) for an already
// resolved instance.
func connectEC2Instance(ctx context.Context, pm *aws.ProfileManager, discovery *aws.Discovery, cfg *config.Config, instance *aws.EC2Instance) error {
	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())

	// Interactive shell mode (default, unless --port-forward)
	if !connectPortForward {
		if connectDetach {
			return fmt.Errorf("--detach requires --port-forward (an interactive shell cannot run in the background)")
		}
		if !instance.SSMEnabled {
			return fmt.Errorf("instance %s is not SSM-enabled. Interactive shell requires SSM", instance.InstanceID)
		}

		fmt.Printf("%s Opening interactive shell on %s",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID))
		if instance.Name != "" {
			fmt.Printf(" (%s)", tui.DimStyle.Render(instance.Name))
		}
		fmt.Println("...")
		fmt.Println()

		session, err := ssmMgr.StartInteractiveSession(ctx, instance.InstanceID)
		if err != nil {
			return fmt.Errorf("failed to start interactive session: %w", err)
		}

		<-session.Done()

		fmt.Println()
		fmt.Println(tui.DimStyle.Render("Session closed"))
		return nil
	}

	// Port forwarding mode
	localPort, err := resolveLocalPort(connectLocalPort, 0)
	if err != nil {
		return err
	}

	spec := tunnelSpec{
		Type:       string(tunnel.TunnelTypeEC2),
		Target:     instance.InstanceID,
		LocalPort:  localPort,
		RemotePort: connectRemotePort,
		Profile:    pm.GetCurrentProfile(),
	}
	var selectedHost *aws.JumpHost

	if connectDirect {
		if !instance.SSMEnabled {
			return fmt.Errorf("instance %s is not SSM-enabled. Use --via to specify a jump host", instance.InstanceID)
		}
		spec.Direct = true
		spec.TargetID = instance.InstanceID
	} else {
		var jumpHost string
		selectedHost, jumpHost, err = chooseJumpHost(ctx, discovery, cfg)
		if err != nil {
			return err
		}
		spec.RemoteHost = instance.PrivateIP
		spec.JumpHostID = jumpHost
	}

	if connectDetach {
		applyAutoStop(&spec, selectedHost, cfg)
		fmt.Printf("%s Starting background tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID))
		st, err := spawnDetached(spec)
		if err != nil {
			return err
		}
		printDetached(st)
		return nil
	}

	if spec.Direct {
		fmt.Printf("%s Creating direct tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID))
	} else {
		fmt.Printf("%s Creating tunnel to %s via %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID),
			tui.DimStyle.Render(spec.JumpHostID))
	}

	tunnelMgr := tunnel.NewManager(ssmMgr)
	t, err := tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       tunnel.TunnelTypeEC2,
		LocalPort:  spec.LocalPort,
		RemoteHost: spec.RemoteHost,
		RemotePort: spec.RemotePort,
		JumpHostID: spec.JumpHostID,
		Direct:     spec.Direct,
		TargetID:   spec.TargetID,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	registerAutoStopIfECS(t, selectedHost, cfg, discovery)

	fmt.Println()
	fmt.Printf("%s Tunnel active\n", tui.SuccessStyle.Render("✓"))
	fmt.Printf("  %s localhost:%d → %s:%d\n",
		tui.DimStyle.Render("Port:"),
		t.LocalPort,
		instance.PrivateIP,
		connectRemotePort)
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	holdTunnel(tunnelMgr, tunnelStateFor(t, instance.InstanceID, pm.GetCurrentProfile()))

	return nil
}

// matchesNamePattern checks if an instance name matches a pattern (case-insensitive substring match)
func matchesNamePattern(instanceName, pattern string) bool {
	if instanceName == "" {
		return false
	}
	// Convert both to lowercase for case-insensitive matching
	return containsIgnoreCase(instanceName, pattern)
}

// containsIgnoreCase checks if s contains substr (case-insensitive)
func containsIgnoreCase(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	return len(s) >= len(substr) && (s == substr || containsSubstring(s, substr))
}

func toLower(s string) string {
	// Simple ASCII lowercase (good enough for EC2 names)
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func runConnectPreset(cmd *cobra.Command, args []string) error {
	// If no args, show help
	if len(args) == 0 {
		return cmd.Help()
	}

	presetName := args[0]

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Look up connection preset
	conn, exists := cfg.Connections[presetName]
	if !exists {
		return fmt.Errorf("connection preset %q not found in config", presetName)
	}

	// Check AWS profile if specified in preset
	if conn.AWSProfile != "" {
		currentProfile := os.Getenv("AWS_PROFILE")
		hasEnvCreds := os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
		
		// If we have environment credentials and AWS_PROFILE is set correctly, we're good
		// (This handles both manual `assume` and our auto-switch via `assume --exec`)
		if hasEnvCreds && currentProfile == conn.AWSProfile {
			// Already authenticated with correct profile via Granted
			viper.Set("profile", conn.AWSProfile)
		} else if currentProfile != conn.AWSProfile {
			// Profile mismatch - try to switch using Granted
			
			// Check if we're already being executed via Granted (to avoid infinite recursion)
			if os.Getenv("GRANTED_EXEC") == "true" {
				// Already in Granted exec, something went wrong
				return fmt.Errorf("profile mismatch: expected '%s', got '%s'", conn.AWSProfile, currentProfile)
			}
			
			// Try to use Granted to switch profiles
			if err := reexecWithGranted(conn.AWSProfile, presetName); err != nil {
				// Granted not available or failed
				return fmt.Errorf("this preset requires profile '%s', but you're using '%s'\n\nRun: assume %s\n\n(Or install Granted for automatic profile switching)", 
					conn.AWSProfile, currentProfile, conn.AWSProfile)
			}
			// If reexecWithGranted returns, it means it executed successfully
			// This line won't be reached as the process will be replaced
			return nil
		} else {
			// Profile matches but no env creds (standard AWS SSO)
			viper.Set("profile", conn.AWSProfile)
		}
	}

	// Set flags from preset
	if conn.LocalPort > 0 {
		connectLocalPort = conn.LocalPort
	}
	if conn.Via != "" {
		connectVia = conn.Via
	}
	if conn.Direct {
		connectDirect = conn.Direct
	}
	if conn.Detach {
		connectDetach = true
	}

	// Route to appropriate subcommand
	switch conn.Type {
	case "rds":
		if conn.DBUser != "" {
			connectDBUser = conn.DBUser
		}
		if conn.DBName != "" {
			connectDBName = conn.DBName
		}
		if conn.Exec {
			connectExec = true
		}
		return runConnectRDS(cmd, []string{conn.Identifier})
	
	case "opensearch":
		if conn.KibanaPort > 0 {
			connectKibanaPort = conn.KibanaPort
		}
		return runConnectOpenSearch(cmd, []string{conn.Domain})
	
	case "ec2":
		if conn.RemotePort > 0 {
			connectRemotePort = conn.RemotePort
		}
		
		// Handle connection type (default: shell)
		connectionType := conn.ConnectionType
		if connectionType == "" {
			connectionType = "shell" // Default to shell
		}
		
		if connectionType == "port_forward" {
			connectPortForward = true
		}
		
		// Handle instance lookup by name pattern or ID
		if conn.NamePattern != "" {
			return runConnectEC2ByName(cmd, conn.NamePattern)
		}
		
		if conn.Instance != "" {
			return runConnectEC2(cmd, []string{conn.Instance})
		}
		
		return fmt.Errorf("EC2 preset %q must specify either 'instance' or 'name_pattern'", presetName)

	case "redis", "elasticache":
		return runConnectEndpoint(elastiCacheKind, []string{conn.Identifier})

	case "docdb":
		return runConnectEndpoint(docDBKind, []string{conn.Identifier})

	case "msk", "kafka":
		return runConnectEndpoint(mskKind, []string{conn.Identifier})

	default:
		return fmt.Errorf("unknown connection type %q for preset %q", conn.Type, presetName)
	}
}

// safeNamePattern allows only characters valid in AWS profile and preset names.
// reexecWithGranted interpolates these into a `sh -c` string, so anything outside
// this set (shell metacharacters, spaces, quotes) is rejected to prevent injection.
var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// reexecWithGranted re-executes TunnelBoy via Granted's assume --exec
func reexecWithGranted(profile string, presetName string) error {
	if !safeNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid profile name %q: only letters, digits, '.', '_' and '-' are allowed", profile)
	}
	if !safeNamePattern.MatchString(presetName) {
		return fmt.Errorf("invalid preset name %q: only letters, digits, '.', '_' and '-' are allowed", presetName)
	}

	// Get the user's shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh" // Default to zsh on macOS
	}

	// Get the path to the current tunnelboy executable
	tunnelboyPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Build the shell command to invoke Granted's assume function
	// We need to source the shell init files to get the assume alias/function.
	// Propagate --detach so a background launch stays a background launch
	// through the re-exec (the dashboard depends on this).
	detachFlag := ""
	if connectDetach {
		detachFlag = " --detach"
	}
	shellCmd := fmt.Sprintf("source ~/.zshenv 2>/dev/null; source ~/.zshrc 2>/dev/null; assume %s --exec -- %s connect %s%s",
		profile, tunnelboyPath, presetName, detachFlag)

	// Create command - invoke through shell. profile and presetName are validated
	// against safeNamePattern above; tunnelboyPath is from os.Executable, not user
	// input — so the interpolated command carries no untrusted shell metacharacters.
	cmd := exec.Command(shell, "-c", shellCmd) // #nosec G702,G204 -- inputs validated against safeNamePattern; no untrusted data reaches the shell
	
	// Set environment variable to prevent infinite recursion
	cmd.Env = append(os.Environ(), "GRANTED_EXEC=true")
	
	// Pass through stdin, stdout, stderr
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run and wait
	fmt.Printf("%s Switching to profile '%s' via Granted...\n", tui.DimStyle.Render("►"), profile)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to execute via Granted: %w", err)
	}

	// Exit the current process (the re-exec has completed)
	os.Exit(0)
	return nil
}

// tunnelStateFor builds the state-dir record for a foreground tunnel.
func tunnelStateFor(t *tunnel.Tunnel, target, profile string) state.TunnelState {
	return state.TunnelState{
		ID:         t.ID,
		PID:        os.Getpid(),
		Type:       string(t.Type),
		Target:     target,
		LocalPort:  t.LocalPort,
		RemoteHost: t.RemoteHost,
		RemotePort: t.RemotePort,
		JumpHost:   t.JumpHost,
		Profile:    profile,
		StartedAt:  time.Now(),
	}
}

// holdTunnel registers a foreground tunnel in the state dir so `tunnelboy
// tunnels`/`disconnect` can see it, waits for Ctrl+C/SIGTERM, then cleans up.
// Status transitions (reconnecting/active/disconnected) are mirrored into the
// state file so the dashboard shows real health.
func holdTunnel(tunnelMgr *tunnel.Manager, st state.TunnelState) {
	st.Status = "active"
	_ = state.Write(st)
	tunnelMgr.SetStatusCallback(func(_, status string) {
		st.Status = status
		st.UpdatedAt = time.Now()
		_ = state.Write(st)
	})
	waitForInterrupt()
	_ = state.Remove(st.ID)
	tunnelMgr.CloseAll()
	fmt.Println(tui.DimStyle.Render("\nTunnel closed"))
}

// applyAutoStop copies ECS auto-stop teardown info into a detach spec when the
// parent auto-started the jump host task and the config asks for teardown.
func applyAutoStop(spec *tunnelSpec, host *aws.JumpHost, cfg *config.Config) {
	if host == nil || host.Type != "ecs" || host.StartedTaskARN == "" || host.ClusterName == "" {
		return
	}
	if !cfg.JumpHosts.ECSAutoStop {
		return
	}
	spec.AutoStopCluster = host.ClusterName
	spec.AutoStopTaskARN = host.StartedTaskARN
}

// printDetached tells the user how to reach and manage a background tunnel.
func printDetached(st *state.TunnelState) {
	fmt.Println()
	fmt.Printf("%s Tunnel running in background\n", tui.SuccessStyle.Render("✓"))
	fmt.Printf("  %s %s\n", tui.DimStyle.Render("ID:        "), st.ID)
	fmt.Printf("  %s localhost:%d\n", tui.DimStyle.Render("Endpoint:  "), st.LocalPort)
	fmt.Printf("  %s %d\n", tui.DimStyle.Render("PID:       "), st.PID)
	if st.LogFile != "" {
		fmt.Printf("  %s %s\n", tui.DimStyle.Render("Log:       "), st.LogFile)
	}
	fmt.Println()
	fmt.Println(tui.DimStyle.Render(fmt.Sprintf("Disconnect with: tunnelboy disconnect %s", st.ID)))
}

// resolveLocalPort picks the local port to bind. requested is the value of
// --local-port (0 = not set, use fallback). An explicitly requested port that
// is busy is an error; a busy fallback silently degrades to a free port with
// a notice, since the user expressed no preference.
func resolveLocalPort(requested, fallback int) (int, error) {
	if requested != 0 {
		if !tunnel.PortAvailable(requested) {
			return 0, fmt.Errorf("local port %d is already in use (pick another with --local-port, or omit the flag to auto-assign)", requested)
		}
		return requested, nil
	}
	if fallback != 0 && tunnel.PortAvailable(fallback) {
		return fallback, nil
	}
	port, err := tunnel.FindFreePort()
	if err != nil {
		return 0, fmt.Errorf("failed to find free port: %w", err)
	}
	if fallback != 0 {
		fmt.Printf("%s Port %d is in use, using %d instead\n", tui.WarningStyle.Render("⚠"), fallback, port)
	}
	return port, nil
}

func waitForInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}

// Shell completion functions

func completeConnectionPresets(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var completions []string
	for name, conn := range cfg.Connections {
		var desc string
		
		// Use custom description if provided
		if conn.Description != "" {
			desc = fmt.Sprintf("%s\t%s", name, conn.Description)
		} else {
			// Auto-generate description from connection details
			desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Identifier)
			if conn.Type == "opensearch" {
				desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Domain)
			} else if conn.Type == "ec2" {
				if conn.NamePattern != "" {
					desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.NamePattern)
				} else {
					desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Instance)
				}
			}
		}
		
		completions = append(completions, desc)
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

func completeRDSInstances(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := context.Background()
	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	discovery := aws.NewDiscovery(pm.GetConfig())
	instances, err := discovery.DiscoverRDSInstances(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var completions []string
	for _, instance := range instances {
		desc := fmt.Sprintf("%s\t%s %s", instance.Identifier, instance.Engine, instance.InstanceClass)
		completions = append(completions, desc)
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

func completeOpenSearchDomains(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := context.Background()
	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	discovery := aws.NewDiscovery(pm.GetConfig())
	domains, err := discovery.DiscoverOpenSearchDomains(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var completions []string
	for _, domain := range domains {
		desc := fmt.Sprintf("%s\t%s", domain.DomainName, domain.EngineVersion)
		completions = append(completions, desc)
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

func completeEC2Instances(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := context.Background()
	pm := aws.NewProfileManager()
	profileName := viper.GetString("profile")
	
	if err := pm.LoadProfile(ctx, profileName); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	discovery := aws.NewDiscovery(pm.GetConfig())
	instances, err := discovery.DiscoverEC2Instances(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	var completions []string
	for _, instance := range instances {
		desc := fmt.Sprintf("%s\t%s", instance.InstanceID, instance.Name)
		completions = append(completions, desc)
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}
