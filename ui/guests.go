package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/db"
)

type confirmKind int

const (
	confirmNone confirmKind = iota
	confirmStop
	confirmReboot
	confirmDeleteSnapshot
)

type GuestsModel struct {
	client      *api.Client
	database    *db.DB
	node        api.Node
	guests      []api.Guest
	cursor      int
	selected    map[int]bool // index → selected for bulk ops
	loading     bool
	acting      bool
	confirm     confirmKind
	bulkConfirm string // "start","stop","reboot" or ""
	notice      string
	err         error
	width       int
	height      int
	spinner     spinner.Model
	sshUser     string
	sshPassword string
	sshTarget   api.Guest
	fetchingIP  bool
}

func NewGuestsModel(client *api.Client, node api.Node, sshUser, sshPassword string, database *db.DB) GuestsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return GuestsModel{
		client:      client,
		database:    database,
		node:        node,
		loading:     true,
		spinner:     s,
		sshUser:     sshUser,
		sshPassword: sshPassword,
		selected:    make(map[int]bool),
	}
}

func (m GuestsModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetch())
}

func (m GuestsModel) fetch() tea.Cmd {
	client := m.client
	node := m.node.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		guests, err := client.Guests(ctx, node)
		if err != nil {
			return errMsg{err}
		}
		return guestsLoadedMsg{guests}
	}
}

// syncVMStatuses cross-references the freshly-loaded guests against the
// provisioned_vms table and writes back status + type for any that match.
func (m GuestsModel) syncVMStatuses(guests []api.Guest) tea.Cmd {
	database := m.database
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		// Build vmid → guest map.
		byID := make(map[int]api.Guest, len(guests))
		for _, g := range guests {
			byID[g.VMID] = g
		}
		// Load provisioned VMs and update any that appear in the live list.
		vms, err := database.ProvisionedVMs()
		if err != nil {
			return vmStatusSyncedMsg{}
		}
		for _, vm := range vms {
			if g, ok := byID[vm.VMID]; ok {
				_ = database.UpdateVMStatus(vm.VMID, g.Status, vm.VMIP, g.Type)
			}
		}
		return vmStatusSyncedMsg{}
	}
}

func (m GuestsModel) doAction(action string) tea.Cmd {
	if m.cursor >= len(m.guests) {
		return nil
	}
	g := m.guests[m.cursor]
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := client.GuestAction(ctx, g, action)
		return actionDoneMsg{err}
	}
}

func (m GuestsModel) fetchIP(g api.Guest) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ip := client.GuestPrimaryIP(ctx, g.Node, g.Type, g.VMID)
		return guestIPMsg{guest: g, ip: ip}
	}
}

func (m GuestsModel) Update(msg tea.Msg) (GuestsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case guestsLoadedMsg:
		m.guests = msg.guests
		m.loading = false
		m.err = nil
		if m.cursor >= len(m.guests) && len(m.guests) > 0 {
			m.cursor = len(m.guests) - 1
		}
		return m, m.syncVMStatuses(msg.guests)

	case vmStatusSyncedMsg:
		// DB write is done; no UI change needed.
		return m, nil

	case actionDoneMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
			m.notice = ""
		} else {
			m.notice = "command sent"
			m.err = nil
		}
		m.confirm = confirmNone
		return m, m.fetch()

	case guestIPMsg:
		m.fetchingIP = false
		if msg.err != nil || msg.ip == "" {
			m.err = fmt.Errorf("could not get IP — is the guest agent running?")
			return m, nil
		}
		// Persist the IP we just discovered.
		if m.database != nil {
			_ = m.database.UpdateVMStatus(msg.guest.VMID, msg.guest.Status, msg.ip, msg.guest.Type)
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
		return m, nil

	case errMsg:
		m.err = msg.err
		m.loading = false
		return m, nil

	case bulkActionDoneMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = fmt.Sprintf("bulk %s sent to %d guests", msg.action, msg.count)
		}
		m.selected = make(map[int]bool)
		m.bulkConfirm = ""
		return m, m.fetch()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if m.bulkConfirm != "" {
			switch msg.String() {
			case "y", "Y", "enter":
				action := m.bulkConfirm
				m.acting = true
				m.bulkConfirm = ""
				return m, m.doBulkAction(action)
			default:
				m.bulkConfirm = ""
			}
			return m, nil
		}

		if m.confirm != confirmNone {
			switch msg.String() {
			case "y", "Y", "enter":
				action := "stop"
				if m.confirm == confirmReboot {
					action = "reboot"
				}
				m.acting = true
				m.confirm = confirmNone
				return m, m.doAction(action)
			default:
				m.confirm = confirmNone
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
			if m.cursor < len(m.guests)-1 {
				m.cursor++
			}
			m.notice = ""
		case key.Matches(msg, keys.Enter):
			if len(m.guests) > 0 {
				selected := m.guests[m.cursor]
				return m, func() tea.Msg { return guestSelectedMsg{selected} }
			}
		case key.Matches(msg, keys.Start):
			if len(m.guests) > 0 && m.guests[m.cursor].Status == "stopped" {
				m.acting = true
				return m, m.doAction("start")
			}
		case key.Matches(msg, keys.Stop):
			if len(m.guests) > 0 && m.guests[m.cursor].Status == "running" {
				m.confirm = confirmStop
			}
		case key.Matches(msg, keys.Reboot):
			if len(m.guests) > 0 && m.guests[m.cursor].Status == "running" {
				m.confirm = confirmReboot
			}
		case key.Matches(msg, keys.SSH):
			if len(m.guests) > 0 && m.guests[m.cursor].Status == "running" {
				m.fetchingIP = true
				m.err = nil
				m.sshTarget = m.guests[m.cursor]
				return m, m.fetchIP(m.sshTarget)
			}
		case key.Matches(msg, keys.Clone):
			if len(m.guests) > 0 {
				node := m.node
				return m, func() tea.Msg { return openProvisionMsg{node: node} }
			}
		case msg.String() == "z":
			if len(m.guests) > 0 {
				node := m.node
				guests := m.guests
				return m, func() tea.Msg { return openResizeMsg{node: node, guests: guests} }
			}
		case key.Matches(msg, keys.Select):
			if len(m.guests) > 0 {
				m.selected[m.cursor] = !m.selected[m.cursor]
				if m.cursor < len(m.guests)-1 {
					m.cursor++
				}
			}
		case msg.String() == "A" && len(m.selected) > 0:
			m.bulkConfirm = "start"
		case msg.String() == "X" && len(m.selected) > 0:
			m.bulkConfirm = "stop"
		case msg.String() == "B" && len(m.selected) > 0:
			m.bulkConfirm = "reboot"
		case msg.String() == "D" && len(m.selected) > 0:
			m.selected = make(map[int]bool)
			m.notice = "selection cleared"
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			m.notice = ""
			m.err = nil
			return m, m.fetch()
		}
	}
	return m, nil
}

func (m GuestsModel) doBulkAction(action string) tea.Cmd {
	targets := make([]api.Guest, 0, len(m.selected))
	for idx := range m.selected {
		if idx < len(m.guests) {
			targets = append(targets, m.guests[idx])
		}
	}
	client := m.client
	count := len(targets)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var lastErr error
		for _, g := range targets {
			if err := client.GuestAction(ctx, g, action); err != nil {
				lastErr = err
			}
		}
		return bulkActionDoneMsg{action: action, count: count, err: lastErr}
	}
}

func (m GuestsModel) View() string {
	var sb strings.Builder

	breadcrumb := styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render(m.node.Name)
	sb.WriteString(breadcrumb + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading guests...\n")
		return sb.String()
	}

	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		sb.WriteString(styleHelp.Render("  R to retry  b back"))
		return sb.String()
	}

	if len(m.guests) == 0 {
		sb.WriteString(styleDim.Render("  no guests on this node") + "\n")
		sb.WriteString("\n  " + guestHelpStr() + "\n")
		return sb.String()
	}

	header := fmt.Sprintf("  %-6s %-25s %-5s %-10s %7s  %s",
		"ID", "NAME", "TYPE", "STATUS", "CPU", "MEMORY")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 70))) + "\n")

	for i, g := range m.guests {
		cpuStr := "      -"
		memStr := "-"

		if g.Status == "running" {
			pct := g.CPU * 100
			if g.CPUs > 0 {
				pct = g.CPU * 100 / float64(g.CPUs)
			}
			cpuStr = fmt.Sprintf("%6.1f%%", pct)
			if g.MaxMem > 0 {
				memStr = fmt.Sprintf("%s / %s", fmtBytes(g.Mem), fmtBytes(g.MaxMem))
			}
		}

		typeLabel := "vm"
		if g.Type == "lxc" {
			typeLabel = "ct"
		}

		selMark := "  "
		if m.selected[i] {
			selMark = styleRunning.Render("✓ ")
		}

		line := fmt.Sprintf("%s%-6d %-25s %-5s %s %-8s  %s  %s",
			selMark,
			g.VMID,
			truncate(g.Name, 25),
			styleDim.Render(typeLabel),
			statusDot(g.Status),
			g.Status,
			cpuStr,
			memStr,
		)

		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n")

	selCount := len(m.selected)
	switch {
	case m.bulkConfirm == "start":
		sb.WriteString(stylePaused.Render(fmt.Sprintf("  Start %d selected guests? [Y/n]", selCount)) + "\n")
	case m.bulkConfirm == "stop":
		sb.WriteString(styleError.Render(fmt.Sprintf("  Stop %d selected guests? [Y/n]", selCount)) + "\n")
	case m.bulkConfirm == "reboot":
		sb.WriteString(stylePaused.Render(fmt.Sprintf("  Reboot %d selected guests? [Y/n]", selCount)) + "\n")
	case m.confirm == confirmStop && m.cursor < len(m.guests):
		sb.WriteString(styleError.Render(fmt.Sprintf("  Stop %q? [Y/n]", m.guests[m.cursor].Name)) + "\n")
	case m.confirm == confirmReboot && m.cursor < len(m.guests):
		sb.WriteString(stylePaused.Render(fmt.Sprintf("  Reboot %q? [Y/n]", m.guests[m.cursor].Name)) + "\n")
	case m.fetchingIP:
		sb.WriteString("  " + m.spinner.View() + " looking up IP...\n")
	case m.acting:
		sb.WriteString("  " + m.spinner.View() + " sending command...\n")
	case m.notice != "":
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n")
	default:
		sb.WriteString("\n")
	}

	if selCount > 0 {
		sb.WriteString(styleRunning.Render(fmt.Sprintf("  %d selected  ", selCount)) +
			styleDim.Render("A start-all  X stop-all  B reboot-all  D clear") + "\n")
	}
	sb.WriteString("\n  " + guestHelpStr() + "\n")
	return sb.String()
}
