package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/adamw2/tunnelboy/internal/aws"
)

// Item represents a selectable item
type Item struct {
	id          string
	title       string
	description string
}

func (i Item) Title() string       { return i.title }
func (i Item) Description() string { return i.description }
func (i Item) FilterValue() string { return i.title + " " + i.description }
func (i Item) ID() string          { return i.id }

// SelectRDS prompts user to select an RDS instance
func SelectRDS(instances []aws.RDSInstance) (*aws.RDSInstance, error) {
	items := make([]list.Item, len(instances))
	for i, inst := range instances {
		desc := fmt.Sprintf("%s %s  %s", inst.Engine, inst.EngineVersion, inst.InstanceClass)
		items[i] = Item{
			id:          inst.Identifier,
			title:       inst.Identifier,
			description: desc,
		}
	}

	selected, err := runSelector("SELECT RDS INSTANCE", items)
	if err != nil {
		return nil, err
	}

	// Find the selected instance
	for _, inst := range instances {
		if inst.Identifier == selected {
			return &inst, nil
		}
	}

	return nil, fmt.Errorf("selection cancelled")
}

// SelectOpenSearch prompts user to select an OpenSearch domain
func SelectOpenSearch(domains []aws.OpenSearchDomain) (*aws.OpenSearchDomain, error) {
	items := make([]list.Item, len(domains))
	for i, domain := range domains {
		desc := fmt.Sprintf("%s  %d nodes", domain.EngineVersion, domain.InstanceCount)
		items[i] = Item{
			id:          domain.DomainName,
			title:       domain.DomainName,
			description: desc,
		}
	}

	selected, err := runSelector("SELECT OPENSEARCH DOMAIN", items)
	if err != nil {
		return nil, err
	}

	for _, domain := range domains {
		if domain.DomainName == selected {
			return &domain, nil
		}
	}

	return nil, fmt.Errorf("selection cancelled")
}

// SelectEC2 prompts user to select an EC2 instance
func SelectEC2(instances []aws.EC2Instance) (*aws.EC2Instance, error) {
	items := make([]list.Item, len(instances))
	for i, inst := range instances {
		ssmStatus := "SSM: ✗"
		if inst.SSMEnabled {
			ssmStatus = "SSM: ✓"
		}
		desc := fmt.Sprintf("%s  %s  %s", inst.InstanceType, inst.PrivateIP, ssmStatus)
		items[i] = Item{
			id:          inst.InstanceID,
			title:       fmt.Sprintf("%s  %s", inst.InstanceID, inst.Name),
			description: desc,
		}
	}

	selected, err := runSelector("SELECT EC2 INSTANCE", items)
	if err != nil {
		return nil, err
	}

	for _, inst := range instances {
		if inst.InstanceID == selected {
			return &inst, nil
		}
	}

	return nil, fmt.Errorf("selection cancelled")
}

// SelectEndpoint prompts user to select a generic endpoint target
// (ElastiCache, DocumentDB, MSK)
func SelectEndpoint(title string, targets []aws.EndpointTarget) (*aws.EndpointTarget, error) {
	items := make([]list.Item, len(targets))
	for i, t := range targets {
		items[i] = Item{
			id:          t.Name,
			title:       t.Name,
			description: t.Detail,
		}
	}

	selected, err := runSelector(title, items)
	if err != nil {
		return nil, err
	}

	for _, t := range targets {
		if t.Name == selected {
			return &t, nil
		}
	}

	return nil, fmt.Errorf("selection cancelled")
}

// SelectJumpHost prompts user to select a jump host
func SelectJumpHost(jumpHosts []aws.JumpHost) (*aws.JumpHost, error) {
	items := make([]list.Item, len(jumpHosts))
	for i, jh := range jumpHosts {
		ssmStatus := "SSM: ✗"
		if jh.SSMEnabled {
			ssmStatus = "SSM: ✓"
		}
		
		var desc string
		if jh.Type == "ecs" {
			desc = fmt.Sprintf("ECS  %s  %s  %s", jh.ClusterName, jh.PrivateIP, ssmStatus)
		} else {
			desc = fmt.Sprintf("EC2  %s  %s", jh.PrivateIP, ssmStatus)
		}
		
		items[i] = Item{
			id:          jh.ID,
			title:       fmt.Sprintf("%s  %s", jh.Type, jh.Name),
			description: desc,
		}
	}

	selected, err := runSelector("SELECT JUMP HOST", items)
	if err != nil {
		return nil, err
	}

	for _, jh := range jumpHosts {
		if jh.ID == selected {
			return &jh, nil
		}
	}

	return nil, fmt.Errorf("selection cancelled")
}

// selectorModel is the bubbletea model for the selector
type selectorModel struct {
	list     list.Model
	title    string
	selected string
	quitting bool
}

func (m selectorModel) Init() tea.Cmd {
	return nil
}

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(Item); ok {
				m.selected = item.ID()
			}
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m selectorModel) View() string {
	if m.quitting && m.selected == "" {
		return ""
	}
	if m.selected != "" {
		return ""
	}

	// Build the view
	var b strings.Builder

	// Header
	header := RenderHeader(versionInfo.Version)
	b.WriteString(BorderStyle.Render(header))
	b.WriteString("\n\n")

	// Title
	b.WriteString(TitleStyle.Render(m.title))
	b.WriteString("\n\n")

	// List
	b.WriteString(m.list.View())
	b.WriteString("\n")

	// Status bar
	b.WriteString(RenderStatusBar("Type to filter • ↑↓ Navigate • Enter Select • Esc Cancel"))

	return b.String()
}

var versionInfo struct {
	Version string
}

func init() {
	versionInfo.Version = "dev"
}

// SetVersion sets the version for display in TUI
func SetVersion(v string) {
	versionInfo.Version = v
}

func runSelector(title string, items []list.Item) (string, error) {
	// Custom delegate with Pip-Boy styling
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true).
		Padding(0, 0, 0, 2)
	delegate.Styles.SelectedDesc = lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Padding(0, 0, 0, 2)
	delegate.Styles.NormalTitle = lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Padding(0, 0, 0, 2)
	delegate.Styles.NormalDesc = lipgloss.NewStyle().
		Foreground(ColorDim).
		Padding(0, 0, 0, 2)

	l := list.New(items, delegate, 60, min(len(items)*3+4, 15))
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Styles.Title = TitleStyle
	l.Styles.FilterPrompt = PromptStyle
	l.Styles.FilterCursor = CursorStyle

	m := selectorModel{
		list:  l,
		title: title,
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	result := finalModel.(selectorModel)
	if result.quitting && result.selected == "" {
		return "", fmt.Errorf("cancelled")
	}

	return result.selected, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PromptInput prompts for text input
func PromptInput(prompt, defaultValue string) (string, error) {
	ti := textinput.New()
	ti.Placeholder = defaultValue
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 40
	ti.PromptStyle = PromptStyle
	ti.TextStyle = TextStyle
	ti.Cursor.Style = CursorStyle

	m := inputModel{
		textInput: ti,
		prompt:    prompt,
	}

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	result := finalModel.(inputModel)
	if result.cancelled {
		return "", fmt.Errorf("cancelled")
	}

	value := result.textInput.Value()
	if value == "" {
		value = defaultValue
	}

	return value, nil
}

type inputModel struct {
	textInput textinput.Model
	prompt    string
	cancelled bool
}

func (m inputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m inputModel) View() string {
	return fmt.Sprintf(
		"%s\n\n%s\n\n%s",
		TitleStyle.Render(m.prompt),
		m.textInput.View(),
		DimStyle.Render("Enter to confirm • Esc to cancel"),
	)
}
