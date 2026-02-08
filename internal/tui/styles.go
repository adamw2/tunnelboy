package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Pip-Boy color palette
var (
	// Primary green colors
	ColorPrimary   = lipgloss.Color("#00FF00") // Bright green
	ColorSecondary = lipgloss.Color("#00CC00") // Medium green
	ColorDim       = lipgloss.Color("#008800") // Dim green
	ColorBg        = lipgloss.Color("#001100") // Dark background
	ColorError     = lipgloss.Color("#FF3333") // Error red
	ColorWarning   = lipgloss.Color("#FFCC00") // Warning yellow
)

// Base styles
var (
	// Border style for main container
	BorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Padding(0, 1)

	// Header style (TUNNELBOY title)
	HeaderStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Padding(0, 1)

	// Subheader style (VAULT-TEC INDUSTRIES)
	SubheaderStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Padding(0, 1)

	// Title style for section headers
	TitleStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Underline(true).
			MarginBottom(1)

	// Normal text
	TextStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	// Dim text (for hints, secondary info)
	DimStyle = lipgloss.NewStyle().
			Foreground(ColorDim)

	// Selected item style
	SelectedStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Background(lipgloss.Color("#003300"))

	// Unselected item style
	ItemStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	// Status bar style
	StatusStyle = lipgloss.NewStyle().
			Foreground(ColorDim).
			Padding(0, 1).
			MarginTop(1)

	// Error style
	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	// Warning style
	WarningStyle = lipgloss.NewStyle().
			Foreground(ColorWarning)

	// Success style
	SuccessStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	// Input prompt style
	PromptStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	// Input cursor style
	CursorStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	// Table header style
	TableHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true).
				Underline(true)

	// Table row style
	TableRowStyle = lipgloss.NewStyle().
			Foreground(ColorSecondary)

	// Spinner style
	SpinnerStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)
)

// RenderHeader renders the TunnelBoy header
func RenderHeader(version string) string {
	left := HeaderStyle.Render("TUNNELBOY " + version)
	right := SubheaderStyle.Render("VAULT-TEC INDUSTRIES")

	// Calculate spacing
	width := 60
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	spacing := width - leftWidth - rightWidth

	if spacing < 1 {
		spacing = 1
	}

	spacer := lipgloss.NewStyle().Width(spacing).Render("")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, spacer, right)
}

// RenderDivider renders a horizontal divider
func RenderDivider(width int) string {
	divider := ""
	for i := 0; i < width; i++ {
		divider += "─"
	}
	return DimStyle.Render(divider)
}

// RenderStatusBar renders the bottom status bar with keybindings
func RenderStatusBar(hints string) string {
	return StatusStyle.Render(hints)
}

// FormatSSMStatus formats SSM status indicator
func FormatSSMStatus(enabled bool) string {
	if enabled {
		return SuccessStyle.Render("✓")
	}
	return ErrorStyle.Render("✗")
}
