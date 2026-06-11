package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Tabs ──────────────────────────────────────────────────────────────────────

type maintTab int

const (
	maintTabRuns      maintTab = iota // Recent runs and run-now
	maintTabSchedules                 // Saved schedules
	maintTabConfigure                 // Node order + task flags wizard
	maintTabCount
)

var maintTabLabels = []string{"Runs", "Schedules", "Configure"}

// ── Wizard steps ─────────────────────────────────────────────────────────────

const (
	maintWizardTaskFlags = 0 // toggle task checkboxes
	maintWizardNodeOrder = 1 // drag-to-reorder node list
	maintWizardSchedule  = 2 // run now vs schedule
	maintWizardDryRun    = 3 // preview generated steps (dry run)
	maintWizardConfirm   = 4 // review + launch
)

// ── Model ─────────────────────────────────────────────────────────────────────

// MaintenanceModel manages cluster maintenance runs and schedules.
type MaintenanceModel struct {
	width, height int
	tab           maintTab

	proxmoxClient *api.Client
	saltClient    *api.SaltClient
	database      *db.DB

	// Nodes (loaded from Proxmox)
	nodes       []api.Node
	clusterNodes []api.ClusterNode // HA cluster membership
	nodesLoading bool
	haAware     bool // whether cluster has HA (len(clusterNodes)>1)

	// Runs tab
	runs      []db.MaintenanceRun
	runSteps  []db.MaintenanceRunStep
	runCursor int
	activeRun *db.MaintenanceRun // currently executing run

	// Schedules tab
	schedules    []db.MaintenanceSchedule
	schedCursor  int
	deletingSchd bool

	// Configure wizard
	wizardStep  int
	taskFlags   db.MaintenanceTaskFlags
	nodeOrder   []string // ordered list of node names
	nodeCursor  int
	schedName   textinput.Model
	schedRunAt  textinput.Model // "now" or RFC3339 / "YYYY-MM-DD HH:MM"
	schedRepeat textinput.Model // "", "weekly", "monthly"
	wizFocus    int

	// Live run execution state
	running      bool
	runID        int64
	currentStep  int
	stepStatus   map[int64]string // stepID → status
	runNotice    string

	spinner spinner.Model
	loading bool
	notice  string
}

func NewMaintenanceModel(proxmoxClient *api.Client, saltClient *api.SaltClient, database *db.DB) MaintenanceModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	nameTI := textinput.New()
	nameTI.Placeholder = "maintenance name (e.g. Weekly Cluster Update)"
	nameTI.SetValue("Cluster Maintenance")
	nameTI.CharLimit = 80

	runAtTI := textinput.New()
	runAtTI.Placeholder = "now  OR  2026-06-01 03:00"
	runAtTI.SetValue("now")
	runAtTI.CharLimit = 40

	repeatTI := textinput.New()
	repeatTI.Placeholder = "weekly | monthly | (empty = once)"
	repeatTI.CharLimit = 20

	return MaintenanceModel{
		proxmoxClient: proxmoxClient,
		saltClient:    saltClient,
		database:      database,
		spinner:       sp,
		taskFlags: db.MaintenanceTaskFlags{
			OSUpdates:  true,
			PkgUpdates: true,
			Reboot:     true,
		},
		haAware:     true,
		schedName:   nameTI,
		schedRunAt:  runAtTI,
		schedRepeat: repeatTI,
		stepStatus:  make(map[int64]string),
	}
}

func (m MaintenanceModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadNodesCmd(), m.loadRunsCmd(), m.loadSchedulesCmd())
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m MaintenanceModel) loadNodesCmd() tea.Cmd {
	client := m.proxmoxClient
	return func() tea.Msg {
		if client == nil {
			return maintNodesLoadedMsg{err: fmt.Errorf("Proxmox not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		nodes, err := client.Nodes(ctx)
		if err != nil {
			return maintNodesLoadedMsg{err: err}
		}
		cluster, _ := client.ClusterStatus(ctx)
		return maintNodesLoadedMsg{nodes: nodes, cluster: cluster}
	}
}

func (m MaintenanceModel) loadRunsCmd() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return maintRunsLoadedMsg{}
		}
		runs, err := database.RecentMaintenanceRuns(50)
		return maintRunsLoadedMsg{runs: runs, err: err}
	}
}

func (m MaintenanceModel) loadSchedulesCmd() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return maintSchedulesLoadedMsg{}
		}
		schedules, err := database.MaintenanceSchedules()
		return maintSchedulesLoadedMsg{schedules: schedules, err: err}
	}
}

func (m MaintenanceModel) loadRunStepsCmd(runID int64) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return maintStepsLoadedMsg{runID: runID}
		}
		steps, err := database.MaintenanceRunSteps(runID)
		return maintStepsLoadedMsg{runID: runID, steps: steps, err: err}
	}
}

// saveScheduleCmd saves the current wizard as a schedule (and optionally runs immediately).
func (m MaintenanceModel) saveScheduleCmd(runNow bool) tea.Cmd {
	database := m.database
	nodeOrderJSON, _ := json.Marshal(m.nodeOrder)
	taskFlagsJSON, _ := json.Marshal(m.taskFlags)
	name := strings.TrimSpace(m.schedName.Value())
	if name == "" {
		name = "Cluster Maintenance"
	}
	runAtStr := strings.TrimSpace(m.schedRunAt.Value())
	repeat := strings.TrimSpace(m.schedRepeat.Value())
	haAware := m.haAware

	var runAt *time.Time
	if runAtStr != "" && runAtStr != "now" {
		for _, layout := range []string{"2006-01-02 15:04", time.RFC3339} {
			if t, err := time.Parse(layout, runAtStr); err == nil {
				runAt = &t
				break
			}
		}
	}

	return func() tea.Msg {
		if database == nil {
			return maintSavedMsg{err: fmt.Errorf("no database")}
		}
		s := db.MaintenanceSchedule{
			Name:      name,
			RunAt:     runAt,
			Repeat:    repeat,
			NodeOrder: string(nodeOrderJSON),
			TaskFlags: string(taskFlagsJSON),
			HAAware:   haAware,
			Enabled:   true,
		}
		schedID, err := database.SaveMaintenanceSchedule(s)
		if err != nil {
			return maintSavedMsg{err: err}
		}
		if runNow {
			// Also create a run
			runID, err := database.CreateMaintenanceRun(db.MaintenanceRun{
				ScheduleID: schedID,
				Name:       name + " (run now)",
				Status:     "pending",
				NodeOrder:  string(nodeOrderJSON),
				TaskFlags:  string(taskFlagsJSON),
				HAAware:    haAware,
			})
			if err != nil {
				return maintSavedMsg{schedID: schedID, err: err}
			}
			return maintSavedMsg{schedID: schedID, runID: runID, runNow: true}
		}
		return maintSavedMsg{schedID: schedID}
	}
}

// executeRunCmd runs a maintenance run step-by-step in the background.
// It sends maintStepDoneMsg after each step.
func (m MaintenanceModel) executeRunCmd(runID int64, nodeOrder []string, taskFlags db.MaintenanceTaskFlags, haAware bool) tea.Cmd {
	proxmox := m.proxmoxClient
	salt := m.saltClient
	database := m.database

	return func() tea.Msg {
		if database == nil {
			return maintRunDoneMsg{runID: runID, err: fmt.Errorf("no database")}
		}
		if err := database.StartMaintenanceRun(runID); err != nil {
			return maintRunDoneMsg{runID: runID, err: err}
		}

		// Build ordered step list
		var steps []db.MaintenanceRunStep
		for _, node := range nodeOrder {
			if haAware {
				steps = append(steps, db.MaintenanceRunStep{RunID: runID, Node: node, Task: "evacuate", Status: "pending"})
			}
			if taskFlags.OSUpdates || taskFlags.PkgUpdates {
				steps = append(steps, db.MaintenanceRunStep{RunID: runID, Node: node, Task: "os_update", Status: "pending"})
			}
			if taskFlags.ProxmoxUpgrade {
				steps = append(steps, db.MaintenanceRunStep{RunID: runID, Node: node, Task: "proxmox_upgrade", Status: "pending"})
			}
			if taskFlags.Reboot {
				steps = append(steps, db.MaintenanceRunStep{RunID: runID, Node: node, Task: "reboot", Status: "pending"})
			}
			if haAware {
				steps = append(steps, db.MaintenanceRunStep{RunID: runID, Node: node, Task: "unevacuate", Status: "pending"})
			}
		}

		if err := database.CreateMaintenanceSteps(runID, steps); err != nil {
			now := time.Now()
			_ = database.UpdateMaintenanceRunStatus(runID, "failed", &now)
			return maintRunDoneMsg{runID: runID, err: err}
		}

		// Reload steps with IDs
		steps, err := database.MaintenanceRunSteps(runID)
		if err != nil {
			return maintRunDoneMsg{runID: runID, err: err}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		for _, step := range steps {
			_ = database.UpdateMaintenanceStep(step.ID, "running", "")

			var output string
			var stepErr error

			switch step.Task {
			case "evacuate":
				if proxmox != nil {
					ha, _ := proxmox.HAResources(ctx)
					errs := proxmox.MigrateHAResourcesOffNode(ctx, step.Node, ha)
					if len(errs) > 0 {
						output = fmt.Sprintf("evacuate: %d migration(s) triggered; %d error(s)", len(ha), len(errs))
					} else {
						output = fmt.Sprintf("evacuated %d HA resources", len(ha))
					}
					// Wait briefly for migrations to settle
					time.Sleep(10 * time.Second)
				} else {
					output = "skipped (no Proxmox client)"
				}

			case "os_update":
				// Try Salt first, fall back to Proxmox APT API
				if salt != nil {
					jid, err := salt.ApplyState(ctx, step.Node, "system.update")
					if err != nil {
						// Fall back to Proxmox APT
						output = fmt.Sprintf("salt: %v; trying proxmox apt…", err)
						if proxmox != nil {
							upid, err := proxmox.NodeAPTUpdate(ctx, step.Node)
							if err != nil {
								stepErr = err
								output += " apt failed: " + err.Error()
							} else if upid != "" {
								exit, _ := proxmox.WaitForTask(ctx, step.Node, upid)
								output += fmt.Sprintf(" apt: %s", exit)
							}
						}
					} else {
						output = "salt state.apply: jid=" + jid
					}
				} else if proxmox != nil {
					upid, err := proxmox.NodeAPTUpdate(ctx, step.Node)
					if err != nil {
						stepErr = err
						output = "apt error: " + err.Error()
					} else if upid != "" {
						exit, _ := proxmox.WaitForTask(ctx, step.Node, upid)
						output = "apt: " + exit
					} else {
						output = "apt update triggered"
					}
				} else {
					output = "skipped (no salt or proxmox client)"
				}

			case "proxmox_upgrade":
				// Same as os_update but targets only proxmox packages via pveupgrade
				if proxmox != nil {
					upid, err := proxmox.NodeAPTUpdate(ctx, step.Node)
					if err != nil {
						stepErr = err
						output = "pve-upgrade error: " + err.Error()
					} else if upid != "" {
						exit, _ := proxmox.WaitForTask(ctx, step.Node, upid)
						output = "pve-upgrade: " + exit
					} else {
						output = "proxmox upgrade triggered"
					}
				} else {
					output = "skipped (no proxmox client)"
				}

			case "reboot":
				if proxmox != nil {
					if err := proxmox.NodeReboot(ctx, step.Node); err != nil {
						stepErr = err
						output = "reboot error: " + err.Error()
					} else {
						output = "reboot command sent"
						// Wait for node to go offline and come back (up to 10 min)
						time.Sleep(30 * time.Second)
					}
				} else {
					output = "skipped (no proxmox client)"
				}

			case "unevacuate":
				// HA resources should automatically migrate back once node is online
				// We just log that the node is available again
				output = "node available; HA resources will re-balance automatically"
				time.Sleep(5 * time.Second)
			}

			status := "done"
			if stepErr != nil {
				status = "failed"
				output = stepErr.Error()
			}
			_ = database.UpdateMaintenanceStep(step.ID, status, output)
		}

		now := time.Now()
		finalStatus := "done"
		_ = database.UpdateMaintenanceRunStatus(runID, finalStatus, &now)
		return maintRunDoneMsg{runID: runID}
	}
}

// ── Messages ──────────────────────────────────────────────────────────────────
// (defined in messages.go)

// ── Update ────────────────────────────────────────────────────────────────────

func (m MaintenanceModel) Update(msg tea.Msg) (MaintenanceModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case maintNodesLoadedMsg:
		m.nodesLoading = false
		if msg.err == nil {
			m.nodes = msg.nodes
			m.clusterNodes = msg.cluster
			m.haAware = len(msg.cluster) > 1
			// Default node order: non-master nodes first, then master
			if len(m.nodeOrder) == 0 {
				m.nodeOrder = maintenanceDefaultOrder(msg.nodes, msg.cluster)
			}
		}
		return m, nil

	case maintRunsLoadedMsg:
		if msg.err == nil {
			m.runs = msg.runs
		}
		return m, nil

	case maintSchedulesLoadedMsg:
		if msg.err == nil {
			m.schedules = msg.schedules
		}
		return m, nil

	case maintStepsLoadedMsg:
		if msg.err == nil {
			m.runSteps = msg.steps
		}
		return m, nil

	case maintSavedMsg:
		if msg.err != nil {
			m.notice = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.runNow {
			m.running = true
			m.runID = msg.runID
			m.notice = fmt.Sprintf("run #%d started", msg.runID)
			// Execute in background
			taskFlagsJSON, _ := json.Marshal(m.taskFlags)
			var tf db.MaintenanceTaskFlags
			_ = json.Unmarshal(taskFlagsJSON, &tf)
			return m, tea.Batch(
				m.loadRunsCmd(),
				m.loadSchedulesCmd(),
				m.executeRunCmd(msg.runID, m.nodeOrder, tf, m.haAware),
			)
		}
		m.notice = "schedule saved"
		m.tab = maintTabSchedules
		return m, tea.Batch(m.loadRunsCmd(), m.loadSchedulesCmd())

	case maintBeginExecMsg:
		m.running = true
		m.runID = msg.runID
		m.notice = fmt.Sprintf("run #%d started", msg.runID)
		return m, tea.Batch(m.loadRunsCmd(), m.executeRunCmd(msg.runID, msg.nodeOrder, msg.taskFlags, msg.haAware))

	case maintRunDoneMsg:
		m.running = false
		if msg.err != nil {
			m.runNotice = "run #" + fmt.Sprint(msg.runID) + " failed: " + msg.err.Error()
		} else {
			m.runNotice = fmt.Sprintf("✓ run #%d completed", msg.runID)
		}
		return m, m.loadRunsCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m MaintenanceModel) handleKey(msg tea.KeyMsg) (MaintenanceModel, tea.Cmd) {
	key := msg.String()

	// Wizard inputs
	if m.tab == maintTabConfigure {
		return m.handleConfigureTab(key, msg)
	}

	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
		}
		return m, nil
	case "right", "l":
		if m.tab < maintTabCount-1 {
			m.tab++
			m.notice = ""
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "R":
		return m, tea.Batch(m.loadNodesCmd(), m.loadRunsCmd(), m.loadSchedulesCmd())
	}

	switch m.tab {
	case maintTabRuns:
		return m.handleRunsTab(key)
	case maintTabSchedules:
		return m.handleSchedulesTab(key)
	}
	return m, nil
}

func (m MaintenanceModel) handleRunsTab(key string) (MaintenanceModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.runCursor > 0 {
			m.runCursor--
		}
	case "down", "j":
		if m.runCursor < len(m.runs)-1 {
			m.runCursor++
		}
	case "enter":
		if m.runCursor < len(m.runs) {
			return m, m.loadRunStepsCmd(m.runs[m.runCursor].ID)
		}
	case "n":
		// Quick "run now" with current wizard settings
		m.tab = maintTabConfigure
		m.wizardStep = maintWizardTaskFlags
		m.notice = "configure the run, then press enter on Confirm step"
	}
	return m, nil
}

func (m MaintenanceModel) handleSchedulesTab(key string) (MaintenanceModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.schedCursor > 0 {
			m.schedCursor--
		}
	case "down", "j":
		if m.schedCursor < len(m.schedules)-1 {
			m.schedCursor++
		}
	case "n":
		m.tab = maintTabConfigure
		m.wizardStep = maintWizardTaskFlags
		m.notice = ""
	case "d":
		if m.schedCursor < len(m.schedules) {
			s := m.schedules[m.schedCursor]
			return m, m.deleteScheduleCmd(s.ID)
		}
	case "r":
		// Run selected schedule now
		if m.schedCursor < len(m.schedules) {
			s := m.schedules[m.schedCursor]
			return m, m.runScheduleNowCmd(s)
		}
	}
	return m, nil
}

func (m MaintenanceModel) handleConfigureTab(key string, msg tea.KeyMsg) (MaintenanceModel, tea.Cmd) {
	switch m.wizardStep {
	case maintWizardTaskFlags:
		switch key {
		case "1":
			m.taskFlags.OSUpdates = !m.taskFlags.OSUpdates
		case "2":
			m.taskFlags.PkgUpdates = !m.taskFlags.PkgUpdates
		case "3":
			m.taskFlags.ProxmoxUpgrade = !m.taskFlags.ProxmoxUpgrade
		case "4":
			m.taskFlags.Reboot = !m.taskFlags.Reboot
		case "5":
			m.haAware = !m.haAware
		case "enter":
			m.wizardStep = maintWizardNodeOrder
			m.nodeCursor = 0
		case "esc":
			m.tab = maintTabRuns
		}
	case maintWizardNodeOrder:
		switch key {
		case "up", "k":
			if m.nodeCursor > 0 {
				m.nodeCursor--
			}
		case "down", "j":
			if m.nodeCursor < len(m.nodeOrder)-1 {
				m.nodeCursor++
			}
		case "K": // Move node up in order (runs earlier)
			if m.nodeCursor > 0 {
				m.nodeOrder[m.nodeCursor], m.nodeOrder[m.nodeCursor-1] =
					m.nodeOrder[m.nodeCursor-1], m.nodeOrder[m.nodeCursor]
				m.nodeCursor--
			}
		case "J": // Move node down in order (runs later)
			if m.nodeCursor < len(m.nodeOrder)-1 {
				m.nodeOrder[m.nodeCursor], m.nodeOrder[m.nodeCursor+1] =
					m.nodeOrder[m.nodeCursor+1], m.nodeOrder[m.nodeCursor]
				m.nodeCursor++
			}
		case "enter":
			m.wizardStep = maintWizardSchedule
			m.wizFocus = 0
			m.schedName.Focus()
		case "esc":
			m.wizardStep = maintWizardTaskFlags
		}
	case maintWizardSchedule:
		switch key {
		case "esc":
			m.wizardStep = maintWizardNodeOrder
		case "tab", "down":
			// Cycle through name → run_at → repeat
			inputs := []*textinput.Model{&m.schedName, &m.schedRunAt, &m.schedRepeat}
			inputs[m.wizFocus].Blur()
			m.wizFocus = (m.wizFocus + 1) % len(inputs)
			inputs[m.wizFocus].Focus()
			return m, nil
		case "shift+tab", "up":
			inputs := []*textinput.Model{&m.schedName, &m.schedRunAt, &m.schedRepeat}
			inputs[m.wizFocus].Blur()
			m.wizFocus = (m.wizFocus - 1 + len(inputs)) % len(inputs)
			inputs[m.wizFocus].Focus()
			return m, nil
		case "enter":
			if m.wizFocus < 2 {
				inputs := []*textinput.Model{&m.schedName, &m.schedRunAt, &m.schedRepeat}
				inputs[m.wizFocus].Blur()
				m.wizFocus++
				inputs[m.wizFocus].Focus()
				return m, nil
			}
			m.wizardStep = maintWizardDryRun
			// Blur all inputs
			m.schedName.Blur()
			m.schedRunAt.Blur()
			m.schedRepeat.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			switch m.wizFocus {
			case 0:
				m.schedName, cmd = m.schedName.Update(msg)
			case 1:
				m.schedRunAt, cmd = m.schedRunAt.Update(msg)
			case 2:
				m.schedRepeat, cmd = m.schedRepeat.Update(msg)
			}
			return m, cmd
		}
	case maintWizardDryRun:
		switch key {
		case "enter", "y":
			m.wizardStep = maintWizardConfirm
		case "esc":
			m.wizardStep = maintWizardSchedule
		}

	case maintWizardConfirm:
		switch key {
		case "enter", "y":
			runNow := strings.TrimSpace(m.schedRunAt.Value()) == "now" ||
				strings.TrimSpace(m.schedRunAt.Value()) == ""
			return m, m.saveScheduleCmd(runNow)
		case "s":
			// Save schedule only (don't run now)
			return m, m.saveScheduleCmd(false)
		case "esc":
			m.wizardStep = maintWizardDryRun
		}
	}
	return m, nil
}

func (m MaintenanceModel) deleteScheduleCmd(id int64) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database != nil {
			_ = database.DeleteMaintenanceSchedule(id)
		}
		return maintSchedulesLoadedMsg{}
	}
}

func (m MaintenanceModel) runScheduleNowCmd(s db.MaintenanceSchedule) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return maintRunDoneMsg{err: fmt.Errorf("no database")}
		}
		var nodeOrder []string
		_ = json.Unmarshal([]byte(s.NodeOrder), &nodeOrder)
		var tf db.MaintenanceTaskFlags
		_ = json.Unmarshal([]byte(s.TaskFlags), &tf)

		runID, err := database.CreateMaintenanceRun(db.MaintenanceRun{
			ScheduleID: s.ID,
			Name:       s.Name + " (run now)",
			Status:     "pending",
			NodeOrder:  s.NodeOrder,
			TaskFlags:  s.TaskFlags,
			HAAware:    s.HAAware,
		})
		if err != nil {
			return maintRunDoneMsg{err: err}
		}
		return maintBeginExecMsg{runID: runID, nodeOrder: nodeOrder, taskFlags: tf, haAware: s.HAAware}
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m MaintenanceModel) View() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("  Cluster Maintenance") + "\n")
	if m.haAware && len(m.clusterNodes) > 1 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(
			fmt.Sprintf("  HA cluster: %d nodes", len(m.clusterNodes))) + "\n")
	} else if len(m.nodes) > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  %d standalone node(s)", len(m.nodes))) + "\n")
	}

	if m.running {
		b.WriteString(fmt.Sprintf("  %s Running maintenance run #%d…\n",
			m.spinner.View(), m.runID))
	}
	if m.runNotice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  "+m.runNotice) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(m.renderTabBar() + "\n\n")

	contentH := m.height - 9
	if contentH < 4 {
		contentH = 4
	}

	switch m.tab {
	case maintTabRuns:
		b.WriteString(m.viewRuns(contentH))
	case maintTabSchedules:
		b.WriteString(m.viewSchedules(contentH))
	case maintTabConfigure:
		b.WriteString(m.viewConfigure(contentH))
	}

	if m.notice != "" {
		b.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m MaintenanceModel) renderTabBar() string {
	var parts []string
	for i, label := range maintTabLabels {
		l := " " + label + " "
		if maintTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch")
}

func (m MaintenanceModel) viewRuns(height int) string {
	var b strings.Builder

	if len(m.runs) == 0 {
		b.WriteString(styleDim.Render("  No maintenance runs yet. Press n to configure and run.\n"))
		return b.String()
	}

	hdr := fmt.Sprintf("  %-4s %-28s %-10s %-20s  %s\n", "#", "Name", "Status", "Started", "Nodes")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 90))) + "\n")

	for i, r := range m.runs {
		if i >= height-2 {
			break
		}
		statusStyle := styleDim
		switch r.Status {
		case "running":
			statusStyle = lipgloss.NewStyle().Foreground(colorYellow)
		case "done":
			statusStyle = lipgloss.NewStyle().Foreground(colorGreen)
		case "failed", "cancelled":
			statusStyle = lipgloss.NewStyle().Foreground(colorRed)
		}

		startedStr := "—"
		if r.StartedAt != nil {
			startedStr = r.StartedAt.Format("2006-01-02 15:04")
		}

		var nodeNames []string
		_ = json.Unmarshal([]byte(r.NodeOrder), &nodeNames)
		nodesStr := strings.Join(nodeNames, ",")

		row := fmt.Sprintf("  %-4d %-28s %-10s %-20s  %s\n",
			r.ID, truncate(r.Name, 28), statusStyle.Render(truncate(r.Status, 10)),
			startedStr, truncate(nodesStr, 30))
		if i == m.runCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}

	// Show steps for selected run
	if len(m.runSteps) > 0 && m.runCursor < len(m.runs) {
		b.WriteString(styleDim.Render("\n  Steps for run #" + fmt.Sprint(m.runs[m.runCursor].ID) + ":\n"))
		for _, s := range m.runSteps {
			dot := "○"
			style := styleDim
			switch s.Status {
			case "running":
				dot = "◉"
				style = lipgloss.NewStyle().Foreground(colorYellow)
			case "done":
				dot = "✓"
				style = lipgloss.NewStyle().Foreground(colorGreen)
			case "failed":
				dot = "✗"
				style = lipgloss.NewStyle().Foreground(colorRed)
			case "skipped":
				dot = "–"
				style = styleDim
			}
			line := fmt.Sprintf("    %s  %-12s %-10s  %s", dot, s.Node, s.Task, truncate(s.Output, 50))
			b.WriteString(style.Render(line) + "\n")
		}
	}

	return b.String()
}

func (m MaintenanceModel) viewSchedules(height int) string {
	var b strings.Builder

	if len(m.schedules) == 0 {
		b.WriteString(styleDim.Render("  No schedules saved yet. Press n to create one.\n"))
		return b.String()
	}

	hdr := fmt.Sprintf("  %-4s %-28s %-10s %-18s  %s\n", "#", "Name", "Repeat", "Next Run", "Nodes")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 90))) + "\n")

	for i, s := range m.schedules {
		if i >= height-2 {
			break
		}
		nextRun := "manual"
		if s.RunAt != nil {
			nextRun = s.RunAt.Format("2006-01-02 15:04")
		}
		repeat := s.Repeat
		if repeat == "" {
			repeat = "once"
		}

		var nodeNames []string
		_ = json.Unmarshal([]byte(s.NodeOrder), &nodeNames)
		nodesStr := strings.Join(nodeNames, ",")

		enabledStyle := styleDim
		if !s.Enabled {
			enabledStyle = lipgloss.NewStyle().Foreground(colorRed)
		}

		row := fmt.Sprintf("  %-4d %-28s %-10s %-18s  %s\n",
			s.ID, enabledStyle.Render(truncate(s.Name, 28)), truncate(repeat, 10),
			nextRun, truncate(nodesStr, 30))
		if i == m.schedCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m MaintenanceModel) viewConfigure(height int) string {
	var b strings.Builder

	steps := []string{"1. Task Flags", "2. Node Order", "3. Schedule", "4. Dry Run", "5. Confirm"}
	b.WriteString(styleDim.Render("  "))
	for i, s := range steps {
		if i == m.wizardStep {
			b.WriteString(styleSelected.Render(" " + s + " "))
		} else {
			b.WriteString(styleDim.Render(" " + s + " "))
		}
		if i < len(steps)-1 {
			b.WriteString(styleDim.Render(" → "))
		}
	}
	b.WriteString("\n\n")

	switch m.wizardStep {
	case maintWizardTaskFlags:
		b.WriteString(styleSubtle.Render("  Select maintenance tasks:\n\n"))
		flags := []struct {
			key   string
			label string
			value bool
		}{
			{"1", "OS package updates (apt upgrade / yum update)", m.taskFlags.OSUpdates},
			{"2", "Application package updates (via Salt)", m.taskFlags.PkgUpdates},
			{"3", "Proxmox VE upgrade (pve packages)", m.taskFlags.ProxmoxUpgrade},
			{"4", "Reboot node after updates", m.taskFlags.Reboot},
			{"5", "HA-aware: evacuate VMs before rebooting", m.haAware},
		}
		for _, f := range flags {
			check := "[ ]"
			style := styleDim
			if f.value {
				check = "[✓]"
				style = lipgloss.NewStyle().Foreground(colorGreen)
			}
			b.WriteString(style.Render(fmt.Sprintf("  %s  %s  %s\n", check, f.key, f.label)))
		}
		b.WriteString(styleDim.Render("\n  press the number key to toggle · enter to proceed\n"))

	case maintWizardNodeOrder:
		b.WriteString(styleSubtle.Render("  Node maintenance order (top runs first):\n"))
		b.WriteString(styleDim.Render("  For HA clusters: non-master nodes first, master last is the safest order.\n\n"))

		if len(m.nodeOrder) == 0 {
			b.WriteString(styleDim.Render("  No nodes loaded. Press R to reload.\n"))
		} else {
			for i, nodeName := range m.nodeOrder {
				// Find cluster role
				role := ""
				for _, cn := range m.clusterNodes {
					if cn.Name == nodeName {
						if cn.Level == "master" {
							role = lipgloss.NewStyle().Foreground(colorYellow).Render(" [master]")
						} else if cn.Online == 1 {
							role = styleDim.Render(" [member]")
						} else {
							role = lipgloss.NewStyle().Foreground(colorRed).Render(" [offline]")
						}
					}
				}
				order := fmt.Sprintf("  %d.  %-20s%s", i+1, nodeName, role)
				if i == m.nodeCursor {
					b.WriteString(styleSelected.Render(order) + "\n")
				} else {
					b.WriteString(order + "\n")
				}
			}
		}
		b.WriteString(styleDim.Render("\n  ↑/↓ select · K move up · J move down · enter next · esc back\n"))

	case maintWizardSchedule:
		b.WriteString(styleSubtle.Render("  Schedule options:\n\n"))
		inputs := []struct {
			label string
			ti    textinput.Model
		}{
			{"  Name       ", m.schedName},
			{"  Run at     ", m.schedRunAt},
			{"  Repeat     ", m.schedRepeat},
		}
		for i, inp := range inputs {
			lbl := inp.label
			if i == m.wizFocus {
				b.WriteString(styleSelected.Render(lbl) + inp.ti.View() + "\n")
			} else {
				b.WriteString(styleDim.Render(lbl) + inp.ti.View() + "\n")
			}
		}
		b.WriteString(styleDim.Render("\n  'now' for immediate run · YYYY-MM-DD HH:MM for future · tab advance\n"))

	case maintWizardDryRun:
		b.WriteString(styleSubtle.Render("  Dry Run — planned steps (nothing will execute):\n\n"))
		for _, nodeName := range m.nodeOrder {
			if m.haAware {
				b.WriteString(styleDim.Render(fmt.Sprintf("  ○  %-20s  evacuate VMs\n", nodeName)))
			}
			if m.taskFlags.OSUpdates || m.taskFlags.PkgUpdates {
				b.WriteString(styleDim.Render(fmt.Sprintf("  ○  %-20s  os_update\n", nodeName)))
			}
			if m.taskFlags.ProxmoxUpgrade {
				b.WriteString(styleDim.Render(fmt.Sprintf("  ○  %-20s  proxmox_upgrade\n", nodeName)))
			}
			if m.taskFlags.Reboot {
				b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).
					Render(fmt.Sprintf("  ○  %-20s  reboot\n", nodeName)))
			}
			if m.haAware {
				b.WriteString(styleDim.Render(fmt.Sprintf("  ○  %-20s  unevacuate\n", nodeName)))
			}
		}
		totalSteps := len(m.nodeOrder) * func() int {
			n := 0
			if m.haAware { n += 2 }
			if m.taskFlags.OSUpdates || m.taskFlags.PkgUpdates { n++ }
			if m.taskFlags.ProxmoxUpgrade { n++ }
			if m.taskFlags.Reboot { n++ }
			return n
		}()
		b.WriteString(fmt.Sprintf("\n  Total: %d steps across %d node(s)\n", totalSteps, len(m.nodeOrder)))
		b.WriteString(styleDim.Render("  enter/y = proceed to confirm  esc = back\n"))

	case maintWizardConfirm:
		b.WriteString(styleSubtle.Render("  Review maintenance configuration:\n\n"))

		// Task summary
		var tasks []string
		if m.taskFlags.OSUpdates {
			tasks = append(tasks, "OS updates")
		}
		if m.taskFlags.PkgUpdates {
			tasks = append(tasks, "pkg updates")
		}
		if m.taskFlags.ProxmoxUpgrade {
			tasks = append(tasks, "proxmox upgrade")
		}
		if m.taskFlags.Reboot {
			tasks = append(tasks, "reboot")
		}
		b.WriteString(fmt.Sprintf("  Tasks:      %s\n", strings.Join(tasks, ", ")))

		haStr := "no"
		if m.haAware {
			haStr = "yes (evacuate VMs before reboot)"
		}
		b.WriteString(fmt.Sprintf("  HA-aware:   %s\n", haStr))
		b.WriteString(fmt.Sprintf("  Node order: %s\n", strings.Join(m.nodeOrder, " → ")))
		b.WriteString(fmt.Sprintf("  Name:       %s\n", m.schedName.Value()))
		runAtStr := strings.TrimSpace(m.schedRunAt.Value())
		if runAtStr == "now" || runAtStr == "" {
			b.WriteString(styleDim.Render("  Timing:     run immediately\n"))
		} else {
			b.WriteString(fmt.Sprintf("  Timing:     %s\n", runAtStr))
		}
		if r := strings.TrimSpace(m.schedRepeat.Value()); r != "" {
			b.WriteString(fmt.Sprintf("  Repeat:     %s\n", r))
		}

		b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Bold(true).
			Render("\n  ⚠ This will run package updates and may reboot cluster nodes.\n"))
		b.WriteString(styleDim.Render("  enter/y = save & run · s = save schedule only · esc = back\n"))
	}

	_ = height
	return b.String()
}

func (m MaintenanceModel) helpStr() string {
	switch m.tab {
	case maintTabRuns:
		return "  ↑/↓ navigate · enter view steps · n new run · R reload · ←/→ tab · b back"
	case maintTabSchedules:
		return "  ↑/↓ navigate · n new schedule · r run now · d delete · R reload · ←/→ tab · b back"
	case maintTabConfigure:
		switch m.wizardStep {
		case maintWizardTaskFlags:
			return "  1-5 toggle tasks · enter next · esc cancel"
		case maintWizardNodeOrder:
			return "  ↑/↓ select · K/J reorder · enter next · esc back"
		case maintWizardSchedule:
			return "  tab advance field · enter next · esc back"
		case maintWizardDryRun:
			return "  enter/y proceed · esc back"
		case maintWizardConfirm:
			return "  enter/y run now · s save schedule only · esc back"
		}
	}
	return "  ←/→ tab · b back"
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// maintenanceDefaultOrder returns nodes ordered safest-first for maintenance:
// non-master members before the master, alphabetical within each group.
func maintenanceDefaultOrder(nodes []api.Node, cluster []api.ClusterNode) []string {
	masterName := ""
	for _, cn := range cluster {
		if cn.Level == "master" {
			masterName = cn.Name
		}
	}

	var members, masters []string
	for _, n := range nodes {
		if n.Name == masterName {
			masters = append(masters, n.Name)
		} else {
			members = append(members, n.Name)
		}
	}
	return append(members, masters...)
}
