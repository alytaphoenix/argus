package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Tabs ──────────────────────────────────────────────────────────────────────

type secTab int

const (
	secTabCrowdSec secTab = iota
	secTabWazuh
	secTabHistory
	secTabScan
	secTabCVE
	secTabCount
)

var secTabLabels = []string{"CrowdSec", "Wazuh", "History", "Scan", "CVE"}

// ── Model ─────────────────────────────────────────────────────────────────────

// SecurityModel shows CrowdSec bans, Wazuh agent health, event log, and security scans.
type SecurityModel struct {
	width, height int
	tab           secTab

	csClient      *api.CrowdSecClient
	wazuhClient   *api.WazuhClient
	proxmoxClient *api.Client
	database      *db.DB

	// CrowdSec tab
	csDecisions []api.CrowdSecDecision
	csAlerts    []api.CrowdSecAlert
	csBouncers  []api.CrowdSecBouncer
	csCursor    int
	csOnline    bool
	csChecked   bool
	csLoading   bool

	// Wazuh tab
	wazuhAgents   []api.WazuhAgent
	wazuhAlerts   []api.WazuhAlert
	wazuhSummary  api.WazuhSummary
	wazuhCursor   int
	wazuhOnline   bool
	wazuhChecked  bool
	wazuhLoading  bool

	// History tab
	history    []db.SecurityEvent
	histCursor int

	// Scan tab
	scanGuests      []api.Guest
	scanFindings    []api.ScanFinding  // current in-memory results
	scanSavedResult []db.ScanResult    // results from DB
	scanCursor      int
	scanRunning     bool
	scanLoading     bool // loading guests
	scanMode        int  // 0 = all VMs, 1 = custom IP
	scanInput       textinput.Model
	scanEditing     bool
	scanConfirm     bool // confirm remediation dialog
	scanConfirmFinding api.ScanFinding

	// CVE tab
	cveScanner   *api.CVEScanner
	cveTarget    textinput.Model
	cveEditing   bool
	cveFindings  []api.CVEFinding
	cveRunning   bool
	cveCursor    int
	cveErr       error

	// Cross-system: tracked service hosts (for Wazuh/CrowdSec cross-reference)
	serviceHosts map[string]db.ServiceHost

	// App-wide settings (Wazuh thresholds, scan timeout, etc.)
	appSettings db.AppSettings

	spinner spinner.Model
	notice  string
}

func NewSecurityModel(csClient *api.CrowdSecClient, wazuhClient *api.WazuhClient,
	proxmoxClient *api.Client, database *db.DB, appSettings db.AppSettings) SecurityModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Placeholder = "IP or hostname to scan"
	ti.CharLimit = 128

	cveInput := textinput.New()
	cveInput.Placeholder = "image (e.g. nginx:latest) or filesystem path"
	cveInput.CharLimit = 256

	return SecurityModel{
		csClient:      csClient,
		wazuhClient:   wazuhClient,
		proxmoxClient: proxmoxClient,
		database:      database,
		spinner:       sp,
		scanInput:     ti,
		cveScanner:    api.NewCVEScanner(),
		cveTarget:     cveInput,
		appSettings:   appSettings,
	}
}

func (m SecurityModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.loadHistory(),
		m.loadCrowdSec(),
		m.loadWazuh(),
		m.loadScanHistory(),
		m.loadServiceHostsCmd(),
	)
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m SecurityModel) loadCrowdSec() tea.Cmd {
	client := m.csClient
	database := m.database
	return func() tea.Msg {
		if client == nil {
			return crowdSecLoadedMsg{err: fmt.Errorf("not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		decisions, err := client.Decisions(ctx)
		if err != nil {
			return crowdSecLoadedMsg{err: err}
		}
		alerts, _ := client.Alerts(ctx)
		bouncers, _ := client.Bouncers(ctx)

		if database != nil {
			for _, d := range decisions {
				_ = database.RecordSecurityEvent(db.SecurityEvent{
					Source:      "crowdsec",
					EventType:   "ban",
					Severity:    "medium",
					Description: d.Scenario,
					Target:      d.Value,
				})
			}
		}
		return crowdSecLoadedMsg{decisions: decisions, alerts: alerts, bouncers: bouncers}
	}
}

func (m SecurityModel) loadWazuh() tea.Cmd {
	client := m.wazuhClient
	database := m.database
	return func() tea.Msg {
		if client == nil {
			return wazuhLoadedMsg{err: fmt.Errorf("not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		agents, err := client.Agents(ctx)
		if err != nil {
			return wazuhLoadedMsg{err: err}
		}
		alerts, alertErr := client.RecentAlerts(ctx)
		summary := api.SummariseAgents(agents, alerts)

		if database != nil {
			for _, a := range agents {
				if a.Status != "active" {
					_ = database.RecordSecurityEvent(db.SecurityEvent{
						Source:      "wazuh",
						EventType:   "agent_down",
						Severity:    "high",
						Description: fmt.Sprintf("Agent %s (%s) is %s", a.Name, a.IP, a.Status),
						Target:      a.Name,
					})
				}
			}
			// Use configurable thresholds from app_settings
			critLevel := 12
			highLevel := 10
			medLevel := 7
			if m.appSettings.WazuhCriticalLevel > 0 {
				critLevel = m.appSettings.WazuhCriticalLevel
			}
			if m.appSettings.WazuhHighLevel > 0 {
				highLevel = m.appSettings.WazuhHighLevel
			}
			if m.appSettings.WazuhMediumLevel > 0 {
				medLevel = m.appSettings.WazuhMediumLevel
			}
			for _, al := range alerts {
				sev := "low"
				switch {
				case al.Rule.Level >= critLevel:
					sev = "critical"
				case al.Rule.Level >= highLevel:
					sev = "high"
				case al.Rule.Level >= medLevel:
					sev = "medium"
				}
				_ = database.RecordSecurityEvent(db.SecurityEvent{
					Source:      "wazuh",
					EventType:   "alert",
					Severity:    sev,
					Description: al.Rule.Description,
					Target:      al.AgentName,
				})
			}
		}
		return wazuhLoadedMsg{agents: agents, alerts: alerts, summary: summary, alertErr: alertErr}
	}
}

func (m SecurityModel) loadHistory() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return secHistoryLoadedMsg{}
		}
		events, err := database.RecentSecurityEvents(200)
		return secHistoryLoadedMsg{events: events, err: err}
	}
}

func (m SecurityModel) loadScanHistory() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return secScanHistoryMsg{}
		}
		results, err := database.RecentScanResults(500)
		return secScanHistoryMsg{results: results, err: err}
	}
}

func (m SecurityModel) loadServiceHostsCmd() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return secServiceHostsLoadedMsg{hosts: make(map[string]db.ServiceHost)}
		}
		hosts, err := database.ServiceHosts()
		return secServiceHostsLoadedMsg{hosts: hosts, err: err}
	}
}

// updateDiscoveredPortsCmd persists discovered port findings to service_hosts for known hosts.
func (m SecurityModel) updateDiscoveredPortsCmd(findings []api.ScanFinding) tea.Cmd {
	database := m.database
	if database == nil {
		return nil
	}
	// Group port findings by target
	byTarget := make(map[string][]string)
	for _, f := range findings {
		if f.ScanType == "port" {
			byTarget[f.Target] = append(byTarget[f.Target], f.Check)
		}
	}
	hosts := m.serviceHosts
	return func() tea.Msg {
		for target, checks := range byTarget {
			if _, ok := hosts[target]; ok {
				ports := strings.Join(checks, ",")
				_ = database.UpdateDiscoveredPorts(target, ports)
			}
		}
		return nil
	}
}

func (m SecurityModel) unbanCmd(decisionID int) tea.Cmd {
	client := m.csClient
	return func() tea.Msg {
		if client == nil {
			return secActionDoneMsg{err: fmt.Errorf("CrowdSec not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return secActionDoneMsg{err: client.DeleteDecision(ctx, decisionID)}
	}
}

func (m SecurityModel) loadGuestsCmd() tea.Cmd {
	client := m.proxmoxClient
	return func() tea.Msg {
		if client == nil {
			return scanGuestsLoadedMsg{err: fmt.Errorf("Proxmox not connected")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		nodes, err := client.Nodes(ctx)
		if err != nil {
			return scanGuestsLoadedMsg{err: err}
		}
		var all []api.Guest
		for _, n := range nodes {
			gs, err := client.Guests(ctx, n.Name)
			if err != nil {
				continue
			}
			all = append(all, gs...)
		}
		return scanGuestsLoadedMsg{guests: all}
	}
}

func (m SecurityModel) runScanCmd(targets []string, guests []api.Guest) tea.Cmd {
	client := m.proxmoxClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		var all []api.ScanFinding

		// VM firewall audit (requires Proxmox client)
		if client != nil && len(guests) > 0 {
			fw := api.VMFirewallScan(ctx, client, guests)
			all = append(all, fw...)
		}

		// Port scan each target IP
		for _, t := range targets {
			if t == "" {
				continue
			}
			findings := api.PortScan(ctx, t, api.CommonPorts)
			all = append(all, findings...)
		}

		// Sort: highest severity first
		sort.Slice(all, func(i, j int) bool {
			si := api.ScanSeverityOrder(all[i].Severity)
			sj := api.ScanSeverityOrder(all[j].Severity)
			if si != sj {
				return si > sj
			}
			return all[i].Target < all[j].Target
		})

		return scanCompleteMsg{findings: all}
	}
}

func (m SecurityModel) remediateCmd(f api.ScanFinding) tea.Cmd {
	client := m.proxmoxClient
	return func() tea.Msg {
		switch f.RemAction {
		case "enable_vm_firewall":
			if client == nil {
				return scanRemediatedMsg{finding: f, err: fmt.Errorf("Proxmox client not available")}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := client.SetVMFirewall(ctx, f.Node, "qemu", f.VMID, true)
			if err != nil {
				// Try LXC type if qemu failed
				err = client.SetVMFirewall(ctx, f.Node, "lxc", f.VMID, true)
			}
			return scanRemediatedMsg{finding: f, err: err}
		default:
			return scanRemediatedMsg{finding: f, err: fmt.Errorf("no automated remediation available")}
		}
	}
}

func (m SecurityModel) runCVEScanCmd(target string) tea.Cmd {
	scanner := m.cveScanner
	return func() tea.Msg {
		if scanner == nil || !scanner.Available() {
			return cveScanDoneMsg{err: fmt.Errorf("no CVE scanner found — install trivy or grype")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var findings []api.CVEFinding
		var err error
		if strings.HasPrefix(target, "/") {
			findings, err = scanner.ScanFS(ctx, target)
		} else {
			findings, err = scanner.ScanImage(ctx, target)
		}
		return cveScanDoneMsg{findings: findings, err: err}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m SecurityModel) Update(msg tea.Msg) (SecurityModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case crowdSecLoadedMsg:
		m.csLoading = false
		m.csChecked = true
		if msg.err != nil {
			m.csOnline = false
			if msg.err.Error() != "not configured" {
				m.notice = "CrowdSec: " + msg.err.Error()
			}
		} else {
			m.csOnline = true
			m.csDecisions = msg.decisions
			m.csAlerts = msg.alerts
			m.csBouncers = msg.bouncers
		}
		return m, nil

	case wazuhLoadedMsg:
		m.wazuhLoading = false
		m.wazuhChecked = true
		if msg.err != nil {
			m.wazuhOnline = false
			if msg.err.Error() != "not configured" {
				m.notice = "Wazuh: " + msg.err.Error()
			}
		} else {
			m.wazuhOnline = true
			m.wazuhAgents = msg.agents
			m.wazuhAlerts = msg.alerts
			m.wazuhSummary = msg.summary
			if msg.alertErr != nil {
				m.notice = "Wazuh alerts unavailable: " + msg.alertErr.Error()
			}
		}
		return m, nil

	case secHistoryLoadedMsg:
		if msg.err != nil {
			m.notice = "history: " + msg.err.Error()
		} else {
			m.history = msg.events
		}
		return m, nil

	case secScanHistoryMsg:
		if msg.err != nil {
			m.notice = "scan history: " + msg.err.Error()
		} else {
			m.scanSavedResult = msg.results
		}
		return m, nil

	case secServiceHostsLoadedMsg:
		if msg.err == nil && msg.hosts != nil {
			m.serviceHosts = msg.hosts
		}
		return m, nil

	case secActionDoneMsg:
		if msg.err != nil {
			m.notice = "error: " + msg.err.Error()
		} else {
			m.notice = "action applied"
		}
		return m, tea.Batch(m.loadCrowdSec(), m.loadHistory())

	case scanGuestsLoadedMsg:
		m.scanLoading = false
		if msg.err != nil {
			m.notice = "scan: " + msg.err.Error()
		} else {
			m.scanGuests = msg.guests
			// Extract IPs for port scan
			var targets []string
			for _, g := range msg.guests {
				if g.Status == "running" {
					// Guest IP is not in the basic struct; we'll rely on VM firewall scan
					// Port scan targets can be added manually
					_ = g
				}
			}
			m.notice = fmt.Sprintf("loaded %d VMs; running scan…", len(msg.guests))
			m.scanRunning = true
			return m, m.runScanCmd(targets, msg.guests)
		}
		return m, nil

	case scanCompleteMsg:
		m.scanRunning = false
		if msg.err != nil {
			m.notice = "scan error: " + msg.err.Error()
		} else {
			m.scanFindings = msg.findings
			m.scanCursor = 0
			count := len(msg.findings)
			if count == 0 {
				m.notice = "scan complete: no findings"
			} else {
				m.notice = fmt.Sprintf("scan complete: %d finding(s)", count)
			}
			// Persist to DB
			if m.database != nil {
				var results []db.ScanResult
				for _, f := range msg.findings {
					results = append(results, db.ScanResult{
						Target:      f.Target,
						Node:        f.Node,
						VMID:        f.VMID,
						ScanType:    f.ScanType,
						CheckName:   f.Check,
						Status:      f.Status,
						Severity:    f.Severity,
						Detail:      f.Detail,
						Remediation: f.Remediation,
						RemAction:   f.RemAction,
					})
				}
				if len(results) > 0 {
					_ = m.database.SaveScanResults(results)
				}
				m.scanSavedResult, _ = m.database.RecentScanResults(500)
			}
			// Cross-system: update discovered_ports for any known service hosts
			if updateCmd := m.updateDiscoveredPortsCmd(msg.findings); updateCmd != nil {
				return m, updateCmd
			}
		}
		return m, nil

	case scanRemediatedMsg:
		m.scanConfirm = false
		if msg.err != nil {
			m.notice = "remediation error: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("✓ remediated: %s on %s", msg.finding.Check, msg.finding.Target)
			// Mark remediated in DB and re-run scan to refresh
			if m.database != nil {
				// Find matching result in DB and mark it
				for _, sr := range m.scanSavedResult {
					if sr.Target == msg.finding.Target && sr.CheckName == msg.finding.Check {
						_ = m.database.MarkScanResultRemediated(sr.ID)
					}
				}
			}
			// Remove from in-memory findings
			var remaining []api.ScanFinding
			for _, f := range m.scanFindings {
				if !(f.Target == msg.finding.Target && f.Check == msg.finding.Check) {
					remaining = append(remaining, f)
				}
			}
			m.scanFindings = remaining
			if m.scanCursor >= len(m.scanFindings) && m.scanCursor > 0 {
				m.scanCursor--
			}
		}
		return m, nil

	case cveScanDoneMsg:
		m.cveRunning = false
		m.cveErr = msg.err
		m.cveFindings = msg.findings
		m.cveCursor = 0
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m SecurityModel) handleKey(msg tea.KeyMsg) (SecurityModel, tea.Cmd) {
	key := msg.String()

	// Remediation confirmation dialog
	if m.scanConfirm {
		switch key {
		case "y", "enter":
			f := m.scanConfirmFinding
			return m, m.remediateCmd(f)
		case "n", "esc":
			m.scanConfirm = false
			m.notice = "remediation cancelled"
		}
		return m, nil
	}

	// Scan input
	if m.scanEditing {
		switch key {
		case "enter":
			target := strings.TrimSpace(m.scanInput.Value())
			m.scanEditing = false
			if target != "" {
				m.scanRunning = true
				m.notice = "scanning " + target + "…"
				return m, m.runScanCmd([]string{target}, nil)
			}
		case "esc":
			m.scanEditing = false
		default:
			var cmd tea.Cmd
			m.scanInput, cmd = m.scanInput.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	// CVE target input
	if m.cveEditing {
		switch key {
		case "enter":
			target := strings.TrimSpace(m.cveTarget.Value())
			m.cveEditing = false
			if target != "" && !m.cveRunning {
				m.cveRunning = true
				m.cveFindings = nil
				m.cveErr = nil
				return m, m.runCVEScanCmd(target)
			}
		case "esc":
			m.cveEditing = false
			m.cveTarget.Blur()
		default:
			var cmd tea.Cmd
			m.cveTarget, cmd = m.cveTarget.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
		}
		return m, nil
	case "right", "l":
		if m.tab < secTabCount-1 {
			m.tab++
			m.notice = ""
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "R":
		m.csLoading = true
		m.wazuhLoading = true
		m.notice = ""
		return m, tea.Batch(m.loadCrowdSec(), m.loadWazuh(), m.loadHistory())
	}

	switch m.tab {
	case secTabCrowdSec:
		return m.handleCSTab(key)
	case secTabWazuh:
		return m.handleWazuhTab(key)
	case secTabHistory:
		return m.handleHistoryTab(key)
	case secTabScan:
		return m.handleScanTab(key)
	case secTabCVE:
		return m.handleCVETab(key)
	}
	return m, nil
}

func (m SecurityModel) handleCVETab(key string) (SecurityModel, tea.Cmd) {
	switch key {
	case "s", "enter":
		m.cveEditing = true
		m.cveTarget.Focus()
	case "up", "k":
		if m.cveCursor > 0 {
			m.cveCursor--
		}
	case "down", "j":
		if m.cveCursor < len(m.cveFindings)-1 {
			m.cveCursor++
		}
	}
	return m, nil
}

func (m SecurityModel) handleCSTab(key string) (SecurityModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.csCursor > 0 {
			m.csCursor--
		}
	case "down", "j":
		if m.csCursor < len(m.csDecisions)-1 {
			m.csCursor++
		}
	case "d", "u":
		if m.csCursor < len(m.csDecisions) {
			id := m.csDecisions[m.csCursor].ID
			return m, m.unbanCmd(id)
		}
	case "r":
		m.csLoading = true
		return m, m.loadCrowdSec()
	}
	return m, nil
}

func (m SecurityModel) handleWazuhTab(key string) (SecurityModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.wazuhCursor > 0 {
			m.wazuhCursor--
		}
	case "down", "j":
		if m.wazuhCursor < len(m.wazuhAgents)-1 {
			m.wazuhCursor++
		}
	case "r":
		m.wazuhLoading = true
		return m, m.loadWazuh()
	}
	return m, nil
}

func (m SecurityModel) handleHistoryTab(key string) (SecurityModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.histCursor > 0 {
			m.histCursor--
		}
	case "down", "j":
		if m.histCursor < len(m.history)-1 {
			m.histCursor++
		}
	case "r":
		return m, m.loadHistory()
	}
	return m, nil
}

func (m SecurityModel) handleScanTab(key string) (SecurityModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.scanCursor > 0 {
			m.scanCursor--
		}
	case "down", "j":
		if m.scanCursor < len(m.scanFindings)-1 {
			m.scanCursor++
		}
	case "r":
		// Run scan: load guests first if using all-VMs mode
		if m.scanMode == 0 {
			m.scanLoading = true
			m.notice = "loading VMs…"
			return m, m.loadGuestsCmd()
		} else {
			// Custom mode — open text input
			m.scanInput.SetValue("")
			m.scanInput.Focus()
			m.scanEditing = true
			return m, textinput.Blink
		}
	case "t":
		// Toggle scan mode
		m.scanMode = (m.scanMode + 1) % 2
		m.notice = ""
	case "i":
		// Enter custom IP
		m.scanInput.SetValue("")
		m.scanInput.Focus()
		m.scanEditing = true
		m.scanMode = 1
		return m, textinput.Blink
	case "enter":
		// Apply remediation for selected finding
		if m.scanCursor < len(m.scanFindings) {
			f := m.scanFindings[m.scanCursor]
			if f.RemAction != "" {
				m.scanConfirm = true
				m.scanConfirmFinding = f
			} else {
				m.notice = "no automated remediation for this finding"
			}
		}
	case "x":
		// Clear scan results
		m.scanFindings = nil
		m.scanCursor = 0
		if m.database != nil {
			_ = m.database.ClearScanResults()
			m.scanSavedResult = nil
		}
		m.notice = "scan results cleared"
	}
	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m SecurityModel) View() string {
	var b strings.Builder

	b.WriteString(m.renderStatusHeader() + "\n")
	b.WriteString(m.renderTabBar() + "\n\n")

	contentH := m.height - 7
	if contentH < 4 {
		contentH = 4
	}

	switch m.tab {
	case secTabCrowdSec:
		b.WriteString(m.viewCrowdSec(contentH))
	case secTabWazuh:
		b.WriteString(m.viewWazuh(contentH))
	case secTabHistory:
		b.WriteString(m.viewHistory(contentH))
	case secTabScan:
		b.WriteString(m.viewScan(contentH))
	case secTabCVE:
		b.WriteString(m.viewCVE(contentH))
	}

	if m.notice != "" {
		b.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m SecurityModel) renderStatusHeader() string {
	var parts []string

	// CrowdSec
	if m.csClient == nil {
		parts = append(parts, styleDim.Render("CrowdSec: not configured"))
	} else if !m.csChecked {
		parts = append(parts, styleDim.Render("CrowdSec: "+m.spinner.View()))
	} else if m.csOnline {
		bans := len(m.csDecisions)
		txt := fmt.Sprintf("CrowdSec: %d ban(s)  %d bouncer(s)", bans, len(m.csBouncers))
		clr := colorGreen
		if bans > 0 {
			clr = colorYellow
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(clr).Render("● "+txt))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorRed).Render("● CrowdSec: offline"))
	}

	// Wazuh
	if m.wazuhClient == nil {
		parts = append(parts, styleDim.Render("Wazuh: not configured"))
	} else if !m.wazuhChecked {
		parts = append(parts, styleDim.Render("Wazuh: "+m.spinner.View()))
	} else if m.wazuhOnline {
		s := m.wazuhSummary
		txt := fmt.Sprintf("Wazuh: %d/%d active  %d alert(s)/24h",
			s.ActiveAgents, s.TotalAgents, s.AlertsLast24h)
		clr := colorGreen
		if s.CriticalAlerts > 0 {
			clr = colorRed
		} else if s.AlertsLast24h > 0 || s.DisconnectedAgents > 0 {
			clr = colorYellow
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(clr).Render("● "+txt))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorRed).Render("● Wazuh: offline"))
	}

	// Scan summary
	critical := 0
	for _, f := range m.scanFindings {
		if f.Severity == "critical" || f.Severity == "high" {
			critical++
		}
	}
	if len(m.scanFindings) > 0 {
		txt := fmt.Sprintf("Scan: %d finding(s)", len(m.scanFindings))
		clr := colorGreen
		if critical > 0 {
			clr = colorRed
			txt += fmt.Sprintf(" (%d high/critical)", critical)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(clr).Render("● "+txt))
	}

	return "  " + strings.Join(parts, "   ")
}

func (m SecurityModel) renderTabBar() string {
	var parts []string
	for i, label := range secTabLabels {
		l := " " + label + " "
		if secTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch")
}

func (m SecurityModel) viewCrowdSec(height int) string {
	var b strings.Builder

	if m.csClient == nil {
		b.WriteString(styleDim.Render("  CrowdSec not configured.\n"))
		b.WriteString(styleDim.Render("  Set crowdsec.url and crowdsec.api_key in config.toml\n"))
		b.WriteString(styleDim.Render("  Get the API key: cscli bouncers add ratataskr\n"))
		return b.String()
	}

	if m.csLoading {
		b.WriteString("  " + m.spinner.View() + " loading…\n")
		return b.String()
	}

	// Bouncers summary
	if len(m.csBouncers) > 0 {
		var names []string
		for _, bo := range m.csBouncers {
			s := bo.Name
			if bo.IsDisabled {
				s = styleDim.Render(s + " (disabled)")
			}
			names = append(names, s)
		}
		b.WriteString(styleDim.Render("  Bouncers: ") + strings.Join(names, ", ") + "\n\n")
	}

	if len(m.csDecisions) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ No active bans\n"))
	} else {
		hdr := fmt.Sprintf("  %-6s %-18s %-36s %-10s  %s\n", "ID", "Value", "Scenario", "Duration", "Origin")
		b.WriteString(styleSubtle.Render(hdr))
		b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 95))) + "\n")
		for i, d := range m.csDecisions {
			if i >= height-6 {
				b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more\n", len(m.csDecisions)-i)))
				break
			}
			row := fmt.Sprintf("  %-6d %-18s %-36s %-10s  %s\n",
				d.ID, truncate(d.Value, 18), truncate(d.Scenario, 36),
				truncate(d.Duration, 10), d.Origin)
			if i == m.csCursor {
				b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
			} else {
				b.WriteString(severityColor(d.Type).Render(strings.TrimRight(row, "\n")) + "\n")
			}
		}
	}

	if len(m.csAlerts) > 0 {
		b.WriteString(fmt.Sprintf("\n  Recent alerts: %d\n", len(m.csAlerts)))
		shown := 3
		if len(m.csAlerts) < shown {
			shown = len(m.csAlerts)
		}
		for _, al := range m.csAlerts[:shown] {
			b.WriteString(fmt.Sprintf("  • [%s] %s — %d event(s)\n",
				al.Source.IP, truncate(al.Scenario, 50), al.EventsCount))
		}
	}
	return b.String()
}

func (m SecurityModel) viewWazuh(height int) string {
	var b strings.Builder

	if m.wazuhClient == nil {
		b.WriteString(styleDim.Render("  Wazuh not configured.\n"))
		b.WriteString(styleDim.Render("  Set wazuh.url, wazuh.username, wazuh.password in config.toml\n"))
		return b.String()
	}

	if m.wazuhLoading {
		b.WriteString("  " + m.spinner.View() + " loading…\n")
		return b.String()
	}

	if len(m.wazuhAgents) == 0 {
		b.WriteString(styleDim.Render("  No agents found. Verify Wazuh connectivity.\n"))
		return b.String()
	}

	hdr := fmt.Sprintf("  %-3s  %-22s %-16s %-10s %-14s  %-16s  %s\n",
		"●", "Agent Name", "IP", "Status", "Managed", "OS", "Last Seen")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 100))) + "\n")

	for i, a := range m.wazuhAgents {
		if i >= height-4 {
			b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more\n", len(m.wazuhAgents)-i)))
			break
		}
		dot := lipgloss.NewStyle().Foreground(colorGreen).Render("●")
		if a.Status != "active" {
			dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
		}
		osStr := a.OS.Platform
		if a.OS.Name != "" {
			osStr = truncate(a.OS.Name, 14)
		}
		// Cross-reference with service_hosts
		managedBadge := styleDim.Render("—")
		if svcHost, ok := m.serviceHosts[a.IP]; ok {
			switch svcHost.RegistrationType {
			case "managed":
				managedBadge = lipgloss.NewStyle().Foreground(colorGreen).Render("[M]")
			case "unmanaged":
				managedBadge = styleDim.Render("[U]")
			default:
				managedBadge = lipgloss.NewStyle().Foreground(colorYellow).Render("[?]")
			}
		}
		row := fmt.Sprintf("  %-3s  %-22s %-16s %-10s %-14s  %-16s  %s\n",
			dot, truncate(a.Name, 22), truncate(a.IP, 16),
			a.Status, managedBadge, osStr, truncate(a.LastKeepAlive, 20))
		if i == m.wazuhCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(strings.TrimRight(row, "\n") + "\n")
		}
	}

	if len(m.wazuhAlerts) > 0 {
		b.WriteString(fmt.Sprintf("\n  Recent alerts (24h): %d total", len(m.wazuhAlerts)))
		if m.wazuhSummary.CriticalAlerts > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorRed).
				Render(fmt.Sprintf("  ⚠ %d critical", m.wazuhSummary.CriticalAlerts)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m SecurityModel) viewHistory(height int) string {
	var b strings.Builder

	if len(m.history) == 0 {
		b.WriteString(styleDim.Render("  No security events recorded yet.\n"))
		return b.String()
	}

	hdr := fmt.Sprintf("  %-10s %-10s %-10s %-20s  %s\n",
		"When", "Source", "Severity", "Target", "Description")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 95))) + "\n")

	for i, ev := range m.history {
		if i >= height-2 {
			break
		}
		when := fmtAge(ev.RecordedAt)
		sevStyle := severityStyle(ev.Severity)
		row := fmt.Sprintf("  %-10s %-10s %-10s %-20s  %s\n",
			when, ev.Source, ev.Severity, truncate(ev.Target, 20), truncate(ev.Description, 50))
		if i == m.histCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(sevStyle.Render(strings.TrimRight(row, "\n")) + "\n")
		}
	}
	return b.String()
}

func (m SecurityModel) viewScan(height int) string {
	var b strings.Builder

	// Mode bar
	modeAll := " All VMs (Proxmox) "
	modeCustom := " Custom target "
	if m.scanMode == 0 {
		b.WriteString(styleTabActive.Render("▸"+modeAll) + styleDim.Render("│") + styleTabInactive.Render(" "+modeCustom))
	} else {
		b.WriteString(styleTabInactive.Render(" "+modeAll) + styleDim.Render("│") + styleTabActive.Render("▸"+modeCustom))
	}
	b.WriteString(styleDim.Render("  t=toggle mode") + "\n\n")

	// Input field (custom mode)
	if m.scanEditing {
		b.WriteString("  Target: " + m.scanInput.View() + "\n")
		b.WriteString(styleDim.Render("  enter=scan  esc=cancel") + "\n\n")
	}

	// Confirmation dialog
	if m.scanConfirm {
		f := m.scanConfirmFinding
		b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(fmt.Sprintf(
			"  Remediate: %s on %s\n  Action: %s\n  Press y/enter to apply, n/esc to cancel.",
			f.Check, f.Target, f.RemAction)) + "\n\n")
		return b.String()
	}

	// Running indicator
	if m.scanRunning || m.scanLoading {
		b.WriteString("  " + m.spinner.View() + " scanning…\n")
		return b.String()
	}

	if len(m.scanFindings) == 0 {
		b.WriteString(styleDim.Render("  No scan results. Press r to run a scan.\n"))
		if m.proxmoxClient == nil {
			b.WriteString(styleDim.Render("  Proxmox client not available — VM firewall check disabled.\n"))
		}
		// Show historical results from DB if available
		if len(m.scanSavedResult) > 0 {
			b.WriteString(styleDim.Render(fmt.Sprintf("  Last scan: %d saved result(s) — x to clear.\n",
				len(m.scanSavedResult))))
		}
		return b.String()
	}

	hdr := fmt.Sprintf("  %-10s %-20s %-16s %-10s  %s\n",
		"STATUS", "TARGET", "CHECK", "SEVERITY", "DETAIL")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 95))) + "\n")

	tableH := height - 8
	if tableH < 2 {
		tableH = 2
	}

	for i, f := range m.scanFindings {
		if i >= tableH {
			b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more\n", len(m.scanFindings)-i)))
			break
		}
		statusStr := strings.ToUpper(f.Status)
		row := fmt.Sprintf("  %-10s %-20s %-16s %-10s  %s",
			statusStr,
			truncate(f.Target, 20),
			truncate(f.Check, 16),
			f.Severity,
			truncate(f.Detail, min(m.width-72, 60)))
		if i == m.scanCursor {
			b.WriteString(styleSelected.Render(row) + "\n")
		} else {
			b.WriteString(scanSevStyle(f.Severity).Render(row) + "\n")
		}
	}

	// Detail panel for selected finding
	if m.scanCursor < len(m.scanFindings) {
		f := m.scanFindings[m.scanCursor]
		b.WriteString("\n")
		b.WriteString(styleDim.Render("  ─── Selected finding ───") + "\n")
		b.WriteString(fmt.Sprintf("  %s\n", f.Detail))
		if f.Remediation != "" {
			b.WriteString(styleDim.Render("  Remediation: ") + f.Remediation + "\n")
		}
		if f.RemAction != "" {
			b.WriteString(styleRunning.Render("  enter=apply automated remediation") + "\n")
		}
	}

	return b.String()
}

func (m SecurityModel) viewCVE(height int) string {
	var b strings.Builder

	available := m.cveScanner != nil && m.cveScanner.Available()
	if !available {
		b.WriteString(styleDim.Render("\n  No CVE scanner found in PATH.\n"))
		b.WriteString(styleDim.Render("  Install trivy: https://aquasecurity.github.io/trivy\n"))
		b.WriteString(styleDim.Render("  or grype:  https://github.com/anchore/grype\n"))
		return b.String()
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("  Scanner: %s\n", m.cveScanner.Binary())))

	b.WriteString(styleDim.Render("  Target: ") + m.cveTarget.View() + "\n")
	if !m.cveEditing {
		b.WriteString(styleDim.Render("  (press s to set target, enter to scan)\n"))
	}
	b.WriteString("\n")

	switch {
	case m.cveRunning:
		b.WriteString("  " + m.spinner.View() + styleDim.Render(" scanning…") + "\n")
	case m.cveErr != nil:
		b.WriteString(styleError.Render("  error: "+m.cveErr.Error()) + "\n")
	case len(m.cveFindings) == 0 && !m.cveRunning:
		b.WriteString(styleDim.Render("  No findings yet. Press s/enter to scan.\n"))
	default:
		counts := map[string]int{"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0}
		for _, f := range m.cveFindings {
			counts[strings.ToUpper(f.Severity)]++
		}
		summary := fmt.Sprintf("  Found %d vulnerabilities — CRITICAL:%d  HIGH:%d  MEDIUM:%d  LOW:%d",
			len(m.cveFindings), counts["CRITICAL"], counts["HIGH"], counts["MEDIUM"], counts["LOW"])
		b.WriteString(styleBold.Render(summary) + "\n\n")

		hdr := fmt.Sprintf("  %-16s %-12s %-10s %-20s %-12s  %s",
			"CVE ID", "Severity", "Package", "Installed", "Fixed In", "Title")
		b.WriteString(styleDim.Render(hdr) + "\n")
		b.WriteString(styleDim.Render("  "+strings.Repeat("─", min(m.width-4, 100))) + "\n")

		visible := height - 10
		if visible < 4 {
			visible = 4
		}
		for i, f := range m.cveFindings {
			if i >= visible {
				b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more", len(m.cveFindings)-visible)) + "\n")
				break
			}
			sevStyle := scanSevStyle(strings.ToLower(f.Severity))
			row := fmt.Sprintf("  %-16s %-12s %-10s %-20s %-12s  %s",
				truncate(f.ID, 16),
				sevStyle.Render(truncate(f.Severity, 12)),
				truncate(f.PkgName, 10),
				truncate(f.Version, 20),
				truncate(f.FixedIn, 12),
				truncate(f.Title, m.width-80))
			if i == m.cveCursor {
				b.WriteString(styleSelected.Render(row) + "\n")
			} else {
				b.WriteString(row + "\n")
			}
		}
	}
	return b.String()
}

func (m SecurityModel) helpStr() string {
	switch m.tab {
	case secTabCrowdSec:
		return "  ↑/↓ navigate · d/u unban · r refresh · R reload all · ←/→ tab · b back"
	case secTabWazuh:
		return "  ↑/↓ navigate · r refresh · R reload all · ←/→ tab · b back"
	case secTabHistory:
		return "  ↑/↓ navigate · r reload · ←/→ tab · b back"
	case secTabScan:
		return "  ↑/↓ navigate · r run scan · i custom target · t toggle mode · enter remediate · x clear · b back"
	case secTabCVE:
		return "  s/enter set target · ↑/↓ navigate · ←/→ tab · b back"
	}
	return "  ←/→ tab · b back"
}

// ── Style helpers ─────────────────────────────────────────────────────────────

func severityColor(t string) lipgloss.Style {
	switch strings.ToLower(t) {
	case "ban":
		return lipgloss.NewStyle().Foreground(colorRed)
	case "captcha", "throttle":
		return lipgloss.NewStyle().Foreground(colorYellow)
	default:
		return lipgloss.NewStyle()
	}
}

func severityStyle(sev string) lipgloss.Style {
	switch sev {
	case "critical":
		return lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	case "high":
		return lipgloss.NewStyle().Foreground(colorRed)
	case "medium":
		return lipgloss.NewStyle().Foreground(colorYellow)
	default:
		return styleDim
	}
}

func scanSevStyle(sev string) lipgloss.Style {
	switch sev {
	case "critical":
		return lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	case "high":
		return lipgloss.NewStyle().Foreground(colorRed)
	case "medium":
		return lipgloss.NewStyle().Foreground(colorYellow)
	case "low":
		return lipgloss.NewStyle().Foreground(colorWhite)
	default:
		return styleDim
	}
}
