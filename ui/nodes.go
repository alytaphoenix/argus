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

type NodesModel struct {
	client    *api.Client
	database  *db.DB
	nodes     []api.Node
	cursor    int
	loading   bool
	err       error
	hasSalt   bool
	hasNomad  bool
	width     int
	height    int
	spinner   spinner.Model
	// sparkline cache: node name → []cpu ratio (last 24 points)
	sparkData map[string][]float64
}

func NewNodesModel(client *api.Client, hasSalt, hasNomad bool, database *db.DB) NodesModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return NodesModel{
		client:    client,
		database:  database,
		hasSalt:   hasSalt,
		hasNomad:  hasNomad,
		loading:   true,
		spinner:   s,
		sparkData: make(map[string][]float64),
	}
}

func (m NodesModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetch())
}

func (m NodesModel) recordMetricsCmd(nodes []api.Node) tea.Cmd {
	database := m.database
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		for _, n := range nodes {
			memRatio := float64(0)
			if n.MaxMem > 0 {
				memRatio = float64(n.Mem) / float64(n.MaxMem)
			}
			memUsedMB := n.Mem / 1024 / 1024
			memMaxMB := n.MaxMem / 1024 / 1024
			_ = database.RecordNodeMetrics(n.Name, n.CPU, memRatio, memUsedMB, memMaxMB)
		}
		return nodeMetricsSnapshotMsg{}
	}
}

func (m NodesModel) fetch() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		nodes, err := client.Nodes(ctx)
		if err != nil {
			return errMsg{err}
		}
		return nodesLoadedMsg{nodes}
	}
}

func (m NodesModel) Update(msg tea.Msg) (NodesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case nodesLoadedMsg:
		m.nodes = msg.nodes
		m.loading = false
		m.err = nil
		return m, m.recordMetricsCmd(msg.nodes)

	case nodeMetricsSnapshotMsg:
		// Reload sparklines from DB after recording
		if m.database != nil {
			for _, n := range m.nodes {
				pts, err := m.database.NodeMetricsHistory(n.Name, 24)
				if err != nil {
					continue
				}
				vals := make([]float64, len(pts))
				for i, p := range pts {
					vals[i] = p.CPURatio
				}
				m.sparkData[n.Name] = vals
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
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
		case key.Matches(msg, keys.Down):
			if m.cursor < len(m.nodes)-1 {
				m.cursor++
			}
		case key.Matches(msg, keys.Enter):
			if len(m.nodes) > 0 {
				selected := m.nodes[m.cursor]
				return m, func() tea.Msg { return nodeSelectedMsg{selected} }
			}
		case key.Matches(msg, keys.OpenSalt):
			return m, func() tea.Msg { return openSaltMsg{} }
		case key.Matches(msg, keys.OpenNomad):
			return m, func() tea.Msg { return openNomadMsg{} }
		case key.Matches(msg, keys.Credentials):
			return m, func() tea.Msg { return openCredentialsMsg{} }
		case msg.String() == "p":
			return m, func() tea.Msg { return openProfilesMsg{} }
		case key.Matches(msg, keys.Refresh):
			m.loading = true
			m.err = nil
			return m, m.fetch()
		}
	}
	return m, nil
}

func (m NodesModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") + "  " + styleDim.Render("proxmox homelab") + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " loading nodes...\n")
		return sb.String()
	}

	if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		sb.WriteString(styleHelp.Render("  R to retry"))
		return sb.String()
	}

	if len(m.nodes) == 0 {
		sb.WriteString(styleDim.Render("  no nodes found") + "\n")
		return sb.String()
	}

	const barW = 12
	header := fmt.Sprintf("  %-20s %-8s  %-20s %-24s  %-16s  %s",
		"NODE", "STATUS", "CPU", "MEMORY", "CPU 24H", "UPTIME")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 70))) + "\n")

	for i, n := range m.nodes {
		cpuRatio := n.CPU
		memRatio := float64(0)
		if n.MaxMem > 0 {
			memRatio = float64(n.Mem) / float64(n.MaxMem)
		}

		cpuStr := fmt.Sprintf("%s %5.1f%%", progressBar(cpuRatio, barW), cpuRatio*100)
		memStr := fmt.Sprintf("%s %s/%s", progressBar(memRatio, barW), fmtBytes(n.Mem), fmtBytes(n.MaxMem))
		spark := sparkline(m.sparkData[n.Name], 16)

		line := fmt.Sprintf("  %-20s %s %-6s  %-20s  %-24s  %s  %s",
			truncate(n.Name, 20),
			statusDot(n.Status),
			n.Status,
			cpuStr,
			memStr,
			spark,
			fmtUptime(n.Uptime),
		)

		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n  " + nodeHelpStr(m.hasSalt, m.hasNomad) + "\n")
	return sb.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
