package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
)

// ClaudeOverlayModel is a floating overlay for natural-language homelab queries.
type ClaudeOverlayModel struct {
	visible  bool
	input    textinput.Model
	answer   string
	err      error
	loading  bool
	spinner  spinner.Model
	client   *api.ClaudeClient
	context  string // snapshot of homelab state injected as system prompt
	history  []string // last N Q&A pairs
	width    int
	height   int
}

func NewClaudeOverlay(client *api.ClaudeClient) ClaudeOverlayModel {
	ti := textinput.New()
	ti.Placeholder = "ask about your homelab… (e.g. which VMs are stopped?)"
	ti.CharLimit = 256

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return ClaudeOverlayModel{
		client:  client,
		input:   ti,
		spinner: sp,
	}
}

func (m ClaudeOverlayModel) Open(ctx string) ClaudeOverlayModel {
	m.visible = true
	m.context = ctx
	m.answer = ""
	m.err = nil
	m.loading = false
	m.input.SetValue("")
	m.input.Focus()
	return m
}

func (m ClaudeOverlayModel) Close() ClaudeOverlayModel {
	m.visible = false
	m.input.Blur()
	return m
}

func (m ClaudeOverlayModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textinput.Blink)
}

func (m ClaudeOverlayModel) queryCmd(question string) tea.Cmd {
	client := m.client
	ctx := m.context
	return func() tea.Msg {
		if client == nil {
			return claudeResponseMsg{err: fmt.Errorf("Claude API key not configured — add it via Keys screen")}
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		answer, err := client.NaturalLanguageQuery(c, ctx, question)
		return claudeResponseMsg{answer: answer, err: err}
	}
}

func (m ClaudeOverlayModel) Update(msg tea.Msg) (ClaudeOverlayModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	switch msg := msg.(type) {
	case claudeResponseMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.answer = msg.answer
			q := m.input.Value()
			m.history = append(m.history, q+"  →  "+truncate(msg.answer, 80))
			if len(m.history) > 10 {
				m.history = m.history[len(m.history)-10:]
			}
		}
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+a":
			return m.Close(), nil
		case "enter":
			q := strings.TrimSpace(m.input.Value())
			if q != "" && !m.loading {
				m.loading = true
				m.answer = ""
				m.err = nil
				return m, m.queryCmd(q)
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m ClaudeOverlayModel) View() string {
	if !m.visible {
		return ""
	}

	w := m.width
	if w < 60 {
		w = 60
	}
	inner := w * 3 / 4

	var content strings.Builder
	content.WriteString(styleTitle.Render("  Claude AI  ") +
		styleDim.Render("ctrl+a or esc to close") + "\n\n")

	// History
	if len(m.history) > 0 {
		content.WriteString(styleDim.Render("  Recent:\n"))
		for _, h := range m.history {
			content.WriteString(styleDim.Render("    " + truncate(h, inner-6)) + "\n")
		}
		content.WriteString("\n")
	}

	// Input
	content.WriteString(styleBold.Render("  Question: ") + m.input.View() + "\n\n")

	// Response
	switch {
	case m.loading:
		content.WriteString("  " + m.spinner.View() + styleDim.Render(" asking Claude…") + "\n")
	case m.err != nil:
		content.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n")
	case m.answer != "":
		content.WriteString(styleBold.Render("  Answer:\n"))
		for _, line := range strings.Split(m.answer, "\n") {
			content.WriteString("  " + line + "\n")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPurple).
		Width(inner).
		Render(content.String())

	// Centre horizontally
	pad := (w - lipgloss.Width(box)) / 2
	if pad < 0 {
		pad = 0
	}
	prefix := strings.Repeat(" ", pad)
	var out strings.Builder
	for _, line := range strings.Split(box, "\n") {
		out.WriteString(prefix + line + "\n")
	}
	return out.String()
}

// renderOver overlays the Claude box on top of the full background view string.
func (m ClaudeOverlayModel) renderOver(background string, w, h int) string {
	m.width = w
	m.height = h
	overlay := m.View()
	if overlay == "" {
		return background
	}
	// Place overlay after the first 3 lines (tab bar) of the background
	lines := strings.Split(background, "\n")
	overlayLines := strings.Split(strings.TrimRight(overlay, "\n"), "\n")
	insertAt := 3
	if insertAt > len(lines) {
		insertAt = len(lines)
	}
	result := strings.Join(lines[:insertAt], "\n") + "\n" + strings.Join(overlayLines, "\n")
	return result
}
