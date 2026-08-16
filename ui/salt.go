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
)

type SaltModel struct {
	client  *api.SaltClient
	minions []api.Minion
	cursor  int
	loading bool
	acting  bool
	notice  string
	err     error
	width   int
	height  int
	spinner spinner.Model
}

func NewSaltModel(client *api.SaltClient) SaltModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return SaltModel{client: client, loading: true, spinner: s}
}

func (m SaltModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetch())
}

func (m SaltModel) fetch() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		minions, err := client.Minions(ctx)
		if err != nil {
			return errMsg{err}
		}
		return minionsLoadedMsg{minions}
	}
}

func (m SaltModel) doApplyHighstate() tea.Cmd {
	if m.cursor >= len(m.minions) {
		return nil
	}
	target := m.minions[m.cursor].ID
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jid, err := client.ApplyState(ctx, target, "")
		if err != nil {
			return saltActionDoneMsg{err: err}
		}
		return saltActionDoneMsg{notice: "highstate dispatched — JID " + jid}
	}
}

func (m SaltModel) Update(msg tea.Msg) (SaltModel, tea.Cmd) {
	switch msg := msg.(type) {
	case minionsLoadedMsg:
		m.minions = msg.minions
		m.loading = false
		m.err = nil
		if m.cursor >= len(m.minions) && len(m.minions) > 0 {
			m.cursor = len(m.minions) - 1
		}
		return m, nil

	case saltActionDoneMsg:
		m.acting = false
		m.err = msg.err
		if msg.err == nil {
			m.notice = msg.notice
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
		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			m.notice = ""
		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.minions)-1 {
				m.cursor++
			}
			m.notice = ""
		case key.Matches(msg, keys.Enter):
			if len(m.minions) > 0 {
				selected := m.minions[m.cursor]
				return m, func() tea.Msg { return minionSelectedMsg{selected} }
			}
		case key.Matches(msg, keys.SaltApply):
			if len(m.minions) > 0 && m.minions[m.cursor].Online {
				m.acting = true
				m.notice = ""
				return m, m.doApplyHighstate()
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

func (m SaltModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("salt minions") + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading minions...\n")
		return sb.String()
	}

	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		sb.WriteString(styleHelp.Render("  R to retry  b back"))
		return sb.String()
	}

	if len(m.minions) == 0 {
		sb.WriteString(styleDim.Render("  no minions found") + "\n\n")
		sb.WriteString("  " + saltHelpStr() + "\n")
		return sb.String()
	}

	header := fmt.Sprintf("  %-24s %-9s  %-22s  %-16s  %s",
		"MINION", "STATUS", "OS", "ROLES", "ADDRESS")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 80))) + "\n")

	for i, mn := range m.minions {
		status, dot := "offline", statusDot("stopped")
		if mn.Online {
			status, dot = "online", statusDot("running")
		}

		os := mn.Grains.OSFullName
		if mn.Grains.OSRelease != "" {
			os += " " + mn.Grains.OSRelease
		}

		roles := "-"
		if len(mn.Grains.Roles) > 0 {
			roles = strings.Join(mn.Grains.Roles, ",")
		}

		addr := "-"
		for _, ip := range mn.Grains.IPv4 {
			if ip != "127.0.0.1" {
				addr = ip
				break
			}
		}

		line := fmt.Sprintf("  %-24s %s %-7s  %-22s  %-16s  %s",
			truncate(mn.ID, 24),
			dot, status,
			truncate(os, 22),
			truncate(roles, 16),
			addr,
		)

		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n")
	if m.acting {
		sb.WriteString("  " + m.spinner.View() + " dispatching...\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n")
	} else {
		sb.WriteString("\n")
	}

	sb.WriteString("\n  " + saltHelpStr() + "\n")
	return sb.String()
}
