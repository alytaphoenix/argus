package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
)

// ── TrueNAS tab ───────────────────────────────────────────────────────────────

func (m ServicesModel) loadTrueNAS() tea.Cmd {
	client := m.trueNASClient
	return func() tea.Msg {
		if client == nil {
			return trueNASLoadedMsg{err: fmt.Errorf("not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		status, err := client.FetchAll(ctx)
		return trueNASLoadedMsg{status: status, err: err}
	}
}

func (m ServicesModel) updateTrueNAS(msg tea.Msg) (ServicesModel, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case trueNASLoadedMsg:
		m.trueNASLoading = false
		m.trueNASChecked = true
		if msg.err != nil {
			if msg.err.Error() != "not configured" {
				m.notice = "TrueNAS: " + msg.err.Error()
			}
		} else {
			m.trueNASStatus = msg.status
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m ServicesModel) handleTrueNASTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.trueNASCursor > 0 {
			m.trueNASCursor--
		}
	case "down", "j":
		if m.trueNASStatus != nil && m.trueNASCursor < len(m.trueNASStatus.Pools)-1 {
			m.trueNASCursor++
		}
	case "r":
		m.trueNASLoading = true
		return m, m.loadTrueNAS()
	}
	return m, nil
}

func (m ServicesModel) viewTrueNAS(height int) string {
	var b strings.Builder

	if m.trueNASClient == nil {
		b.WriteString(styleDim.Render("  TrueNAS not configured.\n"))
		b.WriteString(styleDim.Render("  Set truenas.url and truenas.api_key in config.toml\n"))
		b.WriteString(styleDim.Render("  API key: TrueNAS UI → System → API Keys → Add\n"))
		return b.String()
	}

	if m.trueNASLoading || !m.trueNASChecked {
		b.WriteString("  " + m.spinner.View() + " loading TrueNAS…\n")
		return b.String()
	}

	if m.trueNASStatus == nil {
		b.WriteString(styleDim.Render("  Failed to load TrueNAS data. Press r to retry.\n"))
		return b.String()
	}

	s := m.trueNASStatus

	sysClr := colorGreen
	if !s.SysInfo.SystemReady {
		sysClr = colorYellow
	}
	readyStr := "ready"
	if !s.SysInfo.SystemReady {
		readyStr = "not ready"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(sysClr).Render(
		fmt.Sprintf("  ● %s  v%s  up %s  (%s)",
			s.SysInfo.Hostname,
			truncate(s.SysInfo.Version, 30),
			api.TrueNASUptimeStr(s.SysInfo.UptimeSeconds),
			readyStr)) + "\n\n")

	if len(s.Alerts) > 0 {
		for _, a := range s.Alerts {
			clr := colorYellow
			if a.Level == "CRITICAL" {
				clr = colorRed
			}
			msg := truncate(a.Formatted, min(m.width-6, 100))
			b.WriteString(lipgloss.NewStyle().Foreground(clr).Render(
				fmt.Sprintf("  ⚠ [%s] %s", a.Level, msg)) + "\n")
		}
		b.WriteString("\n")
	}

	if len(s.Pools) == 0 {
		b.WriteString(styleDim.Render("  No pools found.\n"))
	} else {
		hdr := fmt.Sprintf("  %-4s %-20s %-10s %-12s %-12s  %s\n",
			"●", "Pool", "Status", "Used", "Free", "Scan")
		b.WriteString(styleSubtle.Render(hdr))
		b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 80))) + "\n")

		for i, p := range s.Pools {
			if i >= height-12 {
				b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more pools\n", len(s.Pools)-i)))
				break
			}
			dot := lipgloss.NewStyle().Foreground(colorGreen).Render("●")
			if !p.Healthy {
				dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
			} else if p.Status != "ONLINE" {
				dot = lipgloss.NewStyle().Foreground(colorYellow).Render("●")
			}
			used := fmtBytes(p.Allocated)
			free := fmtBytes(p.Free)
			row := fmt.Sprintf("  %-4s %-20s %-10s %-12s %-12s  %s",
				dot, truncate(p.Name, 20), p.Status, used, free, p.ScanState)
			if i == m.trueNASCursor {
				b.WriteString(styleSelected.Render(row) + "\n")
			} else if !p.Healthy {
				b.WriteString(lipgloss.NewStyle().Foreground(colorRed).Render(row) + "\n")
			} else {
				b.WriteString(row + "\n")
			}
		}
	}

	if len(s.Datasets) > 0 {
		b.WriteString("\n")
		hdr := fmt.Sprintf("  %-30s  %-14s  %s\n", "Dataset", "Used", "Available")
		b.WriteString(styleSubtle.Render(hdr))
		b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 60))) + "\n")
		shown := 0
		for _, ds := range s.Datasets {
			if shown >= 8 {
				break
			}
			b.WriteString(fmt.Sprintf("  %-30s  %-14s  %s\n",
				truncate(ds.Name, 30), fmtBytes(ds.UsedBytes), fmtBytes(ds.AvailBytes)))
			shown++
		}
	}

	if len(s.Services) > 0 {
		b.WriteString("\n")
		var running, stopped []string
		for _, svc := range s.Services {
			if svc.Enable {
				if svc.State == "RUNNING" {
					running = append(running, svc.Service)
				} else {
					stopped = append(stopped, svc.Service)
				}
			}
		}
		if len(running) > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).
				Render("  ● Services: "+strings.Join(running, " · ")) + "\n")
		}
		if len(stopped) > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorRed).
				Render("  ● Stopped:  "+strings.Join(stopped, " · ")) + "\n")
		}
	}

	return b.String()
}

func (m ServicesModel) trueNASHelpString() string {
	return "  ↑/↓ pools · r refresh · ←/→ tab · b back"
}
