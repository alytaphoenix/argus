package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/api"
)

type detailTab int

const (
	detailTabStatus detailTab = iota
	detailTabLogs
)

// logMsg is sent to messages.go
type logLinesMsg struct {
	lines []string
	err   error
}

type DetailModel struct {
	client      *api.Client
	saltClient  *api.SaltClient
	guest       api.Guest
	loading     bool
	acting      bool
	notice      string
	err         error
	width       int
	height      int
	spinner     spinner.Model
	sshUser     string
	sshPassword string
	fetchingIP  bool

	// log tab
	tab        detailTab
	logLines   []string
	logLoading bool
	logUnit    textinput.Model
	logScroll  int
}

func NewDetailModel(client *api.Client, saltClient *api.SaltClient, guest api.Guest, sshUser, sshPassword string) DetailModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	logUnitTI := textinput.New()
	logUnitTI.Placeholder = "unit name (blank = all)"
	logUnitTI.CharLimit = 64

	return DetailModel{
		client:      client,
		saltClient:  saltClient,
		guest:       guest,
		loading:     true,
		spinner:     s,
		sshUser:     sshUser,
		sshPassword: sshPassword,
		logUnit:     logUnitTI,
	}
}

func (m DetailModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchStatus())
}

func (m DetailModel) fetchStatus() tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, err := client.GuestStatus(ctx, g.Node, g.Type, g.VMID)
		if err != nil {
			return errMsg{err}
		}
		return guestStatusMsg{*status}
	}
}

func (m DetailModel) doAction(action string) tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.GuestAction(ctx, g, action)
		return actionDoneMsg{err}
	}
}

func (m DetailModel) fetchLogsCmd() tea.Cmd {
	saltClient := m.saltClient
	target := m.guest.Name
	unit := strings.TrimSpace(m.logUnit.Value())
	return func() tea.Msg {
		if saltClient == nil {
			return logLinesMsg{err: fmt.Errorf("Salt not configured — logs require Salt API access")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		lines, err := saltClient.JournalLogs(ctx, target, unit, 100)
		return logLinesMsg{lines: lines, err: err}
	}
}

func (m DetailModel) fetchIP() tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ip := client.GuestPrimaryIP(ctx, g.Node, g.Type, g.VMID)
		return guestIPMsg{guest: g, ip: ip}
	}
}

func (m DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case guestStatusMsg:
		m.guest = msg.guest
		m.loading = false
		m.err = nil
		return m, nil

	case actionDoneMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "command sent"
		}
		return m, m.fetchStatus()

	case guestIPMsg:
		m.fetchingIP = false
		if msg.err != nil || msg.ip == "" {
			m.err = fmt.Errorf("could not get IP — is the guest agent running?")
			return m, nil
		}
		user := m.sshUser
		if user == "" {
			user = "root"
		}
		host := user + "@" + msg.ip
		var cmd *exec.Cmd
		if m.sshPassword != "" {
			cmd = exec.Command("sshpass", "-p", m.sshPassword, "ssh", "-o", "StrictHostKeyChecking=no", host)
		} else {
			cmd = exec.Command("ssh", "-o", "StrictHostKeyChecking=no", host)
		}
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return sshDoneMsg{err}
		})

	case sshDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "SSH session ended"
		}
		return m, m.fetchStatus()

	case errMsg:
		m.err = msg.err
		m.loading = false
		return m, nil

	case logLinesMsg:
		m.logLoading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.logLines = msg.lines
			m.logScroll = max(0, len(m.logLines)-20)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// Log tab text input intercepts keys
		if m.tab == detailTabLogs && m.logUnit.Focused() {
			switch msg.String() {
			case "esc":
				m.logUnit.Blur()
			case "enter":
				m.logUnit.Blur()
				m.logLoading = true
				return m, m.fetchLogsCmd()
			default:
				var cmd tea.Cmd
				m.logUnit, cmd = m.logUnit.Update(msg)
				return m, cmd
			}
		}

		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Back):
			return m, func() tea.Msg { return backMsg{} }
		case msg.String() == "L":
			if m.tab == detailTabStatus {
				m.tab = detailTabLogs
			} else {
				m.tab = detailTabStatus
			}
		case m.tab == detailTabLogs && (msg.String() == "up" || msg.String() == "k"):
			if m.logScroll > 0 {
				m.logScroll--
			}
		case m.tab == detailTabLogs && (msg.String() == "down" || msg.String() == "j"):
			if m.logScroll < len(m.logLines)-1 {
				m.logScroll++
			}
		case m.tab == detailTabLogs && msg.String() == "f":
			m.logUnit.Focus()
		case m.tab == detailTabLogs && msg.String() == "R":
			m.logLoading = true
			return m, m.fetchLogsCmd()
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			return m, m.fetchStatus()
		case key.Matches(msg, keys.Start):
			if m.guest.Status == "stopped" {
				m.acting = true
				return m, m.doAction("start")
			}
		case key.Matches(msg, keys.Stop):
			if m.guest.Status == "running" {
				m.acting = true
				return m, m.doAction("stop")
			}
		case key.Matches(msg, keys.Reboot):
			if m.guest.Status == "running" {
				m.acting = true
				return m, m.doAction("reboot")
			}
		case key.Matches(msg, keys.SSH):
			if m.guest.Status == "running" {
				m.fetchingIP = true
				m.err = nil
				return m, m.fetchIP()
			}
		}
	}
	return m, nil
}

func (m DetailModel) View() string {
	var sb strings.Builder
	g := m.guest

	breadcrumb := styleTitle.Render("⚡ ratataskr") +
		styleDim.Render(" › ") +
		styleBold.Render(g.Node) +
		styleDim.Render(" › ") +
		styleBold.Render(fmt.Sprintf("%s (%d)", g.Name, g.VMID))
	sb.WriteString(breadcrumb + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading...\n")
		return sb.String()
	}

	row := func(label, value string) {
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render(label), value))
	}

	typeLabel := "Virtual Machine"
	if g.Type == "lxc" {
		typeLabel = "LXC Container"
	}

	row("Type", typeLabel)
	row("Status", statusDot(g.Status)+" "+g.Status)
	row("Node", g.Node)
	row("Uptime", fmtUptime(g.Uptime))

	if g.Status == "running" {
		sb.WriteString("\n")

		cpuPct := g.CPU * 100
		if g.CPUs > 0 {
			cpuPct = g.CPU * 100 / float64(g.CPUs)
		}
		row("vCPUs", fmt.Sprintf("%d", g.CPUs))
		row("CPU usage", fmt.Sprintf("%.1f%%  %s", cpuPct, progressBar(cpuPct/100, 24)))

		if g.MaxMem > 0 {
			memRatio := float64(g.Mem) / float64(g.MaxMem)
			row("Memory", fmt.Sprintf("%s / %s  %s", fmtBytes(g.Mem), fmtBytes(g.MaxMem), progressBar(memRatio, 24)))
		}

		if g.MaxDisk > 0 {
			diskRatio := float64(g.Disk) / float64(g.MaxDisk)
			row("Disk", fmt.Sprintf("%s / %s  %s", fmtBytes(g.Disk), fmtBytes(g.MaxDisk), progressBar(diskRatio, 24)))
		}

		if g.NetIn > 0 || g.NetOut > 0 {
			sb.WriteString("\n")
			row("Net in", fmtBytes(g.NetIn))
			row("Net out", fmtBytes(g.NetOut))
		}
	}

	sb.WriteString("\n")

	if m.fetchingIP {
		sb.WriteString("  " + m.spinner.View() + " looking up IP...\n\n")
	} else if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.acting {
		sb.WriteString("  " + m.spinner.View() + " sending command...\n\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
	}

	if m.tab == detailTabLogs {
		sb.WriteString(m.viewLogs())
	}

	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 40))) + "\n")
	help := detailHelpStr()
	if m.tab == detailTabLogs {
		help = styleHelp.Render("↑/↓ scroll  f filter unit  R refresh  L back to status  b back  q quit")
	} else {
		help += styleHelp.Render("  L logs")
	}
	sb.WriteString("  " + help + "\n")

	return sb.String()
}

func (m DetailModel) viewLogs() string {
	var sb strings.Builder
	sb.WriteString("\n" + styleBold.Render("  Journal Logs") + "\n")
	sb.WriteString(styleDim.Render("  Unit filter: ") + m.logUnit.View() + "\n\n")

	if m.logLoading {
		sb.WriteString("  " + m.spinner.View() + " fetching logs…\n")
		return sb.String()
	}
	if m.saltClient == nil {
		sb.WriteString(styleDim.Render("  Salt not configured. Logs require Salt API access.\n"))
		return sb.String()
	}
	if len(m.logLines) == 0 {
		sb.WriteString(styleDim.Render("  No logs loaded. Press R to fetch or f to set unit filter.\n"))
		return sb.String()
	}

	visible := m.height - 18
	if visible < 5 {
		visible = 5
	}
	start := m.logScroll
	if start > len(m.logLines)-1 {
		start = len(m.logLines) - 1
	}
	end := start + visible
	if end > len(m.logLines) {
		end = len(m.logLines)
	}
	for _, l := range m.logLines[start:end] {
		sb.WriteString("  " + truncate(l, m.width-4) + "\n")
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("\n  line %d/%d", start+1, len(m.logLines))) + "\n")
	return sb.String()
}
