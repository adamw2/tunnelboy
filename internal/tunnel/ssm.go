package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// SSMManager manages SSM sessions
type SSMManager struct {
	cfg     aws.Config
	profile string
	region  string
}

// NewSSMManager creates a new SSM manager
func NewSSMManager(cfg aws.Config, profile string) *SSMManager {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	return &SSMManager{
		cfg:     cfg,
		profile: profile,
		region:  region,
	}
}

// SSMSession represents an active SSM session
type SSMSession struct {
	SessionID string
	cmd       *exec.Cmd
	done      chan struct{}
}

// Done returns a channel that's closed when the session ends
func (s *SSMSession) Done() <-chan struct{} {
	return s.done
}

// Close terminates the SSM session
func (s *SSMSession) Close() error {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

// SSMPortForwardConfig configures direct port forwarding
type SSMPortForwardConfig struct {
	TargetID   string // EC2 instance ID or ECS task ARN
	LocalPort  int
	RemotePort int
}

// SSMRemotePortForwardConfig configures port forwarding to a remote host through a jump host
type SSMRemotePortForwardConfig struct {
	JumpHostID string // EC2 instance ID or ECS task ARN
	LocalPort  int
	RemoteHost string // Target host (e.g., RDS endpoint)
	RemotePort int
}

// CheckSessionManagerPlugin checks if the SSM plugin is installed
func CheckSessionManagerPlugin() error {
	_, err := exec.LookPath("session-manager-plugin")
	if err != nil {
		return fmt.Errorf("session-manager-plugin not found. Install with: brew install --cask session-manager-plugin")
	}
	return nil
}

// StartPortForward starts a direct port forwarding session
func (m *SSMManager) StartPortForward(ctx context.Context, cfg SSMPortForwardConfig) (*SSMSession, error) {
	if err := CheckSessionManagerPlugin(); err != nil {
		return nil, err
	}

	// Create SSM client
	ssmClient := ssm.NewFromConfig(m.cfg)

	// Start session via API
	input := &ssm.StartSessionInput{
		Target:       aws.String(cfg.TargetID),
		DocumentName: aws.String("AWS-StartPortForwardingSession"),
		Parameters: map[string][]string{
			"portNumber":      {fmt.Sprintf("%d", cfg.RemotePort)},
			"localPortNumber": {fmt.Sprintf("%d", cfg.LocalPort)},
		},
	}

	output, err := ssmClient.StartSession(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}

	// Start session-manager-plugin
	session, err := m.runSessionPlugin(ctx, output, cfg.TargetID, input, false)
	if err != nil {
		return nil, err
	}

	session.SessionID = aws.ToString(output.SessionId)
	return session, nil
}

// StartRemotePortForward starts port forwarding through a jump host to a remote host
func (m *SSMManager) StartRemotePortForward(ctx context.Context, cfg SSMRemotePortForwardConfig) (*SSMSession, error) {
	if err := CheckSessionManagerPlugin(); err != nil {
		return nil, err
	}

	// Create SSM client
	ssmClient := ssm.NewFromConfig(m.cfg)

	// Start session via API using AWS-StartPortForwardingSessionToRemoteHost
	input := &ssm.StartSessionInput{
		Target:       aws.String(cfg.JumpHostID),
		DocumentName: aws.String("AWS-StartPortForwardingSessionToRemoteHost"),
		Parameters: map[string][]string{
			"host":            {cfg.RemoteHost},
			"portNumber":      {fmt.Sprintf("%d", cfg.RemotePort)},
			"localPortNumber": {fmt.Sprintf("%d", cfg.LocalPort)},
		},
	}

	output, err := ssmClient.StartSession(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}

	// Start session-manager-plugin
	session, err := m.runSessionPlugin(ctx, output, cfg.JumpHostID, input, false)
	if err != nil {
		return nil, err
	}

	session.SessionID = aws.ToString(output.SessionId)
	return session, nil
}

// StartInteractiveSession starts an interactive shell session
func (m *SSMManager) StartInteractiveSession(ctx context.Context, targetID string) (*SSMSession, error) {
	if err := CheckSessionManagerPlugin(); err != nil {
		return nil, err
	}

	// Create SSM client
	ssmClient := ssm.NewFromConfig(m.cfg)

	// Start session via API - no document name means interactive shell
	input := &ssm.StartSessionInput{
		Target: aws.String(targetID),
	}

	output, err := ssmClient.StartSession(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}

	// Start session-manager-plugin in interactive mode
	session, err := m.runSessionPlugin(ctx, output, targetID, input, true)
	if err != nil {
		return nil, err
	}

	session.SessionID = aws.ToString(output.SessionId)
	return session, nil
}

// runSessionPlugin executes the session-manager-plugin binary
func (m *SSMManager) runSessionPlugin(ctx context.Context, output *ssm.StartSessionOutput, target string, input *ssm.StartSessionInput, interactive bool) (*SSMSession, error) {
	// Convert session output to JSON for the plugin
	sessionJSON, err := json.Marshal(map[string]string{
		"SessionId":  aws.ToString(output.SessionId),
		"TokenValue": aws.ToString(output.TokenValue),
		"StreamUrl":  aws.ToString(output.StreamUrl),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	// Convert input parameters to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build endpoint URL
	endpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", m.region)

	// Run session-manager-plugin
	// session-manager-plugin <session-json> <region> StartSession <profile> <parameters-json> <endpoint>
	args := []string{
		string(sessionJSON),
		m.region,
		"StartSession",
		m.profile,
		string(inputJSON),
		endpoint,
	}

	cmd := exec.CommandContext(ctx, "session-manager-plugin", args...) // #nosec G204 -- fixed binary, args passed as argv slice (no shell), so no injection
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	// For interactive sessions, connect stdin
	if interactive {
		cmd.Stdin = os.Stdin
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start session-manager-plugin: %w", err)
	}

	done := make(chan struct{})
	session := &SSMSession{
		cmd:  cmd,
		done: done,
	}

	// Wait for process in background
	go func() {
		cmd.Wait()
		close(done)
	}()

	return session, nil
}

// FormatTargetForECS formats an ECS task ARN for SSM
func FormatTargetForECS(clusterName, taskID, containerName string) string {
	// Format: ecs:cluster-name_task-id_container-runtime-id
	// For execute-command, the format is: ecs:cluster_task_container
	return fmt.Sprintf("ecs:%s_%s_%s", clusterName, taskID, containerName)
}

// IsECSTarget checks if the target is an ECS task
func IsECSTarget(target string) bool {
	return strings.HasPrefix(target, "ecs:") || strings.Contains(target, ":task/")
}
