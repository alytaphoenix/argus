package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/db"
)

// AlertRoutesModel manages routing rules that forward security events to webhooks.
type AlertRoutesModel struct {
	db      *db.DB
	routes  []db.AlertRoute
	cursor  int
	editing bool
	editID  int64

	nameInput    textinput.Model
	sourceInput  textinput.Model
	severityInput textinput.Model
	webhookInput textinput.Model
	editFocus    int

	notice string
	err    error
	width  int
	height int
}

func NewAlertRoutesModel(database *db.DB) AlertRoutesModel {
	make := func(ph string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.CharLimit = 256
		return ti
	}
	m := AlertRoutesModel{
		db:            database,
		nameInput:     make("route name"),
		sourceInput:   make("source: crowdsec, wazuh, or * for all"),
		severityInput: make("severity: critical, high, medium, low, or *"),
		webhookInput:  make("https://hooks.example.com/…"),
	}
	m.reload()
	return m
}

func (m *AlertRoutesModel) reload() {
	if m.db != nil {
		m.routes, _ = m.db.AlertRoutes()
	}
}

func (m AlertRoutesModel) Init() tea.Cmd { return nil }

func (m AlertRoutesModel) Update(msg tea.Msg) (AlertRoutesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case alertFiredMsg:
		if msg.err != nil {
			m.notice = "webhook error: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("route #%d fired", msg.routeID)
		}
		m.reload()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m AlertRoutesModel) handleKey(msg tea.KeyMsg) (AlertRoutesModel, tea.Cmd) {
	key := msg.String()

	if m.editing {
		return m.handleEdit(key, msg)
	}

	switch key {
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.routes)-1 {
			m.cursor++
		}
	case "n":
		m.editing = true
		m.editID = 0
		m.nameInput.SetValue("")
		m.sourceInput.SetValue("*")
		m.severityInput.SetValue("*")
		m.webhookInput.SetValue("")
		m.editFocus = 0
		m.nameInput.Focus()
	case "e":
		if m.cursor < len(m.routes) {
			r := m.routes[m.cursor]
			m.editing = true
			m.editID = r.ID
			m.nameInput.SetValue(r.Name)
			m.sourceInput.SetValue(r.Source)
			m.severityInput.SetValue(r.Severity)
			m.webhookInput.SetValue(r.WebhookURL)
			m.editFocus = 0
			m.nameInput.Focus()
		}
	case "d":
		if m.cursor < len(m.routes) && m.db != nil {
			_ = m.db.DeleteAlertRoute(m.routes[m.cursor].ID)
			m.reload()
			if m.cursor >= len(m.routes) && m.cursor > 0 {
				m.cursor--
			}
		}
	case "t":
		if m.cursor < len(m.routes) {
			r := m.routes[m.cursor]
			return m, m.testWebhookCmd(r)
		}
	}
	return m, nil
}

func (m AlertRoutesModel) handleEdit(key string, msg tea.KeyMsg) (AlertRoutesModel, tea.Cmd) {
	inputs := []*textinput.Model{&m.nameInput, &m.sourceInput, &m.severityInput, &m.webhookInput}
	switch key {
	case "esc":
		inputs[m.editFocus].Blur()
		m.editing = false
	case "tab":
		inputs[m.editFocus].Blur()
		m.editFocus = (m.editFocus + 1) % len(inputs)
		inputs[m.editFocus].Focus()
		return m, nil
	case "enter":
		if m.editFocus < len(inputs)-1 {
			inputs[m.editFocus].Blur()
			m.editFocus++
			inputs[m.editFocus].Focus()
			return m, nil
		}
		route := db.AlertRoute{
			ID:         m.editID,
			Name:       strings.TrimSpace(m.nameInput.Value()),
			Source:     strings.TrimSpace(m.sourceInput.Value()),
			Severity:   strings.TrimSpace(m.severityInput.Value()),
			WebhookURL: strings.TrimSpace(m.webhookInput.Value()),
			Enabled:    true,
		}
		if route.Name == "" || route.WebhookURL == "" {
			m.notice = "name and webhook URL required"
			return m, nil
		}
		if m.db != nil {
			if _, err := m.db.SaveAlertRoute(route); err != nil {
				m.notice = "save error: " + err.Error()
				return m, nil
			}
		}
		inputs[m.editFocus].Blur()
		m.editing = false
		m.reload()
		m.notice = "saved"
	default:
		var cmd tea.Cmd
		switch m.editFocus {
		case 0:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case 1:
			m.sourceInput, cmd = m.sourceInput.Update(msg)
		case 2:
			m.severityInput, cmd = m.severityInput.Update(msg)
		case 3:
			m.webhookInput, cmd = m.webhookInput.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m AlertRoutesModel) testWebhookCmd(r db.AlertRoute) tea.Cmd {
	database := m.db
	routeID := r.ID
	webhookURL := r.WebhookURL
	return func() tea.Msg {
		payload, _ := json.Marshal(map[string]string{
			"source":   "ratataskr",
			"severity": "test",
			"message":  "Test alert from ratataskr alert routing",
			"route":    r.Name,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := (&http.Client{}).Do(func() *http.Request {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL,
				bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			return req
		}())
		if err != nil {
			return alertFiredMsg{routeID: routeID, err: err}
		}
		resp.Body.Close()
		if database != nil {
			_ = database.TouchAlertRoute(routeID)
		}
		return alertFiredMsg{routeID: routeID}
	}
}

// FireMatchingRoutes sends a webhook for all enabled routes matching source+severity.
// Called from the security screen when new events arrive.
func (m AlertRoutesModel) FireMatchingRoutes(source, severity string) tea.Cmd {
	var matched []db.AlertRoute
	for _, r := range m.routes {
		if !r.Enabled {
			continue
		}
		srcMatch := r.Source == "" || r.Source == "*" || r.Source == source
		sevMatch := r.Severity == "" || r.Severity == "*" || r.Severity == severity
		if srcMatch && sevMatch {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	database := m.db
	return func() tea.Msg {
		for _, r := range matched {
			payload, _ := json.Marshal(map[string]string{
				"source":   source,
				"severity": severity,
				"route":    r.Name,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			resp, err := (&http.Client{}).Do(func() *http.Request {
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.WebhookURL,
					bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				return req
			}())
			cancel()
			if err == nil {
				resp.Body.Close()
			}
			if database != nil {
				_ = database.TouchAlertRoute(r.ID)
			}
		}
		return alertFiredMsg{routeID: matched[0].ID}
	}
}

func (m AlertRoutesModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("  Alert Routes") + "\n")
	sb.WriteString(styleDim.Render("  Route security events (CrowdSec/Wazuh) to webhooks") + "\n\n")

	if m.editing {
		sb.WriteString(m.viewEdit())
	} else {
		sb.WriteString(m.viewList())
	}

	if m.notice != "" {
		sb.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	sb.WriteString("\n" + styleDim.Render(m.helpStr()))
	return sb.String()
}

func (m AlertRoutesModel) viewList() string {
	var sb strings.Builder
	if len(m.routes) == 0 {
		sb.WriteString(styleDim.Render("  No alert routes. Press n to create one.\n"))
		return sb.String()
	}
	sb.WriteString(styleSubtle.Render(fmt.Sprintf("  %-4s %-20s %-12s %-12s %-8s  %s\n",
		"#", "Name", "Source", "Severity", "Enabled", "Webhook")))
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 70))) + "\n")
	for i, r := range m.routes {
		enStyle := styleDim
		enStr := "no"
		if r.Enabled {
			enStyle = lipgloss.NewStyle().Foreground(colorGreen)
			enStr = "yes"
		}
		line := fmt.Sprintf("  %-4d %-20s %-12s %-12s %-8s  %s",
			r.ID, truncate(r.Name, 20),
			truncate(r.Source, 12), truncate(r.Severity, 12),
			enStyle.Render(enStr), truncate(r.WebhookURL, 40))
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m AlertRoutesModel) viewEdit() string {
	var sb strings.Builder
	title := "New Alert Route"
	if m.editID != 0 {
		title = "Edit Alert Route"
	}
	sb.WriteString(styleBold.Render("  "+title) + "\n\n")
	fields := []struct {
		label string
		ti    textinput.Model
	}{
		{"  Name     ", m.nameInput},
		{"  Source   ", m.sourceInput},
		{"  Severity ", m.severityInput},
		{"  Webhook  ", m.webhookInput},
	}
	for i, f := range fields {
		lbl := f.label
		if i == m.editFocus {
			sb.WriteString(styleCyan.Render(lbl) + f.ti.View() + "\n")
		} else {
			sb.WriteString(styleDim.Render(lbl) + f.ti.View() + "\n")
		}
	}
	sb.WriteString(styleDim.Render("\n  Source: crowdsec | wazuh | *    Severity: critical | high | medium | low | *\n"))
	return sb.String()
}

func (m AlertRoutesModel) helpStr() string {
	if m.editing {
		return "  tab advance · enter save · esc cancel"
	}
	return "  ↑/↓ navigate · n new · e edit · d delete · t test webhook · b back"
}
