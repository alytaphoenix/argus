package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/config"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Service Templates ─────────────────────────────────────────────────────────

type svcTemplate struct {
	Name   string
	Port   int
	Scheme string
	Notes  string // health-check path or other hint
}

var serviceTemplates = []svcTemplate{
	{"Proxmox Web UI", 8006, "https", ""},
	{"Grafana", 3000, "http", "/api/health"},
	{"Prometheus", 9090, "http", "/-/healthy"},
	{"Alertmanager", 9093, "http", "/-/healthy"},
	{"Loki", 3100, "http", "/ready"},
	{"Node Exporter", 9100, "tcp", ""},
	{"Gitea", 3001, "http", "/api/healthz"},
	{"Vault", 8200, "https", "/v1/sys/health"},
	{"Harbor", 443, "https", "/api/v2.0/health"},
	{"Nextcloud", 443, "https", "/status.php"},
	{"Home Assistant", 8123, "http", "/api/"},
	{"Jellyfin", 8096, "http", "/health"},
	{"Authentik", 9000, "https", "/api/v3/-/health/live/"},
	{"MinIO", 9001, "https", "/minio/health/live"},
	{"AdGuard Home", 3001, "http", "/control/status"},
	{"Uptime Kuma", 3001, "http", ""},
	{"Technitium DNS", 5380, "http", "/api/user/login"},
	{"Wazuh Dashboard", 443, "https", ""},
	{"Smallstep CA", 9000, "https", "/health"},
	{"Matchbox PXE", 8080, "http", "/"},
	{"CrowdSec API", 8080, "http", "/v1/decisions"},
	{"Traefik Dashboard", 8080, "http", "/api/rawdata"},
	{"Portainer", 9443, "https", "/api/status"},
	{"Netdata", 19999, "http", "/api/v1/info"},
}

// ── Tabs ──────────────────────────────────────────────────────────────────────

type servicesTab int

const (
	svcTabServices servicesTab = iota
	svcTabDNS
	svcTabCerts
	svcTabTrueNAS
	svcTabCount
)

var svcTabLabels = []string{"Services", "DNS", "Certificates", "TrueNAS"}

// ── Provision / register dialog state ────────────────────────────────────────

type svcDialogState int

const (
	svcDialogNone              svcDialogState = iota
	svcDialogProvisionOrRegister              // host never provisioned: Provision | Register
	svcDialogRegisterType                     // Managed | Unmanaged
	svcDialogDiscovery                        // show port-scan discovery results
	svcDialogAlreadyRegistered                // host already registered — show status
)

// ── Add-service form ─────────────────────────────────────────────────────────

const (
	svcFieldName   = 0
	svcFieldNode   = 1
	svcFieldHost   = 2
	svcFieldPort   = 3
	svcFieldScheme = 4
	svcFieldNotes  = 5
	svcFieldCount  = 6
)

// ── Add-DNS-record form ───────────────────────────────────────────────────────

const (
	dnsFieldType  = 0
	dnsFieldName  = 1
	dnsFieldValue = 2
	dnsFieldTTL   = 3
	dnsFieldCount = 4
)

// ── Model ─────────────────────────────────────────────────────────────────────

// ServicesModel is the top-level services screen with three tabs.
type ServicesModel struct {
	width, height int
	tab           servicesTab

	// Services tab
	services    []db.Service
	latestHC    map[int64]db.HealthCheck
	uptimePct   map[int64]float64 // 24h uptime ratio per service ID
	svcCursor   int
	addingSvc   bool
	svcFields   [svcFieldCount]textinput.Model
	svcFocus    int

	// Template picker
	pickingTemplate  bool
	templateFilter   textinput.Model
	templateCursor   int

	// DNS tab
	dnsClient  *api.TechnitiumClient
	zones      []api.DNSZone
	zoneCursor int
	dnsRecords []api.DNSRecord
	recCursor  int
	addingDNS  bool
	dnsFields  [dnsFieldCount]textinput.Model
	dnsFocus   int
	dnsZone    string // currently displayed zone name
	dnsLoading bool

	// Certs tab
	certs      []db.Certificate
	certCursor int

	// step-ca
	caClient *api.StepCAClient
	caStatus string // "ok", "error …", ""

	// TrueNAS tab
	trueNASClient  *api.TrueNASClient
	trueNASStatus  *api.TrueNASStatus
	trueNASLoading bool
	trueNASChecked bool
	trueNASCursor  int

	// Host provision / register dialog
	serviceHosts map[string]db.ServiceHost // keyed by host IP/name
	bannedHosts  map[string]bool           // hosts with active CrowdSec bans (from security_events)
	dialog       svcDialogState
	dialogHost   string          // service host being provisioned/registered
	dialogVMID   int             // if already in provisioned_vms
	dialogVMNode string          // if already in provisioned_vms
	discFindings []api.ScanFinding // discovery scan results
	discRunning  bool

	database    *db.DB
	cfg         config.Config
	appSettings db.AppSettings
	loading     bool
	spinner     spinner.Model
	notice      string
}

func NewServicesModel(dnsClient *api.TechnitiumClient, caClient *api.StepCAClient,
	trueNASClient *api.TrueNASClient, database *db.DB, cfg config.Config, appSettings db.AppSettings) ServicesModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	svcFields := [svcFieldCount]textinput.Model{}
	placeholders := []string{"name", "node (optional)", "hostname / IP", "port", "tcp|http|https", "notes (optional)"}
	for i := range svcFields {
		ti := textinput.New()
		ti.Placeholder = placeholders[i]
		ti.CharLimit = 120
		svcFields[i] = ti
	}
	svcFields[svcFieldScheme].SetValue("tcp")

	dnsFields := [dnsFieldCount]textinput.Model{}
	dnsPlaceholders := []string{"type (A/CNAME/TXT/MX)", "name", "value / IP", "TTL (300)"}
	for i := range dnsFields {
		ti := textinput.New()
		ti.Placeholder = dnsPlaceholders[i]
		ti.CharLimit = 200
		dnsFields[i] = ti
	}
	dnsFields[dnsFieldTTL].SetValue("300")

	return ServicesModel{
		dnsClient:     dnsClient,
		caClient:      caClient,
		trueNASClient: trueNASClient,
		database:      database,
		cfg:           cfg,
		appSettings:   appSettings,
		spinner:       sp,
		latestHC:      make(map[int64]db.HealthCheck),
		uptimePct:     make(map[int64]float64),
		serviceHosts:  make(map[string]db.ServiceHost),
		bannedHosts:   make(map[string]bool),
		svcFields:      svcFields,
		dnsFields:      dnsFields,
		templateFilter: func() textinput.Model {
			ti := textinput.New()
			ti.Placeholder = "filter templates…"
			ti.CharLimit = 64
			return ti
		}(),
	}
}

func (m ServicesModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, m.loadAll(), m.loadServiceHosts(), m.loadBannedHosts()}
	if m.trueNASClient != nil {
		cmds = append(cmds, m.loadTrueNAS())
	}
	return tea.Batch(cmds...)
}

// ── Commands ─────────────────────────────────────────────────────────────────

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

// openProvisionDialogCmd checks the host against provisioned_vms and existing registrations.
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
		// Check provisioned_vms by IP
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

// discoverHostCmd runs a port scan on the service host.
func (m ServicesModel) discoverHostCmd(host string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, findings := api.DiscoverHost(ctx, host)
		return svcDiscoveryDoneMsg{host: host, findings: findings}
	}
}

// registerHostCmd persists a registration decision and records a security event.
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

// markProvisionPendingCmd stores a "provision pending" entry for a new host.
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
				// Persist the discovered certificate
				san := strings.Join(r.CertInfo.DNSNames, " ")
				// Find the host for this service
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
			database.RecordHealthCheck(hc) //nolint:errcheck
			database.RecordServiceUptime(r.ServiceID, r.Up) //nolint:errcheck
		}
		// Reload after persisting
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

func (m ServicesModel) loadDNSZones() tea.Cmd {
	client := m.dnsClient
	return func() tea.Msg {
		if client == nil {
			return dnsZonesLoadedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		zones, err := client.Zones(ctx)
		return dnsZonesLoadedMsg{zones: zones, err: err}
	}
}

func (m ServicesModel) loadDNSRecords(zone string) tea.Cmd {
	client := m.dnsClient
	return func() tea.Msg {
		if client == nil {
			return dnsRecordsLoadedMsg{zone: zone, err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recs, err := client.Records(ctx, zone, "")
		return dnsRecordsLoadedMsg{zone: zone, records: recs, err: err}
	}
}

func (m ServicesModel) pingCA() tea.Cmd {
	client := m.caClient
	return func() tea.Msg {
		if client == nil {
			return caHealthMsg{status: "not configured"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx); err != nil {
			return caHealthMsg{status: "error", err: err}
		}
		return caHealthMsg{status: "ok"}
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

func (m ServicesModel) addDNSCmd() tea.Cmd {
	client := m.dnsClient
	zone := m.dnsZone
	recType := strings.ToUpper(m.dnsFields[dnsFieldType].Value())
	name := m.dnsFields[dnsFieldName].Value()
	value := m.dnsFields[dnsFieldValue].Value()
	ttl := 300
	fmt.Sscanf(m.dnsFields[dnsFieldTTL].Value(), "%d", &ttl)
	return func() tea.Msg {
		if client == nil {
			return dnsAddedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		opts := api.DNSAddOptions{
			Zone:   zone,
			Domain: name,
			Type:   recType,
			TTL:    ttl,
		}
		switch recType {
		case "A", "AAAA":
			opts.IPAddress = value
		case "CNAME":
			opts.CName = value
		case "TXT":
			opts.Text = value
		case "MX":
			opts.Exchange = value
			opts.Preference = 10
		}
		err := client.AddRecord(ctx, opts)
		return dnsAddedMsg{err: err}
	}
}

func (m ServicesModel) deleteDNSCmd() tea.Cmd {
	client := m.dnsClient
	if len(m.dnsRecords) == 0 || m.recCursor >= len(m.dnsRecords) {
		return nil
	}
	rec := m.dnsRecords[m.recCursor]
	zone := m.dnsZone
	value := dnsRecordValue(rec)
	return func() tea.Msg {
		if client == nil {
			return dnsDeletedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.DeleteRecord(ctx, zone, rec.Name, rec.Type, value)
		return dnsDeletedMsg{err: err}
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m ServicesModel) Update(msg tea.Msg) (ServicesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

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
		return m, nil

	case healthCheckedMsg:
		if msg.err != nil {
			m.notice = "health check error: " + msg.err.Error()
			m.loading = false
			return m, nil
		}
		m.notice = fmt.Sprintf("checked %d services", len(msg.results))
		return m, m.saveHealthChecks(msg.results)

	case dnsZonesLoadedMsg:
		m.dnsLoading = false
		if msg.err != nil {
			m.notice = "DNS: " + msg.err.Error()
			return m, nil
		}
		m.zones = msg.zones
		if len(m.zones) > 0 {
			m.dnsZone = m.zones[0].Name
			return m, m.loadDNSRecords(m.dnsZone)
		}
		return m, nil

	case dnsRecordsLoadedMsg:
		m.dnsLoading = false
		if msg.err != nil {
			m.notice = "DNS records: " + msg.err.Error()
			return m, nil
		}
		m.dnsRecords = msg.records
		m.recCursor = 0
		m.dnsZone = msg.zone
		return m, nil

	case caHealthMsg:
		if msg.err != nil {
			m.caStatus = "error: " + msg.err.Error()
		} else {
			m.caStatus = msg.status
		}
		return m, nil

	case serviceAddedMsg:
		m.addingSvc = false
		if msg.err != nil {
			m.notice = "add failed: " + msg.err.Error()
		} else {
			m.notice = "service added"
			m.resetSvcForm()
		}
		return m, m.loadAll()

	case serviceDeletedMsg:
		if msg.err != nil {
			m.notice = "delete failed: " + msg.err.Error()
		} else {
			m.notice = "service deleted"
			if m.svcCursor >= len(m.services)-1 && m.svcCursor > 0 {
				m.svcCursor--
			}
		}
		return m, m.loadAll()

	case dnsAddedMsg:
		m.addingDNS = false
		if msg.err != nil {
			m.notice = "DNS add failed: " + msg.err.Error()
		} else {
			m.notice = "record added"
			m.resetDNSForm()
		}
		return m, m.loadDNSRecords(m.dnsZone)

	case dnsDeletedMsg:
		if msg.err != nil {
			m.notice = "DNS delete failed: " + msg.err.Error()
		} else {
			m.notice = "record deleted"
		}
		return m, m.loadDNSRecords(m.dnsZone)

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
		return m, nil

	case svcHostsLoadedMsg:
		if msg.err == nil && msg.hosts != nil {
			m.serviceHosts = msg.hosts
		}
		return m, nil

	case svcBannedHostsLoadedMsg:
		if msg.banned != nil {
			m.bannedHosts = msg.banned
		}
		return m, nil

	case svcProvisionCheckMsg:
		m.dialogHost = msg.host
		m.dialogVMID = msg.provisionedVMID
		m.dialogVMNode = msg.provisionedNode
		if msg.alreadyRegistered {
			m.dialog = svcDialogAlreadyRegistered
		} else if msg.alreadyProvisioned {
			// Host is in provisioned_vms → go straight to Register choice
			m.dialog = svcDialogRegisterType
		} else {
			// Never seen → offer Provision or Register
			m.dialog = svcDialogProvisionOrRegister
		}
		return m, nil

	case svcDiscoveryDoneMsg:
		m.discRunning = false
		if msg.err != nil {
			m.notice = "discovery error: " + msg.err.Error()
			m.dialog = svcDialogNone
		} else {
			m.discFindings = msg.findings
			m.dialog = svcDialogDiscovery
			// Persist discovered ports
			if m.database != nil {
				now := time.Now()
				existing := m.serviceHosts[msg.host]
				existing.Host = msg.host
				existing.LastDiscovered = &now
				var parts []string
				for _, f := range msg.findings {
					parts = append(parts, f.Check)
				}
				if len(parts) > 0 {
					existing.DiscoveredPorts = strings.Join(parts, ",")
				}
				_ = m.database.SaveServiceHost(existing)
				m.serviceHosts[msg.host] = existing
			}
		}
		return m, nil

	case svcRegisteredMsg:
		if msg.err != nil {
			m.notice = "registration error: " + msg.err.Error()
		} else {
			switch msg.registrationType {
			case "managed":
				m.notice = fmt.Sprintf("✓ %s registered as managed. Apply Salt highstate to complete setup.", msg.host)
			case "unmanaged":
				m.notice = fmt.Sprintf("✓ %s registered for monitoring only. Security events will be collected.", msg.host)
			case "provision_pending":
				m.notice = fmt.Sprintf("✓ %s queued for provisioning. Use the PXE screen to boot and configure.", msg.host)
			}
		}
		m.dialog = svcDialogNone
		return m, m.loadServiceHosts()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m ServicesModel) handleKey(msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	key := msg.String()

	// Handle active dialog first
	if m.dialog != svcDialogNone {
		return m.handleDialog(key)
	}

	// Template picker
	if m.pickingTemplate {
		return m.handleTemplatePicker(key, msg)
	}

	// Handle form input
	if m.addingSvc {
		return m.handleSvcForm(key, msg)
	}
	if m.addingDNS {
		return m.handleDNSForm(key, msg)
	}

	// Tab switching
	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
			if m.tab == svcTabDNS && len(m.zones) == 0 {
				m.dnsLoading = true
				return m, tea.Batch(m.loadDNSZones(), m.pingCA())
			}
			if m.tab == svcTabTrueNAS && !m.trueNASChecked {
				m.trueNASLoading = true
				return m, m.loadTrueNAS()
			}
		}
		return m, nil
	case "right", "l":
		if m.tab < svcTabCount-1 {
			m.tab++
			m.notice = ""
			if m.tab == svcTabDNS && len(m.zones) == 0 {
				m.dnsLoading = true
				return m, tea.Batch(m.loadDNSZones(), m.pingCA())
			}
			if m.tab == svcTabTrueNAS && !m.trueNASChecked {
				m.trueNASLoading = true
				return m, m.loadTrueNAS()
			}
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	}

	// Per-tab navigation
	switch m.tab {
	case svcTabServices:
		return m.handleSvcTab(key)
	case svcTabDNS:
		return m.handleDNSTab(key)
	case svcTabCerts:
		return m.handleCertTab(key)
	case svcTabTrueNAS:
		return m.handleTrueNASTab(key)
	}
	return m, nil
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
		// Provision or register the selected service's host
		if m.svcCursor < len(m.services) {
			host := m.services[m.svcCursor].Host
			return m, m.openProvisionDialogCmd(host)
		}
	case "D":
		// Discover all open ports on the selected service's host
		if m.svcCursor < len(m.services) {
			host := m.services[m.svcCursor].Host
			m.discRunning = true
			m.dialogHost = host
			m.notice = fmt.Sprintf("scanning %s…", host)
			return m, m.discoverHostCmd(host)
		}
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
		// Reset cursor when filter changes
		filtered2 := m.filteredTemplates()
		if m.templateCursor >= len(filtered2) {
			m.templateCursor = 0
		}
		return m, cmd
	}
	return m, nil
}

// handleDialog handles keystrokes when a dialog overlay is active.
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
			// Provision: mark as pending, then navigate to PXE Spawn wizard
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
			// Re-register: let user change type
			m.dialog = svcDialogRegisterType
		}

	case svcDialogDiscovery:
		// Any key closes discovery results
		m.dialog = svcDialogNone
		m.discFindings = nil
	}

	return m, nil
}

func (m ServicesModel) handleDNSTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.recCursor > 0 {
			m.recCursor--
		}
	case "down", "j":
		if m.recCursor < len(m.dnsRecords)-1 {
			m.recCursor++
		}
	case "[":
		if m.zoneCursor > 0 {
			m.zoneCursor--
			m.dnsZone = m.zones[m.zoneCursor].Name
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "]":
		if m.zoneCursor < len(m.zones)-1 {
			m.zoneCursor++
			m.dnsZone = m.zones[m.zoneCursor].Name
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "r":
		if m.dnsZone != "" {
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "R":
		m.dnsLoading = true
		return m, m.loadDNSZones()
	case "a":
		m.addingDNS = true
		m.dnsFocus = 0
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "d":
		if len(m.dnsRecords) > 0 {
			return m, m.deleteDNSCmd()
		}
	}
	return m, nil
}

func (m ServicesModel) handleCertTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.certCursor > 0 {
			m.certCursor--
		}
	case "down", "j":
		if m.certCursor < len(m.certs)-1 {
			m.certCursor++
		}
	case "r":
		return m, m.loadAll()
	}
	return m, nil
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
		// Last field → submit
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

func (m ServicesModel) handleDNSForm(key string, msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	switch key {
	case "esc":
		m.addingDNS = false
		m.resetDNSForm()
		return m, nil
	case "tab", "down":
		m.dnsFields[m.dnsFocus].Blur()
		m.dnsFocus = (m.dnsFocus + 1) % dnsFieldCount
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.dnsFields[m.dnsFocus].Blur()
		m.dnsFocus = (m.dnsFocus - 1 + dnsFieldCount) % dnsFieldCount
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "enter":
		if m.dnsFocus < dnsFieldCount-1 {
			m.dnsFields[m.dnsFocus].Blur()
			m.dnsFocus++
			m.dnsFields[m.dnsFocus].Focus()
			return m, nil
		}
		// Submit
		if m.dnsZone == "" {
			m.notice = "no zone selected"
			m.addingDNS = false
			return m, nil
		}
		recType := strings.TrimSpace(m.dnsFields[dnsFieldType].Value())
		name := strings.TrimSpace(m.dnsFields[dnsFieldName].Value())
		value := strings.TrimSpace(m.dnsFields[dnsFieldValue].Value())
		if recType == "" || name == "" || value == "" {
			m.notice = "type, name, and value are required"
			return m, nil
		}
		return m, m.addDNSCmd()
	}
	var cmd tea.Cmd
	m.dnsFields[m.dnsFocus], cmd = m.dnsFields[m.dnsFocus].Update(msg)
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

func (m *ServicesModel) resetDNSForm() {
	for i := range m.dnsFields {
		m.dnsFields[i].SetValue("")
		m.dnsFields[i].Blur()
	}
	m.dnsFields[dnsFieldTTL].SetValue("300")
	m.dnsFocus = 0
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m ServicesModel) View() string {
	var b strings.Builder

	// Tab bar
	tabBar := m.renderTabBar()
	b.WriteString(tabBar)
	b.WriteString("\n")

	contentHeight := m.height - 6
	if contentHeight < 4 {
		contentHeight = 4
	}

	switch m.tab {
	case svcTabServices:
		b.WriteString(m.viewServices(contentHeight))
	case svcTabDNS:
		b.WriteString(m.viewDNS(contentHeight))
	case svcTabCerts:
		b.WriteString(m.viewCerts(contentHeight))
	case svcTabTrueNAS:
		b.WriteString(m.viewTrueNAS(contentHeight))
	}

	// Notice / status bar
	b.WriteString("\n")
	if m.notice != "" {
		b.WriteString(styleDim.Render("  " + m.notice))
	}
	b.WriteString("\n")
	b.WriteString(styleDim.Render(m.helpString()))

	return b.String()
}

func (m ServicesModel) renderTabBar() string {
	var parts []string
	for i, label := range svcTabLabels {
		l := " " + label + " "
		if servicesTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch tab")
}

func (m ServicesModel) viewServices(height int) string {
	// If dialog is active, show it as an overlay
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

		// TLS expiry warning
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

// registrationBadge returns a short styled badge for a host's management state.
// It also shows a ⚡CS banner if the host has an active CrowdSec ban.
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

// viewHostDialog renders the provision / register / discovery overlay.
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

// wellKnownFromCheck extracts the service name from a "tcp:22" style check string.
func wellKnownFromCheck(check string) string {
	if strings.HasPrefix(check, "tcp:") {
		// already have the service from the detail; just return empty
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
			line += styleDim.Render("  "+t.Notes)
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

func (m ServicesModel) viewDNS(height int) string {
	// step-ca status header
	var b strings.Builder
	if m.caClient != nil {
		caLine := "  step-ca: "
		switch m.caStatus {
		case "ok":
			caLine += lipgloss.NewStyle().Foreground(colorGreen).Render("● online")
		case "not configured":
			caLine += styleDim.Render("not configured")
		case "":
			caLine += styleDim.Render("checking…")
		default:
			caLine += lipgloss.NewStyle().Foreground(colorRed).Render(m.caStatus)
		}
		b.WriteString(caLine + "\n")
	}

	if m.addingDNS {
		b.WriteString(m.viewDNSForm())
		return b.String()
	}

	// Zone navigation bar
	if m.dnsClient == nil {
		b.WriteString(styleDim.Render("  Technitium not configured — set technitium.url in config.toml\n"))
		return b.String()
	}

	if len(m.zones) == 0 {
		if m.dnsLoading {
			b.WriteString(styleDim.Render("  " + m.spinner.View() + " loading zones…\n"))
		} else {
			b.WriteString(styleDim.Render("  No zones found. Press R to refresh.\n"))
		}
		return b.String()
	}

	// Zone selector
	var zoneBar strings.Builder
	zoneBar.WriteString("  Zones: ")
	for i, z := range m.zones {
		label := z.Name
		if z.Disabled {
			label = styleDim.Render(label)
		}
		if i == m.zoneCursor {
			zoneBar.WriteString(styleSelected.Render("[" + label + "]"))
		} else {
			zoneBar.WriteString(styleDim.Render(" " + label + " "))
		}
		if i < len(m.zones)-1 {
			zoneBar.WriteString(styleDim.Render("│"))
		}
	}
	b.WriteString(zoneBar.String() + styleDim.Render("  [/] switch zone") + "\n")
	b.WriteString(styleDim.Render("  ─────────────────────────────────────\n"))

	if m.dnsLoading {
		b.WriteString(styleDim.Render("  " + m.spinner.View() + " loading records…\n"))
		return b.String()
	}

	if len(m.dnsRecords) == 0 {
		b.WriteString(styleDim.Render("  No records in zone.\n"))
		return b.String()
	}

	hdrFmt := "  %-36s %-8s %-6s  %s\n"
	b.WriteString(styleSubtle.Render(fmt.Sprintf(hdrFmt, "Name", "Type", "TTL", "Value")))

	maxRows := height - 6
	for i, rec := range m.dnsRecords {
		if i >= maxRows {
			break
		}
		value := dnsRecordValue(rec)
		row := fmt.Sprintf(hdrFmt, truncate(rec.Name, 36), rec.Type, fmt.Sprintf("%d", rec.TTL), truncate(value, 40))
		if i == m.recCursor {
			b.WriteString(styleSelected.Render(truncate(row, m.width)))
		} else {
			b.WriteString(truncate(row, m.width))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m ServicesModel) viewDNSForm() string {
	labels := []string{"Type", "Name", "Value", "TTL"}
	zone := m.dnsZone
	var b strings.Builder
	b.WriteString(styleSubtle.Render(fmt.Sprintf("  Add DNS record → zone: %s\n", zone)))
	b.WriteString(styleDim.Render("  ─────────────────────────────────────\n"))
	for i, f := range m.dnsFields {
		label := fmt.Sprintf("  %-8s ", labels[i])
		if i == m.dnsFocus {
			b.WriteString(styleSelected.Render(label) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(label) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("\n  enter to advance · esc to cancel\n"))
	return b.String()
}

func (m ServicesModel) viewCerts(height int) string {
	var b strings.Builder
	if len(m.certs) == 0 {
		b.WriteString(styleDim.Render("  No certificates tracked yet.\n"))
		b.WriteString(styleDim.Render("  Add HTTPS services and press r to run health checks — certs will be discovered automatically.\n"))
		return b.String()
	}

	hdrFmt := "  %-3s %-32s %-28s %-12s %s\n"
	b.WriteString(styleSubtle.Render(fmt.Sprintf(hdrFmt, "●", "Common Name / Host", "Issuer", "Expires", "Days")))
	b.WriteString(styleDim.Render(strings.Repeat("─", min(m.width, 100))) + "\n")

	for i, cert := range m.certs {
		if i >= height-3 {
			break
		}
		days := cert.DaysUntilExpiry()
		var dot, daysStr string
		switch {
		case days < 0:
			dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorRed).Render("EXPIRED")
		case days <= 7:
			dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d days", days))
		case days <= 30:
			dot = lipgloss.NewStyle().Foreground(colorYellow).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("%d days", days))
		default:
			dot = lipgloss.NewStyle().Foreground(colorGreen).Render("●")
			daysStr = styleDim.Render(fmt.Sprintf("%d days", days))
		}

		label := cert.CommonName
		if label == "" {
			label = fmt.Sprintf("%s:%d", cert.Host, cert.Port)
		}
		expiry := cert.NotAfter.Format("2006-01-02")
		row := fmt.Sprintf(hdrFmt, dot, truncate(label, 32), truncate(cert.Issuer, 28), expiry, daysStr)
		if i == m.certCursor {
			b.WriteString(styleSelected.Render(truncate(row, m.width)) + "\n")
		} else {
			b.WriteString(truncate(row, m.width) + "\n")
		}

		// SANs on next line if selected
		if i == m.certCursor && cert.SANNames != "" {
			sans := "     SANs: " + cert.SANNames
			b.WriteString(styleDim.Render(truncate(sans, m.width)) + "\n")
		}
	}
	return b.String()
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

	// System info header
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

	// Active alerts
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

	// Pools
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

	// Datasets (top-level usage summary)
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

	// Services running summary
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

func (m ServicesModel) helpString() string {
	switch m.tab {
	case svcTabServices:
		if m.addingSvc {
			return "  tab/enter advance · esc cancel"
		}
		if m.dialog != svcDialogNone {
			return ""
		}
		if m.pickingTemplate {
			return "  ↑/↓ navigate · enter apply · esc cancel"
		}
		return "  ↑/↓ navigate · a add · ctrl+t template · d delete · r health-check · p provision/register · D discover · ←/→ tab"
	case svcTabDNS:
		if m.addingDNS {
			return "  tab/enter advance · esc cancel"
		}
		return "  ↑/↓ records · [/] zone · a add · d delete · r refresh · ←/→ tab"
	case svcTabCerts:
		return "  ↑/↓ navigate · r reload · ←/→ tab"
	case svcTabTrueNAS:
		return "  ↑/↓ pools · r refresh · ←/→ tab · b back"
	}
	return ""
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// dnsRecordValue returns the primary value for a DNS record for display / delete.
func dnsRecordValue(rec api.DNSRecord) string {
	switch strings.ToUpper(rec.Type) {
	case "A", "AAAA":
		return rec.RData.IPAddress
	case "CNAME":
		return rec.RData.CName
	case "TXT":
		return rec.RData.Text
	case "MX":
		return fmt.Sprintf("%d %s", rec.RData.Preference, rec.RData.Exchange)
	default:
		return ""
	}
}
