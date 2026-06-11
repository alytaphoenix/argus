package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

type NomadDetailModel struct {
	client   *api.NomadClient
	database *db.DB
	job      api.NomadJob
	allocs   []api.NomadAllocation
	events   []db.NomadJobEvent
	loading  bool
	acting   bool
	notice   string
	err      error
	width    int
	height   int
	spinner  spinner.Model
}

func NewNomadDetailModel(client *api.NomadClient, job api.NomadJob, database *db.DB) NomadDetailModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := NomadDetailModel{
		client:   client,
		database: database,
		job:      job,
		loading:  true,
		spinner:  s,
	}
	if database != nil {
		m.events, _ = database.NomadEventsForJob(job.ID, 20)
	}
	return m
}

func (m NomadDetailModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchAllocs())
}

func (m NomadDetailModel) fetchAllocs() tea.Cmd {
	client := m.client
	jobID := m.job.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		allocs, err := client.JobAllocations(ctx, jobID)
		if err != nil {
			return nomadAllocsLoadedMsg{err: err}
		}
		return nomadAllocsLoadedMsg{allocs: allocs}
	}
}

func (m NomadDetailModel) doStop() tea.Cmd {
	client := m.client
	jobID := m.job.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.StopJob(ctx, jobID)
		return nomadActionDoneMsg{notice: "stop dispatched", err: err}
	}
}

func (m NomadDetailModel) Update(msg tea.Msg) (NomadDetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case nomadAllocsLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.allocs = msg.allocs
		// Refresh job history from DB on every alloc load.
		if m.database != nil {
			m.events, _ = m.database.NomadEventsForJob(m.job.ID, 20)
		}
		return m, nil

	case nomadActionDoneMsg:
		m.acting = false
		m.err = msg.err
		if msg.err == nil {
			m.notice = msg.notice
			m.job.Status = "dead"
		}
		return m, m.fetchAllocs()

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
		case key.Matches(msg, keys.Stop):
			if m.job.Status == "running" {
				m.acting = true
				m.notice = ""
				return m, m.doStop()
			}
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			return m, m.fetchAllocs()
		}
	}
	return m, nil
}

func (m NomadDetailModel) View() string {
	var sb strings.Builder
	j := m.job

	sb.WriteString(styleTitle.Render("⚡ ratataskr") +
		styleDim.Render(" › ") +
		styleBold.Render("nomad") +
		styleDim.Render(" › ") +
		styleBold.Render(j.ID) + "\n\n")

	row := func(label, value string) {
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render(label), value))
	}

	row("Job", j.ID)
	row("Type", j.Type)
	row("Status", nomadJobDot(j.Status)+" "+j.Status)
	row("Priority", fmt.Sprintf("%d", j.Priority))
	if len(j.Datacenters) > 0 {
		row("Datacenters", strings.Join(j.Datacenters, ", "))
	}

	if j.JobSummary != nil && len(j.JobSummary.Summary) > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleDim.Render("  Task Groups") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 50))) + "\n")
		for tg, s := range j.JobSummary.Summary {
			counts := fmt.Sprintf("running=%d  queued=%d  complete=%d  failed=%d  lost=%d",
				s.Running, s.Queued, s.Complete, s.Failed, s.Lost)
			sb.WriteString(fmt.Sprintf("  %-20s %s\n", styleBold.Render(tg), styleDim.Render(counts)))
		}
	}

	sb.WriteString("\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading allocations...\n")
	} else if len(m.allocs) > 0 {
		sb.WriteString(styleDim.Render("  Allocations") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 60))) + "\n")

		header := fmt.Sprintf("  %-10s  %-20s  %-9s  %-16s  %s",
			"ID", "TASK GROUP", "STATUS", "NODE", "AGE")
		sb.WriteString(styleDim.Render(header) + "\n")

		now := time.Now().UnixNano()
		for _, a := range m.allocs {
			uptimeSec := (now - a.CreateTime) / 1e9
			if uptimeSec < 0 {
				uptimeSec = 0
			}
			allocID := a.ID
			if len(allocID) > 8 {
				allocID = allocID[:8]
			}
			line := fmt.Sprintf("  %-10s  %-20s  %s %-7s  %-16s  %s",
				allocID,
				truncate(a.TaskGroup, 20),
				nomadAllocDot(a.ClientStatus),
				a.ClientStatus,
				truncate(a.NodeName, 16),
				fmtUptime(uptimeSec),
			)
			sb.WriteString(line + "\n")
		}
	} else if !m.loading {
		sb.WriteString(styleDim.Render("  no allocations") + "\n")
	}

	// ── Job history from local DB ──────────────────────────────────────────
	if len(m.events) > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleDim.Render("  Job history") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 60))) + "\n")
		for _, ev := range m.events {
			dot := nomadEventDot(ev.EventType)
			detail := ev.NewStatus
			if ev.PrevStatus != "" {
				detail = ev.PrevStatus + " → " + ev.NewStatus
			}
			counts := ""
			if ev.Running > 0 || ev.Failed > 0 || ev.Lost > 0 {
				counts = fmt.Sprintf("  run=%d fail=%d lost=%d", ev.Running, ev.Failed, ev.Lost)
			}
			sb.WriteString(fmt.Sprintf("  %s  %s  %-16s  %s%s\n",
				styleDim.Render(ev.RecordedAt.Format("2006-01-02 15:04")),
				dot,
				ev.EventType,
				styleDim.Render(detail),
				styleDim.Render(counts),
			))
		}
	}

	sb.WriteString("\n")

	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.acting {
		sb.WriteString("  " + m.spinner.View() + " stopping...\n\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
	}

	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 40))) + "\n")
	sb.WriteString("  " + nomadDetailHelpStr() + "\n")
	return sb.String()
}
