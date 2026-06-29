package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Services tab ──────────────────────────────────────────────────────────────

func (m ServicesModel) loadAll() tea.Cmd {
	return func() tea.Msg {
		if m.database == nil {
			return servicesLoadedMsg{}
		}
		services, err := m.database.Services()
		if err != nil {
			return servicesLoadedMsg{err: err}
		}
		checks, err := m.database.LatestHealthChecks()
		if err != nil {
			return servicesLoadedMsg{services: services, err: err}
		}
		certs, err := m.database.Certificates()
		if err != nil {
			return servicesLoadedMsg{services: services, checks: checks, err: err}
		}
		uptimes := make(map[int64]float64, len(services))
		for _, s := range services {
			uptimes[s.ID] = m.database.ServiceUptimePct(s.ID)
		}
		return servicesLoadedMsg{services: services, checks: checks, certs: certs, uptimes: uptimes}
	}
}

func (m ServicesModel) loadServiceHosts() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return svcHostsLoadedMsg{hosts: make(map[string]db.ServiceHost)}
		}
		hosts, err := database.ServiceHosts()
		return svcHostsLoadedMsg{hosts: hosts, err: err}
	}
}

func (m ServicesModel) loadBannedHosts() tea.Cmd {
	database := m.database
	lookback := m.appSettings.BanLookbackHours
	if lookback <= 0 {
		lookback = 48
	}
	return func() tea.Msg {
		if database == nil {
			return svcBannedHostsLoadedMsg{banned: make(map[string]bool)}
		}
		hosts, err := database.RecentBannedHosts(lookback)
		if err != nil {
			return svcBannedHostsLoadedMsg{banned: make(map[string]bool)}
		}
		banned := make(map[string]bool, len(hosts))
		for _, h := range hosts {
			banned[h] = true
		}
		return svcBannedHostsLoadedMsg{banned: banned}
	}
}

func (m ServicesModel) openProvisionDialogCmd(host string) tea.Cmd {
	database := m.database
	existingHost := m.serviceHosts[host]
	return func() tea.Msg {
		msg := svcProvisionCheckMsg{host: host}
		if existingHost.Registered {
			msg.alreadyRegistered = true
			h := existingHost
			msg.existingHost = &h
			return msg
		}
		if database != nil {
			vm, err := database.ProvisionedVMByIP(host)
			if err == nil && vm != nil {
				msg.alreadyProvisioned = true
				msg.provisionedVMID = vm.VMID
				msg.provisionedNode = vm.Node
			}
		}
		return msg
	}
}

func (m ServicesModel) discoverHostCmd(host string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, findings := api.DiscoverHost(ctx, host)
		return svcDiscoveryDoneMsg{host: host, findings: findings}
	}
}

func (m ServicesModel) registerHostCmd(host, rtype string, vmid int, vmNode string) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		now := time.Now()
		h := db.ServiceHost{
			Host:             host,
			VMID:             vmid,
			Node:             vmNode,
			Managed:          rtype == "managed",
			Registered:       true,
			RegistrationType: rtype,
			RegisteredAt:     &now,
		}
		if database != nil {
			if err := database.SaveServiceHost(h); err != nil {
				return svcRegisteredMsg{host: host, err: err}
			}
			sev := "low"
			desc := fmt.Sprintf("Host %s registered as %s", host, rtype)
			if rtype == "managed" {
				sev = "medium"
			}
			_ = database.RecordSecurityEvent(db.SecurityEvent{
				Source:      "ratataskr",
				EventType:   "registration",
				Severity:    sev,
				Description: desc,
				Target:      host,
			})
		}
		return svcRegisteredMsg{host: host, registrationType: rtype}
	}
}

func (m ServicesModel) markProvisionPendingCmd(host string) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database != nil {
			_ = database.SaveServiceHost(db.ServiceHost{
				Host:             host,
				ProvisionPending: true,
			})
		}
		return svcRegisteredMsg{host: host, registrationType: "provision_pending"}
	}
}

func (m ServicesModel) runHealthChecks() tea.Cmd {
	services := m.services
	timeout := time.Duration(m.appSettings.HealthCheckTimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		checks := make([]api.ServiceCheck, 0, len(services))
		for _, s := range services {
			if !s.Enabled {
				continue
			}
			checks = append(checks, api.ServiceCheck{
				ID:     s.ID,
				Name:   s.Name,
				Host:   s.Host,
				Port:   s.Port,
				Scheme: api.ServiceScheme(s.Scheme),
				Path:   s.Notes,
			})
		}
		results := api.CheckAll(ctx, checks)
		return healthCheckedMsg{results: results}
	}
}

func (m ServicesModel) saveHealthChecks(results []api.HealthResult) tea.Cmd {
	database := m.database
	services := m.services
	return func() tea.Msg {
		if database == nil {
			return servicesLoadedMsg{}
		}
		for _, r := range results {
			hc := db.HealthCheck{
				ServiceID:  r.ServiceID,
				Up:         r.Up,
				LatencyMs:  r.LatencyMs,
				StatusCode: r.StatusCode,
				ErrorMsg:   r.Error,
				CheckedAt:  r.CheckedAt,
			}
			if r.CertInfo != nil {
				hc.TLSExpiry = &r.CertInfo.NotAfter
				san := strings.Join(r.CertInfo.DNSNames, " ")
				host := ""
				port := 443
				for _, s := range services {
					if s.ID == r.ServiceID {
						host = s.Host
						port = s.Port
						break
					}
				}
				if host != "" {
					_ = database.UpsertCertificate(db.Certificate{
						Host:        host,
						Port:        port,
						CommonName:  r.CertInfo.CommonName,
						SANNames:    san,
						Issuer:      r.CertInfo.Issuer,
						Serial:      r.CertInfo.Serial,
						NotBefore:   r.CertInfo.NotBefore,
						NotAfter:    r.CertInfo.NotAfter,
						Fingerprint: r.CertInfo.Fingerprint,
					})
				}
			}
			database.RecordHealthCheck(hc)       //nolint:errcheck
			database.RecordServiceUptime(r.ServiceID, r.Up) //nolint:errcheck
		}
		svcs, _ := database.Services()
		checks, _ := database.LatestHealthChecks()
		certs, _ := database.Certificates()
		uptimes := make(map[int64]float64, len(svcs))
		for _, s := range svcs {
			uptimes[s.ID] = database.ServiceUptimePct(s.ID)
		}
		return servicesLoadedMsg{services: svcs, checks: checks, certs: certs, uptimes: uptimes}
	}
}

func (m ServicesModel) addServiceCmd() tea.Cmd {
	database := m.database
	port := 0
	fmt.Sscanf(m.svcFields[svcFieldPort].Value(), "%d", &port)
	svc := db.Service{
		Name:    m.svcFields[svcFieldName].Value(),
		Node:    m.svcFields[svcFieldNode].Value(),
		Host:    m.svcFields[svcFieldHost].Value(),
		Port:    port,
		Scheme:  m.svcFields[svcFieldScheme].Value(),
		Notes:   m.svcFields[svcFieldNotes].Value(),
		Enabled: true,
	}
	return func() tea.Msg {
		if database == nil {
			return serviceAddedMsg{err: fmt.Errorf("no database")}
		}
		id, err := database.SaveService(svc)
		return serviceAddedMsg{id: id, err: err}
	}
}

func (m ServicesModel) deleteServiceCmd(id int64) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return serviceDeletedMsg{}
		}
		return serviceDeletedMsg{err: database.DeleteService(id)}
	}
}

func (m ServicesModel) wiringApplyCmd() tea.Cmd {
	saltClient := m.saltClient
	masterMinion := m.masterMinion
	pillarPath := m.pillarBase + "/ratataskr_services.sls"
	statePath := m.stateBase + "/services/wiring.sls"
	pillarContent := m.allServicesPillarYAML()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := saltClient.WriteFile(ctx, masterMinion, pillarPath, pillarContent); err != nil {
			return svcWiringAppliedMsg{err: fmt.Errorf("pillar: %w", err)}
		}
		if err := saltClient.WriteFile(ctx, masterMinion, statePath, api.WiringStateTemplate); err != nil {
			return svcWiringAppliedMsg{err: fmt.Errorf("state: %w", err)}
		}
		jid, err := saltClient.ApplyState(ctx, masterMinion, "services.wiring")
		if err != nil {
			return svcWiringAppliedMsg{err: fmt.Errorf("apply: %w", err)}
		}
		return svcWiringAppliedMsg{notice: "wiring dispatched — JID " + jid}
	}
}

func (m ServicesModel) allServicesPillarYAML() string {
	var sb strings.Builder
	sb.WriteString("# ratataskr-managed — do not edit; regenerated with 'w' from the Services screen\n")
	sb.WriteString("# Add to /srv/pillar/top.sls: base: '<master_minion>': [ratataskr_services]\n")
	sb.WriteString("services:\n")
	seen := map[string]bool{}
	for _, svc := range m.services {
		slug := serviceSlug(svc.Name)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		fqdn := slug
		zone := m.domain
		if m.domain != "" {
			fqdn = slug + "." + m.domain
		}
		sb.WriteString(fmt.Sprintf("  %s:\n", slug))
		sb.WriteString(fmt.Sprintf("    host: \"%s\"\n", svc.Host))
		sb.WriteString(fmt.Sprintf("    port: %d\n", svc.Port))
		sb.WriteString(fmt.Sprintf("    scheme: \"%s\"\n", svc.Scheme))
		sb.WriteString(fmt.Sprintf("    fqdn: \"%s\"\n", fqdn))
		sb.WriteString(fmt.Sprintf("    dns_zone: \"%s\"\n", zone))
		sb.WriteString("    traefik: true\n")
		sb.WriteString("    cert: false\n")
	}
	return sb.String()
}

func serviceSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

func (m ServicesModel) updateSvc(msg tea.Msg) (ServicesModel, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case servicesLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.notice = "load error: " + msg.err.Error()
		} else {
			m.services = msg.services
			m.latestHC = msg.checks
			if m.latestHC == nil {
				m.latestHC = make(map[int64]db.HealthCheck)
			}
			m.certs = msg.certs
			if msg.uptimes != nil {
				m.uptimePct = msg.uptimes
			}
		}
		return m, nil, true

	case healthCheckedMsg:
		if msg.err != nil {
			m.notice = "health check error: " + msg.err.Error()
			m.loading = false
			return m, nil, true
		}
		m.notice = fmt.Sprintf("checked %d services", len(msg.results))
		return m, m.saveHealthChecks(msg.results), true

	case serviceAddedMsg:
		m.addingSvc = false
		if msg.err != nil {
			m.notice = "add failed: " + msg.err.Error()
		} else {
			m.notice = "service added"
			m.resetSvcForm()
		}
		return m, m.loadAll(), true

	case serviceDeletedMsg:
		if msg.err != nil {
			m.notice = "delete failed: " + msg.err.Error()
		} else {
			m.notice = "service deleted"
			if m.svcCursor >= len(m.services)-1 && m.svcCursor > 0 {
				m.svcCursor--
			}
		}
		return m, m.loadAll(), true

	case svcWiringAppliedMsg:
		if msg.err != nil {
			m.notice = "wiring: " + msg.err.Error()
		} else {
			m.notice = msg.notice
		}
		return m, nil, true

	case svcHostsLoadedMsg:
		if msg.err == nil && msg.hosts != nil {
			m.serviceHosts = msg.hosts
		}
		return m, nil, true

	case svcBannedHostsLoadedMsg:
		if msg.banned != nil {
			m.bannedHosts = msg.banned
		}
		return m, nil, true

	case svcProvisionCheckMsg:
		m.dialogHost = msg.host
		m.dialogVMID = msg.provisionedVMID
		m.dialogVMNode = msg.provisionedNode
		if msg.alreadyRegistered {
			m.dialog = svcDialogAlreadyRegistered
		} else if msg.alreadyProvisioned {
			m.dialog = svcDialogRegisterType
		} else {
			m.dialog = svcDialogProvisionOrRegister
		}
		return m, nil, true

	case svcDiscoveryDoneMsg:
		m.discRunning = false
		if msg.err != nil {
			m.notice = "discovery error: " + msg.err.Error()
			m.dialog = svcDialogNone
		} else {
			m.discFindings = msg.findings
			m.dialog = svcDialogDiscovery
			if m.database != nil && len(msg.findings) > 0 {
				var ports []string
				for _, f := range msg.findings {
					ports = append(ports, f.Check)
				}
				_ = m.database.UpdateDiscoveredPorts(msg.host, strings.Join(ports, ","))
			}
		}
		return m, nil, true

	case svcRegisteredMsg:
		if msg.err != nil {
			m.notice = "registration failed: " + msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("host %s registered as %s", msg.host, msg.registrationType)
			m.dialog = svcDialogNone
		}
		return m, m.loadServiceHosts(), true
	}
	return m, nil, false
}

func (m ServicesModel) handleSvcTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.svcCursor > 0 {
			m.svcCursor--
		}
	case "down", "j":
		if m.svcCursor < len(m.services)-1 {
			m.svcCursor++
		}
	case "r":
		if len(m.services) > 0 {
			m.loading = true
			m.notice = "checking…"
			return m, m.runHealthChecks()
		}
	case "a":
		m.addingSvc = true
		m.svcFocus = 0
		m.svcFields[m.svcFocus].Focus()
		return m, nil
	case "ctrl+t":
		m.pickingTemplate = true
		m.templateCursor = 0
		m.templateFilter.SetValue("")
		m.templateFilter.Focus()
		return m, textinput.Blink
	case "d":
		if len(m.services) > 0 && m.svcCursor < len(m.services) {
			id := m.services[m.svcCursor].ID
			return m, m.deleteServiceCmd(id)
		}
	case "p":
		if m.svcCursor < len(m.services) {
			host := m.services[m.svcCursor].Host
			return m, m.openProvisionDialogCmd(host)
		}
	case "D":
		if m.svcCursor < len(m.services) {
			host := m.services[m.svcCursor].Host
			m.discRunning = true
			m.dialogHost = host
			m.notice = fmt.Sprintf("scanning %s…", host)
			return m, m.discoverHostCmd(host)
		}
	case "w":
		if m.saltClient == nil || m.masterMinion == "" {
			m.notice = "salt wiring: set [salt] master_minion in config"
			return m, nil
		}
		m.notice = "pushing pillar + state to " + m.masterMinion + "…"
		return m, m.wiringApplyCmd()
	}
	return m, nil
}

func (m ServicesModel) filteredTemplates() []svcTemplate {
	q := strings.ToLower(strings.TrimSpace(m.templateFilter.Value()))
	if q == "" {
		return serviceTemplates
	}
	var out []svcTemplate
	for _, t := range serviceTemplates {
		if strings.Contains(strings.ToLower(t.Name), q) {
			out = append(out, t)
		}
	}
	return out
}

func (m ServicesModel) handleTemplatePicker(key string, msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	filtered := m.filteredTemplates()
	switch key {
	case "esc":
		m.pickingTemplate = false
		m.templateFilter.Blur()
	case "enter":
		if m.templateCursor < len(filtered) {
			t := filtered[m.templateCursor]
			m.pickingTemplate = false
			m.templateFilter.Blur()
			m.addingSvc = true
			m.svcFocus = 0
			m.svcFields[svcFieldName].SetValue(t.Name)
			m.svcFields[svcFieldPort].SetValue(fmt.Sprintf("%d", t.Port))
			m.svcFields[svcFieldScheme].SetValue(t.Scheme)
			m.svcFields[svcFieldNotes].SetValue(t.Notes)
			m.svcFields[svcFieldHost].Focus()
			m.svcFocus = svcFieldHost
		}
	case "up", "k":
		if m.templateCursor > 0 {
			m.templateCursor--
		}
	case "down", "j":
		if m.templateCursor < len(filtered)-1 {
			m.templateCursor++
		}
	default:
		var cmd tea.Cmd
		m.templateFilter, cmd = m.templateFilter.Update(msg)
		filtered2 := m.filteredTemplates()
		if m.templateCursor >= len(filtered2) {
			m.templateCursor = 0
		}
		return m, cmd
	}
	return m, nil
}

func (m ServicesModel) handleDialog(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.dialog = svcDialogNone
		return m, nil
	}

	switch m.dialog {
	case svcDialogProvisionOrRegister:
		switch key {
		case "p", "P":
			host := m.dialogHost
			m.dialog = svcDialogNone
			return m, tea.Batch(
				m.markProvisionPendingCmd(host),
				func() tea.Msg { return openPXEMsg{} },
			)
		case "r", "R":
			m.dialog = svcDialogRegisterType
		}

	case svcDialogRegisterType:
		switch key {
		case "m", "M":
			return m, m.registerHostCmd(m.dialogHost, "managed", m.dialogVMID, m.dialogVMNode)
		case "u", "U":
			return m, m.registerHostCmd(m.dialogHost, "unmanaged", m.dialogVMID, m.dialogVMNode)
		}

	case svcDialogAlreadyRegistered:
		switch key {
		case "r", "R":
			m.dialog = svcDialogRegisterType
		}

	case svcDialogDiscovery:
		m.dialog = svcDialogNone
		m.discFindings = nil
	}

	return m, nil
}

func (m ServicesModel) handleSvcForm(key string, msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	switch key {
	case "esc":
		m.addingSvc = false
		m.resetSvcForm()
		return m, nil
	case "tab", "down":
		m.svcFields[m.svcFocus].Blur()
		m.svcFocus = (m.svcFocus + 1) % svcFieldCount
		m.svcFields[m.svcFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.svcFields[m.svcFocus].Blur()
		m.svcFocus = (m.svcFocus - 1 + svcFieldCount) % svcFieldCount
		m.svcFields[m.svcFocus].Focus()
		return m, nil
	case "enter":
		if m.svcFocus < svcFieldCount-1 {
			m.svcFields[m.svcFocus].Blur()
			m.svcFocus++
			m.svcFields[m.svcFocus].Focus()
			return m, nil
		}
		name := strings.TrimSpace(m.svcFields[svcFieldName].Value())
		host := strings.TrimSpace(m.svcFields[svcFieldHost].Value())
		portStr := strings.TrimSpace(m.svcFields[svcFieldPort].Value())
		if name == "" || host == "" || portStr == "" {
			m.notice = "name, host, and port are required"
			return m, nil
		}
		return m, m.addServiceCmd()
	}
	var cmd tea.Cmd
	m.svcFields[m.svcFocus], cmd = m.svcFields[m.svcFocus].Update(msg)
	return m, cmd
}

func (m *ServicesModel) resetSvcForm() {
	for i := range m.svcFields {
		m.svcFields[i].SetValue("")
		m.svcFields[i].Blur()
	}
	m.svcFields[svcFieldScheme].SetValue("tcp")
	m.svcFocus = 0
}

func (m ServicesModel) viewServices(height int) string {
	if m.dialog != svcDialogNone {
		return m.viewHostDialog()
	}
	if m.pickingTemplate {
		return m.viewTemplatePicker()
	}
	if m.addingSvc {
		return m.viewSvcForm()
	}

	var b strings.Builder
	if len(m.services) == 0 {
		b.WriteString(styleDim.Render("  No services tracked.  Press a to add, ctrl+t for template.\n"))
		return b.String()
	}

	colW := m.width
	hdrFmt := "  %-4s %-20s %-20s %-6s %-7s %-7s %-8s %-7s  %s\n"
	header := fmt.Sprintf(hdrFmt, "●", "Name", "Host", "Port", "Scheme", "Status", "Latency", "24h Up", "Registration")
	b.WriteString(styleSubtle.Render(truncate(header, colW)))
	b.WriteString(styleDim.Render(strings.Repeat("─", min(colW, 112))) + "\n")

	for i, svc := range m.services {
		if i >= height-3 {
			break
		}
		hc, hasHC := m.latestHC[svc.ID]
		dot := styleDim.Render("●")
		status := "unknown"
		latency := "—"
		if hasHC {
			if hc.Up {
				dot = lipgloss.NewStyle().Foreground(colorGreen).Render("●")
				status = "up"
				latency = fmt.Sprintf("%dms", hc.LatencyMs)
			} else {
				dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
				status = "down"
			}
		}
		uptimeStr := "—"
		if pct, ok := m.uptimePct[svc.ID]; ok {
			uptimeStr = fmt.Sprintf("%.0f%%", pct*100)
		}
		regBadge := m.registrationBadge(svc.Host)
		row := fmt.Sprintf("  %-4s %-20s %-20s %-6d %-7s %-7s %-8s %-7s  %s",
			dot, truncate(svc.Name, 20), truncate(svc.Host, 20),
			svc.Port, svc.Scheme, status, latency, uptimeStr, regBadge)
		if i == m.svcCursor {
			b.WriteString(styleSelected.Render(truncate(row, colW)) + "\n")
		} else {
			b.WriteString(truncate(row, colW) + "\n")
		}

		if hasHC && hc.TLSExpiry != nil {
			days := int(time.Until(*hc.TLSExpiry).Hours() / 24)
			if days <= 30 {
				clr := colorYellow
				if days <= 7 {
					clr = colorRed
				}
				warn := fmt.Sprintf("  └─ TLS expires in %d days (%s)", days, hc.TLSExpiry.Format("2006-01-02"))
				b.WriteString(lipgloss.NewStyle().Foreground(clr).Render(warn) + "\n")
			}
		}
	}
	return b.String()
}

func (m ServicesModel) registrationBadge(host string) string {
	banBadge := ""
	if m.bannedHosts[host] {
		banBadge = " " + lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("⚡CS")
	}

	h, ok := m.serviceHosts[host]
	if !ok || (!h.Registered && !h.ProvisionPending) {
		return styleDim.Render("[unregistered — p to provision]") + banBadge
	}
	if h.ProvisionPending {
		return lipgloss.NewStyle().Foreground(colorYellow).Render("[provision pending]") + banBadge
	}
	switch h.RegistrationType {
	case "managed":
		extras := ""
		if h.SaltApplied {
			extras += " salt"
		}
		if h.WazuhEnrolled {
			extras += " wazuh"
		}
		return lipgloss.NewStyle().Foreground(colorGreen).Render("[M] managed"+extras) + banBadge
	case "unmanaged":
		return lipgloss.NewStyle().Foreground(colorWhite).Faint(true).Render("[U] monitor only") + banBadge
	}
	return styleDim.Render("[?]") + banBadge
}

func (m ServicesModel) viewHostDialog() string {
	var b strings.Builder
	host := m.dialogHost
	border := styleDim.Render("  " + strings.Repeat("─", min(m.width-6, 70)))

	switch m.dialog {
	case svcDialogProvisionOrRegister:
		b.WriteString("\n")
		b.WriteString(styleBold.Render(fmt.Sprintf("  Host: %s", host)) + "\n")
		b.WriteString(border + "\n\n")
		b.WriteString(styleDim.Render("  This host has not been provisioned or registered.\n\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(
			"  [P]  Provision   — Queue for new deployment via PXE\n"))
		b.WriteString(styleDim.Render("         Sets up a fresh node; redirects to PXE screen.\n\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Render(
			"  [R]  Register    — Onboard this existing running host\n"))
		b.WriteString(styleDim.Render("         Host is already running; bring it under management.\n\n"))
		b.WriteString(border + "\n")
		b.WriteString(styleDim.Render("  p provision · r register · esc cancel\n"))

	case svcDialogRegisterType:
		vmInfo := ""
		if m.dialogVMID != 0 {
			vmInfo = fmt.Sprintf(" (VMID %d on %s)", m.dialogVMID, m.dialogVMNode)
		}
		b.WriteString("\n")
		b.WriteString(styleBold.Render(fmt.Sprintf("  Register: %s%s", host, vmInfo)) + "\n")
		b.WriteString(border + "\n\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(
			"  [M]  Managed      — Full management\n"))
		b.WriteString(styleDim.Render("         • Apply Salt states (hardening, config, packages)\n"))
		b.WriteString(styleDim.Render("         • Enroll in CrowdSec bouncer network\n"))
		b.WriteString(styleDim.Render("         • Configure Wazuh agent monitoring\n\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Render(
			"  [U]  Unmanaged    — Monitor & alert only\n"))
		b.WriteString(styleDim.Render("         • Health check polling (existing service checks)\n"))
		b.WriteString(styleDim.Render("         • Security events forwarded to Security screen\n"))
		b.WriteString(styleDim.Render("         • No config changes applied\n\n"))
		b.WriteString(border + "\n")
		b.WriteString(styleDim.Render("  m managed · u unmanaged · esc cancel\n"))

	case svcDialogAlreadyRegistered:
		h := m.serviceHosts[host]
		badge := m.registrationBadge(host)
		b.WriteString("\n")
		b.WriteString(styleBold.Render(fmt.Sprintf("  Host: %s", host)) + "\n")
		b.WriteString(border + "\n\n")
		b.WriteString(fmt.Sprintf("  Status:  %s\n", badge))
		if h.RegisteredAt != nil {
			b.WriteString(styleDim.Render(fmt.Sprintf("  Since:   %s\n", h.RegisteredAt.Format("2006-01-02 15:04"))))
		}
		if h.SaltApplied {
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ Salt applied\n"))
		}
		if h.WazuhEnrolled {
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render("  ✓ Wazuh enrolled\n"))
		}
		if h.DiscoveredPorts != "" {
			b.WriteString(styleDim.Render(fmt.Sprintf("  Ports:   %s\n", h.DiscoveredPorts)))
		}
		b.WriteString("\n")
		b.WriteString(styleDim.Render("  [R] re-register with different type\n"))
		b.WriteString(border + "\n")
		b.WriteString(styleDim.Render("  r re-register · esc close\n"))

	case svcDialogDiscovery:
		b.WriteString("\n")
		b.WriteString(styleBold.Render(fmt.Sprintf("  Discovery: %s", host)) + "\n")
		b.WriteString(border + "\n\n")
		if len(m.discFindings) == 0 {
			b.WriteString(styleDim.Render("  No open ports found in common port range.\n"))
		} else {
			hdr := fmt.Sprintf("  %-14s %-12s  %s\n", "Port", "Service", "Severity")
			b.WriteString(styleSubtle.Render(hdr))
			b.WriteString(styleDim.Render("  " + strings.Repeat("─", 50)) + "\n")
			for _, f := range m.discFindings {
				sev := f.Severity
				style := styleDim
				switch sev {
				case "critical":
					style = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
				case "high":
					style = lipgloss.NewStyle().Foreground(colorRed)
				case "medium":
					style = lipgloss.NewStyle().Foreground(colorYellow)
				}
				row := fmt.Sprintf("  %-14s %-12s  %s", f.Check, wellKnownFromCheck(f.Check), sev)
				b.WriteString(style.Render(row) + "\n")
			}
		}
		b.WriteString("\n" + border + "\n")
		b.WriteString(styleDim.Render("  any key to close\n"))
	}

	return b.String()
}

func wellKnownFromCheck(check string) string {
	if strings.HasPrefix(check, "tcp:") {
		return ""
	}
	return check
}

func (m ServicesModel) viewTemplatePicker() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("  Add from template") + styleDim.Render("  (type to filter, ↑/↓ select, enter apply, esc cancel)") + "\n\n")
	b.WriteString(styleDim.Render("  Filter: ") + m.templateFilter.View() + "\n\n")

	filtered := m.filteredTemplates()
	if len(filtered) == 0 {
		b.WriteString(styleDim.Render("  No templates match.\n"))
		return b.String()
	}
	for i, t := range filtered {
		line := fmt.Sprintf("  %-28s  %-7s  %d", t.Name, t.Scheme, t.Port)
		if t.Notes != "" {
			line += styleDim.Render("  " + t.Notes)
		}
		if i == m.templateCursor {
			b.WriteString(styleSelected.Render(line) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func (m ServicesModel) viewSvcForm() string {
	labels := []string{"Name", "Node", "Host", "Port", "Scheme", "Notes"}
	var b strings.Builder
	b.WriteString(styleSubtle.Render("  Add service\n"))
	b.WriteString(styleDim.Render("  ─────────────────────────────────────\n"))
	for i, f := range m.svcFields {
		label := fmt.Sprintf("  %-8s ", labels[i])
		if i == m.svcFocus {
			b.WriteString(styleSelected.Render(label) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(label) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("\n  enter to advance · esc to cancel\n"))
	return b.String()
}

func (m ServicesModel) svcHelpString() string {
	if m.addingSvc {
		return "  tab/enter advance · esc cancel"
	}
	if m.dialog != svcDialogNone {
		return ""
	}
	if m.pickingTemplate {
		return "  ↑/↓ navigate · enter apply · esc cancel"
	}
	hint := "  ↑/↓ navigate · a add · ctrl+t template · d delete · r health-check · p provision/register · D discover"
	if m.saltClient != nil && m.masterMinion != "" {
		hint += " · w wire (salt)"
	}
	return hint + " · ←/→ tab"
}
