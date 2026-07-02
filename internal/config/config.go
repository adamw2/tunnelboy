package config

import (
	"os"

	"github.com/spf13/viper"
)

// Config represents the application configuration
type Config struct {
	DefaultProfile string     `mapstructure:"default_profile"`
	JumpHosts      JumpHosts  `mapstructure:"jump_hosts"`
	Connections    map[string]Connection `mapstructure:"connections"`
}

// JumpHosts defines how to discover jump hosts
type JumpHosts struct {
	Patterns  []string   `mapstructure:"patterns"`
	Tags      []TagFilter `mapstructure:"tags"`
	Instances []string   `mapstructure:"instances"`
	ECS       []ECSTarget `mapstructure:"ecs"`

	// Prefer constrains discovery to a single host type. Currently only "ecs"
	// is meaningful — it skips all EC2 branches (explicit instances, tags,
	// patterns) so a bastion can never be picked even if it would otherwise
	// match. Empty string keeps the default mixed behavior.
	Prefer string `mapstructure:"prefer"`

	// ECSAutoStart enables scaling an ECS service to 1 when no running task is
	// found during discovery. Pointer so users can explicitly disable; nil means
	// use the default (true).
	ECSAutoStart *bool `mapstructure:"ecs_auto_start"`
	// ECSAutoStop scales the service back to 0 on tunnel close. Off by default
	// since most tunnel tasks self-terminate on idle.
	ECSAutoStop bool `mapstructure:"ecs_auto_stop"`
}

// PreferECS reports whether discovery should restrict itself to ECS hosts.
func (j JumpHosts) PreferECS() bool {
	return j.Prefer == "ecs"
}

// AutoStartEnabled reports whether ECS auto-start should run during discovery.
// Defaults to true when unset.
func (j JumpHosts) AutoStartEnabled() bool {
	if j.ECSAutoStart == nil {
		return true
	}
	return *j.ECSAutoStart
}

// TagFilter represents an AWS tag filter
type TagFilter struct {
	Key   string `mapstructure:"key"`
	Value string `mapstructure:"value"`
}

// ECSTarget represents an ECS task target
type ECSTarget struct {
	Cluster string `mapstructure:"cluster"`
	Service string `mapstructure:"service"`
}

// Connection represents a saved connection preset
type Connection struct {
	Type           string `mapstructure:"type"`            // rds, opensearch, ec2
	Description    string `mapstructure:"description"`     // Custom description for shell completion
	Identifier     string `mapstructure:"identifier"`      // RDS identifier or OpenSearch domain
	Instance       string `mapstructure:"instance"`        // EC2 instance ID
	NamePattern    string `mapstructure:"name_pattern"`    // EC2 name pattern (alternative to instance)
	Domain         string `mapstructure:"domain"`          // OpenSearch domain name
	DBUser         string `mapstructure:"db_user"`         // Database user for RDS
	DBName         string `mapstructure:"db_name"`         // Database name (for exec)
	Exec           bool   `mapstructure:"exec"`            // Launch DB client after connecting (RDS)
	AWSProfile     string `mapstructure:"aws_profile"`     // AWS profile to use for this connection
	ConnectionType string `mapstructure:"connection_type"` // shell or port_forward (for EC2, default: shell)
	LocalPort      int    `mapstructure:"local_port"`
	RemotePort     int    `mapstructure:"remote_port"`
	KibanaPort     int    `mapstructure:"kibana_port"`
	Via            string `mapstructure:"via"`    // Jump host instance ID
	Direct         bool   `mapstructure:"direct"` // Direct SSM connection
	Detach         bool   `mapstructure:"detach"` // Run tunnel in the background
}

// Load loads the configuration from viper
func Load() (*Config, error) {
	var cfg Config

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Set defaults if not configured
	if len(cfg.JumpHosts.Patterns) == 0 && 
	   len(cfg.JumpHosts.Tags) == 0 && 
	   len(cfg.JumpHosts.Instances) == 0 &&
	   len(cfg.JumpHosts.ECS) == 0 {
		cfg.JumpHosts.Patterns = []string{"*bastion*", "*jump*"}
	}

	return &cfg, nil
}

// GetConnection returns a saved connection by name
func (c *Config) GetConnection(name string) (*Connection, bool) {
	if c.Connections == nil {
		return nil, false
	}
	conn, ok := c.Connections[name]
	if !ok {
		return nil, false
	}
	return &conn, true
}

// GetEffectiveProfile returns the profile to use
// Priority: 1. CLI flag, 2. AWS_PROFILE env var, 3. config file default
func (c *Config) GetEffectiveProfile() string {
	// Check viper for CLI override first (highest priority)
	if profile := viper.GetString("profile"); profile != "" {
		return profile
	}
	// Check AWS_PROFILE environment variable (medium priority)
	if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		return profile
	}
	// Fall back to config default (lowest priority)
	return c.DefaultProfile
}
