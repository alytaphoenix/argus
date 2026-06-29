package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/api"
)

// ── Home Assistant tab ────────────────────────────────────────────────────────

type haLoadedMsg struct {
	entities []api.HAEntity
	version  string
	err      error
}

func (m ServicesModel) loadHA() tea.Cmd {
	client := m.haClient
	return func() tea.Msg {
		if client == nil {
			return haLoadedMsg{err: fmt.Errorf("not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		version, _ := client.Version(ctx)
		entities, err := client.States(ctx)
		return haLoadedMsg{entities: entities, version: version, err: err}
	}
}

func (m ServicesModel) updateHA(msg tea.Msg) (ServicesModel, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case haLoadedMsg:
		m.haLoading = false
		m.haChecked = true
		if msg.err != nil {
			if msg.err.Error() != "not configured" {
				m.haErr = msg.err.Error()
			}
		} else {
			m.haEntities = msg.entities
			m.haVersion = msg.version
			m.haErr = ""
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m ServicesModel) handleHATab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.haCursor > 0 {
			m.haCursor--
		}
	case "down", "j":
		if m.haCursor < len(m.haEntities)-1 {
			m.haCursor++
		}
	case "r":
		m.haLoading = true
		m.haChecked = false
		return m, m.loadHA()
	}
	return m, nil
}

func (m ServicesModel) viewHomeAssistant(height int) string {
	var b strings.Builder
	if m.haClient == nil {
		b.WriteString(styleDim.Render("  Home Assistant not configured — add [homeassistant] to config.toml"))
		return b.String()
	}
	if m.haLoading {
		b.WriteString(fmt.Sprintf("  %s loading…", m.spinner.View()))
		return b.String()
	}
	if m.haErr != "" {
		b.WriteString(styleError.Render("  error: " + m.haErr))
		return b.String()
	}

	header := "  Home Assistant"
	if m.haVersion != "" {
		header += fmt.Sprintf(" v%s", m.haVersion)
	}
	b.WriteString(styleBold.Render(header))
	b.WriteString(fmt.Sprintf("  (%d entities)\n\n", len(m.haEntities)))

	if len(m.haEntities) == 0 {
		b.WriteString(styleDim.Render("  no entities"))
		return b.String()
	}

	maxRows := height - 4
	if maxRows < 1 {
		maxRows = 1
	}
	start := 0
	if m.haCursor >= maxRows {
		start = m.haCursor - maxRows + 1
	}

	for i := start; i < len(m.haEntities) && i < start+maxRows; i++ {
		e := m.haEntities[i]
		domain := e.EntityID
		if idx := strings.Index(e.EntityID, "."); idx >= 0 {
			domain = e.EntityID[:idx]
		}
		stateStyle := styleDim
		switch e.State {
		case "on", "home", "playing", "open":
			stateStyle = styleRunning
		case "unavailable", "unknown":
			stateStyle = styleError
		}
		cursor := "  "
		if i == m.haCursor {
			cursor = styleSelected.Render("▸ ")
		}
		name := truncate(e.EntityID, 40)
		b.WriteString(fmt.Sprintf("%s%-8s %-40s %s\n",
			cursor,
			styleDim.Render(domain),
			name,
			stateStyle.Render(e.State),
		))
	}
	return b.String()
}

func (m ServicesModel) haHelpString() string {
	return "  ↑/↓ entities · r refresh · ←/→ tab · b back"
}
