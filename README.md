# TunnelBoy

AWS VPC tunneling CLI with Pip-Boy theming. Securely connect to RDS databases, OpenSearch clusters, and EC2 instances through SSM Session Manager.

```
┌─────────────────────────────────────────────────────────────┐
│  TUNNELBOY v1.0.0                    VAULT-TEC INDUSTRIES   │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  SELECT RDS INSTANCE                                        │
│  ──────────────────                                         │
│                                                             │
│  > prod-analytics-postgres   PostgreSQL 14   db.r5.large   │
│    staging-api-mysql         MySQL 8.0       db.t3.medium  │
│    dev-warehouse             PostgreSQL 15   db.t3.small   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Features

- **AWS SSO Support**: Works with your existing AWS profiles and SSO sessions
- **Service Discovery**: Automatically discovers RDS instances, OpenSearch domains, and EC2 instances
- **Flexible Jump Hosts**: Configure jump hosts by name patterns, tags, or explicit IDs
- **Direct SSM**: Connect directly to SSM-enabled instances without a jump host
- **OpenSearch Proxy**: Automatic SigV4 signing proxy for browser-based Kibana/OpenSearch Dashboard access
- **RDS IAM Auth**: Generate temporary credentials for IAM-authenticated databases
- **Interactive TUI**: Beautiful Pip-Boy themed interface for selecting resources

## Prerequisites

- macOS (Intel or Apple Silicon)
- AWS CLI configured with profiles
- [Session Manager Plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)

```bash
brew install --cask session-manager-plugin
```

## Installation

```bash
# Via Homebrew
brew tap adamw2/tunnelboy
brew install tunnelboy

# Verify installation
tunnelboy version
```

## Quick Start

```bash
# List available AWS profiles
tunnelboy profile list

# Use a specific profile
tunnelboy profile use prod-account

# Connect to an RDS instance (interactive)
tunnelboy connect rds

# Connect to a specific RDS instance
tunnelboy connect rds my-database --db-user readonly --local-port 5432

# Connect to OpenSearch with Kibana
tunnelboy connect opensearch my-domain
# Then open http://localhost:9250/_dashboards in your browser

# Open interactive shell on an EC2 instance (default behavior)
tunnelboy connect ec2 i-0abc123def
```

## Usage

### Profile Management

```bash
tunnelboy profile list              # List available AWS profiles
tunnelboy profile use <name>        # Switch to a profile
tunnelboy profile current           # Show current identity
```

### Service Discovery

```bash
tunnelboy list jump-hosts           # List discovered jump hosts
tunnelboy list rds                  # List RDS instances
tunnelboy list opensearch           # List OpenSearch domains
tunnelboy list ec2                  # List EC2 instances (shows SSM status)
tunnelboy list all                  # List everything
```

### Tunneling

```bash
# RDS
tunnelboy connect rds                              # Interactive selection
tunnelboy connect rds <identifier> --db-user <user> --local-port 5432

# OpenSearch (with automatic SigV4 signing proxy)
tunnelboy connect opensearch                       # Interactive selection
tunnelboy connect opensearch <domain> --local-port 9250

# EC2 (interactive shell is default)
tunnelboy connect ec2                              # Interactive selection → shell
tunnelboy connect ec2 <instance-id>                # Open shell on instance (default)
tunnelboy connect ec2 <instance-id> --port-forward --remote-port 8080  # Port forwarding mode
tunnelboy connect ec2 <instance-id> --direct       # Direct SSM (no jump host)

# Using a specific jump host
tunnelboy connect rds my-db --via i-0abc123def456
```

### Tunnel Management

```bash
tunnelboy tunnels                   # List active tunnels
tunnelboy disconnect <tunnel-id>    # Close specific tunnel
tunnelboy disconnect --all          # Close all tunnels
```

### EC2 Interactive Shell

EC2 connections default to interactive shell mode. Port forwarding is opt-in with `--port-forward`.

```bash
# Interactive selection → opens shell (default)
tunnelboy connect ec2

# Direct connection to instance → opens shell
tunnelboy connect ec2 i-0abc123def

# Port forwarding mode (opt-in)
tunnelboy connect ec2 i-0abc123def --port-forward --remote-port 8080 --local-port 8080

# Example shell session
► Opening interactive shell on i-0abc123def (app-server)...

Starting session with SessionId: user@example.com-0abc123def
sh-4.2$ whoami
ssm-user
sh-4.2$ cd /var/log
sh-4.2$ exit
exit

Session closed
```

**Shell Mode (Default):**
- Requires SSM-enabled instances
- Uses SSM Session Manager for secure access (no SSH keys needed)
- Session logs can be configured via AWS Systems Manager
- Port forwarding flags are ignored in shell mode

**Port Forwarding Mode:**
- Use `--port-forward` flag to enable
- Specify ports with `--local-port` and `--remote-port`
- Can use jump hosts with `--via` or `--direct` for SSM-enabled targets

### EC2 Name Pattern Matching

Connect to EC2 instances by name pattern instead of instance ID using connection presets:

```yaml
# ~/.tunnelboy.yaml
connections:
  render-servers:
    type: ec2
    name_pattern: "ecrion render"  # Matches: "ecrion render 01", "ecrion render 02", etc.
    connection_type: port_forward  # Options: shell (default) or port_forward
    remote_port: 50100
    local_port: 50100
    aws_profile: production

  bastion-shell:
    type: ec2
    name_pattern: "bastion"
    connection_type: shell  # Opens interactive shell (default)
```

**Usage:**

```bash
# Connect to render server
tunnelboy connect render-servers
# If multiple instances match "ecrion render", you'll get an interactive selection:
# ? Select EC2 Instance:
#   > i-0abc123 (ecrion render 01)
#     i-0def456 (ecrion render 02)
#     i-0ghi789 (ecrion render 03)

# Connect to bastion
tunnelboy connect bastion-shell
# Opens shell on selected bastion instance
```

**Features:**
- Case-insensitive substring matching
- Interactive selection if multiple matches
- Direct connection if single match
- Works with both `shell` and `port_forward` modes
- Can specify `aws_profile` for automatic profile switching

## Configuration

Create `~/.tunnelboy.yaml` for persistent settings. See [.tunnelboy.yaml.example](.tunnelboy.yaml.example) for a full example.

Example configuration:

```yaml
# Default AWS profile (used as fallback if AWS_PROFILE is not set)
# Priority: 1. --profile flag, 2. AWS_PROFILE env var, 3. default_profile
default_profile: prod-account

# Jump host discovery
jump_hosts:
  # Name patterns (glob-style)
  patterns:
    - "*bastion*"
    - "*jump*"
  
  # AWS tags
  tags:
    - key: "role"
      value: "bastion"
  
  # Explicit instances
  instances:
    - i-0abc123def456

# Saved connection presets
connections:
  analytics:
    type: rds
    identifier: prod-analytics-postgres
    db_user: readonly
    local_port: 5432

  logs:
    type: opensearch
    domain: prod-logs
    local_port: 9250

  app-server:
    type: ec2
    instance: i-0abc123def
    connection_type: shell  # Opens interactive shell (default)

  render-server:
    type: ec2
    description: "Connect to Ecrion Render servers"  # Custom completion description
    name_pattern: "ecrion render"
    connection_type: port_forward  # Use port forwarding
    remote_port: 50100
    local_port: 50100
```

**Connection Descriptions:**

Add a `description` field to customize what appears in shell completion:

```yaml
ecrion-tunnel:
  type: ec2
  description: "Port forward to Ecrion Render service"
  name_pattern: "ecrion render"
  connection_type: port_forward
  remote_port: 50100
  local_port: 50100
```

Without a description, TunnelBoy auto-generates one based on the connection type and resource.

Use saved connections:

```bash
tunnelboy connect analytics      # RDS connection
tunnelboy connect logs           # OpenSearch with signing proxy
tunnelboy connect app-server     # EC2 interactive shell
tunnelboy connect render-server  # EC2 port forwarding by name
```

**Profile Switching:**
Each connection preset can specify its own AWS profile:
```yaml
connections:
  prod-db:
    type: rds
    identifier: prod-database
    aws_profile: production  # Automatically switches to production profile
    db_user: readonly

  staging-db:
    type: rds
    identifier: staging-database
    aws_profile: staging  # Automatically switches to staging profile
    db_user: app_user
```

**With Granted CLI (Automatic Profile Switching):**

TunnelBoy integrates with [Granted](https://github.com/fwdcloudsec/granted) for automatic profile switching using `assume --exec`:

```yaml
# ~/.tunnelboy.yaml
connections:
  latest-readonly:
    type: rds
    identifier: latest-pes-db
    aws_profile: latest  # TunnelBoy will automatically switch to this profile
    db_user: pf-readonly

  staging-readonly:
    type: rds  
    identifier: staging-pes-db
    aws_profile: staging  # Different profile, automatic switching
    db_user: pf-readonly
```

```bash
# Just connect - TunnelBoy automatically switches profiles!
assume staging  # You're on staging
tunnelboy connect latest-readonly
# ► Switching to profile 'latest' via Granted...
# ✓ Connected to latest account automatically!

# Works from any profile
tunnelboy connect staging-readonly
# ► Switching to profile 'staging' via Granted...
# ✓ Connected!
```

**How it works:**
- TunnelBoy detects profile mismatch
- Uses `assume <profile> --exec` to re-execute with correct credentials
- Completely transparent to the user!

**Without Granted:**

If Granted isn't installed, TunnelBoy will tell you which profile to switch to:
```bash
tunnelboy connect latest-readonly
# Error: this preset requires profile 'latest', but you're using 'staging'
# Run: assume latest
# (Or install Granted for automatic profile switching)
```

**Without aws_profile field:**

If you omit `aws_profile`, TunnelBoy uses whatever profile is currently active:
```yaml
connections:
  my-db:
    type: rds
    identifier: my-database
    db_user: readonly
    # No aws_profile - works with any active profile
```

**With standard AWS SSO:**

```bash
aws sso login --profile production
tunnelboy connect prod-db
```

## Output Formats

```bash
tunnelboy list rds                 # Human-readable table
tunnelboy list rds --output json   # JSON for scripting
tunnelboy list rds --quiet         # Just identifiers
```

## Shell Completion

TunnelBoy supports shell completion for commands, flags, and resource names (RDS instances, OpenSearch domains, EC2 instances).

### Zsh (macOS default)

**Recommended Setup** (one-time installation):

```bash
# 1. Generate completion file
mkdir -p ~/.zsh/completions
tunnelboy completion zsh > ~/.zsh/completions/_tunnelboy
```

Then manually add these lines to `~/.zshrc` **in this order** (fpath must come before compinit):

```zsh
fpath=(~/.zsh/completions $fpath)
autoload -Uz compinit && compinit
```

```bash
# 2. Reload shell
source ~/.zshrc
```

> **Note:** `fpath` must be set before `compinit` runs. If you use the append-based setup scripts and `compinit` is already in your `.zshrc`, completion will silently fail. Edit `.zshrc` manually to ensure the correct order.

**Alternative** (dynamic loading on every shell startup):

```bash
# Ensure zsh completion prerequisites are enabled
grep -qxF 'autoload -Uz compinit && compinit' ~/.zshrc || echo 'autoload -Uz compinit && compinit' >> ~/.zshrc
grep -qxF 'autoload bashcompinit && bashcompinit' ~/.zshrc || echo 'autoload bashcompinit && bashcompinit' >> ~/.zshrc

# Add dynamic completion loading
grep -qxF 'source <(tunnelboy completion zsh)' ~/.zshrc || echo 'source <(tunnelboy completion zsh)' >> ~/.zshrc

# Reload shell
source ~/.zshrc
```

### Bash

```bash
# Add to ~/.bash_profile or ~/.bashrc
source <(tunnelboy completion bash)

# Or install to system location (one-time)
tunnelboy completion bash > /usr/local/etc/bash_completion.d/tunnelboy
```

### Fish

```bash
# One-time setup
tunnelboy completion fish > ~/.config/fish/completions/tunnelboy.fish
```

### Usage

After setup, restart your shell and try:

```bash
tunnelboy connect rds <TAB>
# Shows: prod-analytics-postgres   staging-api-mysql   dev-warehouse
#        PostgreSQL 14 db.r5.large  MySQL 8.0 db.t3.medium  PostgreSQL 15 db.t3.small

tunnelboy connect opensearch <TAB>
# Shows: prod-logs-domain    staging-search-domain
#        OpenSearch_2.11     OpenSearch_2.11

tunnelboy connect ec2 <TAB>
# Shows: i-0abc123    i-0xyz789
#        app-server   bastion-host
```

## Troubleshooting

### OpenSearch/Kibana Access

**Browser shows "Connection Refused"**
- Ensure the signing proxy started successfully (look for "✓ Signing proxy active")
- Check that you're using the correct port (default is 9250)
- Verify your AWS credentials are valid: `aws sts get-caller-identity --profile <your-profile>`

**TLS/Certificate Warnings**
- The signing proxy uses a secure tunnel to OpenSearch
- Browser warnings are expected since the proxy runs on localhost
- You can safely proceed through certificate warnings

**Authentication Errors**
- Verify your AWS profile has permissions to access the OpenSearch domain
- Check that your IAM role has the necessary OpenSearch permissions
- Ensure your AWS session hasn't expired (re-run `aws sso login` if needed)

**Port Already in Use**
- Use `--local-port <port>` to specify a different port
- Check for other processes: `lsof -i :<port>`

### RDS Connection Issues

**IAM Authentication Token Expired**
- Tokens are valid for 15 minutes
- Reconnect to generate a new token
- For longer sessions, consider using standard database authentication

### General Issues

**Session Manager Plugin Not Found**
```bash
brew install --cask session-manager-plugin
```

**No Jump Hosts Found**
- Configure jump host patterns in `~/.tunnelboy.yaml`
- Use `--direct` flag if your instances support direct SSM connections
- Run `tunnelboy list jump-hosts` to see discovered hosts

## Development

Build tooling uses [Task](https://taskfile.dev) (`brew install go-task`). Run `task --list` to see all targets.

```bash
# Build
task build

# Run tests
task test

# Build for all macOS architectures
task build-all

# Install for development (creates symlink)
task install-dev

# Remove development symlink
task uninstall-dev

# Install to GOPATH/bin
task install
```

### Development Setup

For active development, use the symlink approach:

```bash
# Build and install development symlink
task install-dev

# Now tunnelboy is available system-wide
tunnelboy version

# Enable shell completion
source <(tunnelboy completion zsh)

# Make changes, rebuild, and test immediately
task build
tunnelboy connect rds  # Uses latest build!
```

**Important:** Remove the development symlink before installing via Homebrew:
```bash
task uninstall-dev
brew install tunnelboy
```

## License

MIT
