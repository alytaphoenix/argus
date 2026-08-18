package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/db"
)

type profilesTab int

const (
	profTabCloudInit profilesTab = iota
	profTabSalt
	profTabHistory
	profTabSaltEvents  // salt apply event log
	profTabNomadEvents // nomad job event log
)

type editingProfile int

const (
	editNone editingProfile = iota
	editCloudInit
	editSalt
)

// ProfilesModel manages saved cloud-init and salt profiles, VM history,
// and the salt/nomad event logs.
type ProfilesModel struct {
	db                *db.DB
	tab               profilesTab
	cursor            int
	width             int
	height            int
	editing           editingProfile
	editIdx           int // index into current list being edited (-1 = new)
	inputs            []textinput.Model
	focused           int
	err               error
	notice            string
	confirmDelete     bool
	confirmDeleteTab  profilesTab
	confirmDeleteName string
	ciProfs           []db.CloudInitProfile
	saltProfs         []db.SaltProfile
	history           []db.ProvisionedVM
	saltEvents        []db.SaltApplyEvent
	nomadEvents       []db.NomadJobEvent
	ciUsage           map[string]int // profile name → VM count
	saltUsage         map[string]int // profile name → apply event count
}

func NewProfilesModel(database *db.DB) ProfilesModel {
	m := ProfilesModel{db: database}
	m.reload()
	return m
}

func (m *ProfilesModel) reload() {
	if m.db == nil {
		return
	}
	m.ciProfs, _ = m.db.CloudInitProfiles()
	m.saltProfs, _ = m.db.SaltProfiles()
	m.history, _ = m.db.ProvisionedVMs()
	m.saltEvents, _ = m.db.RecentSaltApplyEvents(100)
	m.nomadEvents, _ = m.db.RecentNomadEvents(100)
	m.ciUsage, m.saltUsage, _ = m.db.ProfileUsageCounts()
}

func (m ProfilesModel) Init() tea.Cmd { return nil }

func ciInputs(p db.CloudInitProfile) []textinput.Model {
	labels := []string{p.Name, p.CIUser, p.CIIPConfig, p.Nameserver, p.SearchDomain, p.Notes}
	hints := []string{
		"profile name", "cloud-init user", "ip=dhcp or ip=x/24,gw=...",
		"nameserver", "search domain", "notes",
	}
	inputs := make([]textinput.Model, len(labels))
	for i := range inputs {
		ti := textinput.New()
		ti.Placeholder = hints[i]
		ti.SetValue(labels[i])
		ti.CharLimit = 256
		inputs[i] = ti
	}
	// SSH key gets larger limit
	sshInput := textinput.New()
	sshInput.Placeholder = "SSH public key"
	sshInput.SetValue(p.CISSHKey)
	sshInput.CharLimit = 2048
	inputs = append(inputs, sshInput)
	// password masked
	pwInput := textinput.New()
	pwInput.Placeholder = "cloud-init password"
	pwInput.SetValue(p.CIPassword)
	pwInput.EchoMode = textinput.EchoPassword
	pwInput.CharLimit = 256
	inputs = append(inputs, pwInput)
	inputs[0].Focus()
	return inputs
}

func saltInputs(p db.SaltProfile) []textinput.Model {
	vals := []string{p.Name, p.Target, p.States, p.Notes, p.PillarYAML}
	hints := []string{
		"profile name",
		"target (minion or glob)",
		"states (comma-separated)",
		"notes",
		"pillar yaml (optional; written to master before state.apply)",
	}
	limits := []int{256, 512, 512, 256, 4096}
	inputs := make([]textinput.Model, len(vals))
	for i := range inputs {
		ti := textinput.New()
		ti.Placeholder = hints[i]
		ti.SetValue(vals[i])
		ti.CharLimit = limits[i]
		inputs[i] = ti
	}
	inputs[0].Focus()
	return inputs
}

func (m ProfilesModel) Update(msg tea.Msg) (ProfilesModel, tea.Cmd) {
	if m.editing != editNone {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "ctrl+s":
				return m.saveEditing()
			case "esc":
				m.editing = editNone
				m.inputs = nil
				return m, nil
			case "tab", "down":
				m.inputs[m.focused].Blur()
				m.focused = (m.focused + 1) % len(m.inputs)
				m.inputs[m.focused].Focus()
				return m, textinput.Blink
			case "shift+tab", "up":
				m.inputs[m.focused].Blur()
				m.focused = (m.focused - 1 + len(m.inputs)) % len(m.inputs)
				m.inputs[m.focused].Focus()
				return m, textinput.Blink
			}
		}
		var cmd tea.Cmd
		m.inputs[m.focused], cmd = m.inputs[m.focused].Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y", "enter":
				return m.deleteConfirmed()
			default:
				m.confirmDelete = false
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			return m, func() tea.Msg { return backMsg{} }
		case "right", "l":
			m.tab = (m.tab + 1) % 5
			m.cursor = 0
			m.notice, m.err = "", nil
		case "left", "h":
			m.tab = (m.tab + 4) % 5
			m.cursor = 0
			m.notice, m.err = "", nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
		case "n":
			return m.startNew()
		case "enter", "e":
			return m.startEdit()
		case "d":
			return m.armDelete()
		case "R":
			m.reload()
			m.notice = "refreshed"
		}
	}
	return m, nil
}

func (m ProfilesModel) listLen() int {
	switch m.tab {
	case profTabCloudInit:
		return len(m.ciProfs)
	case profTabSalt:
		return len(m.saltProfs)
	case profTabHistory:
		return len(m.history)
	case profTabSaltEvents:
		return len(m.saltEvents)
	case profTabNomadEvents:
		return len(m.nomadEvents)
	}
	return 0
}

func (m ProfilesModel) startNew() (ProfilesModel, tea.Cmd) {
	m.editIdx = -1
	m.focused = 0
	m.err = nil
	switch m.tab {
	case profTabCloudInit:
		m.editing = editCloudInit
		m.inputs = ciInputs(db.CloudInitProfile{})
	case profTabSalt:
		m.editing = editSalt
		m.inputs = saltInputs(db.SaltProfile{})
	}
	return m, textinput.Blink
}

func (m ProfilesModel) startEdit() (ProfilesModel, tea.Cmd) {
	m.focused = 0
	m.err = nil
	switch m.tab {
	case profTabCloudInit:
		if m.cursor >= len(m.ciProfs) {
			return m, nil
		}
		m.editIdx = m.cursor
		m.editing = editCloudInit
		m.inputs = ciInputs(m.ciProfs[m.cursor])
	case profTabSalt:
		if m.cursor >= len(m.saltProfs) {
			return m, nil
		}
		m.editIdx = m.cursor
		m.editing = editSalt
		m.inputs = saltInputs(m.saltProfs[m.cursor])
	}
	return m, textinput.Blink
}

func (m ProfilesModel) saveEditing() (ProfilesModel, tea.Cmd) {
	m.err = nil
	switch m.editing {
	case editCloudInit:
		// inputs order: name, ciUser, ipconfig, nameserver, searchdomain, notes, sshkey, password
		p := db.CloudInitProfile{
			Name:         m.inputs[0].Value(),
			CIUser:       m.inputs[1].Value(),
			CIIPConfig:   m.inputs[2].Value(),
			Nameserver:   m.inputs[3].Value(),
			SearchDomain: m.inputs[4].Value(),
			Notes:        m.inputs[5].Value(),
			CISSHKey:     m.inputs[6].Value(),
			CIPassword:   m.inputs[7].Value(),
		}
		if p.Name == "" {
			m.err = fmt.Errorf("profile name is required")
			return m, nil
		}
		if m.editIdx >= 0 && m.editIdx < len(m.ciProfs) {
			p.ID = m.ciProfs[m.editIdx].ID
		}
		if err := m.db.SaveCloudInitProfile(p); err != nil {
			m.err = err
			return m, nil
		}
		m.notice = "saved cloud-init profile " + p.Name

	case editSalt:
		// inputs order: name, target, states, notes, pillar_yaml
		p := db.SaltProfile{
			Name:       m.inputs[0].Value(),
			Target:     m.inputs[1].Value(),
			States:     m.inputs[2].Value(),
			Notes:      m.inputs[3].Value(),
			PillarYAML: m.inputs[4].Value(),
		}
		if p.Name == "" || p.Target == "" {
			m.err = fmt.Errorf("profile name and target are required")
			return m, nil
		}
		if m.editIdx >= 0 && m.editIdx < len(m.saltProfs) {
			p.ID = m.saltProfs[m.editIdx].ID
		}
		if err := m.db.SaveSaltProfile(p); err != nil {
			m.err = err
			return m, nil
		}
		m.notice = "saved salt profile " + p.Name
	}
	m.editing = editNone
	m.inputs = nil
	m.reload()
	return m, nil
}

// armDelete captures the currently selected profile's tab and name and asks
// for confirmation before deleteConfirmed actually removes it. Only the
// Cloud-init and Salt tabs hold deletable profiles.
func (m ProfilesModel) armDelete() (ProfilesModel, tea.Cmd) {
	switch m.tab {
	case profTabCloudInit:
		if m.cursor < len(m.ciProfs) {
			m.confirmDelete = true
			m.confirmDeleteTab = m.tab
			m.confirmDeleteName = m.ciProfs[m.cursor].Name
		}
	case profTabSalt:
		if m.cursor < len(m.saltProfs) {
			m.confirmDelete = true
			m.confirmDeleteTab = m.tab
			m.confirmDeleteName = m.saltProfs[m.cursor].Name
		}
	}
	return m, nil
}

func (m ProfilesModel) deleteConfirmed() (ProfilesModel, tea.Cmd) {
	m.confirmDelete = false
	name := m.confirmDeleteName
	var err error
	switch m.confirmDeleteTab {
	case profTabCloudInit:
		err = m.db.DeleteCloudInitProfile(name)
	case profTabSalt:
		err = m.db.DeleteSaltProfile(name)
	}
	if err != nil {
		m.err = err
	} else {
		m.err = nil
		m.notice = "deleted " + name
		m.reload()
		if m.cursor > 0 && m.cursor >= m.listLen() {
			m.cursor = m.listLen() - 1
		}
	}
	return m, nil
}

func (m ProfilesModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("profiles & history") + "\n\n")

	// Sub-tabs
	tabs := []string{"Cloud-init", "Salt", "History", "Salt Events", "Nomad Events"}
	for i, t := range tabs {
		if profilesTab(i) == m.tab {
			sb.WriteString(styleTabActive.Render("▸ " + t))
		} else {
			sb.WriteString(styleTabInactive.Render("  " + t))
		}
		if i < len(tabs)-1 {
			sb.WriteString(styleDim.Render("  │  "))
		}
	}
	sb.WriteString("\n" + styleDim.Render(strings.Repeat("─", max(m.width-4, 40))) + "\n\n")

	if m.editing != editNone {
		return sb.String() + m.editView()
	}

	switch m.tab {
	case profTabCloudInit:
		sb.WriteString(m.ciView())
	case profTabSalt:
		sb.WriteString(m.saltView())
	case profTabHistory:
		sb.WriteString(m.historyView())
	case profTabSaltEvents:
		sb.WriteString(m.saltEventsView())
	case profTabNomadEvents:
		sb.WriteString(m.nomadEventsView())
	}

	sb.WriteString("\n")
	if m.confirmDelete {
		noun := "cloud-init profile"
		if m.confirmDeleteTab == profTabSalt {
			noun = "salt profile"
		}
		sb.WriteString(styleError.Render(fmt.Sprintf("  Delete %s %q? [Y/n]", noun, m.confirmDeleteName)) + "\n\n")
	} else if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
	} else {
		sb.WriteString("\n\n")
	}

	help := "  n new  enter/e edit  d delete  ←/→ switch tab  R refresh  b back  q quit"
	if m.tab == profTabHistory || m.tab == profTabSaltEvents || m.tab == profTabNomadEvents {
		help = "  ←/→ switch tab  R refresh  b back  q quit"
	}
	sb.WriteString(styleHelp.Render(help) + "\n")
	return sb.String()
}

func (m ProfilesModel) ciView() string {
	var sb strings.Builder
	if len(m.ciProfs) == 0 {
		sb.WriteString(styleDim.Render("  no cloud-init profiles — press n to create one") + "\n")
		return sb.String()
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-20s %-12s %-20s  %-5s  %s",
		"NAME", "USER", "IP CONFIG", "VMs", "UPDATED")) + "\n")
	for i, p := range m.ciProfs {
		vmCount := m.ciUsage[p.Name]
		vmStr := styleDim.Render("—")
		if vmCount > 0 {
			vmStr = styleRunning.Render(fmt.Sprintf("%d", vmCount))
		}
		line := fmt.Sprintf("  %-20s %-12s %-20s  %-5s  %s",
			truncate(p.Name, 20),
			truncate(p.CIUser, 12),
			truncate(p.CIIPConfig, 20),
			vmStr,
			p.UpdatedAt.Format(time.RFC3339)[:10])
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m ProfilesModel) saltView() string {
	var sb strings.Builder
	if len(m.saltProfs) == 0 {
		sb.WriteString(styleDim.Render("  no salt profiles — press n to create one") + "\n")
		return sb.String()
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-20s %-25s  %-6s  %s",
		"NAME", "TARGET", "RUNS", "STATES")) + "\n")
	for i, p := range m.saltProfs {
		runCount := m.saltUsage[p.Name]
		runStr := styleDim.Render("—")
		if runCount > 0 {
			runStr = styleRunning.Render(fmt.Sprintf("%d", runCount))
		}
		line := fmt.Sprintf("  %-20s %-25s  %-6s  %s",
			truncate(p.Name, 20),
			truncate(p.Target, 25),
			runStr,
			truncate(p.States, 40))
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m ProfilesModel) historyView() string {
	var sb strings.Builder
	if len(m.history) == 0 {
		sb.WriteString(styleDim.Render("  no provisioned VMs recorded yet") + "\n")
		return sb.String()
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-6s %-18s %-10s %-15s %-15s %-12s  %s",
		"VMID", "NAME", "NODE", "CLOUD-INIT", "SALT", "STATUS", "PROVISIONED")) + "\n")
	for i, vm := range m.history {
		ciProf := styleDim.Render("—")
		if vm.CloudInitProfile != "" {
			ciProf = vm.CloudInitProfile
		}
		saltProf := styleDim.Render("—")
		if vm.SaltProfile != "" {
			saltProf = vm.SaltProfile
		}
		statusStr := styleDim.Render("—")
		if vm.LastStatus != "" {
			statusStr = statusDot(vm.LastStatus) + " " + vm.LastStatus
		}
		line := fmt.Sprintf("  %-6d %-18s %-10s %-15s %-15s %-12s  %s",
			vm.VMID,
			truncate(vm.Name, 18),
			truncate(vm.Node, 10),
			truncate(ciProf, 15),
			truncate(saltProf, 15),
			truncate(statusStr, 12),
			vm.ProvisionedAt.Format("2006-01-02 15:04"),
		)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	// detail panel for selected VM
	if len(m.history) > 0 && m.cursor < len(m.history) {
		vm := m.history[m.cursor]
		sb.WriteString("\n")
		sb.WriteString(styleDim.Render("  ──────────────────────────────────────────────") + "\n")
		if vm.VMIP != "" {
			sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("IP"), vm.VMIP))
		}
		if vm.VMType != "" {
			sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("Type"), vm.VMType))
		}
		if vm.TemplateID != 0 {
			sb.WriteString(fmt.Sprintf("  %-16s %d\n", styleDim.Render("Template"), vm.TemplateID))
		}
		if vm.CloudInitAppliedAt != nil {
			sb.WriteString(fmt.Sprintf("  %-16s %s\n",
				styleDim.Render("CI applied"),
				vm.CloudInitAppliedAt.Format("2006-01-02 15:04")))
		}
		if vm.SaltAppliedAt != nil {
			sb.WriteString(fmt.Sprintf("  %-16s %s\n",
				styleDim.Render("Salt applied"),
				vm.SaltAppliedAt.Format("2006-01-02 15:04")))
		}
		if vm.Notes != "" {
			sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("Notes"), vm.Notes))
		}
	}
	return sb.String()
}

func (m ProfilesModel) saltEventsView() string {
	var sb strings.Builder
	if len(m.saltEvents) == 0 {
		sb.WriteString(styleDim.Render("  no salt apply events recorded yet") + "\n")
		return sb.String()
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-16s  %s  %-20s  %-18s  %s",
		"WHEN", " ", "MINION", "STATES", "PROFILE")) + "\n")
	for i, ev := range m.saltEvents {
		dot := styleRunning.Render("✓")
		if !ev.Success {
			dot = styleError.Render("✗")
		}
		profStr := styleDim.Render("—")
		if ev.SaltProfile != "" {
			profStr = ev.SaltProfile
		}
		line := fmt.Sprintf("  %-16s  %s  %-20s  %-18s  %s",
			ev.TriggeredAt.Format("2006-01-02 15:04"),
			dot,
			truncate(ev.Minion, 20),
			truncate(ev.States, 18),
			truncate(profStr, 18),
		)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	// detail for selected event
	if len(m.saltEvents) > 0 && m.cursor < len(m.saltEvents) {
		ev := m.saltEvents[m.cursor]
		if !ev.Success && ev.ErrorMsg != "" {
			sb.WriteString("\n")
			sb.WriteString(styleError.Render("  error: "+ev.ErrorMsg) + "\n")
		}
		if ev.JID != "" {
			sb.WriteString(fmt.Sprintf("\n  %-16s %s\n", styleDim.Render("JID"), ev.JID))
		}
	}
	return sb.String()
}

func (m ProfilesModel) nomadEventsView() string {
	var sb strings.Builder
	if len(m.nomadEvents) == 0 {
		sb.WriteString(styleDim.Render("  no nomad job events recorded yet — load the Nomad tab to start tracking") + "\n")
		return sb.String()
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-16s  %s  %-32s  %-16s  %s",
		"WHEN", " ", "JOB", "EVENT", "STATUS")) + "\n")
	for i, ev := range m.nomadEvents {
		dot := nomadEventDot(ev.EventType)
		statusStr := ev.NewStatus
		if ev.PrevStatus != "" {
			statusStr = ev.PrevStatus + "→" + ev.NewStatus
		}
		counts := ""
		if ev.Running > 0 || ev.Failed > 0 || ev.Lost > 0 {
			counts = fmt.Sprintf(" r=%d f=%d l=%d", ev.Running, ev.Failed, ev.Lost)
		}
		line := fmt.Sprintf("  %-16s  %s  %-32s  %-16s  %s",
			ev.RecordedAt.Format("2006-01-02 15:04"),
			dot,
			truncate(ev.JobID, 32),
			truncate(ev.EventType, 16),
			styleDim.Render(statusStr+counts),
		)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m ProfilesModel) editView() string {
	var sb strings.Builder
	if m.editing == editCloudInit {
		sb.WriteString(styleDim.Render("  Cloud-init profile") + "\n\n")
		labels := []string{"Name", "User", "IP config", "Nameserver", "Search domain", "Notes", "SSH key", "Password"}
		for i, inp := range m.inputs {
			prefix := "  "
			if i == m.focused {
				prefix = styleCyan.Render("▶ ")
			}
			sb.WriteString(fmt.Sprintf("%s%-16s %s\n", prefix, styleDim.Render(labels[i]), inp.View()))
		}
	} else {
		sb.WriteString(styleDim.Render("  Salt profile") + "\n\n")
		labels := []string{"Name", "Target", "States", "Notes"}
		for i, inp := range m.inputs {
			prefix := "  "
			if i == m.focused {
				prefix = styleCyan.Render("▶ ")
			}
			sb.WriteString(fmt.Sprintf("%s%-16s %s\n", prefix, styleDim.Render(labels[i]), inp.View()))
		}
	}
	if m.err != nil {
		sb.WriteString("\n" + styleError.Render("  error: "+m.err.Error()) + "\n")
	}
	sb.WriteString("\n" + styleHelp.Render("  tab/↓ next  shift+tab/↑ prev  ctrl+s save  esc cancel") + "\n")
	return sb.String()
}
