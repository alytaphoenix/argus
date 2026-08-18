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

	confirmApply bool              // true when awaiting y/n confirmation to apply highstate
	actingTarget string            // minion ID the in-flight dispatch/poll targets
	polling      map[string]string // JID → minion ID, for in-flight result polls
}

func NewSaltModel(client *api.SaltClient) SaltModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return SaltModel{client: client, loading: true, spinner: s, polling: make(map[string]string)}
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
	// Use the target captured when the confirmation was armed, not the
	// current cursor — a concurrent minion-list refresh while the y/n
	// confirm is pending must not retarget or lose the dispatch.
	target := m.actingTarget
	if target == "" {
		return nil
	}
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jid, err := client.ApplyState(ctx, target, "")
		if err != nil {
			return saltActionDoneMsg{err: err}
		}
		return saltActionDoneMsg{notice: "highstate dispatched — JID " + jid, jid: jid}
	}
}

// pollJobResult waits saltPollInterval then checks whether the Salt job has
// returned a result. On each tick it returns either saltPollTickMsg (retry)
// or saltPollResultMsg (done / gave up). Mirrors SaltDetailModel.pollJobResult
// (ui/salt_detail.go), minus the local DB event bookkeeping this list-level
// screen doesn't have.
func (m SaltModel) pollJobResult(jid string, attempt int) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		time.Sleep(saltPollInterval)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		done, success, failedStates, err := client.JobResult(ctx, jid)
		if err != nil || !done {
			if attempt+1 >= saltPollMaxTries {
				return saltPollResultMsg{jid: jid, done: true, success: false, errMsg: "timed out"}
			}
			return saltPollTickMsg{jid: jid, attempt: attempt + 1}
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
		return saltPollResultMsg{jid: jid, done: true, success: success, errMsg: errMsg}
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
		if msg.err == nil && msg.jid != "" {
			if m.polling == nil {
				m.polling = make(map[string]string)
			}
			m.polling[msg.jid] = m.actingTarget
			return m, m.pollJobResult(msg.jid, 0)
		}
		return m, nil

	case saltPollTickMsg:
		// Ignore JIDs this screen didn't dispatch — e.g. a poll started here
		// can be delivered after the user has navigated into the minion
		// detail screen (and vice versa), since tea.Cmd results route to
		// whichever screen is active when they land, not the one that
		// launched them.
		if _, mine := m.polling[msg.jid]; !mine {
			return m, nil
		}
		// Job not done yet — fire another poll round.
		return m, m.pollJobResult(msg.jid, msg.attempt)

	case saltPollResultMsg:
		target, mine := m.polling[msg.jid]
		if !mine {
			return m, nil
		}
		delete(m.polling, msg.jid)
		if target == "" {
			target = "minion"
		}
		if msg.done {
			shortJID := msg.jid[:min8(msg.jid)]
			if msg.success {
				m.err = nil
				m.notice = fmt.Sprintf("highstate on %s: success (JID %s)", target, shortJID)
			} else {
				errDetail := msg.errMsg
				if errDetail == "" {
					errDetail = "highstate failed"
				}
				m.err = fmt.Errorf("highstate on %s: %s (JID %s)", target, errDetail, shortJID)
			}
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
		if m.confirmApply {
			switch msg.String() {
			case "y", "Y", "enter":
				m.confirmApply = false
				m.acting = true
				m.notice = ""
				m.err = nil
				return m, m.doApplyHighstate()
			default:
				m.confirmApply = false
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
				m.actingTarget = m.minions[m.cursor].ID
				m.confirmApply = true
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

func (m SaltModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("salt minions") + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading minions...\n")
		return sb.String()
	}

	if m.err != nil && len(m.minions) == 0 {
		// Only take over the whole screen for load failures — once minions
		// are loaded, action/poll errors are surfaced inline below so the
		// list stays visible.
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
	switch {
	case m.confirmApply && m.cursor < len(m.minions):
		sb.WriteString(stylePaused.Render(fmt.Sprintf("  Apply highstate to %q? [Y/n]", m.minions[m.cursor].ID)) + "\n")
	case m.acting:
		sb.WriteString("  " + m.spinner.View() + " dispatching...\n")
	case len(m.polling) > 0:
		sb.WriteString("  " + m.spinner.View() + fmt.Sprintf(" polling %d job(s) for result...\n", len(m.polling)))
	case m.err != nil:
		sb.WriteString(styleError.Render("  ✗ "+m.err.Error()) + "\n")
	case m.notice != "":
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n")
	default:
		sb.WriteString("\n")
	}

	sb.WriteString("\n  " + saltHelpStr() + "\n")
	return sb.String()
}
