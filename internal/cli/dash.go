package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/adamw2/tunnelboy/internal/config"
	"github.com/adamw2/tunnelboy/internal/state"
	"github.com/adamw2/tunnelboy/internal/tui"
)

var dashCmd = &cobra.Command{
	Use:     "dash",
	Aliases: []string{"ui", "dashboard"},
	Short:   "Live dashboard of active tunnels",
	Long:    "Interactive Pip-Boy dashboard: watch tunnels, disconnect them, and launch presets.",
	RunE:    runDash,
}

func init() {
	rootCmd.AddCommand(dashCmd)
}

type dashMode int

const (
	modeList dashMode = iota
	modeConfirm
	modePresets
	modeLaunching
)

type presetItem struct {
	name string
	desc string
}

type dashModel struct {
	tunnels      []state.TunnelState
	cursor       int
	presets      []presetItem
	presetCursor int
	mode         dashMode
	launching    string
	message      string
	logLines     []string
	width        int
	height       int
	quitting     bool
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

const dashLogLines = 8

func runDash(cmd *cobra.Command, args []string) error {
	m := dashModel{}

	if cfg, err := config.Load(); err == nil {
		for name, conn := range cfg.Connections {
			desc := conn.Description
			if desc == "" {
				desc = conn.Type
				if id := presetIdentifier(conn); id != "" {
					desc += ": " + id
				}
			}
			m.presets = append(m.presets, presetItem{name: name, desc: desc})
		}
		sort.Slice(m.presets, func(i, j int) bool { return m.presets[i].name < m.presets[j].name })
	}

	m.reload()
	// The dashboard doubles as a launcher: with nothing running, open on the
	// preset list rather than an empty table.
	if len(m.tunnels) == 0 && len(m.presets) > 0 {
		m.mode = modePresets
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
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

func dashTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return dashTickMsg{} })
}

func (m dashModel) Init() tea.Cmd {
	return dashTick()
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case dashTickMsg:
		m.reload()
		return m, dashTick()

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

	case modePresets:
		switch key {
		case "up", "k":
			if m.presetCursor > 0 {
				m.presetCursor--
			}
		case "down", "j":
			if m.presetCursor < len(m.presets)-1 {
				m.presetCursor++
			}
		case "enter":
			if m.presetCursor < len(m.presets) {
				name := m.presets[m.presetCursor].name
				m.mode = modeLaunching
				m.launching = name
				return m, launchPreset(name)
			}
		case "esc", "n":
			m.mode = modeList
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case modeLaunching:
		return m, nil // wait for launchDoneMsg

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
			if len(m.presets) > 0 {
				m.mode = modePresets
			} else {
				m.message = "no presets in config — add connections to ~/.tunnelboy.yaml"
			}
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
// result. The child parents the actual tunnel runner, so nothing here needs to
// stay alive after it returns.
func launchPreset(name string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return launchDoneMsg{name: name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "connect", name, "--detach") // #nosec G204 -- re-exec of our own binary with a config preset name
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out — if this preset needs an SSO login, run it in a terminal first: tunnelboy connect %s --detach", name)
		}
		return launchDoneMsg{name: name, output: string(out), err: err}
	}
}

// tailLines returns the last n lines of a file, stripped of ANSI-free blanks.
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
	case modePresets, modeLaunching:
		m.viewPresets(&b)
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
	case modePresets:
		hints = "↑↓ Navigate • Enter Launch • Esc Back"
	case modeLaunching:
		hints = fmt.Sprintf("Starting %s... (may auto-start a bastion, ~30s)", m.launching)
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

func (m dashModel) viewTunnels(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render("ACTIVE TUNNELS"))
	b.WriteString("\n")

	if len(m.tunnels) == 0 {
		b.WriteString(tui.DimStyle.Render("  No active tunnels. Press [n] to launch a preset."))
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
		prefix := "  "
		row := fmt.Sprintf("%-18s %-22s %-16s %-10s %-4s %-10s %s",
			truncate(t.ID, 18), truncate(t.Target, 22),
			fmt.Sprintf("localhost:%d", t.LocalPort),
			truncate(t.Profile, 10), mode, stripToWidth(dashStatus(t), 10), dashUptime(t))
		if i == m.cursor {
			prefix = tui.TextStyle.Render("> ")
			b.WriteString(prefix + tui.SelectedStyle.Render(row))
		} else {
			b.WriteString(prefix + tui.ItemStyle.Render(row))
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

func (m dashModel) viewPresets(b *strings.Builder) {
	b.WriteString(tui.TitleStyle.Render("LAUNCH PRESET"))
	b.WriteString("\n")

	if len(m.presets) == 0 {
		b.WriteString(tui.DimStyle.Render("  No presets configured in ~/.tunnelboy.yaml"))
		b.WriteString("\n")
		return
	}

	for i, p := range m.presets {
		row := fmt.Sprintf("%-24s %s", truncate(p.name, 24), truncate(p.desc, 50))
		if i == m.presetCursor {
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
