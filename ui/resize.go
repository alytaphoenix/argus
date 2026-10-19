package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/db"
)

// ── Model ─────────────────────────────────────────────────────────────────────

type resizeConfirmKind int

const (
	confirmResizeNone   resizeConfirmKind = iota
	confirmResizeApply                    // apply recommended CPU+mem
	confirmResizeDisk                     // apply disk resize from form
	confirmResizeCustom                   // apply custom CPU+mem from form
)

type ResizeModel struct {
	width, height int
	client        *api.Client
	claudeClient  *api.ClaudeClient
	database      *db.DB
	node          api.Node
	guests        []api.Guest

	// Data
	analyses  []api.VMSizingAnalysis // loaded; index matches guests order
	nodeStats *api.NodeStats
	cursor    int
	loading   bool
	spinner   spinner.Model

	// AI explanation state
	askingClaude bool
	explanation  *api.VMResizeExplanation
	explainVMID  int // which VM was explained

	// Confirmation
	confirmKind resizeConfirmKind
	confirmVM   *api.VMSizingAnalysis

	// Custom resize form
	editMode   bool
	editFields [2]textinput.Model // 0=cores, 1=memMB
	editFocus  int

	// Disk resize form
	diskMode   bool
	diskFields [2]textinput.Model // 0=disk key, 1=new size
	diskFocus  int

	notice string
}

func NewResizeModel(client *api.Client, claudeClient *api.ClaudeClient, node api.Node, guests []api.Guest, database *db.DB) ResizeModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// Edit form
	ef := [2]textinput.Model{}
	ef[0] = textinput.New()
	ef[0].Placeholder = "cores (e.g. 2)"
	ef[0].CharLimit = 4
	ef[1] = textinput.New()
	ef[1].Placeholder = "memory MB (e.g. 2048)"
	ef[1].CharLimit = 6

	// Disk form
	df := [2]textinput.Model{}
	df[0] = textinput.New()
	df[0].Placeholder = "disk key (e.g. scsi0)"
	df[0].CharLimit = 16
	df[1] = textinput.New()
	df[1].Placeholder = "new size (e.g. 40G)"
	df[1].CharLimit = 10

	return ResizeModel{
		client:       client,
		claudeClient: claudeClient,
		database:     database,
		node:         node,
		guests:       guests,
		loading:      true,
		spinner:      sp,
		editFields:   ef,
		diskFields:   df,
	}
}

func (m ResizeModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadAnalyses())
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m ResizeModel) loadAnalyses() tea.Cmd {
	client := m.client
	guests := m.guests
	node := m.node
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		ns, _ := client.NodeStatsFetch(ctx, node.Name)
		results := api.LoadAllAnalyses(ctx, client, guests)
		return resizeLoadedMsg{nodeStats: ns, analyses: results}
	}
}

func (m ResizeModel) askClaudeCmd(a api.VMSizingAnalysis) tea.Cmd {
	cl := m.claudeClient
	ns := m.nodeStats
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		exp, err := cl.ExplainVMResize(ctx, a, ns)
		return claudeExplainMsg{vmid: a.Guest.VMID, explanation: exp, err: err}
	}
}

func (m ResizeModel) applyRecommendedCmd(a api.VMSizingAnalysis) tea.Cmd {
	client := m.client
	database := m.database
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.ResizeVMConfig(ctx, a.Guest.Node, a.Guest.Type, a.Guest.VMID,
			a.RecCores, a.RecMemMB)
		if err == nil && database != nil {
			database.RecordResizeEvent(db.ResizeEvent{ //nolint:errcheck
				VMID: a.Guest.VMID, Node: a.Guest.Node, VMType: a.Guest.Type,
				Field:    "cpu",
				OldValue: fmt.Sprintf("%d", a.Config.TotalCores()),
				NewValue: fmt.Sprintf("%d", a.RecCores),
				Reason:   "recommended",
			})
			database.RecordResizeEvent(db.ResizeEvent{ //nolint:errcheck
				VMID: a.Guest.VMID, Node: a.Guest.Node, VMType: a.Guest.Type,
				Field:    "memory",
				OldValue: fmt.Sprintf("%dMB", a.Config.Memory),
				NewValue: fmt.Sprintf("%dMB", a.RecMemMB),
				Reason:   "recommended",
			})
		}
		return resizeAppliedMsg{vmid: a.Guest.VMID, err: err}
	}
}

func (m ResizeModel) applyCustomCmd(a api.VMSizingAnalysis, cores, memMB int) tea.Cmd {
	client := m.client
	database := m.database
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.ResizeVMConfig(ctx, a.Guest.Node, a.Guest.Type, a.Guest.VMID, cores, memMB)
		if err == nil && database != nil {
			if cores != a.Config.TotalCores() {
				database.RecordResizeEvent(db.ResizeEvent{ //nolint:errcheck
					VMID: a.Guest.VMID, Node: a.Guest.Node, VMType: a.Guest.Type,
					Field:    "cpu",
					OldValue: fmt.Sprintf("%d", a.Config.TotalCores()),
					NewValue: fmt.Sprintf("%d", cores),
					Reason:   "manual",
				})
			}
			if memMB != a.Config.Memory {
				database.RecordResizeEvent(db.ResizeEvent{ //nolint:errcheck
					VMID: a.Guest.VMID, Node: a.Guest.Node, VMType: a.Guest.Type,
					Field:    "memory",
					OldValue: fmt.Sprintf("%dMB", a.Config.Memory),
					NewValue: fmt.Sprintf("%dMB", memMB),
					Reason:   "manual",
				})
			}
		}
		return resizeAppliedMsg{vmid: a.Guest.VMID, err: err}
	}
}

func (m ResizeModel) applyDiskCmd(a api.VMSizingAnalysis, disk, size string) tea.Cmd {
	client := m.client
	database := m.database
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.ResizeDisk(ctx, a.Guest.Node, a.Guest.Type, a.Guest.VMID, disk, size)
		if err == nil && database != nil {
			database.RecordResizeEvent(db.ResizeEvent{ //nolint:errcheck
				VMID: a.Guest.VMID, Node: a.Guest.Node, VMType: a.Guest.Type,
				Field:    "disk",
				OldValue: disk,
				NewValue: size,
				Reason:   "manual",
			})
		}
		return diskResizeAppliedMsg{vmid: a.Guest.VMID, err: err}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m ResizeModel) Update(msg tea.Msg) (ResizeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case resizeLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.notice = "load error: " + msg.err.Error()
			return m, nil
		}
		m.nodeStats = msg.nodeStats
		m.analyses = make([]api.VMSizingAnalysis, 0, len(msg.analyses))
		for _, r := range msg.analyses {
			if r.Err == nil {
				m.analyses = append(m.analyses, r.Analysis)
			}
		}
		// Sort: under-provisioned first, over-provisioned second, ok last.
		sort.Slice(m.analyses, func(i, j int) bool {
			return statusOrder(m.analyses[i].OverallStatus()) < statusOrder(m.analyses[j].OverallStatus())
		})
		return m, nil

	case claudeExplainMsg:
		m.askingClaude = false
		if msg.err != nil {
			m.notice = "Claude error: " + msg.err.Error()
			return m, nil
		}
		m.explanation = msg.explanation
		m.explainVMID = msg.vmid
		// Move into confirmation mode.
		if a := m.currentAnalysis(); a != nil {
			m.confirmKind = confirmResizeApply
			m.confirmVM = a
		}
		return m, nil

	case resizeAppliedMsg:
		m.confirmKind = confirmResizeNone
		m.confirmVM = nil
		m.editMode = false
		m.explanation = nil
		if msg.err != nil {
			m.notice = fmt.Sprintf("resize failed: %v", msg.err)
		} else {
			m.notice = fmt.Sprintf("VM %d resized — reboot may be required", msg.vmid)
		}
		// Reload to get updated configs.
		m.loading = true
		return m, m.loadAnalyses()

	case diskResizeAppliedMsg:
		m.diskMode = false
		m.confirmKind = confirmResizeNone
		if msg.err != nil {
			m.notice = fmt.Sprintf("disk resize failed: %v", msg.err)
		} else {
			m.notice = fmt.Sprintf("VM %d disk expanded — no reboot needed", msg.vmid)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m ResizeModel) handleKey(msg tea.KeyMsg) (ResizeModel, tea.Cmd) {
	key := msg.String()

	// Edit form
	if m.editMode {
		return m.handleEditForm(key, msg)
	}
	// Disk resize form
	if m.diskMode {
		return m.handleDiskForm(key, msg)
	}

	// Confirmation dialog
	if m.confirmKind != confirmResizeNone {
		return m.handleConfirm(key)
	}

	// Loading
	if m.loading || m.askingClaude {
		if key == "b" || key == "esc" {
			return m, func() tea.Msg { return backMsg{} }
		}
		return m, nil
	}

	switch key {
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.explanation = nil
		}
	case "down", "j":
		if m.cursor < len(m.analyses)-1 {
			m.cursor++
			m.explanation = nil
		}
	case "R":
		m.loading = true
		m.explanation = nil
		m.notice = ""
		return m, m.loadAnalyses()

	case "?":
		// Ask Claude about the selected VM.
		if m.claudeClient == nil {
			m.notice = "Claude not configured — set ANTHROPIC_API_KEY"
			return m, nil
		}
		if a := m.currentAnalysis(); a != nil {
			m.askingClaude = true
			m.explanation = nil
			m.notice = ""
			return m, m.askClaudeCmd(*a)
		}

	case "r":
		// Apply recommended resize (or ask Claude first if available).
		if a := m.currentAnalysis(); a != nil {
			if a.OverallStatus() == api.StatusWellSized {
				m.notice = "VM is well-sized — no changes recommended"
				return m, nil
			}
			m.confirmKind = confirmResizeApply
			m.confirmVM = a
		}

	case "e":
		// Open custom edit form.
		if a := m.currentAnalysis(); a != nil {
			m.editMode = true
			m.editFocus = 0
			m.editFields[0].SetValue(fmt.Sprintf("%d", a.Config.TotalCores()))
			m.editFields[1].SetValue(fmt.Sprintf("%d", a.Config.Memory))
			m.editFields[0].Focus()
			m.editFields[1].Blur()
			m.notice = ""
		}

	case "d":
		// Open disk resize form.
		if a := m.currentAnalysis(); a != nil {
			m.diskMode = true
			m.diskFocus = 0
			// Pre-fill first disk key if available.
			if len(a.Config.Disks) > 0 {
				m.diskFields[0].SetValue(a.Config.Disks[0].Key)
			} else {
				m.diskFields[0].SetValue("")
			}
			m.diskFields[1].SetValue("")
			m.diskFields[0].Focus()
			m.diskFields[1].Blur()
			m.notice = ""
		}

	case "A":
		// Apply ALL non-ok recommendations at once (no Claude, direct apply).
		var cmds []tea.Cmd
		for _, a := range m.analyses {
			if a.OverallStatus() != api.StatusWellSized && a.OverallStatus() != api.StatusNoData {
				ac := a // capture
				cmds = append(cmds, m.applyRecommendedCmd(ac))
			}
		}
		if len(cmds) == 0 {
			m.notice = "All VMs are well-sized"
		} else {
			m.notice = fmt.Sprintf("applying %d recommendations…", len(cmds))
			m.loading = true
			return m, tea.Batch(cmds...)
		}
	}
	return m, nil
}

func (m ResizeModel) handleConfirm(key string) (ResizeModel, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		switch m.confirmKind {
		case confirmResizeApply:
			if m.confirmVM != nil {
				a := *m.confirmVM
				m.notice = "applying…"
				return m, m.applyRecommendedCmd(a)
			}
		case confirmResizeCustom:
			if m.confirmVM != nil {
				a := *m.confirmVM
				cores, mem := parseEditFields(m.editFields)
				m.notice = "applying custom resize…"
				return m, m.applyCustomCmd(a, cores, mem)
			}
		case confirmResizeDisk:
			if m.confirmVM != nil {
				a := *m.confirmVM
				disk := strings.TrimSpace(m.diskFields[0].Value())
				size := strings.TrimSpace(m.diskFields[1].Value())
				m.notice = "resizing disk…"
				return m, m.applyDiskCmd(a, disk, size)
			}
		}
	case "n", "N", "esc", "q":
		m.confirmKind = confirmResizeNone
		m.confirmVM = nil
		m.notice = "cancelled"
	}
	// Ignore all other keys so an accidental arrow/tab can't silently cancel.
	return m, nil
}

func (m ResizeModel) handleEditForm(key string, msg tea.KeyMsg) (ResizeModel, tea.Cmd) {
	switch key {
	case "esc":
		m.editMode = false
		m.editFields[0].Blur()
		m.editFields[1].Blur()
		return m, nil
	case "tab", "down":
		m.editFields[m.editFocus].Blur()
		m.editFocus = (m.editFocus + 1) % 2
		m.editFields[m.editFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.editFields[m.editFocus].Blur()
		m.editFocus = (m.editFocus - 1 + 2) % 2
		m.editFields[m.editFocus].Focus()
		return m, nil
	case "enter":
		if m.editFocus == 0 {
			m.editFields[0].Blur()
			m.editFocus = 1
			m.editFields[1].Focus()
			return m, nil
		}
		// Submit → confirm.
		cores, mem := parseEditFields(m.editFields)
		if cores < 1 || mem < 128 {
			m.notice = "invalid values"
			return m, nil
		}
		if a := m.currentAnalysis(); a != nil {
			m.editMode = false
			m.confirmKind = confirmResizeCustom
			m.confirmVM = a
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.editFields[m.editFocus], cmd = m.editFields[m.editFocus].Update(msg)
	return m, cmd
}

func (m ResizeModel) handleDiskForm(key string, msg tea.KeyMsg) (ResizeModel, tea.Cmd) {
	switch key {
	case "esc":
		m.diskMode = false
		return m, nil
	case "tab", "down":
		m.diskFields[m.diskFocus].Blur()
		m.diskFocus = (m.diskFocus + 1) % 2
		m.diskFields[m.diskFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.diskFields[m.diskFocus].Blur()
		m.diskFocus = (m.diskFocus - 1 + 2) % 2
		m.diskFields[m.diskFocus].Focus()
		return m, nil
	case "enter":
		if m.diskFocus == 0 {
			m.diskFields[0].Blur()
			m.diskFocus = 1
			m.diskFields[1].Focus()
			return m, nil
		}
		disk := strings.TrimSpace(m.diskFields[0].Value())
		size := strings.TrimSpace(m.diskFields[1].Value())
		if disk == "" || size == "" {
			m.notice = "disk key and size are required"
			return m, nil
		}
		if a := m.currentAnalysis(); a != nil {
			m.diskMode = false
			m.confirmKind = confirmResizeDisk
			m.confirmVM = a
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.diskFields[m.diskFocus], cmd = m.diskFields[m.diskFocus].Update(msg)
	return m, cmd
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m ResizeModel) View() string {
	var b strings.Builder

	// Header
	b.WriteString(styleTitle.Render("⚡ argus") + styleDim.Render(" › ") +
		styleBold.Render(m.node.Name) + styleDim.Render(" › right-sizing") + "\n")
	if m.nodeStats != nil {
		b.WriteString(m.nodeCapacityBar() + "\n")
	}
	b.WriteString("\n")

	if m.loading {
		b.WriteString("  " + m.spinner.View() + " loading usage data (this takes ~10s)…\n")
		return b.String()
	}

	if m.askingClaude {
		b.WriteString("  " + m.spinner.View() + " asking Claude…\n")
		return b.String()
	}

	if len(m.analyses) == 0 {
		b.WriteString(styleDim.Render("  No VM data available — check Proxmox connectivity.\n"))
		return b.String()
	}

	// Main table
	b.WriteString(m.renderTable())
	b.WriteString("\n")

	// Detail panel for selected VM
	if a := m.currentAnalysis(); a != nil {
		b.WriteString(m.renderDetail(a))
		b.WriteString("\n")
	}

	// Claude explanation panel
	if m.explanation != nil {
		b.WriteString(m.renderExplanation())
		b.WriteString("\n")
	}

	// Edit form
	if m.editMode {
		b.WriteString(m.renderEditForm())
		b.WriteString("\n")
	}

	// Disk form
	if m.diskMode {
		b.WriteString(m.renderDiskForm())
		b.WriteString("\n")
	}

	// Confirmation dialog
	if m.confirmKind != confirmResizeNone {
		b.WriteString(m.renderConfirm())
		b.WriteString("\n")
	}

	// Notice
	if m.notice != "" {
		b.WriteString(styleDim.Render("  "+m.notice) + "\n")
	}

	// Help
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m ResizeModel) nodeCapacityBar() string {
	ns := m.nodeStats
	cpuUsed := int(ns.CPUUsage * 100)
	memUsedGB := ns.MemUsed / 1024 / 1024 / 1024
	memTotGB := ns.MaxMem / 1024 / 1024 / 1024
	return fmt.Sprintf("  Node capacity — CPU: %d%% of %d cores   RAM: %d/%d GB",
		cpuUsed, ns.MaxCPU, memUsedGB, memTotGB)
}

func (m ResizeModel) renderTable() string {
	var b strings.Builder
	hdr := fmt.Sprintf("  %-6s %-22s %-5s  %-18s %-18s %-12s  %s\n",
		"VMID", "Name", "Type", "CPU curr→rec", "RAM curr→rec", "Status", "Savings")
	b.WriteString(styleDim.Render(hdr))
	b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 100))) + "\n")

	for i, a := range m.analyses {
		cpuCol := formatCPUCol(a)
		memCol := formatMemCol(a)
		status := statusLabel(a.OverallStatus())
		savings := savingsStr(a)
		typeLbl := "vm"
		if a.Guest.Type == "lxc" {
			typeLbl = "ct"
		}
		row := fmt.Sprintf("  %-6d %-22s %-5s  %-18s %-18s %-12s  %s\n",
			a.Guest.VMID, truncate(a.Guest.Name, 22), typeLbl, cpuCol, memCol, status, savings)

		if i == m.cursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m ResizeModel) renderDetail(a *api.VMSizingAnalysis) string {
	var b strings.Builder
	b.WriteString(styleDim.Render("  ─── "+a.Guest.Name+" ") +
		styleDim.Render(strings.Repeat("─", max(m.width-10-len(a.Guest.Name), 10))) + "\n")

	// Sparklines
	if len(a.CPUSpark) > 0 {
		cpuP := int(a.CPUP95 * 100)
		b.WriteString(fmt.Sprintf("  CPU (P95 %d%%)  %s\n", cpuP, sparklineRaw(a.CPUSpark)))
	}
	if len(a.MemSpark) > 0 {
		memP := 0
		if a.Config.Memory > 0 {
			memP = int(float64(a.MemP95MB) / float64(a.Config.Memory) * 100)
		}
		b.WriteString(fmt.Sprintf("  Mem (P95 %d%%)  %s\n", memP, sparklineRaw(a.MemSpark)))
	}

	// Disk info
	if a.DiskTotGB > 0 {
		pct := int(float64(a.DiskUsedGB) / float64(a.DiskTotGB) * 100)
		b.WriteString(fmt.Sprintf("  Disk          %d/%d GB (%d%%)\n",
			a.DiskUsedGB, a.DiskTotGB, pct))
		for _, d := range a.Config.Disks {
			b.WriteString(fmt.Sprintf("                %s: %dG  %s\n",
				d.Key, d.SizeGB, styleDim.Render(truncate(d.RawValue, 50))))
		}
	}

	// Data quality
	b.WriteString(styleDim.Render(fmt.Sprintf("  %d data points (7-day AVERAGE from Proxmox RRD)\n", a.DataPoints)))
	return b.String()
}

func (m ResizeModel) renderExplanation() string {
	exp := m.explanation
	var b strings.Builder
	riskColor := colorGreen
	switch exp.RiskLevel {
	case "medium":
		riskColor = colorYellow
	case "high":
		riskColor = colorRed
	}

	b.WriteString(lipgloss.NewStyle().Foreground(colorPink).Render("  ╔═ Claude recommendation ") +
		styleDim.Render(strings.Repeat("═", max(m.width-28, 10))) + "\n")
	b.WriteString("  " + styleBold.Render(exp.Summary) + "\n\n")

	// Word-wrap detail to terminal width.
	for _, line := range wrapText(exp.Detail, m.width-4) {
		b.WriteString("  " + line + "\n")
	}
	if len(exp.Changes) > 0 {
		b.WriteString("\n")
		for _, ch := range exp.Changes {
			b.WriteString("  • " + ch + "\n")
		}
	}
	if len(exp.Caveats) > 0 {
		b.WriteString("\n")
		for _, cv := range exp.Caveats {
			b.WriteString("  ⚠ " + stylePaused.Render(cv) + "\n")
		}
	}
	riskLabel := lipgloss.NewStyle().Foreground(riskColor).Render(exp.RiskLevel + " risk")
	b.WriteString("\n  Risk: " + riskLabel + "\n")
	return b.String()
}

func (m ResizeModel) renderEditForm() string {
	a := m.currentAnalysis()
	if a == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(styleBold.Render(fmt.Sprintf("  Custom resize: %s\n", a.Guest.Name)))
	for i, f := range m.editFields {
		labels := []string{"  Cores      ", "  Memory MB  "}
		if i == m.editFocus {
			b.WriteString(styleSelected.Render(labels[i]) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(labels[i]) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("  enter to confirm · esc to cancel\n"))
	return b.String()
}

func (m ResizeModel) renderDiskForm() string {
	a := m.currentAnalysis()
	if a == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(styleBold.Render(fmt.Sprintf("  Disk resize: %s\n", a.Guest.Name)))
	for i, f := range m.diskFields {
		labels := []string{"  Disk key   ", "  New size   "}
		if i == m.diskFocus {
			b.WriteString(styleSelected.Render(labels[i]) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(labels[i]) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("  Note: disk size is absolute (e.g. 40G), not relative\n"))
	b.WriteString(styleDim.Render("  enter to confirm · esc to cancel\n"))
	return b.String()
}

func (m ResizeModel) renderConfirm() string {
	if m.confirmVM == nil {
		return ""
	}
	a := m.confirmVM
	var b strings.Builder
	b.WriteString(styleError.Render("  ┌─ Confirm resize ──────────────────────────────\n"))

	switch m.confirmKind {
	case confirmResizeApply:
		cpuChanged := a.RecCores != a.Config.TotalCores()
		memChanged := a.RecMemMB != a.Config.Memory
		b.WriteString(fmt.Sprintf("  │  VM: %s (%d)\n", a.Guest.Name, a.Guest.VMID))
		if cpuChanged {
			b.WriteString(fmt.Sprintf("  │  CPU:    %d cores → %d cores\n", a.Config.TotalCores(), a.RecCores))
		}
		if memChanged {
			b.WriteString(fmt.Sprintf("  │  Memory: %d MB → %d MB\n", a.Config.Memory, a.RecMemMB))
		}
		b.WriteString(stylePaused.Render("  │  ⚠ A reboot may be required for changes to take effect\n"))
	case confirmResizeCustom:
		cores, mem := parseEditFields(m.editFields)
		b.WriteString(fmt.Sprintf("  │  VM: %s (%d)\n", a.Guest.Name, a.Guest.VMID))
		b.WriteString(fmt.Sprintf("  │  CPU:    %d cores → %d cores\n", a.Config.TotalCores(), cores))
		b.WriteString(fmt.Sprintf("  │  Memory: %d MB → %d MB\n", a.Config.Memory, mem))
		b.WriteString(stylePaused.Render("  │  ⚠ A reboot may be required for changes to take effect\n"))
	case confirmResizeDisk:
		disk := m.diskFields[0].Value()
		size := m.diskFields[1].Value()
		b.WriteString(fmt.Sprintf("  │  VM: %s (%d)\n", a.Guest.Name, a.Guest.VMID))
		b.WriteString(fmt.Sprintf("  │  Disk %s → %s\n", disk, size))
		b.WriteString(styleRunning.Render("  │  ✓ Disk expansion takes effect immediately (no reboot)\n"))
		b.WriteString(stylePaused.Render("  │  ⚠ You must resize the filesystem inside the VM separately\n"))
	}
	b.WriteString(styleError.Render("  └──────────────────────────────────────────────\n"))
	b.WriteString(styleBold.Render("  Apply? [Y/n] "))
	return b.String()
}

func (m ResizeModel) helpStr() string {
	if m.editMode || m.diskMode {
		return "  tab/enter advance · esc cancel"
	}
	if m.confirmKind != confirmResizeNone {
		return "  y confirm · any key cancel"
	}
	claudeHint := ""
	if m.claudeClient != nil {
		claudeHint = " · ? ask Claude"
	}
	return fmt.Sprintf("  ↑/↓ navigate · r apply rec · e custom · d disk resize · A apply all · R reload%s · b back", claudeHint)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m ResizeModel) currentAnalysis() *api.VMSizingAnalysis {
	if m.cursor < 0 || m.cursor >= len(m.analyses) {
		return nil
	}
	a := m.analyses[m.cursor]
	return &a
}

func statusOrder(s api.SizingStatus) int {
	switch s {
	case api.StatusUnderProvisioned:
		return 0
	case api.StatusOverProvisioned:
		return 1
	case api.StatusNoData:
		return 2
	default:
		return 3
	}
}

func statusLabel(s api.SizingStatus) string {
	switch s {
	case api.StatusUnderProvisioned:
		return lipgloss.NewStyle().Foreground(colorRed).Render("↑ under")
	case api.StatusOverProvisioned:
		return lipgloss.NewStyle().Foreground(colorYellow).Render("↓ over")
	case api.StatusWellSized:
		return lipgloss.NewStyle().Foreground(colorGreen).Render("✓ ok")
	default:
		return styleDim.Render("? no data")
	}
}

func formatCPUCol(a api.VMSizingAnalysis) string {
	curr := a.Config.TotalCores()
	rec := a.RecCores
	if curr == rec || a.CPUStatus == api.StatusNoData {
		return fmt.Sprintf("%dc", curr)
	}
	arrow := "↓"
	color := colorYellow
	if rec > curr {
		arrow = "↑"
		color = colorRed
	}
	return fmt.Sprintf("%dc %s %s", curr, arrow, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%dc", rec)))
}

func formatMemCol(a api.VMSizingAnalysis) string {
	curr := a.Config.Memory
	rec := a.RecMemMB
	if curr == rec || a.MemStatus == api.StatusNoData {
		return fmtMB(curr)
	}
	arrow := "↓"
	color := colorYellow
	if rec > curr {
		arrow = "↑"
		color = colorRed
	}
	return fmt.Sprintf("%s %s %s", fmtMB(curr), arrow, lipgloss.NewStyle().Foreground(color).Render(fmtMB(rec)))
}

func savingsStr(a api.VMSizingAnalysis) string {
	cpuDelta := a.Config.TotalCores() - a.RecCores
	memDeltaMB := a.Config.Memory - a.RecMemMB
	if cpuDelta == 0 && memDeltaMB == 0 {
		return "—"
	}
	var parts []string
	if cpuDelta > 0 {
		parts = append(parts, fmt.Sprintf("-%dc", cpuDelta))
	} else if cpuDelta < 0 {
		parts = append(parts, fmt.Sprintf("+%dc", -cpuDelta))
	}
	if memDeltaMB > 0 {
		parts = append(parts, "-"+fmtMB(memDeltaMB))
	} else if memDeltaMB < 0 {
		parts = append(parts, "+"+fmtMB(-memDeltaMB))
	}
	return strings.Join(parts, " ")
}

func fmtMB(mb int) string {
	if mb >= 1024 {
		gb := float64(mb) / 1024
		if gb == float64(int(gb)) {
			return fmt.Sprintf("%dG", int(gb))
		}
		return fmt.Sprintf("%.1fG", gb)
	}
	return fmt.Sprintf("%dM", mb)
}

// sparkline renders a slice of 0..1 values as unicode block characters.
func sparklineRaw(data []float64) string {
	const blocks = " ▁▂▃▄▅▆▇█"
	var b strings.Builder
	for _, v := range data {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		idx := int(v * float64(len(blocks)-1))
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(rune(blocks[idx]))
	}
	return b.String()
}

func parseEditFields(fields [2]textinput.Model) (cores, memMB int) {
	fmt.Sscanf(strings.TrimSpace(fields[0].Value()), "%d", &cores)
	fmt.Sscanf(strings.TrimSpace(fields[1].Value()), "%d", &memMB)
	return
}

// wrapText wraps text to width characters, breaking on spaces.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len()+1+len(w) > width && cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteRune(' ')
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
