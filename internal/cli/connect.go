package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/tui"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

var (
	connectLocalPort    int
	connectRemotePort   int
	connectVia          string
	connectDirect       bool
	connectDBUser       string
	connectKibanaPort   int
	connectPrintToken   bool
	connectShell        bool
	connectPortForward  bool
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

	// OpenSearch flags
	connectOpenSearchCmd.Flags().IntVar(&connectLocalPort, "local-port", 9250, "local port for API (Chrome blocks 9200)")
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

	// Get database user
	dbUser := connectDBUser
	if dbUser == "" {
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
	jumpHost := connectVia
	if jumpHost == "" {
		jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
		if err != nil || len(jumpHosts) == 0 {
			return fmt.Errorf("no jump host found. Configure jump_hosts in ~/.tunnelboy.yaml or use --via")
		}
		if len(jumpHosts) == 1 {
			jumpHost = jumpHosts[0].ID
		} else {
			selected, err := tui.SelectJumpHost(jumpHosts)
			if err != nil {
				return err
			}
			jumpHost = selected.ID
		}
	}

	// Create tunnel
	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	localPort := connectLocalPort
	if localPort == 0 {
		localPort = int(rdsInstance.Port) // Use same port as RDS
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

	// Wait for interrupt
	waitForInterrupt()
	tunnelMgr.CloseAll()
	fmt.Println(tui.DimStyle.Render("\nTunnel closed"))

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
	jumpHost := connectVia
	if jumpHost == "" {
		jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
		if err != nil || len(jumpHosts) == 0 {
			return fmt.Errorf("no jump host found")
		}
		if len(jumpHosts) == 1 {
			jumpHost = jumpHosts[0].ID
		} else {
			selected, err := tui.SelectJumpHost(jumpHosts)
			if err != nil {
				return err
			}
			jumpHost = selected.ID
		}
	}

	// Create tunnel manager
	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())
	tunnelMgr := tunnel.NewManager(ssmMgr)

	// Set default local port if not specified (user-facing proxy port)
	localPort := connectLocalPort
	if localPort == 0 {
		localPort = 9250 // Default to 9250 (Chrome blocks 9200)
	}

	// Calculate tunnel port (internal, higher port)
	tunnelPort := localPort + 50

	fmt.Printf("%s Creating tunnel to %s...\n",
		tui.DimStyle.Render("►"),
		tui.TextStyle.Render(domain.DomainName))

	// Create SSM tunnel to OpenSearch HTTPS port on internal port
	_, err = tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
		Type:       tunnel.TunnelTypeOpenSearch,
		LocalPort:  tunnelPort,
		RemoteHost: domain.Endpoint,
		RemotePort: 443,
		JumpHostID: jumpHost,
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

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

	// Wait for interrupt
	waitForInterrupt()

	// Clean up
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

	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())

	// Determine connection mode: shell is default, unless --port-forward is specified
	useShell := !connectPortForward
	
	// Handle interactive shell mode (default)
	if useShell {
		// Shell mode only works with direct SSM connection
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

		// Start interactive session
		session, err := ssmMgr.StartInteractiveSession(ctx, instance.InstanceID)
		if err != nil {
			return fmt.Errorf("failed to start interactive session: %w", err)
		}

		// Wait for session to end
		<-session.Done()
		
		fmt.Println()
		fmt.Println(tui.DimStyle.Render("Session closed"))
		return nil
	}

	// Port forwarding mode
	tunnelMgr := tunnel.NewManager(ssmMgr)
	var t *tunnel.Tunnel

	if connectDirect {
		// Direct SSM connection
		if !instance.SSMEnabled {
			return fmt.Errorf("instance %s is not SSM-enabled. Use --via to specify a jump host", instance.InstanceID)
		}

		fmt.Printf("%s Creating direct tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID))

		t, err = tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
			Type:       tunnel.TunnelTypeEC2,
			LocalPort:  connectLocalPort,
			RemotePort: connectRemotePort,
			Direct:     true,
			TargetID:   instance.InstanceID,
		})
	} else {
		// Via jump host
		jumpHost := connectVia
		if jumpHost == "" {
			jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
			if err != nil || len(jumpHosts) == 0 {
				return fmt.Errorf("no jump host found. Use --direct if target has SSM, or --via to specify jump host")
			}
			if len(jumpHosts) == 1 {
				jumpHost = jumpHosts[0].ID
			} else {
				selected, err := tui.SelectJumpHost(jumpHosts)
				if err != nil {
					return err
				}
				jumpHost = selected.ID
			}
		}

		fmt.Printf("%s Creating tunnel to %s via %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID),
			tui.DimStyle.Render(jumpHost))

		t, err = tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
			Type:       tunnel.TunnelTypeEC2,
			LocalPort:  connectLocalPort,
			RemoteHost: instance.PrivateIP,
			RemotePort: connectRemotePort,
			JumpHostID: jumpHost,
		})
	}

	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	fmt.Println()
	fmt.Printf("%s Tunnel active\n", tui.SuccessStyle.Render("✓"))
	fmt.Printf("  %s localhost:%d → %s:%d\n",
		tui.DimStyle.Render("Port:"),
		t.LocalPort,
		instance.PrivateIP,
		connectRemotePort)
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	waitForInterrupt()
	tunnelMgr.CloseAll()
	fmt.Println(tui.DimStyle.Render("\nTunnel closed"))

	return nil
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

	ssmMgr := tunnel.NewSSMManager(pm.GetConfig(), pm.GetCurrentProfile())

	// Determine connection mode: shell is default, unless --port-forward is specified
	useShell := !connectPortForward
	
	// Handle interactive shell mode (default)
	if useShell {
		// Shell mode only works with direct SSM connection
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

		// Start interactive session
		session, err := ssmMgr.StartInteractiveSession(ctx, instance.InstanceID)
		if err != nil {
			return fmt.Errorf("failed to start interactive session: %w", err)
		}

		// Wait for session to end
		<-session.Done()
		
		fmt.Println()
		fmt.Println(tui.DimStyle.Render("Session closed"))
		return nil
	}

	// Port forwarding mode
	tunnelMgr := tunnel.NewManager(ssmMgr)
	var t *tunnel.Tunnel

	if connectDirect {
		// Direct SSM connection
		if !instance.SSMEnabled {
			return fmt.Errorf("instance %s is not SSM-enabled. Use --via to specify a jump host", instance.InstanceID)
		}

		fmt.Printf("%s Creating direct tunnel to %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID))

		t, err = tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
			Type:       tunnel.TunnelTypeEC2,
			LocalPort:  connectLocalPort,
			RemotePort: connectRemotePort,
			Direct:     true,
			TargetID:   instance.InstanceID,
		})
	} else {
		// Via jump host
		jumpHost := connectVia
		if jumpHost == "" {
			jumpHosts, err := discovery.DiscoverJumpHosts(ctx, cfg)
			if err != nil || len(jumpHosts) == 0 {
				return fmt.Errorf("no jump host found. Use --direct if target has SSM, or --via to specify jump host")
			}
			if len(jumpHosts) == 1 {
				jumpHost = jumpHosts[0].ID
			} else {
				selected, err := tui.SelectJumpHost(jumpHosts)
				if err != nil {
					return err
				}
				jumpHost = selected.ID
			}
		}

		fmt.Printf("%s Creating tunnel to %s via %s...\n",
			tui.DimStyle.Render("►"),
			tui.TextStyle.Render(instance.InstanceID),
			tui.DimStyle.Render(jumpHost))

		t, err = tunnelMgr.CreateTunnel(ctx, tunnel.TunnelConfig{
			Type:       tunnel.TunnelTypeEC2,
			LocalPort:  connectLocalPort,
			RemoteHost: instance.PrivateIP,
			RemotePort: connectRemotePort,
			JumpHostID: jumpHost,
		})
	}

	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	fmt.Println()
	fmt.Printf("%s Tunnel active\n", tui.SuccessStyle.Render("✓"))
	fmt.Printf("  %s localhost:%d → %s:%d\n",
		tui.DimStyle.Render("Port:"),
		t.LocalPort,
		instance.PrivateIP,
		connectRemotePort)
	fmt.Println()
	fmt.Println(tui.DimStyle.Render("Press Ctrl+C to disconnect"))

	waitForInterrupt()
	tunnelMgr.CloseAll()
	fmt.Println(tui.DimStyle.Render("\nTunnel closed"))

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

	// Route to appropriate subcommand
	switch conn.Type {
	case "rds":
		if conn.DBUser != "" {
			connectDBUser = conn.DBUser
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
	
	default:
		return fmt.Errorf("unknown connection type %q for preset %q", conn.Type, presetName)
	}
}

// reexecWithGranted re-executes TunnelBoy via Granted's assume --exec
func reexecWithGranted(profile string, presetName string) error {
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
	// We need to source the shell init files to get the assume alias/function
	shellCmd := fmt.Sprintf("source ~/.zshenv 2>/dev/null; source ~/.zshrc 2>/dev/null; assume %s --exec -- %s connect %s",
		profile, tunnelboyPath, presetName)

	// Create command - invoke through shell
	cmd := exec.Command(shell, "-c", shellCmd)
	
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
		desc := fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Identifier)
		if conn.Type == "opensearch" {
			desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Domain)
		} else if conn.Type == "ec2" {
			desc = fmt.Sprintf("%s\t%s: %s", name, conn.Type, conn.Instance)
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
