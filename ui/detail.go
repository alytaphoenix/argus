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

	"github.com/alytaphoenix/argus/api"
)

type detailTab int

const (
	detailTabStatus detailTab = iota
	detailTabLogs
	detailTabSnapshots
	detailTabBackups
)

type logLinesMsg struct {
	lines []string
	err   error
}

type DetailModel struct {
	client        *api.Client
	saltClient    *api.SaltClient
	guest         api.Guest
	backupStorage string
	loading       bool
	acting        bool        // guest power actions (start/stop/reboot)
	snapDeleting  bool        // snapshot delete in progress
	confirm       confirmKind // pending confirmation (stop/reboot/delete snapshot); see guests.go
	notice        string
	err           error
	width         int
	height        int
	spinner       spinner.Model
	sshUser       string
	sshPassword   string
	fetchingIP    bool

	// log tab
	tab        detailTab
	logLines   []string
	logLoading bool
	logUnit    textinput.Model
	logScroll  int

	// snapshot tab
	snaps        []api.Snapshot
	snapLoading  bool
	snapCursor   int
	snapInput    textinput.Model
	snapCreating bool

	// backup tab
	backups       []api.BackupEntry
	backupLoading bool
}

func NewDetailModel(client *api.Client, saltClient *api.SaltClient, guest api.Guest, sshUser, sshPassword, backupStorage string) DetailModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	logUnitTI := textinput.New()
	logUnitTI.Placeholder = "unit name (blank = all)"
	logUnitTI.CharLimit = 64

	snapTI := textinput.New()
	snapTI.Placeholder = "snapshot name (a-z, 0-9, -)"
	snapTI.CharLimit = 40

	return DetailModel{
		client:        client,
		saltClient:    saltClient,
		guest:         guest,
		backupStorage: backupStorage,
		loading:       true,
		spinner:       s,
		sshUser:       sshUser,
		sshPassword:   sshPassword,
		logUnit:       logUnitTI,
		snapInput:     snapTI,
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

func (m DetailModel) fetchSnapshotsCmd() tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snaps, err := client.Snapshots(ctx, g.Node, g.Type, g.VMID)
		return snapshotsLoadedMsg{snaps: snaps, err: err}
	}
}

func (m DetailModel) createSnapshotCmd(name string) tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		upid, err := client.CreateSnapshot(ctx, g.Node, g.Type, g.VMID, name, "")
		return snapshotCreatedMsg{upid: upid, err: err}
	}
}

func (m DetailModel) deleteSnapshotCmd(snapname string) tea.Cmd {
	g := m.guest
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		upid, err := client.DeleteSnapshot(ctx, g.Node, g.Type, g.VMID, snapname)
		return snapshotDeletedMsg{upid: upid, err: err}
	}
}

func (m DetailModel) fetchBackupsCmd() tea.Cmd {
	g := m.guest
	client := m.client
	storage := m.backupStorage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entries, err := client.VMBackups(ctx, g.Node, storage, g.VMID)
		return backupsLoadedMsg{entries: entries, err: err}
	}
}

// nextDetailTab returns the tab that follows the current one, skipping the
// backups tab when no backup storage is configured.
func (m DetailModel) nextDetailTab() detailTab {
	switch m.tab {
	case detailTabStatus:
		return detailTabLogs
	case detailTabLogs:
		return detailTabSnapshots
	case detailTabSnapshots:
		if m.backupStorage != "" {
			return detailTabBackups
		}
		return detailTabStatus
	case detailTabBackups:
		return detailTabStatus
	}
	return detailTabStatus
}

// prevDetailTab returns the tab that precedes the current one, skipping the
// backups tab when no backup storage is configured.
func (m DetailModel) prevDetailTab() detailTab {
	switch m.tab {
	case detailTabStatus:
		if m.backupStorage != "" {
			return detailTabBackups
		}
		return detailTabSnapshots
	case detailTabLogs:
		return detailTabStatus
	case detailTabSnapshots:
		return detailTabLogs
	case detailTabBackups:
		return detailTabSnapshots
	}
	return detailTabStatus
}

// enterTab switches to the given tab, kicking off a fetch if that tab's
// data hasn't been loaded yet (mirrors the lazy-load behavior the old
// inline "L" cycling handler had for snapshots/backups).
func (m *DetailModel) enterTab(t detailTab) tea.Cmd {
	m.tab = t
	switch t {
	case detailTabSnapshots:
		if len(m.snaps) == 0 && !m.snapLoading {
			m.snapLoading = true
			return m.fetchSnapshotsCmd()
		}
	case detailTabBackups:
		if m.backupStorage != "" && len(m.backups) == 0 && !m.backupLoading {
			m.backupLoading = true
			return m.fetchBackupsCmd()
		}
	}
	return nil
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

	case snapshotsLoadedMsg:
		m.snapLoading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.snaps = msg.snaps
			m.snapCursor = 0
			// A refresh landing while a delete confirmation was armed
			// invalidates the captured cursor — the confirm banner would
			// disappear (View's bounds guard) while the intercept kept
			// swallowing keys with no feedback. Cancel it outright.
			if m.confirm == confirmDeleteSnapshot {
				m.confirm = confirmNone
			}
		}
		return m, nil

	case snapshotCreatedMsg:
		m.snapCreating = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "snapshot created"
			m.snapLoading = true
			return m, m.fetchSnapshotsCmd()
		}
		return m, nil

	case snapshotDeletedMsg:
		m.snapDeleting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "snapshot deleted"
			m.snapLoading = true
			return m, m.fetchSnapshotsCmd()
		}
		return m, nil

	case backupsLoadedMsg:
		m.backupLoading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.backups = msg.entries
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if m.confirm != confirmNone {
			switch msg.String() {
			case "y", "Y", "enter":
				switch m.confirm {
				case confirmStop:
					m.confirm = confirmNone
					m.acting = true
					return m, m.doAction("stop")
				case confirmReboot:
					m.confirm = confirmNone
					m.acting = true
					return m, m.doAction("reboot")
				case confirmDeleteSnapshot:
					m.confirm = confirmNone
					if m.snapCursor < len(m.snaps) {
						snap := m.snaps[m.snapCursor]
						m.snapDeleting = true
						return m, m.deleteSnapshotCmd(snap.Name)
					}
				}
			default:
				m.confirm = confirmNone
			}
			return m, nil
		}

		// Snapshot name input intercepts keys
		if m.tab == detailTabSnapshots && m.snapInput.Focused() {
			switch msg.String() {
			case "esc":
				m.snapInput.Blur()
				m.snapInput.SetValue("")
			case "enter":
				name := strings.TrimSpace(m.snapInput.Value())
				if name != "" {
					m.snapInput.Blur()
					m.snapInput.SetValue("")
					m.snapCreating = true
					return m, m.createSnapshotCmd(name)
				}
			default:
				var cmd tea.Cmd
				m.snapInput, cmd = m.snapInput.Update(msg)
				return m, cmd
			}
		}

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
		case msg.String() == "L" || msg.String() == "right" || msg.String() == "l":
			cmd := m.enterTab(m.nextDetailTab())
			return m, cmd
		case msg.String() == "left" || msg.String() == "h":
			cmd := m.enterTab(m.prevDetailTab())
			return m, cmd
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
		case m.tab == detailTabSnapshots && msg.String() == "n":
			m.snapInput.Focus()
		case m.tab == detailTabSnapshots && msg.String() == "R":
			m.snapLoading = true
			return m, m.fetchSnapshotsCmd()
		case m.tab == detailTabSnapshots && (msg.String() == "up" || msg.String() == "k"):
			if m.snapCursor > 0 {
				m.snapCursor--
			}
		case m.tab == detailTabSnapshots && (msg.String() == "down" || msg.String() == "j"):
			if m.snapCursor < len(m.snaps)-1 {
				m.snapCursor++
			}
		case m.tab == detailTabSnapshots && msg.String() == "d":
			if len(m.snaps) > 0 {
				m.confirm = confirmDeleteSnapshot
			}
		case m.tab == detailTabBackups && msg.String() == "R":
			if m.backupStorage != "" {
				m.backupLoading = true
				return m, m.fetchBackupsCmd()
			}
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
				m.confirm = confirmStop
			}
		case key.Matches(msg, keys.Reboot):
			if m.guest.Status == "running" {
				m.confirm = confirmReboot
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

	breadcrumb := styleTitle.Render("⚡ argus") +
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

	if m.confirm == confirmStop {
		sb.WriteString(styleError.Render(fmt.Sprintf("  Stop %q? [Y/n]", g.Name)) + "\n\n")
	} else if m.confirm == confirmReboot {
		sb.WriteString(stylePaused.Render(fmt.Sprintf("  Reboot %q? [Y/n]", g.Name)) + "\n\n")
	} else if m.confirm == confirmDeleteSnapshot && m.snapCursor < len(m.snaps) {
		sb.WriteString(styleError.Render(fmt.Sprintf("  Delete snapshot %q? [Y/n]", m.snaps[m.snapCursor].Name)) + "\n\n")
	} else if m.fetchingIP {
		sb.WriteString("  " + m.spinner.View() + " looking up IP...\n\n")
	} else if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.acting {
		sb.WriteString("  " + m.spinner.View() + " sending command...\n\n")
	} else if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
	}

	switch m.tab {
	case detailTabLogs:
		sb.WriteString(m.viewLogs())
	case detailTabSnapshots:
		sb.WriteString(m.viewSnapshots())
	case detailTabBackups:
		sb.WriteString(m.viewBackups())
	}

	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 40))) + "\n")
	sb.WriteString("  " + m.tabHelp() + "\n")

	return sb.String()
}

func (m DetailModel) tabHelp() string {
	switch m.tab {
	case detailTabLogs:
		return styleHelp.Render("↑/↓ scroll  f filter unit  R refresh  ←/→ tab  b back  q quit")
	case detailTabSnapshots:
		return styleHelp.Render("↑/↓ select  n new snapshot  d delete  R refresh  ←/→ tab  b back  q quit")
	case detailTabBackups:
		return styleHelp.Render("R refresh  ←/→ tab  b back  q quit")
	default:
		return detailHelpStr() + styleHelp.Render("  ←/→ tab (logs/snaps)")
	}
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

func (m DetailModel) viewSnapshots() string {
	var sb strings.Builder
	sb.WriteString("\n" + styleBold.Render("  Snapshots") + "\n\n")

	if m.snapCreating || m.snapDeleting {
		sb.WriteString("  " + m.spinner.View() + " working…\n")
		return sb.String()
	}

	if m.snapInput.Focused() {
		sb.WriteString("  New snapshot name: " + m.snapInput.View() + "\n")
		sb.WriteString(styleDim.Render("  enter to create, esc to cancel") + "\n")
		return sb.String()
	}

	if m.snapLoading {
		sb.WriteString("  " + m.spinner.View() + " loading snapshots…\n")
		return sb.String()
	}

	if len(m.snaps) == 0 {
		sb.WriteString(styleDim.Render("  No snapshots. Press n to create one.\n"))
		return sb.String()
	}

	for i, s := range m.snaps {
		prefix := "  "
		if i == m.snapCursor {
			prefix = styleBold.Render("▸ ")
		}
		ts := time.Unix(s.Snaptime, 0).Format("2006-01-02 15:04")
		desc := s.Description
		if desc == "" {
			desc = styleDim.Render("(no description)")
		}
		sb.WriteString(fmt.Sprintf("%s%-20s  %s  %s\n", prefix, s.Name, ts, desc))
	}
	return sb.String()
}

func (m DetailModel) viewBackups() string {
	var sb strings.Builder
	sb.WriteString("\n" + styleBold.Render("  Backups") + " " + styleDim.Render("("+m.backupStorage+")") + "\n\n")

	if m.backupLoading {
		sb.WriteString("  " + m.spinner.View() + " loading backups…\n")
		return sb.String()
	}
	if m.backupStorage == "" {
		sb.WriteString(styleDim.Render("  backup_storage not configured. Set [defaults] backup_storage in config.\n"))
		return sb.String()
	}
	if len(m.backups) == 0 {
		sb.WriteString(styleDim.Render("  No backups found in storage '" + m.backupStorage + "'.\n"))
		return sb.String()
	}

	for _, b := range m.backups {
		ts := time.Unix(b.CTime, 0).Format("2006-01-02 15:04")
		size := fmtBytes(b.Size)
		notes := b.Notes
		if notes == "" {
			notes = styleDim.Render("(no notes)")
		}
		volid := b.VolID
		if idx := strings.LastIndex(volid, "/"); idx >= 0 {
			volid = volid[idx+1:]
		}
		sb.WriteString(fmt.Sprintf("  %-40s  %s  %s  %s\n", truncate(volid, 40), ts, size, notes))
	}
	return sb.String()
}
