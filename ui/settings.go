package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/argus/db"
)

// SettingsModel lets the user view and edit app-wide configurable settings
// stored in the app_settings DB table.
type SettingsModel struct {
	width, height int
	database      *db.DB

	settings []db.AppSetting
	cursor   int

	// Inline editing
	editing  bool
	editKey  string
	editDesc string
	input    textinput.Model

	notice string
}

func NewSettingsModel(database *db.DB) SettingsModel {
	ti := textinput.New()
	ti.CharLimit = 256
	return SettingsModel{
		database: database,
		input:    ti,
	}
}

func (m SettingsModel) Init() tea.Cmd {
	return m.loadSettings()
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m SettingsModel) loadSettings() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return settingsLoadedMsg{err: fmt.Errorf("no database")}
		}
		settings, err := database.AllSettings()
		return settingsLoadedMsg{settings: settings, err: err}
	}
}

func (m SettingsModel) saveSettingCmd(key, value string) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return settingsSavedMsg{key: key, err: fmt.Errorf("no database")}
		}
		return settingsSavedMsg{key: key, err: database.SetSetting(key, value)}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m SettingsModel) Update(msg tea.Msg) (SettingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsLoadedMsg:
		if msg.err != nil {
			m.notice = "load error: " + msg.err.Error()
		} else {
			m.settings = msg.settings
			if m.cursor >= len(m.settings) && len(m.settings) > 0 {
				m.cursor = len(m.settings) - 1
			}
		}
		return m, nil

	case settingsSavedMsg:
		m.editing = false
		m.input.SetValue("")
		if msg.err != nil {
			m.notice = "save error: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("saved %s", msg.key)
		}
		return m, m.loadSettings()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m SettingsModel) handleKey(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	key := msg.String()

	if m.editing {
		switch key {
		case "esc":
			m.editing = false
			m.input.SetValue("")
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			return m, m.saveSettingCmd(m.editKey, value)
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.settings)-1 {
			m.cursor++
		}
	case "enter", "e":
		if m.cursor < len(m.settings) {
			s := m.settings[m.cursor]
			m.editing = true
			m.editKey = s.Key
			m.editDesc = s.Description
			m.input.SetValue(s.Value)
			m.input.Focus()
			m.notice = ""
		}
	case "R":
		return m, m.loadSettings()
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	}
	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m SettingsModel) View() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("  Settings") + "\n")
	b.WriteString(styleDim.Render("  App-wide configurable parameters stored in local DB.\n\n"))

	if m.editing {
		b.WriteString(m.viewEditor())
	} else {
		b.WriteString(m.viewList())
	}

	if m.notice != "" {
		b.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m SettingsModel) viewList() string {
	var b strings.Builder

	if len(m.settings) == 0 {
		b.WriteString(styleDim.Render("  No settings found. Press R to reload.\n"))
		return b.String()
	}

	hdr := fmt.Sprintf("  %-38s %-18s  %s\n", "Key", "Value", "Description")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 90)) + "\n"))

	for i, s := range m.settings {
		valStr := s.Value
		if valStr == "" {
			valStr = styleDim.Render("(empty)")
		}
		row := fmt.Sprintf("  %-38s %-18s  %s\n",
			truncate(s.Key, 38), truncate(valStr, 18), truncate(s.Description, 45))
		if i == m.cursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m SettingsModel) viewEditor() string {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Foreground(colorPink).Bold(true).
		Render(fmt.Sprintf("  Editing: %s\n", m.editKey)))
	if m.editDesc != "" {
		b.WriteString(styleDim.Render(fmt.Sprintf("  %s\n", m.editDesc)))
	}
	b.WriteString(styleDim.Render("  ──────────────────────────────────────────\n"))
	b.WriteString("  Value: " + m.input.View() + "\n\n")
	b.WriteString(styleDim.Render("  enter save · esc cancel\n"))
	return b.String()
}

func (m SettingsModel) helpStr() string {
	if m.editing {
		return "  enter save · esc cancel"
	}
	return "  ↑/↓ navigate · enter/e edit · R reload · b back"
}
