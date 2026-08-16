package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
)

// ── Service Templates ─────────────────────────────────────────────────────────

type svcTemplate struct {
	Name   string
	Port   int
	Scheme string
	Notes  string
}

var serviceTemplates = []svcTemplate{
	{"Proxmox Web UI", 8006, "https", ""},
	{"Traefik HTTP", 80, "http", "/ping"},
	{"Traefik Dashboard", 8080, "http", "/ping"},
	{"Portainer", 9443, "https", "/api/status"},
	{"Docker Engine API", 2376, "https", "/info"},
	{"Grafana", 3000, "http", "/api/health"},
	{"Prometheus", 9090, "http", "/-/healthy"},
	{"Alertmanager", 9093, "http", "/-/healthy"},
	{"Loki", 3100, "http", "/ready"},
	{"Node Exporter", 9100, "tcp", ""},
	{"Netdata", 19999, "http", "/api/v1/info"},
	{"Uptime Kuma", 3001, "http", "/"},
	{"MinIO S3 API", 9000, "http", "/minio/health/live"},
	{"MinIO Console", 9001, "http", "/"},
	{"Harbor", 443, "https", "/api/v2.0/health"},
	{"Nextcloud", 443, "https", "/status.php"},
	{"Authentik", 9000, "http", "/-/health/live/"},
	{"Vault", 8200, "https", "/v1/sys/health"},
	{"Smallstep CA", 9000, "https", "/health"},
	{"CrowdSec API", 8080, "tcp", ""},
	{"Wazuh Dashboard", 443, "https", "/api/status"},
	{"Technitium DNS", 5380, "http", "/"},
	{"AdGuard Home", 80, "http", "/"},
	{"Gitea", 3000, "http", "/api/healthz"},
	{"Home Assistant", 8123, "http", "/"},
	{"Jellyfin", 8096, "http", "/health"},
	{"Matchbox PXE", 8080, "http", "/"},
	{"Hermes Dashboard", 9119, "http", "/"},
	{"Hermes API Server", 8642, "http", "/v1/models"},
	{"Hermes Gateway Webhook", 8443, "tcp", ""},
	{"signal-cli REST API", 8080, "http", "/v1/about"},
}

// ── Tabs ──────────────────────────────────────────────────────────────────────

type servicesTab int

const (
	svcTabServices servicesTab = iota
	svcTabDNS
	svcTabCerts
	svcTabTrueNAS
	svcTabHomeAssistant
	svcTabCount
)

var svcTabLabels = []string{"Services", "DNS", "Certificates", "TrueNAS", "Home Assistant"}

// ── Provision / register dialog state ────────────────────────────────────────

type svcDialogState int

const (
	svcDialogNone              svcDialogState = iota
	svcDialogProvisionOrRegister
	svcDialogRegisterType
	svcDialogDiscovery
	svcDialogAlreadyRegistered
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

type ServicesModel struct {
	width, height int
	tab           servicesTab

	// Services tab
	services    []db.Service
	latestHC    map[int64]db.HealthCheck
	uptimePct   map[int64]float64
	svcCursor   int
	addingSvc   bool
	svcFields   [svcFieldCount]textinput.Model
	svcFocus    int

	// Template picker
	pickingTemplate bool
	templateFilter  textinput.Model
	templateCursor  int

	// DNS tab
	dnsClient  *api.TechnitiumClient
	zones      []api.DNSZone
	zoneCursor int
	dnsRecords []api.DNSRecord
	recCursor  int
	addingDNS  bool
	dnsFields  [dnsFieldCount]textinput.Model
	dnsFocus   int
	dnsZone    string
	dnsLoading bool

	// Certs tab
	certs      []db.Certificate
	certCursor int

	// step-ca
	caClient *api.StepCAClient
	caStatus string

	// TrueNAS tab
	trueNASClient  *api.TrueNASClient
	trueNASStatus  *api.TrueNASStatus
	trueNASLoading bool
	trueNASChecked bool
	trueNASCursor  int

	// Home Assistant tab
	haClient   *api.HAClient
	haEntities []api.HAEntity
	haLoading  bool
	haChecked  bool
	haCursor  int
	haVersion string
	haErr     string

	// Host provision / register dialog
	serviceHosts map[string]db.ServiceHost
	bannedHosts  map[string]bool
	dialog       svcDialogState
	dialogHost   string
	dialogVMID   int
	dialogVMNode string
	discFindings []api.ScanFinding
	discRunning  bool

	database    *db.DB
	cfg         config.Config
	appSettings db.AppSettings
	loading     bool
	spinner     spinner.Model
	notice      string

	// Salt wiring
	saltClient   *api.SaltClient
	masterMinion string
	pillarBase   string
	stateBase    string
	domain       string
}

// ─────────────────────────────────────────────────────────────────────────────
// Adding a new integration tab — checklist:
//   1. Add tab constant to the iota above (before svcTabCount)
//   2. Add label string to svcTabLabels (same index as the iota)
//   3. Add client field(s) + state fields to ServicesModel
//   4. Add client param to NewServicesModel; assign in return literal
//   5. Add lazy-load in handleKey left/right cases
//   6. Add case in the per-tab switch at the bottom of handleKey
//   7. Add case in View's switch
//   8. Create ui/services_<name>.go with: loadXxx, updateXxx, handleXxxTab, viewXxx, xxxHelpString
// ─────────────────────────────────────────────────────────────────────────────

func NewServicesModel(dnsClient *api.TechnitiumClient, caClient *api.StepCAClient,
	trueNASClient *api.TrueNASClient, haClient *api.HAClient,
	database *db.DB, cfg config.Config, appSettings db.AppSettings,
	saltClient *api.SaltClient) ServicesModel {

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

	pillarBase := cfg.Salt.PillarBase
	if pillarBase == "" {
		pillarBase = "/srv/pillar"
	}
	stateBase := cfg.Salt.StateBase
	if stateBase == "" {
		stateBase = "/srv/salt"
	}

	return ServicesModel{
		dnsClient:     dnsClient,
		caClient:      caClient,
		trueNASClient: trueNASClient,
		haClient:      haClient,
		database:      database,
		cfg:           cfg,
		appSettings:   appSettings,
		spinner:       sp,
		latestHC:      make(map[int64]db.HealthCheck),
		uptimePct:     make(map[int64]float64),
		serviceHosts:  make(map[string]db.ServiceHost),
		bannedHosts:   make(map[string]bool),
		svcFields:     svcFields,
		dnsFields:     dnsFields,
		templateFilter: func() textinput.Model {
			ti := textinput.New()
			ti.Placeholder = "filter templates…"
			ti.CharLimit = 64
			return ti
		}(),
		saltClient:   saltClient,
		masterMinion: cfg.Salt.MasterMinion,
		pillarBase:   pillarBase,
		stateBase:    stateBase,
		domain:       cfg.Globals.Domain,
	}
}

func (m ServicesModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, m.loadAll(), m.loadServiceHosts(), m.loadBannedHosts()}
	if m.trueNASClient != nil {
		cmds = append(cmds, m.loadTrueNAS())
	}
	return tea.Batch(cmds...)
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m ServicesModel) Update(msg tea.Msg) (ServicesModel, tea.Cmd) {
	if tickMsg, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(tickMsg)
		return m, cmd
	}

	// Per-tab message dispatch — each tab file implements updateXxx(msg) (ServicesModel, tea.Cmd, bool)
	if m2, cmd, ok := m.updateSvc(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.updateDNS(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.updateTrueNAS(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.updateHA(msg); ok {
		return m2, cmd
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		return m.handleKey(keyMsg)
	}

	// Window resize
	if wm, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wm.Width
		m.height = wm.Height
	}

	return m, nil
}

// ── handleKey ────────────────────────────────────────────────────────────────

func (m ServicesModel) handleKey(msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	key := msg.String()

	if m.dialog != svcDialogNone {
		return m.handleDialog(key)
	}
	if m.pickingTemplate {
		return m.handleTemplatePicker(key, msg)
	}
	if m.addingSvc {
		return m.handleSvcForm(key, msg)
	}
	if m.addingDNS {
		return m.handleDNSForm(key, msg)
	}

	// Tab switching with lazy-loading
	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
			return m, m.lazyLoadTab(m.tab)
		}
		return m, nil
	case "right", "l":
		if m.tab < svcTabCount-1 {
			m.tab++
			m.notice = ""
			return m, m.lazyLoadTab(m.tab)
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	}

	// Per-tab navigation — add a case here when you add a new tab file
	switch m.tab {
	case svcTabServices:
		return m.handleSvcTab(key)
	case svcTabDNS:
		return m.handleDNSTab(key)
	case svcTabCerts:
		return m.handleCertTab(key)
	case svcTabTrueNAS:
		return m.handleTrueNASTab(key)
	case svcTabHomeAssistant:
		return m.handleHATab(key)
	}
	return m, nil
}

// lazyLoadTab returns a command to load data for a tab the first time it is
// visited. Returns nil if the tab has already been loaded or needs no fetch.
func (m *ServicesModel) lazyLoadTab(tab servicesTab) tea.Cmd {
	switch tab {
	case svcTabDNS:
		if len(m.zones) == 0 && !m.dnsLoading {
			m.dnsLoading = true
			return tea.Batch(m.loadDNSZones(), m.pingCA())
		}
	case svcTabTrueNAS:
		if !m.trueNASChecked && !m.trueNASLoading {
			m.trueNASLoading = true
			return m.loadTrueNAS()
		}
	case svcTabHomeAssistant:
		if !m.haChecked && !m.haLoading {
			m.haLoading = true
			return m.loadHA()
		}
	}
	return nil
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m ServicesModel) View() string {
	var b strings.Builder

	b.WriteString(m.renderTabBar())
	b.WriteString("\n")

	contentHeight := m.height - 6
	if contentHeight < 4 {
		contentHeight = 4
	}

	// Add a case here when you add a new tab file
	switch m.tab {
	case svcTabServices:
		b.WriteString(m.viewServices(contentHeight))
	case svcTabDNS:
		b.WriteString(m.viewDNS(contentHeight))
	case svcTabCerts:
		b.WriteString(m.viewCerts(contentHeight))
	case svcTabTrueNAS:
		b.WriteString(m.viewTrueNAS(contentHeight))
	case svcTabHomeAssistant:
		b.WriteString(m.viewHomeAssistant(contentHeight))
	}

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

func (m ServicesModel) helpString() string {
	// Add a case here when you add a new tab file
	switch m.tab {
	case svcTabServices:
		return m.svcHelpString()
	case svcTabDNS:
		return m.dnsHelpString()
	case svcTabCerts:
		return m.certsHelpString()
	case svcTabTrueNAS:
		return m.trueNASHelpString()
	case svcTabHomeAssistant:
		return m.haHelpString()
	}
	return ""
}
