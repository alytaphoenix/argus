package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/db"
)

func nomadJobDot(status string) string {
	switch status {
	case "running":
		return styleRunning.Render("●")
	case "pending":
		return stylePaused.Render("●")
	default: // dead
		return styleStopped.Render("○")
	}
}

func nomadAllocDot(status string) string {
	switch status {
	case "running":
		return styleRunning.Render("●")
	case "pending":
		return stylePaused.Render("◐")
	case "complete":
		return styleDim.Render("○")
	default: // failed, lost, unknown
		return styleStopped.Render("●")
	}
}

func nomadEventDot(eventType string) string {
	switch eventType {
	case "started":
		return styleRunning.Render("↑")
	case "stopped":
		return styleStopped.Render("↓")
	case "degraded":
		return styleError.Render("!")
	case "first_seen":
		return styleCyan.Render("★")
	default:
		return styleDim.Render("·")
	}
}

type NomadModel struct {
	client   *api.NomadClient
	database *db.DB
	jobs     []api.NomadJob
	events   []db.NomadJobEvent
	cursor   int
	loading  bool
	acting   bool
	confirm  bool
	notice   string
	err      error
	width    int
	height   int
	spinner  spinner.Model
}

func NewNomadModel(client *api.NomadClient, database *db.DB) NomadModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := NomadModel{client: client, database: database, loading: true, spinner: s}
	if database != nil {
		m.events, _ = database.RecentNomadEvents(50)
	}
	return m
}

func (m NomadModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetch())
}

func (m NomadModel) fetch() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jobs, err := client.Jobs(ctx)
		if err != nil {
			return errMsg{err}
		}
		return nomadJobsLoadedMsg{jobs}
	}
}

// syncNomadState converts the live job list to NomadSyncEntry values, persists
// them to the DB, and returns any new events so the UI can update.
func (m NomadModel) syncNomadState(jobs []api.NomadJob) tea.Cmd {
	database := m.database
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		entries := make([]db.NomadSyncEntry, len(jobs))
		for i, j := range jobs {
			running, failed, lost := j.Totals()
			entries[i] = db.NomadSyncEntry{
				JobID:   j.ID,
				JobType: j.Type,
				Status:  j.Status,
				Running: running,
				Failed:  failed,
				Lost:    lost,
			}
		}
		newEvents, err := database.SyncNomadState(entries)
		if err != nil {
			return nomadSyncDoneMsg{}
		}
		recent, _ := database.RecentNomadEvents(50)
		return nomadSyncDoneMsg{newEvents: newEvents, recentEvents: recent}
	}
}

func (m NomadModel) doStop() tea.Cmd {
	if m.cursor >= len(m.jobs) {
		return nil
	}
	jobID := m.jobs[m.cursor].ID
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.StopJob(ctx, jobID)
		return nomadActionDoneMsg{notice: "stop dispatched for " + jobID, err: err}
	}
}

func (m NomadModel) Update(msg tea.Msg) (NomadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case nomadJobsLoadedMsg:
		m.jobs = msg.jobs
		m.loading = false
		m.err = nil
		if m.cursor >= len(m.jobs) && len(m.jobs) > 0 {
			m.cursor = len(m.jobs) - 1
		}
		return m, m.syncNomadState(msg.jobs)

	case nomadSyncDoneMsg:
		if msg.recentEvents != nil {
			m.events = msg.recentEvents
		}
		return m, nil

	case nomadActionDoneMsg:
		m.acting = false
		m.confirm = false
		m.err = msg.err
		if msg.err == nil {
			m.notice = msg.notice
		}
		return m, m.fetch()

	case errMsg:
		m.err = msg.err
		m.loading = false
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if m.confirm {
			switch msg.String() {
			case "y", "Y", "enter":
				m.acting = true
				m.confirm = false
				return m, m.doStop()
			default:
				m.confirm = false
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Back):
			return m, func() tea.Msg { return backMsg{} }
		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			m.notice = ""
		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
			m.notice = ""
		case key.Matches(msg, keys.Enter):
			if len(m.jobs) > 0 {
				selected := m.jobs[m.cursor]
				return m, func() tea.Msg { return nomadJobSelectedMsg{selected} }
			}
		case key.Matches(msg, keys.Stop):
			if len(m.jobs) > 0 && m.jobs[m.cursor].Status == "running" {
				m.confirm = true
				m.notice = ""
			}
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			m.notice = ""
			m.err = nil
			return m, m.fetch()
		}
	}
	return m, nil
}

func (m NomadModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("nomad jobs") + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading jobs...\n")
		return sb.String()
	}

	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		sb.WriteString(styleHelp.Render("  R to retry  b back"))
		return sb.String()
	}

	if len(m.jobs) == 0 {
		sb.WriteString(styleDim.Render("  no jobs found") + "\n\n")
		sb.WriteString("  " + nomadHelpStr() + "\n")
		return sb.String()
	}

	header := fmt.Sprintf("  %-32s %-9s  %-9s  %7s  %6s  %5s",
		"JOB", "TYPE", "STATUS", "RUNNING", "FAILED", "LOST")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 72))) + "\n")

	for i, j := range m.jobs {
		running, failed, lost := j.Totals()

		failedStr := styleDim.Render(fmt.Sprintf("%6d", failed))
		if failed > 0 {
			failedStr = styleStopped.Render(fmt.Sprintf("%6d", failed))
		}
		lostStr := styleDim.Render(fmt.Sprintf("%5d", lost))
		if lost > 0 {
			lostStr = stylePaused.Render(fmt.Sprintf("%5d", lost))
		}

		line := fmt.Sprintf("  %-32s %-9s  %s %-7s  %7d  %s  %s",
			truncate(j.ID, 32),
			truncate(j.Type, 9),
			nomadJobDot(j.Status),
			j.Status,
			running,
			failedStr,
			lostStr,
		)

		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n")

	if m.confirm && m.cursor < len(m.jobs) {
		sb.WriteString(styleError.Render(fmt.Sprintf("  Stop %q? [Y/n]", m.jobs[m.cursor].ID)) + "\n")
	} else if m.acting {
		sb.WriteString("  " + m.spinner.View() + " stopping job...\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n")
	} else {
		sb.WriteString("\n")
	}

	sb.WriteString("\n  " + nomadHelpStr() + "\n")
	return sb.String()
}
