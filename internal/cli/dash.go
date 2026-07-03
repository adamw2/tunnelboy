package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/adamw2/tunnelboy/internal/aws"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tui"
	"github.com/adamw2/tunnelboy/internal/tunnel"
)

var dashCmd = &cobra.Command{
	Use:     "dash",
	Aliases: []string{"ui", "dashboard"},
	Short:   "Live dashboard of active tunnels",
	Long:    "Interactive Pip-Boy dashboard: watch tunnels, disconnect them, launch presets, and discover new targets.",
	RunE:    runDash,
}

func init() {
	rootCmd.AddCommand(dashCmd)
}

type dashMode int

const (
	modeList dashMode = iota
	modeConfirm
	modeNewPick     // combined preset + service-type picker
	modeLaunching   // preset subprocess in flight
	modeDiscovering // AWS discovery in flight
	modeTargetPick  // pick a discovered target
	modePortInput   // EC2: enter remote port
	modeJumpPick    // multiple jump hosts: pick one
	modeStarting    // spawnDetached in flight
)

// newItem is one row of the launcher: either a config preset (launched via
// subprocess) or a service type to discover live.
type newItem struct {
	label   string
	desc    string
	preset  string
	service string
}

// dashTarget is a discovered resource with a pre-filled spec; ports and jump
// host are resolved at launch time.
type dashTarget struct {
	label     string
	desc      string
	needsPort bool // EC2: remote port must be entered
	spec      tunnelSpec
}

type dashModel struct {
	tunnels []state.TunnelState
	cursor  int

	newItems  []newItem
	newCursor int

	service      string
	targets      []dashTarget
	targetCursor int

	jumpHosts  []aws.JumpHost
	jumpCursor int

	pendingSpec tunnelSpec
	portBuf     string

	cfg       *config.Config
	mode      dashMode
	launching string
	message   string
	logLines  []string
	width     int
	height    int
	quitting  bool

	// progress is shared with in-flight launch goroutines; the 1Hz tick
	// re-renders whatever they last reported (ECS auto-start phase, etc.).
	progress *startProgress
	frame    int
}

// startProgress is a tiny thread-safe "latest status line" shared between the
// UI and launch goroutines.
type startProgress struct {
	mu   sync.Mutex
	text string
}

func (p *startProgress) set(s string) {
	p.mu.Lock()
	p.text = s
	p.mu.Unlock()
}

func (p *startProgress) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// progressWriter captures a child process's combined output while mirroring
// its most recent line (the child rewrites one line with \r for ECS progress)
// into a startProgress for live display.
type progressWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	prog *startProgress
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	parts := strings.FieldsFunc(w.buf.String(), func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(parts) - 1; i >= 0; i-- {
		line := strings.TrimSpace(ansiRE.ReplaceAllString(parts[i], ""))
		if line != "" {
			w.prog.set(line)
			break
		}
	}
	return len(p), nil
}

func (w *progressWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type dashTickMsg struct{}
type stopDoneMsg struct {
	id     string
	result stopResult
}
type launchDoneMsg struct {
	name   string
	output string
	err    error
}
type discoverDoneMsg struct {
	service string
	targets []dashTarget
	err     error
}
type jumpPickMsg struct {
	hosts []aws.JumpHost
	spec  tunnelSpec
}
type startDoneMsg struct {
	st  *state.TunnelState
	err error
}

const dashLogLines = 8

// dashServices are the discoverable service types, mirroring the connect
// subcommands.
var dashServices = []newItem{
	{label: "RDS", desc: "discover RDS instances", service: "rds"},
	{label: "OpenSearch", desc: "discover domains (SigV4 proxy)", service: "opensearch"},
	{label: "EC2", desc: "discover instances (port forward)", service: "ec2"},
	{label: "ElastiCache", desc: "discover Redis/Valkey/Memcached", service: "elasticache"},
	{label: "DocumentDB", desc: "discover clusters", service: "docdb"},
	{label: "MSK", desc: "discover Kafka clusters", service: "msk"},
}

func runDash(cmd *cobra.Command, args []string) error {
	m := dashModel{}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	m.cfg = cfg

	var presets []newItem
	for name, conn := range cfg.Connections {
		desc := conn.Description
		if desc == "" {
			desc = conn.Type
			if id := presetIdentifier(conn); id != "" {
				desc += ": " + id
			}
		}
		presets = append(presets, newItem{label: name, desc: desc, preset: name})
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].label < presets[j].label })
	m.newItems = append(presets, dashServices...)

	m.progress = &startProgress{}

	m.reload()
	// The dashboard doubles as a launcher: with nothing running, open on the
	// launcher rather than an empty table.
	if len(m.tunnels) == 0 {
		m.mode = modeNewPick
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func presetIdentifier(c config.Connection) string {
	switch {
	case c.Identifier != "":
		return c.Identifier
	case c.Domain != "":
		return c.Domain
	case c.Instance != "":
		return c.Instance
	case c.NamePattern != "":
		return c.NamePattern
	}
	return ""
}

func (m *dashModel) reload() {
	tunnels, err := state.List()
	if err != nil {
		m.message = "state error: " + err.Error()
		return
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].ID < tunnels[j].ID })
	m.tunnels = tunnels
	if m.cursor >= len(m.tunnels) {
		m.cursor = len(m.tunnels) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}

	m.logLines = nil
	if len(m.tunnels) > 0 {
		t := m.tunnels[m.cursor]
		if t.LogFile != "" {
			m.logLines = tailLines(t.LogFile, dashLogLines)
		} else {
			m.logLines = []string{"(foreground tunnel — output is in its own terminal)"}
		}
	}
}

// tickCmd re-renders at 1Hz normally, faster while a launch is in
// flight so the wait screen (progress line + mascot) animates.
func (m dashModel) tickCmd() tea.Cmd {
	d := time.Second
	switch m.mode {
	case modeLaunching, modeDiscovering, modeStarting:
		d = 300 * time.Millisecond
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return dashTickMsg{} })
}

func (m dashModel) Init() tea.Cmd {
	return m.tickCmd()
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case dashTickMsg:
		m.frame++
		m.reload()
		return m, m.tickCmd()

	case stopDoneMsg:
		switch msg.result {
		case stopKilled:
			m.message = fmt.Sprintf("⚠ %s force-killed (close hooks may not have run)", msg.id)
		case stopStale:
			m.message = fmt.Sprintf("⚠ %s was already dead, record removed", msg.id)
		default:
			m.message = fmt.Sprintf("✓ %s closed", msg.id)
		}
		m.reload()
		return m, nil

	case launchDoneMsg:
		m.mode = modeList
		m.launching = ""
		if msg.err != nil {
			tail := msg.output
			if lines := strings.Split(strings.TrimSpace(tail), "\n"); len(lines) > 3 {
				tail = strings.Join(lines[len(lines)-3:], " / ")
			}
			m.message = fmt.Sprintf("✗ %s failed: %s", msg.name, tail)
		} else {
			m.message = fmt.Sprintf("✓ started %s", msg.name)
		}
		m.reload()
		return m, nil

	case discoverDoneMsg:
		if m.mode != modeDiscovering {
			return m, nil // user backed out while discovery ran
		}
		if msg.err != nil {
			m.mode = modeNewPick
			m.message = fmt.Sprintf("✗ discovery failed: %v", msg.err)
			return m, nil
		}
		if len(msg.targets) == 0 {
			m.mode = modeNewPick
			m.message = fmt.Sprintf("no %s targets found", msg.service)
			return m, nil
		}
		m.service = msg.service
		m.targets = msg.targets
		m.targetCursor = 0
		m.mode = modeTargetPick
		return m, nil

	case jumpPickMsg:
		m.jumpHosts = msg.hosts
		m.jumpCursor = 0
		m.pendingSpec = msg.spec
		m.mode = modeJumpPick
		return m, nil

	case startDoneMsg:
		m.mode = modeList
		if msg.err != nil {
			m.message = fmt.Sprintf("✗ start failed: %v", msg.err)
		} else {
			m.message = fmt.Sprintf("✓ %s running on localhost:%d", msg.st.ID, msg.st.LocalPort)
		}
		m.reload()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m dashModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	switch m.mode {
	case modeConfirm:
		switch key {
		case "y", "Y":
			m.mode = modeList
			if m.cursor < len(m.tunnels) {
				t := m.tunnels[m.cursor]
				m.message = fmt.Sprintf("► closing %s...", t.ID)
				return m, func() tea.Msg { return stopDoneMsg{t.ID, stopTunnelProcess(&t)} }
			}
		default:
			m.mode = modeList
			m.message = ""
		}
		return m, nil

	case modeNewPick:
		switch key {
		case "up", "k":
			if m.newCursor > 0 {
				m.newCursor--
			}
		case "down", "j":
			if m.newCursor < len(m.newItems)-1 {
				m.newCursor++
			}
		case "enter":
			if m.newCursor < len(m.newItems) {
				item := m.newItems[m.newCursor]
				if item.preset != "" {
					m.mode = modeLaunching
					m.launching = item.preset
					m.progress.set("launching...")
					return m, launchPreset(item.preset, m.progress)
				}
				m.mode = modeDiscovering
				m.service = item.service
				return m, discoverCmd(item.service)
			}
		case "esc", "n":
			m.mode = modeList
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case modeTargetPick:
		switch key {
		case "up", "k":
			if m.targetCursor > 0 {
				m.targetCursor--
			}
		case "down", "j":
			if m.targetCursor < len(m.targets)-1 {
				m.targetCursor++
			}
		case "enter":
			if m.targetCursor < len(m.targets) {
				t := m.targets[m.targetCursor]
				if t.needsPort {
					m.pendingSpec = t.spec
					m.portBuf = ""
					m.mode = modePortInput
					return m, nil
				}
				m.mode = modeStarting
				m.progress.set("resolving jump host...")
				return m, resolveJumpCmd(t.spec, m.cfg, m.progress)
			}
		case "esc":
			m.mode = modeNewPick
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case modePortInput:
		switch {
		case key == "enter":
			port := 22
			if m.portBuf != "" {
				p, err := strconv.Atoi(m.portBuf)
				if err != nil || p < 1 || p > 65535 {
					m.message = "invalid port"
					return m, nil
				}
				port = p
			}
			spec := m.pendingSpec
			spec.RemotePort = port
			m.message = ""
			m.mode = modeStarting
			m.progress.set("resolving jump host...")
			return m, resolveJumpCmd(spec, m.cfg, m.progress)
		case key == "esc":
			m.mode = modeTargetPick
			m.message = ""
		case key == "backspace":
			if len(m.portBuf) > 0 {
				m.portBuf = m.portBuf[:len(m.portBuf)-1]
			}
		case len(key) == 1 && key[0] >= '0' && key[0] <= '9' && len(m.portBuf) < 5:
			m.portBuf += key
		}
		return m, nil

	case modeJumpPick:
		switch key {
		case "up", "k":
			if m.jumpCursor > 0 {
				m.jumpCursor--
			}
		case "down", "j":
			if m.jumpCursor < len(m.jumpHosts)-1 {
				m.jumpCursor++
			}
		case "enter":
			if m.jumpCursor < len(m.jumpHosts) {
				host := m.jumpHosts[m.jumpCursor]
				spec := m.pendingSpec
				m.mode = modeStarting
				cfg := m.cfg
				prog := m.progress
				prog.set("starting tunnel...")
				return m, func() tea.Msg { return finishLaunch(spec, &host, cfg, prog) }
			}
		case "esc", "q":
			m.mode = modeList
		}
		return m, nil

	case modeLaunching, modeDiscovering, modeStarting:
		if key == "esc" && m.mode == modeDiscovering {
			m.mode = modeNewPick // result will be ignored
		}
		return m, nil

	default: // modeList
		switch key {
		case "q", "esc":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.reload()
			}
		case "down", "j":
			if m.cursor < len(m.tunnels)-1 {
				m.cursor++
				m.reload()
			}
		case "d":
			if len(m.tunnels) > 0 {
				m.mode = modeConfirm
			}
		case "n":
			m.mode = modeNewPick
			m.newCursor = 0
		case "r":
			m.reload()
			m.message = ""
		}
		return m, nil
	}
}

// launchTimeout bounds a preset launch from the dashboard: ECS auto-start can
// take ~1 min; anything much longer usually means Granted is waiting on an
// interactive SSO login, which can't happen inside the dashboard.
const launchTimeout = 3 * time.Minute

// launchPreset spawns `tunnelboy connect <preset> --detach` and reports the
// result, streaming the child's latest output line (ECS auto-start progress
// etc.) into prog for live display. The child parents the actual tunnel
// runner, so nothing here needs to stay alive after it returns.
func launchPreset(name string, prog *startProgress) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return launchDoneMsg{name: name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
		defer cancel()
		pw := &progressWriter{prog: prog}
		cmd := exec.CommandContext(ctx, exe, "connect", name, "--detach") // #nosec G204 -- re-exec of our own binary with a config preset name
		cmd.Stdout = pw
		cmd.Stderr = pw
		err = cmd.Run()
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out — if this preset needs an SSO login, run it in a terminal first: tunnelboy connect %s --detach", name)
		}
		return launchDoneMsg{name: name, output: pw.String(), err: err}
	}
}

// discoverCmd runs AWS discovery for one service type off the UI thread.
func discoverCmd(service string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		pm := aws.NewProfileManager()
		if err := pm.LoadProfile(ctx, viper.GetString("profile")); err != nil {
			return discoverDoneMsg{service: service, err: err}
		}
		profile := pm.GetCurrentProfile()
		d := aws.NewDiscovery(pm.GetConfig())

		var targets []dashTarget
		switch service {
		case "rds":
			instances, err := d.DiscoverRDSInstances(ctx)
			if err != nil {
				return discoverDoneMsg{service: service, err: err}
			}
			for _, i := range instances {
				targets = append(targets, dashTarget{
					label: i.Identifier,
					desc:  fmt.Sprintf("%s %s  %s", i.Engine, i.EngineVersion, i.InstanceClass),
					spec: tunnelSpec{
						Type: string(tunnel.TunnelTypeRDS), Engine: i.Engine, Target: i.Identifier,
						RemoteHost: i.Endpoint, RemotePort: int(i.Port), Profile: profile,
					},
				})
			}
		case "opensearch":
			domains, err := d.DiscoverOpenSearchDomains(ctx)
			if err != nil {
				return discoverDoneMsg{service: service, err: err}
			}
			for _, dom := range domains {
				targets = append(targets, dashTarget{
					label: dom.DomainName,
					desc:  fmt.Sprintf("%s  %d nodes", dom.EngineVersion, dom.InstanceCount),
					spec: tunnelSpec{
						Type: string(tunnel.TunnelTypeOpenSearch), Target: dom.DomainName,
						RemoteHost: dom.Endpoint, RemotePort: 443, DomainEndpoint: dom.Endpoint,
						Profile: profile,
					},
				})
			}
		case "ec2":
			instances, err := d.DiscoverEC2Instances(ctx)
			if err != nil {
				return discoverDoneMsg{service: service, err: err}
			}
			for _, i := range instances {
				label := i.InstanceID
				if i.Name != "" {
					label = fmt.Sprintf("%s  %s", i.InstanceID, i.Name)
				}
				targets = append(targets, dashTarget{
					label:     label,
					desc:      fmt.Sprintf("%s  %s", i.InstanceType, i.PrivateIP),
					needsPort: true,
					spec: tunnelSpec{
						Type: string(tunnel.TunnelTypeEC2), Target: i.InstanceID,
						RemoteHost: i.PrivateIP, Profile: profile,
					},
				})
			}
		default: // elasticache, docdb, msk
			var eps []aws.EndpointTarget
			var err error
			var tt tunnel.TunnelType
			switch service {
			case "elasticache":
				eps, err = d.DiscoverElastiCache(ctx)
				tt = tunnel.TunnelTypeElastiCache
			case "docdb":
				eps, err = d.DiscoverDocDBClusters(ctx)
				tt = tunnel.TunnelTypeDocDB
			case "msk":
				eps, err = d.DiscoverMSKClusters(ctx)
				tt = tunnel.TunnelTypeMSK
			}
			if err != nil {
				return discoverDoneMsg{service: service, err: err}
			}
			for _, e := range eps {
				targets = append(targets, dashTarget{
					label: e.Name,
					desc:  e.Detail,
					spec: tunnelSpec{
						Type: string(tt), Engine: e.Engine, Target: e.Name,
						RemoteHost: e.Endpoint, RemotePort: int(e.Port), Profile: profile,
					},
				})
			}
		}

		return discoverDoneMsg{service: service, targets: targets}
	}
}

// resolveJumpCmd discovers the jump host for a spec (auto-starting the ECS
// bastion if needed) and either finishes the launch or asks the user to pick
// between multiple hosts.
func resolveJumpCmd(spec tunnelSpec, cfg *config.Config, prog *startProgress) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), aws.DefaultStartupTimeout+time.Minute)
		defer cancel()

		pm := aws.NewProfileManager()
		if err := pm.LoadProfile(ctx, spec.Profile); err != nil {
			return startDoneMsg{err: err}
		}
		d := aws.NewDiscovery(pm.GetConfig())
		d.EnableAutoStart(func(elapsed time.Duration, status string) {
			prog.set(fmt.Sprintf("ECS auto-start: %s (%s)", status, elapsed))
		})

		prog.set("discovering jump hosts...")
		hosts, err := d.DiscoverJumpHosts(ctx, cfg)
		if err != nil {
			return startDoneMsg{err: fmt.Errorf("jump host discovery: %w", err)}
		}
		if len(hosts) == 0 {
			return startDoneMsg{err: fmt.Errorf("no jump host found — configure jump_hosts in ~/.tunnelboy.yaml")}
		}
		if len(hosts) > 1 {
			return jumpPickMsg{hosts: hosts, spec: spec}
		}
		return finishLaunch(spec, &hosts[0], cfg, prog)
	}
}

// finishLaunch resolves local ports and spawns the detached runner.
func finishLaunch(spec tunnelSpec, host *aws.JumpHost, cfg *config.Config, prog *startProgress) tea.Msg {
	spec.JumpHostID = host.ID
	applyAutoStop(&spec, host, cfg)

	var err error
	if spec.Type == string(tunnel.TunnelTypeOpenSearch) {
		spec.ProxyPort, err = silentLocalPort(9250)
		if err == nil {
			spec.LocalPort, err = tunnel.FindFreePort()
		}
	} else {
		spec.LocalPort, err = silentLocalPort(spec.RemotePort)
	}
	if err != nil {
		return startDoneMsg{err: err}
	}

	prog.set(fmt.Sprintf("starting tunnel process on localhost:%d...", spec.userPort()))
	st, err := spawnDetached(spec)
	return startDoneMsg{st: st, err: err}
}

// silentLocalPort prefers the fallback port, quietly picking a free one when
// it's taken (no terminal output — we're inside the TUI).
func silentLocalPort(fallback int) (int, error) {
	if fallback != 0 && tunnel.PortAvailable(fallback) {
		return fallback, nil
	}
	return tunnel.FindFreePort()
}

// tailLines returns the last n non-blank lines of a file.
func tailLines(path string, n int) []string {
	data, err := os.ReadFile(path) // #nosec G304 -- log path recorded by our own tunnel processes under ~/.tunnelboy
	if err != nil {
		return []string{"(no log)"}
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var lines []string
	for _, l := range all {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

var (
	dashActiveStyle = lipgloss.NewStyle().Foreground(tui.ColorPrimary).Bold(true)
	dashWarnStyle   = lipgloss.NewStyle().Foreground(tui.ColorWarning).Bold(true)
	dashDeadStyle   = lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true)
)

func dashStatus(st state.TunnelState) string {
	switch st.Status {
	case "reconnecting":
		return dashWarnStyle.Render("◌ RECONN")
	case "disconnected":
		return dashDeadStyle.Render("✗ DOWN")
	default:
		return dashActiveStyle.Render("● ACTIVE")
	}
}

func dashUptime(st state.TunnelState) string {
	d := time.Since(st.StartedAt).Round(time.Second)
	if d > time.Hour {
		d = d.Round(time.Minute)
	}
	return d.String()
}

func (m dashModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString(tui.BorderStyle.Render(tui.RenderHeader(versionInfo.Version)))
	b.WriteString("\n\n")

	switch m.mode {
	case modeNewPick:
		m.viewNewPick(&b)
	case modeDiscovering:
		b.WriteString(tui.TitleStyle.Render("DISCOVERING"))
		b.WriteString("\n")
		b.WriteString(tui.DimStyle.Render(fmt.Sprintf("  Scanning for %s targets...", m.service)))
		b.WriteString("\n")
	case modeTargetPick:
		m.viewTargets(&b)
	case modePortInput:
		b.WriteString(tui.TitleStyle.Render("REMOTE PORT"))
		b.WriteString("\n")
		b.WriteString(tui.TextStyle.Render(fmt.Sprintf("  Forward to %s port: %s_", m.pendingSpec.Target, m.portBuf)))
		b.WriteString("\n")
		b.WriteString(tui.DimStyle.Render("  (empty = 22)"))
		b.WriteString("\n")
	case modeJumpPick:
		m.viewJumpHosts(&b)
	case modeStarting, modeLaunching:
		what := m.launching
		if m.mode == modeStarting {
			what = m.pendingTargetLabel()
		}
		b.WriteString(tui.TitleStyle.Render("STARTING TUNNEL"))
		b.WriteString("\n")
		if what != "" {
			b.WriteString(tui.TextStyle.Render("  " + what))
			b.WriteString("\n\n")
		}
		status := m.progress.get()
		if status == "" {
			status = "working..."
		}
		b.WriteString(tui.WarningStyle.Render("  ► " + truncate(status, 90)))
		b.WriteString("\n\n")
		b.WriteString(tui.Mascot(m.frame))
		b.WriteString("\n\n")
		b.WriteString(tui.DimStyle.Render("  (a cold ECS bastion takes ~30-60s to auto-start)"))
		b.WriteString("\n")
	default:
		m.viewTunnels(&b)
	}

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(tui.WarningStyle.Render(m.message))
		b.WriteString("\n")
	}

	var hints string
	switch m.mode {
	case modeConfirm:
		hints = fmt.Sprintf("Disconnect %s? [y] yes  [any] cancel", m.selectedID())
	case modeNewPick:
		hints = "↑↓ Navigate • Enter Launch/Discover • Esc Back • q Quit"
	case modeLaunching:
		hints = fmt.Sprintf("Starting %s...", m.launching)
	case modeDiscovering:
		hints = "Esc Cancel"
	case modeTargetPick:
		hints = "↑↓ Navigate • Enter Connect • Esc Back"
	case modePortInput:
		hints = "Digits • Enter Confirm • Esc Back"
	case modeJumpPick:
		hints = "↑↓ Navigate • Enter Select Jump Host • Esc Cancel"
	case modeStarting:
		hints = "Starting..."
	default:
		hints = "↑↓ Select • [d] Disconnect • [n] New • [r] Refresh • [q] Quit"
	}
	b.WriteString(tui.RenderStatusBar(hints))

	return b.String()
}

func (m dashModel) selectedID() string {
	if m.cursor < len(m.tunnels) {
		return m.tunnels[m.cursor].ID
	}
	return "?"
}

// pendingTargetLabel names what's being started in the discovery flow.
func (m dashModel) pendingTargetLabel() string {
	if m.pendingSpec.Target != "" {
		return m.pendingSpec.Target
	}
	if m.targetCursor < len(m.targets) {
		return m.targets[m.targetCursor].spec.Target
	}
	return ""
}

func (m dashModel) viewTunnels(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render("ACTIVE TUNNELS"))
	b.WriteString("\n")

	if len(m.tunnels) == 0 {
		b.WriteString(tui.DimStyle.Render("  No active tunnels. Press [n] to launch or discover."))
		b.WriteString("\n")
		return
	}

	header := fmt.Sprintf("  %-18s %-22s %-16s %-10s %-4s %-10s %s",
		"ID", "TARGET", "ENDPOINT", "PROFILE", "MODE", "STATUS", "UPTIME")
	b.WriteString(tui.DimStyle.Render(header))
	b.WriteString("\n")

	for i, t := range m.tunnels {
		mode := "fg"
		if t.Detached {
			mode = "bg"
		}
		row := fmt.Sprintf("%-18s %-22s %-16s %-10s %-4s %-10s %s",
			truncate(t.ID, 18), truncate(t.Target, 22),
			fmt.Sprintf("localhost:%d", t.LocalPort),
			truncate(t.Profile, 10), mode, stripToWidth(dashStatus(t), 10), dashUptime(t))
		if i == m.cursor {
			b.WriteString(tui.TextStyle.Render("> ") + tui.SelectedStyle.Render(row))
		} else {
			b.WriteString("  " + tui.ItemStyle.Render(row))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(tui.RenderDivider(70))
	b.WriteString("\n")
	b.WriteString(tui.DimStyle.Render(fmt.Sprintf(" LOG: %s", m.selectedID())))
	b.WriteString("\n")
	for _, l := range m.logLines {
		b.WriteString(tui.DimStyle.Render("  " + truncate(l, 100)))
		b.WriteString("\n")
	}
}

func (m dashModel) viewNewPick(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render("NEW TUNNEL"))
	b.WriteString("\n")

	presetsShown := false
	for i, item := range m.newItems {
		if item.preset != "" && !presetsShown {
			b.WriteString(tui.DimStyle.Render("  ── presets ──"))
			b.WriteString("\n")
			presetsShown = true
		}
		if item.service != "" && (i == 0 || m.newItems[i-1].service == "") {
			b.WriteString(tui.DimStyle.Render("  ── discover ──"))
			b.WriteString("\n")
		}
		row := fmt.Sprintf("%-24s %s", truncate(item.label, 24), truncate(item.desc, 50))
		if i == m.newCursor {
			b.WriteString(tui.TextStyle.Render("> ") + tui.SelectedStyle.Render(row))
		} else {
			b.WriteString("  " + tui.ItemStyle.Render(row))
		}
		b.WriteString("\n")
	}
}

func (m dashModel) viewTargets(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render(fmt.Sprintf("SELECT %s TARGET", strings.ToUpper(m.service))))
	b.WriteString("\n")

	for i, t := range m.targets {
		row := fmt.Sprintf("%-36s %s", truncate(t.label, 36), truncate(t.desc, 44))
		if i == m.targetCursor {
			b.WriteString(tui.TextStyle.Render("> ") + tui.SelectedStyle.Render(row))
		} else {
			b.WriteString("  " + tui.ItemStyle.Render(row))
		}
		b.WriteString("\n")
	}
}

func (m dashModel) viewJumpHosts(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render("SELECT JUMP HOST"))
	b.WriteString("\n")

	for i, h := range m.jumpHosts {
		row := fmt.Sprintf("%-30s %s  %s", truncate(h.Name, 30), strings.ToUpper(h.Type), h.PrivateIP)
		if i == m.jumpCursor {
			b.WriteString(tui.TextStyle.Render("> ") + tui.SelectedStyle.Render(row))
		} else {
			b.WriteString("  " + tui.ItemStyle.Render(row))
		}
		b.WriteString("\n")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// stripToWidth pads a styled string's visible width for rough column alignment.
func stripToWidth(styled string, w int) string {
	visible := lipgloss.Width(styled)
	if visible < w {
		return styled + strings.Repeat(" ", w-visible)
	}
	return styled
}
