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

// RunbookStep is one step in a runbook definition.
// Supported kinds: "salt" (state.apply on target) and "shell" (cmd.run on target).
type RunbookStep struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`    // "salt" | "shell"
	Target  string `json:"target"`  // minion ID or glob
	Command string `json:"command"` // salt state name, or shell command
}

type rbTab int

const (
	rbTabList rbTab = iota
	rbTabRuns
	rbTabEdit
)

// RunbooksModel manages runbook definitions and execution history.
type RunbooksModel struct {
	db         *db.DB
	saltClient *api.SaltClient

	tab       rbTab
	runbooks  []db.Runbook
	runs      []db.RunbookRun
	cursor    int
	runCursor int

	// Edit state
	editing   bool
	editID    int64
	nameInput textinput.Model
	descInput textinput.Model
	stepsRaw  textinput.Model // JSON steps (simplified editor)
	editFocus int

	running  bool
	runID    int64
	notice   string
	err      error
	spinner  spinner.Model
	width    int
	height   int
}

func NewRunbooksModel(database *db.DB, saltClient *api.SaltClient) RunbooksModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	nameTI := textinput.New()
	nameTI.Placeholder = "runbook name"
	nameTI.CharLimit = 80

	descTI := textinput.New()
	descTI.Placeholder = "description"
	descTI.CharLimit = 200

	stepsTI := textinput.New()
	stepsTI.Placeholder = `[{"name":"ping","kind":"salt","target":"*","command":"test.ping"}]`
	stepsTI.CharLimit = 2048

	m := RunbooksModel{
		db:         database,
		saltClient: saltClient,
		spinner:    sp,
		nameInput:  nameTI,
		descInput:  descTI,
		stepsRaw:   stepsTI,
	}
	m.reload()
	return m
}

func (m *RunbooksModel) reload() {
	if m.db == nil {
		return
	}
	m.runbooks, _ = m.db.Runbooks()
}

func (m RunbooksModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m RunbooksModel) Update(msg tea.Msg) (RunbooksModel, tea.Cmd) {
	switch msg := msg.(type) {
	case runbookRunDoneMsg:
		m.running = false
		if msg.err != nil {
			m.notice = "run failed: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("run #%d completed", msg.runID)
		}
		m.reload()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m RunbooksModel) handleKey(msg tea.KeyMsg) (RunbooksModel, tea.Cmd) {
	key := msg.String()

	if m.editing {
		return m.handleEdit(key, msg)
	}

	switch key {
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "q", "ctrl+c":
		return m, tea.Quit
	case "left", "h":
		if m.tab > 0 {
			m.tab--
		}
	case "right", "l":
		if m.tab < rbTabRuns {
			m.tab++
		}
	case "up", "k":
		if m.tab == rbTabList && m.cursor > 0 {
			m.cursor--
		} else if m.tab == rbTabRuns && m.runCursor > 0 {
			m.runCursor--
		}
	case "down", "j":
		if m.tab == rbTabList && m.cursor < len(m.runbooks)-1 {
			m.cursor++
		} else if m.tab == rbTabRuns && m.runCursor < len(m.runs)-1 {
			m.runCursor++
		}
	case "n":
		m.editing = true
		m.editID = 0
		m.nameInput.SetValue("")
		m.descInput.SetValue("")
		m.stepsRaw.SetValue("")
		m.editFocus = 0
		m.nameInput.Focus()
	case "e":
		if m.tab == rbTabList && m.cursor < len(m.runbooks) {
			rb := m.runbooks[m.cursor]
			m.editing = true
			m.editID = rb.ID
			m.nameInput.SetValue(rb.Name)
			m.descInput.SetValue(rb.Description)
			m.stepsRaw.SetValue(rb.StepsJSON)
			m.editFocus = 0
			m.nameInput.Focus()
		}
	case "d":
		if m.tab == rbTabList && m.cursor < len(m.runbooks) && m.db != nil {
			_ = m.db.DeleteRunbook(m.runbooks[m.cursor].ID)
			m.reload()
			if m.cursor >= len(m.runbooks) && m.cursor > 0 {
				m.cursor--
			}
		}
	case "enter", "r":
		if m.tab == rbTabList && m.cursor < len(m.runbooks) {
			rb := m.runbooks[m.cursor]
			m.running = true
			m.tab = rbTabRuns
			return m, m.executeRunbookCmd(rb)
		}
		if m.tab == rbTabRuns && m.runCursor < len(m.runs) && m.db != nil {
			rb := m.runs[m.runCursor]
			m.notice = fmt.Sprintf("run #%d: %s", rb.ID, rb.Status)
		}
	}
	return m, nil
}

func (m RunbooksModel) handleEdit(key string, msg tea.KeyMsg) (RunbooksModel, tea.Cmd) {
	inputs := []*textinput.Model{&m.nameInput, &m.descInput, &m.stepsRaw}
	switch key {
	case "esc":
		m.editing = false
		inputs[m.editFocus].Blur()
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
		// Save
		stepsJSON := strings.TrimSpace(m.stepsRaw.Value())
		if stepsJSON == "" {
			stepsJSON = "[]"
		}
		rb := db.Runbook{
			ID:          m.editID,
			Name:        strings.TrimSpace(m.nameInput.Value()),
			Description: strings.TrimSpace(m.descInput.Value()),
			StepsJSON:   stepsJSON,
		}
		if rb.Name == "" {
			m.notice = "name is required"
			return m, nil
		}
		if m.db != nil {
			_, err := m.db.SaveRunbook(rb)
			if err != nil {
				m.notice = "save error: " + err.Error()
				return m, nil
			}
		}
		m.editing = false
		inputs[m.editFocus].Blur()
		m.reload()
		m.notice = "saved"
	default:
		var cmd tea.Cmd
		switch m.editFocus {
		case 0:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case 1:
			m.descInput, cmd = m.descInput.Update(msg)
		case 2:
			m.stepsRaw, cmd = m.stepsRaw.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m RunbooksModel) executeRunbookCmd(rb db.Runbook) tea.Cmd {
	database := m.db
	saltClient := m.saltClient

	return func() tea.Msg {
		if database == nil {
			return runbookRunDoneMsg{err: fmt.Errorf("no database")}
		}
		runID, err := database.CreateRunbookRun(rb.ID)
		if err != nil {
			return runbookRunDoneMsg{err: err}
		}
		_ = database.UpdateRunbookRun(runID, "running", "")

		var steps []RunbookStep
		if err := json.Unmarshal([]byte(rb.StepsJSON), &steps); err != nil {
			_ = database.UpdateRunbookRun(runID, "failed", "invalid steps JSON: "+err.Error())
			return runbookRunDoneMsg{runID: runID, err: err}
		}

		var outputLines []string
		failed := false
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		for _, step := range steps {
			line := fmt.Sprintf("[%s] %s → %s %s", step.Kind, step.Name, step.Target, step.Command)
			switch step.Kind {
			case "salt":
				if saltClient != nil {
					jid, err := saltClient.ApplyState(ctx, step.Target, step.Command)
					if err != nil {
						line += " ERROR: " + err.Error()
						failed = true
					} else {
						line += " jid=" + jid
					}
				} else {
					line += " ERROR: no salt client configured"
					failed = true
				}
			case "shell":
				// Runs the command on the target minion via Salt cmd.run.
				if saltClient != nil {
					out, err := saltClient.RunCmd(ctx, step.Target, step.Command)
					if err != nil {
						line += " ERROR: " + err.Error()
						failed = true
					} else {
						line += " → " + truncate(strings.ReplaceAll(out, "\n", " "), 200)
					}
				} else {
					line += " ERROR: no salt client configured"
					failed = true
				}
			default:
				line += " ERROR: unsupported step kind (supported: salt, shell)"
				failed = true
			}
			outputLines = append(outputLines, line)
		}

		output := strings.Join(outputLines, "\n")
		status := "done"
		if failed {
			status = "failed"
		}
		_ = database.UpdateRunbookRun(runID, status, output)
		if failed {
			return runbookRunDoneMsg{runID: runID, err: fmt.Errorf("one or more steps failed")}
		}
		return runbookRunDoneMsg{runID: runID}
	}
}

func (m RunbooksModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("  Runbooks") + "\n\n")

	// Tab bar
	tabs := []string{"Runbooks", "Run History"}
	for i, t := range tabs {
		if rbTab(i) == m.tab {
			sb.WriteString(styleTabActive.Render("▸ " + t))
		} else {
			sb.WriteString(styleTabInactive.Render("  " + t))
		}
		if i < len(tabs)-1 {
			sb.WriteString(styleDim.Render("  │  "))
		}
	}
	sb.WriteString("\n" + styleDim.Render(strings.Repeat("─", max(m.width-4, 40))) + "\n\n")

	if m.editing {
		sb.WriteString(m.viewEdit())
	} else {
		switch m.tab {
		case rbTabList:
			sb.WriteString(m.viewList())
		case rbTabRuns:
			sb.WriteString(m.viewRuns())
		}
	}

	if m.notice != "" {
		sb.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	sb.WriteString("\n" + styleDim.Render(m.helpStr()))
	return sb.String()
}

func (m RunbooksModel) viewList() string {
	var sb strings.Builder
	if len(m.runbooks) == 0 {
		sb.WriteString(styleDim.Render("  No runbooks. Press n to create one.\n"))
		return sb.String()
	}
	sb.WriteString(styleSubtle.Render(fmt.Sprintf("  %-4s %-30s %s\n", "#", "Name", "Description")))
	sb.WriteString(styleDim.Render("  " + strings.Repeat("─", max(m.width-4, 60))) + "\n")
	for i, rb := range m.runbooks {
		line := fmt.Sprintf("  %-4d %-30s %s", rb.ID, truncate(rb.Name, 30), truncate(rb.Description, 50))
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m RunbooksModel) viewRuns() string {
	var sb strings.Builder
	if m.cursor >= len(m.runbooks) {
		sb.WriteString(styleDim.Render("  Select a runbook first.\n"))
		return sb.String()
	}
	rb := m.runbooks[m.cursor]
	runs, _ := m.db.RecentRunbookRuns(rb.ID, 20)
	if len(runs) == 0 {
		sb.WriteString(styleDim.Render("  No runs for " + rb.Name + " yet. Press enter or r to run.\n"))
		return sb.String()
	}
	sb.WriteString(styleBold.Render("  Runs for: "+rb.Name) + "\n\n")
	for i, r := range runs {
		statusStyle := styleDim
		switch r.Status {
		case "running":
			statusStyle = lipgloss.NewStyle().Foreground(colorYellow)
		case "done":
			statusStyle = lipgloss.NewStyle().Foreground(colorGreen)
		case "failed":
			statusStyle = lipgloss.NewStyle().Foreground(colorRed)
		}
		line := fmt.Sprintf("  %-4d %-10s  %s", r.ID, statusStyle.Render(r.Status), r.CreatedAt.Format("2006-01-02 15:04"))
		if i == m.runCursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
			if r.Output != "" {
				for _, ol := range strings.Split(r.Output, "\n") {
					sb.WriteString(styleDim.Render("       " + truncate(ol, m.width-10)) + "\n")
				}
			}
		} else {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

func (m RunbooksModel) viewEdit() string {
	var sb strings.Builder
	title := "New Runbook"
	if m.editID != 0 {
		title = "Edit Runbook"
	}
	sb.WriteString(styleBold.Render("  "+title) + "\n\n")
	fields := []struct {
		label string
		ti    textinput.Model
	}{
		{"  Name        ", m.nameInput},
		{"  Description ", m.descInput},
		{"  Steps JSON  ", m.stepsRaw},
	}
	for i, f := range fields {
		lbl := f.label
		if i == m.editFocus {
			sb.WriteString(styleCyan.Render(lbl) + f.ti.View() + "\n")
		} else {
			sb.WriteString(styleDim.Render(lbl) + f.ti.View() + "\n")
		}
	}
	sb.WriteString(styleDim.Render("\n  Steps JSON format: [{\"name\":\"…\",\"kind\":\"salt\",\"target\":\"minion\",\"command\":\"state\"}]\n"))
	sb.WriteString(styleDim.Render("  tab advance field · enter save · esc cancel\n"))
	return sb.String()
}

func (m RunbooksModel) helpStr() string {
	if m.editing {
		return "  tab advance · enter save · esc cancel"
	}
	return "  ↑/↓ navigate · n new · e edit · d delete · enter/r run · ←/→ tab · b back"
}
