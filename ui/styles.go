package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Dark-mode pink theme (Catppuccin Mocha-inspired with pink focus)
var (
	colorPink   = lipgloss.Color("#f5c2e7") // pink - titles, active tab
	colorPurple = lipgloss.Color("#cba6f7") // mauve - accents, focused input
	colorGreen  = lipgloss.Color("#a6e3a1") // green - running
	colorRed    = lipgloss.Color("#f38ba8") // red - stopped / errors
	colorYellow = lipgloss.Color("#f9e2af") // yellow - paused
	colorGray   = lipgloss.Color("#6c7086") // overlay0 - dim / help
	colorWhite  = lipgloss.Color("#cdd6f4") // text
	colorBgBase = lipgloss.Color("#1e1e2e") // base background
	colorBgSel  = lipgloss.Color("#313244") // surface0 - selected row

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colorPink)
	styleDim      = lipgloss.NewStyle().Foreground(colorGray)
	styleBold     = lipgloss.NewStyle().Bold(true).Foreground(colorWhite)
	styleHelp     = lipgloss.NewStyle().Foreground(colorGray)
	styleError    = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	styleSelected = lipgloss.NewStyle().Background(colorBgSel).Foreground(colorWhite)
	styleRunning  = lipgloss.NewStyle().Foreground(colorGreen)
	styleStopped  = lipgloss.NewStyle().Foreground(colorRed)
	stylePaused   = lipgloss.NewStyle().Foreground(colorYellow)
	styleCyan     = lipgloss.NewStyle().Foreground(colorPurple) // used for focused fields

	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(colorPink)
	styleTabInactive = lipgloss.NewStyle().Foreground(colorGray)
	styleTabBar      = lipgloss.NewStyle().Background(colorBgBase)
	styleSubtle      = lipgloss.NewStyle().Foreground(colorWhite).Faint(true) // column headers
)

func statusDot(status string) string {
	switch status {
	case "running", "online":
		return styleRunning.Render("●")
	case "stopped", "offline":
		return styleStopped.Render("○")
	case "paused", "suspended":
		return stylePaused.Render("◐")
	default:
		return styleDim.Render("?")
	}
}

func progressBar(ratio float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	switch {
	case ratio > 0.9:
		return styleStopped.Render(bar)
	case ratio > 0.7:
		return stylePaused.Render(bar)
	default:
		return styleRunning.Render(bar)
	}
}

func fmtBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func fmtUptime(seconds int64) string {
	if seconds == 0 {
		return "-"
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// sparkline renders a slice of 0-1 ratios as a single-line ASCII chart using
// block elements (▁▂▃▄▅▆▇█). Empty/nil input returns spaces of width w.
func sparkline(values []float64, w int) string {
	if w <= 0 {
		return ""
	}
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	if len(values) == 0 {
		return strings.Repeat(" ", w)
	}
	// Sample values to fill exactly w chars
	out := make([]rune, w)
	for i := range out {
		idx := i * len(values) / w
		v := values[idx]
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		bi := int(v * float64(len(blocks)-1))
		out[i] = blocks[bi]
	}
	return styleDim.Render(string(out))
}

// renderHelpOverlay returns a centred overlay box with key bindings.
// lines is a list of "key  description" entries.
func renderHelpOverlay(title string, lines []string, w, h int) string {
	inner := max(40, w/2)
	var sb strings.Builder
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPurple).
		Padding(0, 1).
		Width(inner)

	var content strings.Builder
	content.WriteString(styleBold.Render(title) + "\n\n")
	for _, l := range lines {
		content.WriteString(styleDim.Render("  ") + l + "\n")
	}
	box := border.Render(content.String())
	// Centre horizontally
	pad := (w - lipgloss.Width(box)) / 2
	if pad < 0 {
		pad = 0
	}
	prefix := strings.Repeat(" ", pad)
	for _, line := range strings.Split(box, "\n") {
		sb.WriteString(prefix + line + "\n")
	}
	_ = h
	return sb.String()
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
