package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// LifecycleModel manages VM lifecycle policies (auto-stop/snapshot idle VMs).
type LifecycleModel struct {
	db            *db.DB
	proxmoxClient *api.Client

	policies []db.VMLifecyclePolicy
	cursor   int
	editing  bool
	editID   int64

	nameInput    textinput.Model
	tagInput     textinput.Model
	actionInput  textinput.Model
	idleInput    textinput.Model
	editFocus    int

	running bool
	notice  string
	err     error
	width   int
	height  int
}

func NewLifecycleModel(database *db.DB, proxmoxClient *api.Client) LifecycleModel {
	make := func(ph string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.CharLimit = 128
		return ti
	}
	m := LifecycleModel{
		db:            database,
		proxmoxClient: proxmoxClient,
		nameInput:     make("policy name"),
		tagInput:      make("VM name tag/prefix (e.g. dev-) — required"),
		actionInput:   make("stop | snapshot"),
		idleInput:     make("uptime hours threshold (e.g. 168 = 7 days)"),
	}
	m.reload()
	return m
}

func (m *LifecycleModel) reload() {
	if m.db != nil {
		m.policies, _ = m.db.LifecyclePolicies()
	}
}

func (m LifecycleModel) Init() tea.Cmd { return nil }

func (m LifecycleModel) Update(msg tea.Msg) (LifecycleModel, tea.Cmd) {
	switch msg := msg.(type) {
	case lifecycleRunDoneMsg:
		m.running = false
		if msg.err != nil {
			m.notice = "run error: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("applied %d action(s)", msg.applied)
		}
		m.reload()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m LifecycleModel) handleKey(msg tea.KeyMsg) (LifecycleModel, tea.Cmd) {
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
		if m.cursor < len(m.policies)-1 {
			m.cursor++
		}
	case "n":
		m.editing = true
		m.editID = 0
		m.nameInput.SetValue("")
		m.tagInput.SetValue("")
		m.actionInput.SetValue("stop")
		m.idleInput.SetValue("168")
		m.editFocus = 0
		m.nameInput.Focus()
	case "e":
		if m.cursor < len(m.policies) {
			p := m.policies[m.cursor]
			m.editing = true
			m.editID = p.ID
			m.nameInput.SetValue(p.Name)
			m.tagInput.SetValue(p.Tag)
			m.actionInput.SetValue(p.Action)
			m.idleInput.SetValue(fmt.Sprintf("%d", p.IdleHours))
			m.editFocus = 0
			m.nameInput.Focus()
		}
	case "d":
		if m.cursor < len(m.policies) && m.db != nil {
			_ = m.db.DeleteLifecyclePolicy(m.policies[m.cursor].ID)
			m.reload()
			if m.cursor >= len(m.policies) && m.cursor > 0 {
				m.cursor--
			}
		}
	case "r":
		if m.cursor < len(m.policies) {
			p := m.policies[m.cursor]
			if strings.TrimSpace(p.Tag) == "" {
				m.notice = "refusing to run: empty tag would match every VM — set a tag first"
				return m, nil
			}
			m.running = true
			return m, m.runPolicyCmd(p)
		}
	}
	return m, nil
}

func (m LifecycleModel) handleEdit(key string, msg tea.KeyMsg) (LifecycleModel, tea.Cmd) {
	inputs := []*textinput.Model{&m.nameInput, &m.tagInput, &m.actionInput, &m.idleInput}
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
		var idleHours int
		fmt.Sscanf(m.idleInput.Value(), "%d", &idleHours)
		if idleHours <= 0 {
			idleHours = 168
		}
		p := db.VMLifecyclePolicy{
			ID:        m.editID,
			Name:      strings.TrimSpace(m.nameInput.Value()),
			Tag:       strings.TrimSpace(m.tagInput.Value()),
			Action:    strings.TrimSpace(m.actionInput.Value()),
			IdleHours: idleHours,
			Enabled:   true,
		}
		if p.Name == "" {
			m.notice = "name required"
			return m, nil
		}
		if p.Tag == "" {
			m.notice = "tag required — an empty tag would match every VM"
			return m, nil
		}
		if m.db != nil {
			if _, err := m.db.SaveLifecyclePolicy(p); err != nil {
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
			m.tagInput, cmd = m.tagInput.Update(msg)
		case 2:
			m.actionInput, cmd = m.actionInput.Update(msg)
		case 3:
			m.idleInput, cmd = m.idleInput.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m LifecycleModel) runPolicyCmd(p db.VMLifecyclePolicy) tea.Cmd {
	proxmox := m.proxmoxClient
	database := m.db
	tag := p.Tag
	action := p.Action
	policyID := p.ID

	return func() tea.Msg {
		if proxmox == nil {
			return lifecycleRunDoneMsg{err: fmt.Errorf("Proxmox client not available")}
		}
		if strings.TrimSpace(tag) == "" {
			return lifecycleRunDoneMsg{err: fmt.Errorf("policy has no tag — refusing to match every VM")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		nodes, err := proxmox.Nodes(ctx)
		if err != nil {
			return lifecycleRunDoneMsg{err: err}
		}

		applied := 0
		for _, node := range nodes {
			guests, err := proxmox.Guests(ctx, node.Name)
			if err != nil {
				continue
			}
			for _, g := range guests {
				if !strings.Contains(g.Name, tag) {
					continue
				}
				if g.Status != "running" {
					continue
				}
				// Uptime is a proxy for age, not idleness — the threshold means
				// "running continuously for at least this long".
				upThresh := int64(p.IdleHours) * 3600
				if g.Uptime < upThresh {
					continue
				}
				switch action {
				case "stop":
					if proxmox.GuestAction(ctx, g, "stop") == nil {
						applied++
					}
				case "snapshot":
					snapName := fmt.Sprintf("lifecycle-%s", time.Now().Format("20060102-150405"))
					desc := fmt.Sprintf("auto snapshot by lifecycle policy #%d", policyID)
					if _, err := proxmox.CreateSnapshot(ctx, g.Node, g.Type, g.VMID, snapName, desc); err == nil {
						applied++
					}
				}
			}
		}

		if database != nil {
			_ = database.TouchLifecyclePolicy(policyID)
		}
		return lifecycleRunDoneMsg{applied: applied}
	}
}

func (m LifecycleModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("  VM Lifecycle Policies") + "\n")
	sb.WriteString(styleDim.Render("  Stop or snapshot long-running VMs by name tag (threshold = continuous uptime)") + "\n\n")

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

func (m LifecycleModel) viewList() string {
	var sb strings.Builder
	if len(m.policies) == 0 {
		sb.WriteString(styleDim.Render("  No policies. Press n to create one.\n"))
		return sb.String()
	}
	sb.WriteString(styleSubtle.Render(fmt.Sprintf("  %-4s %-22s %-12s %-10s %-8s  %s\n",
		"#", "Name", "Tag", "Action", "IdleH", "Last Run")))
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 70))) + "\n")
	for i, p := range m.policies {
		lastRun := "never"
		if p.LastRun != nil {
			lastRun = p.LastRun.Format("2006-01-02 15:04")
		}
		enStyle := styleDim
		if !p.Enabled {
			enStyle = lipgloss.NewStyle().Foreground(colorRed)
		}
		line := fmt.Sprintf("  %-4d %s %-12s %-10s %-8d  %s",
			p.ID, enStyle.Render(fmt.Sprintf("%-22s", truncate(p.Name, 22))),
			truncate(p.Tag, 12), p.Action, p.IdleHours, lastRun)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m LifecycleModel) viewEdit() string {
	var sb strings.Builder
	title := "New Policy"
	if m.editID != 0 {
		title = "Edit Policy"
	}
	sb.WriteString(styleBold.Render("  "+title) + "\n\n")
	fields := []struct {
		label string
		ti    textinput.Model
	}{
		{"  Name       ", m.nameInput},
		{"  Tag/Prefix ", m.tagInput},
		{"  Action     ", m.actionInput},
		{"  Uptime Hrs ", m.idleInput},
	}
	for i, f := range fields {
		lbl := f.label
		if i == m.editFocus {
			sb.WriteString(styleCyan.Render(lbl) + f.ti.View() + "\n")
		} else {
			sb.WriteString(styleDim.Render(lbl) + f.ti.View() + "\n")
		}
	}
	sb.WriteString(styleDim.Render("\n  Action: stop | snapshot    Tag: substring match on VM name\n"))
	return sb.String()
}

func (m LifecycleModel) helpStr() string {
	if m.editing {
		return "  tab advance · enter save · esc cancel"
	}
	return "  ↑/↓ navigate · n new · e edit · d delete · r run now · b back"
}
