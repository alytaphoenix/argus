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

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/config"
	"github.com/alytaphoenix/ratataskr/db"
)

// metricsTab is one panel in the metrics screen.
type metricsTab int

const (
	metricsTabOverview    metricsTab = iota // always from DB
	metricsTabProxmox                       // pve-exporter via Prometheus
	metricsTabNomad                         // Nomad native + Prometheus
	metricsTabSalt                          // ratataskr DB + Prometheus
	metricsTabAlerts                        // Prometheus alerting rules
	metricsTabScrapeConfig                  // generated YAML
	metricsTabLoki                          // Grafana Loki log browser
)

const metricsTabCount = int(metricsTabLoki) + 1

// promQueryDef is a named PromQL expression shown in a Prometheus panel.
type promQueryDef struct {
	name string
	expr string
	unit string // "%", "MiB", "cores", "", etc.
	max  float64 // denominator for the progress bar; 0 = auto-scale
}

var proxmoxQueries = []promQueryDef{
	{"Node CPU %", `pve_cpu_usage_ratio * 100`, "%", 100},
	{"Node Mem %", `pve_memory_usage_bytes / pve_memory_size_bytes * 100`, "%", 100},
	{"Running VMs", `count(pve_up{id=~"qemu/.*"} == 1)`, "", 0},
	{"Stopped VMs", `count(pve_up{id=~"qemu/.*"} == 0)`, "", 0},
	{"Running LXCs", `count(pve_up{id=~"lxc/.*"} == 1)`, "", 0},
	{"Node storage %", `pve_disk_usage_bytes{id=~"node/.*"} / pve_disk_size_bytes{id=~"node/.*"} * 100`, "%", 100},
}

var nomadQueries = []promQueryDef{
	{"Running allocs", `sum(nomad_nomad_job_summary_running)`, "", 0},
	{"Failed allocs", `sum(nomad_nomad_job_summary_failed)`, "", 0},
	{"Lost allocs", `sum(nomad_nomad_job_summary_lost)`, "", 0},
	{"CPU alloc'd %", `nomad_client_allocated_cpu / nomad_client_unallocated_cpu * 100`, "%", 100},
	{"Mem alloc'd %", `nomad_client_allocated_memory / nomad_client_unallocated_memory * 100`, "%", 100},
	{"Scheduler RPC/s", `rate(nomad_nomad_rpc_request_total[5m])`, "rpc/s", 0},
}

var saltQueries = []promQueryDef{
	{"Online minions", `salt_minion_up`, "", 0},
	{"Highstate runs (1h)", `increase(salt_function_calls_total{function="state.highstate"}[1h])`, "", 0},
	{"Failed functions (1h)", `increase(salt_function_responses_total{success="false"}[1h])`, "", 0},
	{"Ratataskr applies (24h)", `ratataskr_salt_applies_24h_total`, "", 0},
	{"Ratataskr success %", `ratataskr_salt_apply_success_rate * 100`, "%", 100},
}

// promPanelResult holds the loaded samples for one query.
type promPanelResult struct {
	def     promQueryDef
	samples []api.PromSample
	err     error
	loaded  bool
}

// metricsAlertsLoadedMsg carries Prometheus alert rules.
type metricsAlertsLoadedMsg struct {
	rules []api.PromAlertRule
	err   error
}

// lokiLoadedMsg carries Loki log results.
type lokiLoadedMsg struct {
	entries []api.LokiEntry
	err     error
}

// MetricsModel is the TUI screen for metrics.
type MetricsModel struct {
	db         *db.DB
	promClient *api.PrometheusClient
	lokiClient *api.LokiClient
	cfg        config.Config

	tab      metricsTab
	snapshot *db.MetricsSnapshot
	dbErr    error
	dbLoaded bool

	// Prometheus panels: tab → []result (one per query def)
	panels  map[metricsTab][]promPanelResult
	loading map[metricsTab]bool

	// Alert rules
	alertRules       []api.PromAlertRule
	alertRulesLoaded bool
	alertRulesErr    error

	// Custom query input (active on Proxmox/Nomad/Salt tabs)
	customInput  textinput.Model
	customActive bool
	customResult []api.PromSample
	customErr    error

	// Scrape config text
	scrapeConfig string

	// Scroll offset for scrape config tab
	scrapeScroll int

	// Loki log browser
	lokiInput   textinput.Model
	lokiActive  bool // true = text input has focus
	lokiEntries []api.LokiEntry
	lokiErr     error
	lokiLoading bool
	lokiScroll  int

	spinner spinner.Model
	width   int
	height  int
}

func NewMetricsModel(database *db.DB, promClient *api.PrometheusClient, lokiClient *api.LokiClient, cfg config.Config) MetricsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Placeholder = "PromQL expression"
	ti.CharLimit = 512

	lokiTI := textinput.New()
	lokiTI.Placeholder = `{job="myapp"} |= "error"`
	lokiTI.CharLimit = 512

	m := MetricsModel{
		db:           database,
		promClient:   promClient,
		lokiClient:   lokiClient,
		cfg:          cfg,
		spinner:      s,
		customInput:  ti,
		lokiInput:    lokiTI,
		panels:       make(map[metricsTab][]promPanelResult),
		loading:      make(map[metricsTab]bool),
		scrapeConfig: generateScrapeConfigUI(cfg),
	}
	return m
}

func (m MetricsModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadDB(), m.loadProm(metricsTabOverview))
}

// loadDB fetches DB-derived metrics and returns metricsDBLoadedMsg.
func (m MetricsModel) loadDB() tea.Cmd {
	database := m.db
	return func() tea.Msg {
		if database == nil {
			return metricsDBLoadedMsg{err: fmt.Errorf("database not available")}
		}
		snap, err := database.ComputeMetrics()
		return metricsDBLoadedMsg{snapshot: snap, err: err}
	}
}

// loadProm fetches all Prometheus queries for a given tab.
func (m MetricsModel) loadProm(tab metricsTab) tea.Cmd {
	client := m.promClient
	queries := queriesForTab(tab)
	if client == nil || len(queries) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		results := make([]api.PromSample, 0)
		// For the metricsPromLoadedMsg we send all samples tagged with a query
		// index embedded in a synthetic "__qidx__" label — the receiver unpacks them.
		for i, q := range queries {
			samples, err := client.Query(ctx, q.expr)
			if err != nil {
				// Store error as a sentinel sample with no labels and NaN-ish value.
				results = append(results, api.PromSample{
					Labels: map[string]string{"__qidx__": fmt.Sprintf("%d", i), "__error__": err.Error()},
					Value:  -1,
				})
				continue
			}
			for _, s := range samples {
				if s.Labels == nil {
					s.Labels = map[string]string{}
				}
				s.Labels["__qidx__"] = fmt.Sprintf("%d", i)
				results = append(results, s)
			}
		}
		return metricsPromLoadedMsg{tab: int(tab), results: results}
	}
}

// runLokiQuery queries Loki for the last hour of logs matching the given LogQL expression.
func (m MetricsModel) runLokiQuery(expr string) tea.Cmd {
	client := m.lokiClient
	return func() tea.Msg {
		if client == nil {
			return lokiLoadedMsg{err: fmt.Errorf("Loki not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		end := time.Now()
		start := end.Add(-1 * time.Hour)
		entries, err := client.QueryRange(ctx, expr, start, end, 200)
		return lokiLoadedMsg{entries: entries, err: err}
	}
}

// runCustomQuery sends a one-off PromQL query.
func (m MetricsModel) runCustomQuery(expr string) tea.Cmd {
	client := m.promClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		samples, err := client.Query(ctx, expr)
		return metricsPromLoadedMsg{tab: -1, results: samples, err: err}
	}
}

func queriesForTab(tab metricsTab) []promQueryDef {
	switch tab {
	case metricsTabProxmox:
		return proxmoxQueries
	case metricsTabNomad:
		return nomadQueries
	case metricsTabSalt:
		return saltQueries
	}
	return nil
}

func (m MetricsModel) Update(msg tea.Msg) (MetricsModel, tea.Cmd) {
	// Loki query input intercept
	if m.lokiActive {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.lokiActive = false
				m.lokiInput.Blur()
				return m, nil
			case "enter":
				expr := strings.TrimSpace(m.lokiInput.Value())
				m.lokiActive = false
				m.lokiInput.Blur()
				if expr != "" {
					m.lokiLoading = true
					m.lokiEntries = nil
					m.lokiErr = nil
					return m, m.runLokiQuery(expr)
				}
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.lokiInput, cmd = m.lokiInput.Update(msg)
		return m, cmd
	}

	// Custom query input
	if m.customActive {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.customActive = false
				return m, nil
			case "enter":
				expr := strings.TrimSpace(m.customInput.Value())
				if expr != "" && m.promClient != nil {
					m.customResult = nil
					m.customErr = nil
					return m, m.runCustomQuery(expr)
				}
				m.customActive = false
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.customInput, cmd = m.customInput.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case metricsDBLoadedMsg:
		m.dbLoaded = true
		m.snapshot = msg.snapshot
		m.dbErr = msg.err
		return m, nil

	case metricsAlertsLoadedMsg:
		m.alertRulesLoaded = true
		m.alertRulesErr = msg.err
		if msg.err == nil {
			m.alertRules = msg.rules
		}
		return m, nil

	case lokiLoadedMsg:
		m.lokiLoading = false
		m.lokiErr = msg.err
		if msg.err == nil {
			m.lokiEntries = msg.entries
			m.lokiScroll = 0
		}
		return m, nil

	case metricsPromLoadedMsg:
		if msg.tab == -1 {
			// Custom query result
			m.customResult = msg.results
			m.customErr = msg.err
			return m, nil
		}
		t := metricsTab(msg.tab)
		m.loading[t] = false
		queries := queriesForTab(t)
		results := make([]promPanelResult, len(queries))
		for i, q := range queries {
			results[i] = promPanelResult{def: q, loaded: true}
		}
		for _, s := range msg.results {
			idxStr := s.Labels["__qidx__"]
			idx := 0
			fmt.Sscanf(idxStr, "%d", &idx) //nolint:errcheck
			if idx < 0 || idx >= len(results) {
				continue
			}
			if errStr, ok := s.Labels["__error__"]; ok {
				results[idx].err = fmt.Errorf("%s", errStr)
				continue
			}
			// Strip internal labels before appending.
			clean := api.PromSample{Labels: make(map[string]string), Value: s.Value, Time: s.Time}
			for k, v := range s.Labels {
				if k != "__qidx__" && k != "__error__" {
					clean.Labels[k] = v
				}
			}
			results[idx].samples = append(results[idx].samples, clean)
		}
		m.panels[t] = results
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			return m, func() tea.Msg { return backMsg{} }
		case "tab":
			m.tab = (m.tab + 1) % metricsTab(metricsTabCount)
			return m, m.enterTab(m.tab)
		case "shift+tab":
			m.tab = (m.tab + metricsTab(metricsTabCount-1)) % metricsTab(metricsTabCount)
			return m, m.enterTab(m.tab)
		case "R":
			if m.tab == metricsTabLoki {
				expr := strings.TrimSpace(m.lokiInput.Value())
				if expr != "" {
					m.lokiLoading = true
					m.lokiEntries = nil
					m.lokiErr = nil
					return m, m.runLokiQuery(expr)
				}
			} else {
				m.dbLoaded = false
				m.loading[m.tab] = true
				return m, tea.Batch(m.loadDB(), m.loadProm(m.tab))
			}
		case "c", "C":
			if m.promClient != nil && m.tab != metricsTabScrapeConfig && m.tab != metricsTabOverview {
				m.customActive = true
				m.customInput.SetValue("")
				m.customInput.Focus()
				return m, textinput.Blink
			}
		case "up", "k":
			if m.tab == metricsTabScrapeConfig && m.scrapeScroll > 0 {
				m.scrapeScroll--
			}
			if m.tab == metricsTabLoki && m.lokiScroll > 0 {
				m.lokiScroll--
			}
		case "down", "j":
			if m.tab == metricsTabScrapeConfig {
				m.scrapeScroll++
			}
			if m.tab == metricsTabLoki && m.lokiScroll < len(m.lokiEntries)-1 {
				m.lokiScroll++
			}
		case "f":
			if m.tab == metricsTabLoki {
				m.lokiActive = true
				m.lokiInput.Focus()
				return m, textinput.Blink
			}
		}
	}
	return m, nil
}

func (m MetricsModel) loadAlertRules() tea.Cmd {
	client := m.promClient
	return func() tea.Msg {
		if client == nil {
			return metricsAlertsLoadedMsg{err: fmt.Errorf("Prometheus not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rules, err := client.AlertRules(ctx)
		return metricsAlertsLoadedMsg{rules: rules, err: err}
	}
}

// enterTab fires data loading when a tab is first visited.
func (m *MetricsModel) enterTab(tab metricsTab) tea.Cmd {
	if tab == metricsTabScrapeConfig || tab == metricsTabOverview || tab == metricsTabLoki {
		return nil
	}
	if tab == metricsTabAlerts {
		if !m.alertRulesLoaded {
			return m.loadAlertRules()
		}
		return nil
	}
	if _, loaded := m.panels[tab]; loaded {
		return nil // already have data for this tab
	}
	m.loading[tab] = true
	return m.loadProm(tab)
}

func (m MetricsModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ ratataskr") +
		styleDim.Render(" › ") +
		styleBold.Render("metrics") + "\n\n")

	// Tab bar
	tabs := []string{"Overview", "Proxmox", "Nomad", "Salt", "Alerts", "Scrape Config", "Loki"}
	for i, t := range tabs {
		if metricsTab(i) == m.tab {
			sb.WriteString(styleTabActive.Render("▸ " + t))
		} else {
			sb.WriteString(styleTabInactive.Render("  " + t))
		}
		if i < len(tabs)-1 {
			sb.WriteString(styleDim.Render("  │  "))
		}
	}
	sb.WriteString("\n" + styleDim.Render(strings.Repeat("─", max(m.width-4, 40))) + "\n\n")

	// Custom query input overlay
	if m.customActive {
		sb.WriteString(styleBold.Render("  Custom PromQL query") + "\n\n")
		sb.WriteString(fmt.Sprintf("  %-10s %s\n", styleDim.Render("Expression"), m.customInput.View()))
		sb.WriteString("\n" + styleHelp.Render("  enter run  esc cancel") + "\n")
		if m.customErr != nil {
			sb.WriteString(styleError.Render("  error: "+m.customErr.Error()) + "\n")
		}
		if len(m.customResult) > 0 {
			sb.WriteString("\n" + styleBold.Render("  Results") + "\n")
			sb.WriteString(m.renderSamples(m.customResult, 0, 50) + "\n")
		}
		return sb.String()
	}

	switch m.tab {
	case metricsTabOverview:
		sb.WriteString(m.overviewView())
	case metricsTabProxmox:
		sb.WriteString(m.promPanelView("Proxmox  (pve-exporter + Prometheus)", metricsTabProxmox))
	case metricsTabNomad:
		sb.WriteString(m.promPanelView("Nomad  (native metrics + Prometheus)", metricsTabNomad))
	case metricsTabSalt:
		sb.WriteString(m.saltPanelView())
	case metricsTabAlerts:
		sb.WriteString(m.alertsView())
	case metricsTabScrapeConfig:
		sb.WriteString(m.scrapeConfigView())
	case metricsTabLoki:
		sb.WriteString(m.lokiView())
	}

	sb.WriteString("\n")
	helpLine := "  tab next  shift+tab prev  R refresh  b back  q quit"
	switch m.tab {
	case metricsTabScrapeConfig:
		helpLine = "  ↑/↓ scroll  tab next  b back  q quit"
	case metricsTabLoki:
		helpLine = "  f query  ↑/↓ scroll  R re-run  tab next  b back  q quit"
	default:
		if m.promClient != nil {
			helpLine = "  tab next  shift+tab prev  c custom query  R refresh  b back  q quit"
		}
	}
	sb.WriteString(styleHelp.Render(helpLine) + "\n")
	return sb.String()
}

// ── Tab views ────────────────────────────────────────────────────────────────

func (m MetricsModel) overviewView() string {
	var sb strings.Builder

	if !m.dbLoaded {
		sb.WriteString("  " + m.spinner.View() + " loading…\n")
		return sb.String()
	}
	if m.dbErr != nil {
		sb.WriteString(styleError.Render("  "+m.dbErr.Error()) + "\n")
		return sb.String()
	}
	s := m.snapshot

	section := func(title string) {
		sb.WriteString(styleBold.Render("  "+title) + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-6, 50))) + "\n")
	}

	barWidth := 28

	// ── VMs ──────────────────────────────────────────────────────────────
	section("Provisioned VMs")
	sb.WriteString(fmt.Sprintf("  %-22s %d\n", styleDim.Render("Total tracked"), s.ProvisionedTotal))

	// Sort statuses for deterministic output
	statuses := make([]string, 0, len(s.VMsByStatus))
	for k := range s.VMsByStatus {
		statuses = append(statuses, k)
	}
	sort.Strings(statuses)
	for _, status := range statuses {
		n := s.VMsByStatus[status]
		pct := 0.0
		if s.ProvisionedTotal > 0 {
			pct = float64(n) / float64(s.ProvisionedTotal)
		}
		bar := progressBar(pct, barWidth)
		sb.WriteString(fmt.Sprintf("  %-22s %4d  %s  %4.0f%%\n",
			styleDim.Render(status), n, bar, pct*100))
	}

	// ── Salt ─────────────────────────────────────────────────────────────
	sb.WriteString("\n")
	section("Salt (last 24 h)")
	sb.WriteString(fmt.Sprintf("  %-22s %d\n", styleDim.Render("Applies"), s.SaltApplies24h))
	if s.SaltApplies24h > 0 {
		successRate := float64(s.SaltSuccesses24h) / float64(s.SaltApplies24h)
		failures := s.SaltApplies24h - s.SaltSuccesses24h
		sb.WriteString(fmt.Sprintf("  %-22s %4d  %s  %4.0f%%\n",
			styleDim.Render("Success"), s.SaltSuccesses24h,
			progressBar(successRate, barWidth), successRate*100))
		sb.WriteString(fmt.Sprintf("  %-22s %4d\n", styleDim.Render("Failures"), failures))
	}

	// Most active minions (up to 5)
	if len(s.SaltMinionActivity) > 0 {
		type kv struct {
			k string
			v int
		}
		var pairs []kv
		for k, v := range s.SaltMinionActivity {
			pairs = append(pairs, kv{k, v})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })
		if len(pairs) > 5 {
			pairs = pairs[:5]
		}
		sb.WriteString(fmt.Sprintf("  %-22s", styleDim.Render("Most active")))
		for i, p := range pairs {
			if i > 0 {
				sb.WriteString(styleDim.Render(", "))
			}
			sb.WriteString(fmt.Sprintf("%s (%d)", truncate(p.k, 16), p.v))
		}
		sb.WriteString("\n")
	}

	// ── Nomad ─────────────────────────────────────────────────────────────
	sb.WriteString("\n")
	section("Nomad")
	total := s.NomadJobsRunning + s.NomadJobsDead + s.NomadJobsPending
	sb.WriteString(fmt.Sprintf("  %-22s %d\n", styleDim.Render("Jobs tracked"), total))
	if total > 0 {
		runPct := float64(s.NomadJobsRunning) / float64(total)
		sb.WriteString(fmt.Sprintf("  %-22s %4d  %s  %4.0f%%\n",
			styleDim.Render("Running"), s.NomadJobsRunning,
			progressBar(runPct, barWidth), runPct*100))
		sb.WriteString(fmt.Sprintf("  %-22s %4d\n",
			styleDim.Render("Dead"), s.NomadJobsDead))
		if s.NomadJobsPending > 0 {
			sb.WriteString(fmt.Sprintf("  %-22s %4d\n",
				styleDim.Render("Pending"), s.NomadJobsPending))
		}
	}
	sb.WriteString(fmt.Sprintf("  %-22s %d\n", styleDim.Render("Events today"), s.NomadEventsToday))

	// ── Top profiles ──────────────────────────────────────────────────────
	if len(s.TopCIProfiles) > 0 || len(s.TopSaltProfiles) > 0 {
		sb.WriteString("\n")
		section("Top profiles")
		if len(s.TopCIProfiles) > 0 {
			sb.WriteString("  " + styleDim.Render("Cloud-init   "))
			for i, p := range s.TopCIProfiles {
				if i > 0 {
					sb.WriteString(styleDim.Render("  ·  "))
				}
				sb.WriteString(fmt.Sprintf("%s %s", truncate(p.Name, 18),
					styleDim.Render(fmt.Sprintf("(%d VMs)", p.Count))))
			}
			sb.WriteString("\n")
		}
		if len(s.TopSaltProfiles) > 0 {
			sb.WriteString("  " + styleDim.Render("Salt         "))
			for i, p := range s.TopSaltProfiles {
				if i > 0 {
					sb.WriteString(styleDim.Render("  ·  "))
				}
				sb.WriteString(fmt.Sprintf("%s %s", truncate(p.Name, 18),
					styleDim.Render(fmt.Sprintf("(%d runs)", p.Count))))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n" + styleDim.Render(fmt.Sprintf("  computed %s", s.ComputedAt.Format("15:04:05"))))
	if m.promClient != nil {
		sb.WriteString(styleDim.Render("  ·  Prometheus connected"))
	} else {
		sb.WriteString(styleDim.Render("  ·  Prometheus not configured"))
	}
	sb.WriteString("\n")
	return sb.String()
}

func (m MetricsModel) promPanelView(title string, tab metricsTab) string {
	var sb strings.Builder

	sb.WriteString(styleBold.Render("  "+title) + "\n\n")

	if m.promClient == nil {
		sb.WriteString(styleDim.Render("  Prometheus is not configured.\n"))
		sb.WriteString(styleDim.Render("  Add [prometheus] url = \"http://localhost:9090\" to config.toml.\n"))
		return sb.String()
	}

	if m.loading[tab] {
		sb.WriteString("  " + m.spinner.View() + " querying Prometheus…\n")
		return sb.String()
	}

	results, ok := m.panels[tab]
	if !ok {
		sb.WriteString(styleDim.Render("  press R to load") + "\n")
		return sb.String()
	}

	queries := queriesForTab(tab)
	barWidth := max(m.width/2-30, 20)

	for i, r := range results {
		if i >= len(queries) {
			break
		}
		q := queries[i]
		sb.WriteString(styleBold.Render("  "+q.name) +
			styleDim.Render("  "+q.expr) + "\n")

		if r.err != nil {
			sb.WriteString(styleError.Render("    error: "+r.err.Error()) + "\n\n")
			continue
		}
		if !r.loaded || len(r.samples) == 0 {
			sb.WriteString(styleDim.Render("    (no data)") + "\n\n")
			continue
		}
		sb.WriteString(m.renderSamples(r.samples, q.max, barWidth))
		sb.WriteString("\n")
	}

	return sb.String()
}

// saltPanelView shows DB-derived salt metrics plus optional Prometheus queries.
func (m MetricsModel) saltPanelView() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("  Salt  (ratataskr DB + Prometheus)") + "\n\n")

	// Always show DB stats first
	if m.dbLoaded && m.snapshot != nil {
		s := m.snapshot
		barWidth := max(m.width/2-30, 20)

		sb.WriteString(styleDim.Render("  From ratataskr DB") + "\n")
		sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-6, 40))) + "\n")
		sb.WriteString(fmt.Sprintf("  %-28s %d\n", "Applies (24h)", s.SaltApplies24h))

		if s.SaltApplies24h > 0 {
			rate := float64(s.SaltSuccesses24h) / float64(s.SaltApplies24h)
			sb.WriteString(fmt.Sprintf("  %-28s %4d  %s  %4.0f%%\n",
				"Successes", s.SaltSuccesses24h, progressBar(rate, barWidth), rate*100))
			sb.WriteString(fmt.Sprintf("  %-28s %4d\n",
				"Failures", s.SaltApplies24h-s.SaltSuccesses24h))
		}

		// Per-minion breakdown
		if len(s.SaltMinionActivity) > 0 {
			sb.WriteString("\n  " + styleDim.Render("Per-minion (24h)") + "\n")
			type kv struct {
				k string
				v int
			}
			var pairs []kv
			for k, v := range s.SaltMinionActivity {
				pairs = append(pairs, kv{k, v})
			}
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })
			for _, p := range pairs {
				sb.WriteString(fmt.Sprintf("    %-28s %d\n", truncate(p.k, 28), p.v))
			}
		}
		sb.WriteString("\n")
	}

	// Prometheus section
	if m.promClient == nil {
		sb.WriteString(styleDim.Render("  Prometheus not configured — add [prometheus] url to config.toml for salt-exporter metrics.") + "\n")
		return sb.String()
	}

	sb.WriteString(styleDim.Render("  From Prometheus (salt-exporter)") + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-6, 40))) + "\n")

	if m.loading[metricsTabSalt] {
		sb.WriteString("  " + m.spinner.View() + " querying…\n")
		return sb.String()
	}

	results, ok := m.panels[metricsTabSalt]
	if !ok {
		sb.WriteString(styleDim.Render("  press R to load") + "\n")
		return sb.String()
	}

	barWidth := max(m.width/2-30, 20)
	for i, r := range results {
		if i >= len(saltQueries) {
			break
		}
		q := saltQueries[i]
		sb.WriteString(styleBold.Render("  "+q.name) + "\n")
		if r.err != nil {
			sb.WriteString(styleError.Render("    error: "+r.err.Error()) + "\n\n")
			continue
		}
		if len(r.samples) == 0 {
			sb.WriteString(styleDim.Render("    (no data)") + "\n\n")
			continue
		}
		sb.WriteString(m.renderSamples(r.samples, q.max, barWidth))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m MetricsModel) alertsView() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("  Prometheus Alert Rules") + "\n\n")

	if m.promClient == nil {
		sb.WriteString(styleDim.Render("  Prometheus not configured.\n"))
		return sb.String()
	}
	if !m.alertRulesLoaded {
		sb.WriteString("  " + m.spinner.View() + " loading…\n")
		return sb.String()
	}
	if m.alertRulesErr != nil {
		sb.WriteString(styleError.Render("  error: "+m.alertRulesErr.Error()) + "\n")
		return sb.String()
	}
	if len(m.alertRules) == 0 {
		sb.WriteString(styleDim.Render("  No alert rules configured in Prometheus.\n"))
		return sb.String()
	}

	sb.WriteString(styleSubtle.Render(fmt.Sprintf("  %-30s %-10s %-10s  %s\n", "Name", "State", "Severity", "Summary")))
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 70))) + "\n")

	for _, r := range m.alertRules {
		stateStyle := styleDim
		switch r.State {
		case "firing":
			stateStyle = styleError
		case "pending":
			stateStyle = stylePaused
		}
		line := fmt.Sprintf("  %-30s %-10s %-10s  %s",
			truncate(r.Name, 30),
			stateStyle.Render(r.State),
			truncate(r.Severity, 10),
			truncate(r.Summary, 60))
		sb.WriteString(line + "\n")
	}

	firing := 0
	pending := 0
	for _, r := range m.alertRules {
		if r.State == "firing" {
			firing++
		} else if r.State == "pending" {
			pending++
		}
	}
	sb.WriteString(fmt.Sprintf("\n  Total: %d rules  ", len(m.alertRules)))
	if firing > 0 {
		sb.WriteString(styleError.Render(fmt.Sprintf("%d firing  ", firing)))
	}
	if pending > 0 {
		sb.WriteString(stylePaused.Render(fmt.Sprintf("%d pending", pending)))
	}
	sb.WriteString("\n")
	return sb.String()
}

func (m MetricsModel) lokiView() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("  Loki Log Browser") + "\n\n")

	if m.lokiClient == nil {
		sb.WriteString(styleDim.Render("  Loki not configured. Add [loki] url = \"http://loki:3100\" to config.toml.\n"))
		return sb.String()
	}

	sb.WriteString(styleDim.Render("  Query (LogQL): ") + m.lokiInput.View() + "\n")
	sb.WriteString(styleDim.Render("  f to edit query  R to re-run  ↑/↓ scroll") + "\n\n")

	if m.lokiLoading {
		sb.WriteString("  " + m.spinner.View() + " querying Loki…\n")
		return sb.String()
	}
	if m.lokiErr != nil {
		sb.WriteString(styleError.Render("  error: "+m.lokiErr.Error()) + "\n")
		return sb.String()
	}
	if len(m.lokiEntries) == 0 {
		sb.WriteString(styleDim.Render("  No results. Press f to enter a LogQL query, then enter to run.\n"))
		return sb.String()
	}

	visible := m.height - 14
	if visible < 5 {
		visible = 5
	}
	start := m.lokiScroll
	if start > len(m.lokiEntries)-1 {
		start = len(m.lokiEntries) - 1
	}
	end := start + visible
	if end > len(m.lokiEntries) {
		end = len(m.lokiEntries)
	}

	for _, e := range m.lokiEntries[start:end] {
		ts := e.Timestamp.Format("01-02 15:04:05")
		line := truncate(e.Line, m.width-20)
		sb.WriteString(fmt.Sprintf("  %s  %s\n", styleDim.Render(ts), line))
	}
	sb.WriteString(styleDim.Render(fmt.Sprintf("\n  %d/%d entries", start+1, len(m.lokiEntries))) + "\n")
	return sb.String()
}

func (m MetricsModel) scrapeConfigView() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("  Generated Prometheus scrape config") + "\n\n")

	lines := strings.Split(m.scrapeConfig, "\n")
	visible := m.height - 10
	if visible < 5 {
		visible = 5
	}

	start := m.scrapeScroll
	if start > len(lines)-1 {
		start = len(lines) - 1
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}

	for _, line := range lines[start:end] {
		if strings.HasPrefix(line, "#") {
			sb.WriteString("  " + styleDim.Render(line) + "\n")
		} else {
			sb.WriteString("  " + line + "\n")
		}
	}

	if len(lines) > visible {
		pct := float64(start) / float64(len(lines)-visible)
		sb.WriteString("\n  " + styleDim.Render(fmt.Sprintf("line %d/%d  %s",
			start+1, len(lines), progressBar(pct, 20))) + "\n")
	}
	return sb.String()
}

// renderSamples formats a slice of PromSamples as a table with a progress bar.
func (m MetricsModel) renderSamples(samples []api.PromSample, maxVal float64, barWidth int) string {
	var sb strings.Builder

	// Auto-scale: find max value
	autoMax := maxVal
	if autoMax == 0 {
		for _, s := range samples {
			if s.Value > autoMax {
				autoMax = s.Value
			}
		}
	}

	for _, s := range samples {
		label := s.LabelString("__name__", "job", "instance")
		if label == "(no labels)" {
			// For scalar results use the metric name
			if name, ok := s.Labels["__name__"]; ok {
				label = name
			}
		}

		bar := ""
		if autoMax > 0 {
			bar = "  " + progressBar(s.Value/autoMax, barWidth)
		}

		unitSuffix := ""
		val := s.Value
		// Auto-format large numbers
		if val >= 1e9 {
			unitSuffix = "G"
			val /= 1e9
		} else if val >= 1e6 {
			unitSuffix = "M"
			val /= 1e6
		} else if val >= 1e3 {
			unitSuffix = "K"
			val /= 1e3
		}

		valStr := fmt.Sprintf("%.2f%s", val, unitSuffix)

		sb.WriteString(fmt.Sprintf("    %-32s  %8s%s\n",
			truncate(label, 32), valStr, bar))
	}
	return sb.String()
}

// generateScrapeConfigUI is the UI-side version (no cmd dependency).
func generateScrapeConfigUI(cfg config.Config) string {
	var sb strings.Builder

	listen := cfg.Prometheus.Listen
	if listen == "" {
		listen = ":9099 (not started — set [prometheus] listen in config.toml)"
	}
	host := "localhost" + listen
	if i := strings.Index(listen, " "); i > 0 {
		host = "localhost" + listen[:i]
	}

	pveHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Host, "https://"), "http://")
	pveHost = strings.SplitN(pveHost, "/", 2)[0]

	sb.WriteString("# Prometheus scrape configuration for ratataskr homelab\n")
	sb.WriteString("# Copy into your prometheus.yml under scrape_configs:\n\n")

	sb.WriteString("scrape_configs:\n\n")

	sb.WriteString("  # ── Ratataskr metrics server ────────────────────────────────────\n")
	sb.WriteString("  # Exposes: provisioned VMs, salt apply rates, nomad job history\n")
	sb.WriteString("  # Start:   ratataskr-metrics-server   (built with: make build-metrics)\n")
	sb.WriteString("  - job_name: ratataskr\n")
	sb.WriteString("    static_configs:\n")
	sb.WriteString("      - targets: [" + host + "]\n\n")

	sb.WriteString("  # ── Proxmox (pve-exporter) ────────────────────────────────────\n")
	sb.WriteString("  # Install: pip install prometheus-pve-exporter\n")
	sb.WriteString("  # Config:  https://github.com/prometheus-pve/prometheus-pve-exporter\n")
	sb.WriteString("  # Metrics: node CPU/mem/disk, VM status, storage\n")
	sb.WriteString("  - job_name: proxmox\n")
	sb.WriteString("    metrics_path: /pve\n")
	sb.WriteString("    params:\n")
	sb.WriteString("      module: [default]\n")
	sb.WriteString("      cluster: ['1']\n")
	sb.WriteString("      node: ['1']\n")
	sb.WriteString("    static_configs:\n")
	sb.WriteString("      - targets: [" + pveHost + "]\n")
	sb.WriteString("    relabel_configs:\n")
	sb.WriteString("      - source_labels: [__address__]\n")
	sb.WriteString("        target_label: __param_target\n")
	sb.WriteString("      - source_labels: [__param_target]\n")
	sb.WriteString("        target_label: instance\n")
	sb.WriteString("      - target_label: __address__\n")
	sb.WriteString("        replacement: pve-exporter:9221\n\n")
	sb.WriteString("  # pve-exporter config (~/.config/pve-exporter/pve.yml):\n")
	sb.WriteString("  #   default:\n")
	sb.WriteString("  #     user: " + cfg.TokenID + "\n")
	sb.WriteString("  #     token_name: <token-name>\n")
	sb.WriteString("  #     token_value: <secret>\n")
	sb.WriteString("  #     verify_ssl: false\n\n")

	if cfg.Nomad.URL != "" {
		nomadHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Nomad.URL, "https://"), "http://")
		nomadHost = strings.TrimRight(nomadHost, "/")
		sb.WriteString("  # ── Nomad (built-in metrics) ───────────────────────────────\n")
		sb.WriteString("  # No exporter needed — Nomad exports Prometheus natively at /v1/metrics\n")
		sb.WriteString("  # Metrics: alloc counts, scheduler latency, client resources\n")
		sb.WriteString("  - job_name: nomad\n")
		sb.WriteString("    metrics_path: /v1/metrics\n")
		sb.WriteString("    params:\n")
		sb.WriteString("      format: [prometheus]\n")
		sb.WriteString("    static_configs:\n")
		sb.WriteString("      - targets: [" + nomadHost + "]\n")
		if cfg.Nomad.Token != "" {
			sb.WriteString("    authorization:\n")
			sb.WriteString("      credentials: " + cfg.Nomad.Token + "\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("  # ── Nomad ──────────────────────────────────────────────────\n")
		sb.WriteString("  # Not configured. Add [nomad] url = \"http://nomad:4646\" to config.toml.\n\n")
	}

	if cfg.Salt.URL != "" {
		saltHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Salt.URL, "https://"), "http://")
		saltHost = strings.SplitN(saltHost, ":", 2)[0]
		sb.WriteString("  # ── Salt (salt-exporter) ───────────────────────────────────\n")
		sb.WriteString("  # Install: go install github.com/kpetremann/salt-exporter@latest\n")
		sb.WriteString("  # Metrics: minion online/offline, function call rates, job counts\n")
		sb.WriteString("  - job_name: salt\n")
		sb.WriteString("    static_configs:\n")
		sb.WriteString("      - targets: [" + saltHost + ":2112]\n\n")
	} else {
		sb.WriteString("  # ── Salt ───────────────────────────────────────────────────\n")
		sb.WriteString("  # Not configured. Add [salt] url = \"https://salt:8080\" to config.toml.\n\n")
	}

	sb.WriteString("# ──────────────────────────────────────────────────────────────────\n")
	sb.WriteString("# Grafana dashboards:\n")
	sb.WriteString("#   Proxmox:  grafana.com/grafana/dashboards/10347\n")
	sb.WriteString("#   Nomad:    grafana.com/grafana/dashboards/13816\n")
	sb.WriteString("#   Salt:     use salt-exporter README for dashboard JSON\n")

	return sb.String()
}
