package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/db"
)

const (
	saltPollInterval = 10 * time.Second
	saltPollMaxTries = 12 // 12 × 10s = 2 minutes max
)

// saltApplyMode controls what is shown in the "apply" sub-prompt.
type saltApplyMode int

const (
	saltApplyNone   saltApplyMode = iota
	saltApplyCustom               // user is typing a custom state
	saltApplyPicker               // user is picking a saved salt profile
)

type SaltDetailModel struct {
	client   *api.SaltClient
	database *db.DB
	minion   api.Minion
	jobs     []api.Job
	events   []db.SaltApplyEvent // recent apply events for this minion (local DB)
	loading  bool
	acting   bool
	notice   string
	err      error
	width    int
	height   int
	spinner  spinner.Model

	// apply sub-prompt
	applyMode  saltApplyMode
	stateInput textinput.Model
	saltProfs  []db.SaltProfile
	profCursor int

	// polling state: maps jid → eventID for in-flight polls
	polling map[string]int64
}

func NewSaltDetailModel(client *api.SaltClient, minion api.Minion, database *db.DB) SaltDetailModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Placeholder = "state name (blank = highstate)"
	ti.CharLimit = 256

	m := SaltDetailModel{
		client:     client,
		database:   database,
		minion:     minion,
		loading:    true,
		spinner:    s,
		stateInput: ti,
		polling:    make(map[string]int64),
	}

	if database != nil {
		m.saltProfs, _ = database.SaltProfiles()
		m.events, _ = database.SaltApplyEventsForMinion(minion.ID, 10)
	}
	return m
}

func (m SaltDetailModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchJobs())
}

func (m SaltDetailModel) fetchJobs() tea.Cmd {
	client := m.client
	id := m.minion.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jobs, err := client.JobsForMinion(ctx, id)
		if err != nil {
			return saltJobsMsg{err: err}
		}
		return saltJobsMsg{jobs: jobs}
	}
}

// doApply dispatches state.apply asynchronously, records the event, and
// returns saltActionDoneMsg with the JID and DB event ID so polling can start.
func (m SaltDetailModel) doApply(state, profileName string) tea.Cmd {
	target := m.minion.ID
	client := m.client
	database := m.database
	minion := m.minion.ID

	stateLabel := state
	if stateLabel == "" {
		stateLabel = "highstate"
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jid, applyErr := client.ApplyState(ctx, target, state)

		// Record the dispatch attempt regardless of outcome.
		var eventID int64
		if database != nil {
			ev := db.SaltApplyEvent{
				Minion:      minion,
				States:      stateLabel,
				SaltProfile: profileName,
				JID:         jid,
				Success:     applyErr == nil,
			}
			if applyErr != nil {
				ev.ErrorMsg = applyErr.Error()
			}
			eventID, _ = database.RecordSaltApply(ev)
		}

		if applyErr != nil {
			return saltActionDoneMsg{err: applyErr}
		}
		return saltActionDoneMsg{
			notice:  "dispatched JID " + jid,
			jid:     jid,
			eventID: eventID,
		}
	}
}

// pollJobResult waits saltPollInterval then checks whether the Salt job has
// returned a result.  On each tick it returns either saltPollTickMsg (retry)
// or saltPollResultMsg (done / gave up).
func (m SaltDetailModel) pollJobResult(jid string, eventID int64, attempt int) tea.Cmd {
	client := m.client
	database := m.database
	return func() tea.Msg {
		time.Sleep(saltPollInterval)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		done, success, failedStates, err := client.JobResult(ctx, jid)
		if err != nil || !done {
			if attempt+1 >= saltPollMaxTries {
				// Give up — mark the event as an unknown result.
				if database != nil {
					_ = database.UpdateSaltApplyResult(eventID, false, "timed out waiting for result")
				}
				return saltPollResultMsg{jid: jid, eventID: eventID, done: true,
					success: false, errMsg: "timed out"}
			}
			return saltPollTickMsg{jid: jid, eventID: eventID, attempt: attempt + 1}
		}

		errMsg := ""
		if !success && len(failedStates) > 0 {
			// Truncate very long state ID lists.
			if len(failedStates) > 3 {
				errMsg = strings.Join(failedStates[:3], ", ") +
					fmt.Sprintf(" (+%d more)", len(failedStates)-3)
			} else {
				errMsg = "failed: " + strings.Join(failedStates, ", ")
			}
		}
		if database != nil {
			_ = database.UpdateSaltApplyResult(eventID, success, errMsg)
		}
		return saltPollResultMsg{
			jid: jid, eventID: eventID,
			done: true, success: success, errMsg: errMsg,
		}
	}
}

func (m SaltDetailModel) reloadEvents() {
	if m.database != nil {
		m.events, _ = m.database.SaltApplyEventsForMinion(m.minion.ID, 10)
	}
}

func (m SaltDetailModel) Update(msg tea.Msg) (SaltDetailModel, tea.Cmd) {
	// ── apply sub-prompt: custom state ─────────────────────────────────────
	if m.applyMode == saltApplyCustom {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.applyMode = saltApplyNone
				return m, nil
			case "enter":
				state := strings.TrimSpace(m.stateInput.Value())
				m.applyMode = saltApplyNone
				m.acting = true
				m.notice = ""
				return m, m.doApply(state, "")
			}
		}
		var cmd tea.Cmd
		m.stateInput, cmd = m.stateInput.Update(msg)
		return m, cmd
	}

	// ── apply sub-prompt: profile picker ───────────────────────────────────
	if m.applyMode == saltApplyPicker {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.applyMode = saltApplyNone
				return m, nil
			case "up", "k":
				if m.profCursor > 0 {
					m.profCursor--
				}
			case "down", "j":
				if m.profCursor < len(m.saltProfs)-1 {
					m.profCursor++
				}
			case "enter":
				if m.profCursor < len(m.saltProfs) {
					prof := m.saltProfs[m.profCursor]
					m.applyMode = saltApplyNone
					m.acting = true
					m.notice = ""
					return m, m.doApply(prof.States, prof.Name)
				}
			}
		}
		return m, nil
	}

	// ── normal mode ────────────────────────────────────────────────────────
	switch msg := msg.(type) {
	case saltJobsMsg:
		m.loading = false
		m.err = msg.err
		m.jobs = msg.jobs
		if m.database != nil {
			m.events, _ = m.database.SaltApplyEventsForMinion(m.minion.ID, 10)
		}
		return m, nil

	case saltActionDoneMsg:
		m.acting = false
		m.err = msg.err
		if msg.err == nil {
			m.notice = msg.notice
		}
		if m.database != nil {
			m.events, _ = m.database.SaltApplyEventsForMinion(m.minion.ID, 10)
		}
		var cmds []tea.Cmd
		cmds = append(cmds, m.fetchJobs())
		// Start polling if we got a JID.
		if msg.jid != "" && msg.eventID != 0 {
			m.polling[msg.jid] = msg.eventID
			cmds = append(cmds, m.pollJobResult(msg.jid, msg.eventID, 0))
		}
		return m, tea.Batch(cmds...)

	case saltPollTickMsg:
		if _, mine := m.polling[msg.jid]; !mine {
			// Not our JID — likely a poll started on the Salt list screen,
			// or for a different minion's detail view. app.go forwards poll
			// messages to every Salt model since the user may have
			// navigated away from whichever screen started the poll.
			return m, nil
		}
		// Job not done yet — fire another poll round.
		return m, m.pollJobResult(msg.jid, msg.eventID, msg.attempt)

	case saltPollResultMsg:
		if _, mine := m.polling[msg.jid]; !mine {
			return m, nil
		}
		delete(m.polling, msg.jid)
		// Refresh the event list so the updated result shows up.
		if m.database != nil {
			m.events, _ = m.database.SaltApplyEventsForMinion(m.minion.ID, 10)
		}
		if msg.done && !msg.success && msg.errMsg != "" {
			m.err = fmt.Errorf("JID %s: %s", msg.jid[:min8(msg.jid)], msg.errMsg)
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		m.loading = false
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Back):
			return m, func() tea.Msg { return backMsg{} }
		case key.Matches(msg, keys.SaltApply):
			if m.minion.Online {
				m.acting = true
				m.notice = ""
				return m, m.doApply("", "")
			}
		case msg.String() == "A":
			if m.minion.Online {
				m.applyMode = saltApplyCustom
				m.stateInput.SetValue("")
				m.stateInput.Focus()
				return m, textinput.Blink
			}
		case msg.String() == "p":
			if m.minion.Online && len(m.saltProfs) > 0 {
				m.applyMode = saltApplyPicker
				m.profCursor = 0
				return m, nil
			}
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			return m, m.fetchJobs()
		}
	}
	return m, nil
}

// min8 returns min(len(s), 8) — used to abbreviate JIDs in error messages.
func min8(s string) int {
	if len(s) < 8 {
		return len(s)
	}
	return 8
}

func (m SaltDetailModel) View() string {
	var sb strings.Builder
	mn := m.minion

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("salt") +
		styleDim.Render(" › ") +
		styleBold.Render(mn.ID) + "\n\n")

	row := func(label, value string) {
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render(label), value))
	}

	onlineStr := "offline"
	if mn.Online {
		onlineStr = "online"
	}
	statusKey := map[bool]string{true: "running", false: "stopped"}[mn.Online]

	row("Minion", mn.ID)
	row("Status", statusDot(statusKey)+" "+onlineStr)
	if mn.Grains.FQDN != "" && mn.Grains.FQDN != mn.ID {
		row("FQDN", mn.Grains.FQDN)
	}
	if mn.Grains.OSFullName != "" {
		row("OS", strings.TrimSpace(mn.Grains.OSFullName+" "+mn.Grains.OSRelease))
	}
	if mn.Grains.Kernel != "" {
		row("Kernel", mn.Grains.Kernel)
	}
	if mn.Grains.Arch != "" {
		row("Arch", mn.Grains.Arch)
	}
	if len(mn.Grains.Roles) > 0 {
		row("Roles", strings.Join(mn.Grains.Roles, ", "))
	}
	for _, ip := range mn.Grains.IPv4 {
		if ip != "127.0.0.1" {
			row("Address", ip)
			break
		}
	}
	if len(m.polling) > 0 {
		row("Polling", fmt.Sprintf("%d JID(s) awaiting result…", len(m.polling)))
	}

	// ── apply sub-prompts ──────────────────────────────────────────────────
	if m.applyMode == saltApplyCustom {
		sb.WriteString("\n")
		sb.WriteString(styleBold.Render("  Apply state") + "\n")
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("State"), m.stateInput.View()))
		sb.WriteString(styleHelp.Render("  enter confirm  esc cancel") + "\n")
		return sb.String()
	}

	if m.applyMode == saltApplyPicker {
		sb.WriteString("\n")
		sb.WriteString(styleBold.Render("  Pick a salt profile") + "\n")
		for i, p := range m.saltProfs {
			line := fmt.Sprintf("  %-20s %s", truncate(p.Name, 20), styleDim.Render(p.States))
			if i == m.profCursor {
				sb.WriteString(styleSelected.Render(line) + "\n")
			} else {
				sb.WriteString(line + "\n")
			}
		}
		sb.WriteString(styleHelp.Render("  ↑/↓ navigate  enter apply  esc cancel") + "\n")
		return sb.String()
	}

	// ── Salt API job list ──────────────────────────────────────────────────
	sb.WriteString("\n")
	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading jobs...\n")
	} else if len(m.jobs) > 0 {
		sb.WriteString(styleDim.Render("  Recent Salt jobs") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 60))) + "\n")
		for _, j := range m.jobs {
			ts := j.StartTime
			if len(ts) > 19 {
				ts = ts[:19]
			}
			sb.WriteString(fmt.Sprintf("  %s  %-28s  %s\n",
				styleDim.Render(ts),
				j.Function,
				styleDim.Render(j.Target),
			))
		}
	} else {
		sb.WriteString(styleDim.Render("  no recent Salt jobs") + "\n")
	}

	// ── Local event log ────────────────────────────────────────────────────
	if len(m.events) > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleDim.Render("  Argus apply log (last 10)") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 60))) + "\n")
		for _, ev := range m.events {
			dot := styleRunning.Render("✓")
			if !ev.Success {
				dot = styleError.Render("✗")
			}
			// Show spinner for JIDs that are still being polled.
			if _, polling := m.polling[ev.JID]; polling && ev.JID != "" {
				dot = m.spinner.View()
			}
			profLabel := ""
			if ev.SaltProfile != "" {
				profLabel = styleCyan.Render(" [" + ev.SaltProfile + "]")
			}
			sb.WriteString(fmt.Sprintf("  %s  %s  %-20s%s\n",
				styleDim.Render(ev.TriggeredAt.Format("2006-01-02 15:04")),
				dot,
				truncate(ev.States, 20),
				profLabel,
			))
		}
	}

	sb.WriteString("\n")
	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.acting {
		sb.WriteString("  " + m.spinner.View() + " dispatching...\n\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
	}

	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 40))) + "\n")
	sb.WriteString("  " + saltDetailHelpStr() + "\n")
	return sb.String()
}
