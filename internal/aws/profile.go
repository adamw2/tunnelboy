package aws

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"gopkg.in/ini.v1"
)

// ProfileInfo contains information about an AWS profile
type ProfileInfo struct {
	Name      string
	Region    string
	SSOStart  string // SSO start URL if SSO profile
	IsSSO     bool
	AccountID string // Only populated after GetIdentity
	Arn       string // Only populated after GetIdentity
	UserID    string // Only populated after GetIdentity
}

// ProfileManager handles AWS profile operations
type ProfileManager struct {
	currentProfile string
	cfg            aws.Config
}

// NewProfileManager creates a new profile manager
func NewProfileManager() *ProfileManager {
	return &ProfileManager{}
}

// LoadProfile loads an AWS profile configuration
func (pm *ProfileManager) LoadProfile(ctx context.Context, profileName string) error {
	opts := []func(*config.LoadOptions) error{}
	
	// Set AWS_PROFILE environment variable
	if profileName != "" {
		os.Setenv("AWS_PROFILE", profileName)
	}
	
	// Check if we're inside Granted --exec
	if os.Getenv("GRANTED_EXEC") == "true" {
		// Just load default config - environment credentials are already set by Granted
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}
		pm.cfg = cfg
		pm.currentProfile = profileName
		return nil
	}
	
	// Standard profile loading for non-Granted workflows
	if profileName != "" {
		opts = append(opts, config.WithSharedConfigProfile(profileName))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	pm.cfg = cfg
	pm.currentProfile = profileName
	return nil
}

// GetConfig returns the current AWS config
func (pm *ProfileManager) GetConfig() aws.Config {
	return pm.cfg
}

// GetCurrentProfile returns the current profile name
func (pm *ProfileManager) GetCurrentProfile() string {
	if pm.currentProfile != "" {
		return pm.currentProfile
	}
	// Check environment
	if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		return profile
	}
	return "default"
}

// GetIdentity returns the current AWS identity
func (pm *ProfileManager) GetIdentity(ctx context.Context) (*ProfileInfo, error) {
	stsClient := sts.NewFromConfig(pm.cfg)

	result, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get caller identity: %w", err)
	}

	return &ProfileInfo{
		Name:      pm.GetCurrentProfile(),
		Region:    pm.cfg.Region,
		AccountID: aws.ToString(result.Account),
		Arn:       aws.ToString(result.Arn),
		UserID:    aws.ToString(result.UserId),
	}, nil
}

// ListProfiles lists all available AWS profiles
func ListProfiles() ([]ProfileInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(home, ".aws", "config")
	credentialsPath := filepath.Join(home, ".aws", "credentials")

	profiles := make(map[string]*ProfileInfo)

	// Parse config file
	if _, err := os.Stat(configPath); err == nil {
		cfg, err := ini.Load(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AWS config: %w", err)
		}

		for _, section := range cfg.Sections() {
			name := section.Name()
			if name == "DEFAULT" {
				continue
			}

			// Remove "profile " prefix
			profileName := name
			if len(name) > 8 && name[:8] == "profile " {
				profileName = name[8:]
			}

			info := &ProfileInfo{
				Name:   profileName,
				Region: section.Key("region").String(),
			}

			// Check if SSO profile
			if ssoStart := section.Key("sso_start_url").String(); ssoStart != "" {
				info.IsSSO = true
				info.SSOStart = ssoStart
			}

			profiles[profileName] = info
		}
	}

	// Parse credentials file for additional profiles
	if _, err := os.Stat(credentialsPath); err == nil {
		creds, err := ini.Load(credentialsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AWS credentials: %w", err)
		}

		for _, section := range creds.Sections() {
			name := section.Name()
			if name == "DEFAULT" {
				continue
			}

			if _, exists := profiles[name]; !exists {
				profiles[name] = &ProfileInfo{
					Name: name,
				}
			}
		}
	}

	// Convert map to slice
	result := make([]ProfileInfo, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, *p)
	}

	return result, nil
}

// SetProfile sets the AWS_PROFILE environment variable
func SetProfile(profileName string) error {
	return os.Setenv("AWS_PROFILE", profileName)
}
