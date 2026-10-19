package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	gkr "github.com/alytaphoenix/argus/keyring"
	"github.com/alytaphoenix/argus/ui"
)

// version is overridden by GoReleaser via -ldflags "-X main.version=...".
var version = "dev"

const usage = `argus — Bubble Tea TUI for managing a Proxmox homelab

Usage:
  argus              Launch the TUI (first-run setup runs automatically if unconfigured)
  argus config       Launch the interactive config editor
  argus --version    Print the version
  argus --help       Show this help

Config file: ~/.config/argus/config.toml (override with the ARGUS_CONFIG env var)
See config.toml.example in the repo for all available settings.`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			fmt.Println(usage)
			os.Exit(0)
		case "-v", "--version", "version":
			fmt.Println("argus " + version)
			os.Exit(0)
		case "config":
			runConfigEditor()
		default:
			fmt.Fprintf(os.Stderr, "argus: unknown command %q\n\n%s\n", os.Args[1], usage)
			os.Exit(2)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// First-run setup: show the Proxmox config screen instead of bailing out.
	// On success, the main app launches straight into the Bootstrap wizard so
	// the rest of the homelab (Salt, DNS, certs, TrueNAS, Plex, ...) gets set
	// up in the same sitting instead of requiring a second manual pass.
	guidedSetup := false
	if verr := cfg.Validate(); verr != nil {
		done, err := ui.RunSetup(verr.Error())
		if err != nil {
			fmt.Fprintf(os.Stderr, "setup error: %v\n", err)
			os.Exit(1)
		}
		if !done {
			fmt.Fprintln(os.Stderr, "setup aborted")
			os.Exit(0) // deliberate user cancellation, not a program error
		}
		if cfg, err = loadConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "config error: %v\n", err)
			os.Exit(1)
		}
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "config error: %v\n", err)
			os.Exit(1)
		}
		guidedSetup = true
	}

	sshPassword, _ := gkr.Load(gkr.KeySSHPassword)

	proxmoxClient := api.NewClient(cfg)

	var saltClient *api.SaltClient
	if cfg.Salt.URL != "" {
		sc, err := api.NewSaltClient(cfg.Salt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "salt warning: %v (continuing without Salt)\n", err)
		} else {
			saltClient = sc
		}
	}

	var nomadClient *api.NomadClient
	if cfg.Nomad.URL != "" {
		nomadClient = api.NewNomadClient(cfg.Nomad)
	}

	promClient := api.NewPrometheusClient(cfg.Prometheus)
	dnsClient := api.NewTechnitiumClient(cfg.Technitium)
	caClient := api.NewStepCAClient(cfg.StepCA)
	claudeClient := api.NewClaudeClient(cfg.Claude)
	matchboxClient := api.NewMatchboxClient(cfg.Matchbox)
	csClient := api.NewCrowdSecClient(cfg.CrowdSec)
	wazuhClient := api.NewWazuhClient(cfg.Wazuh)
	trueNASClient := api.NewTrueNASClient(cfg.TrueNAS.URL, cfg.TrueNAS.APIKey, cfg.TrueNAS.Insecure)

	var plexClient *api.PlexClient
	if cfg.Plex.URL != "" {
		plexClient = api.NewPlexClient(cfg.Plex.URL, cfg.Plex.Token, cfg.Plex.Insecure)
	}

	var lokiClient *api.LokiClient
	if cfg.Loki.URL != "" {
		lokiClient = api.NewLokiClient(cfg.Loki.URL, cfg.Loki.Token, cfg.Loki.Insecure)
	}

	var haClient *api.HAClient
	if cfg.HomeAssistant.URL != "" {
		haClient = api.NewHAClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, cfg.HomeAssistant.Insecure)
	}

	var hermesClient *api.HermesClient
	if cfg.Hermes.URL != "" {
		hermesClient = api.NewHermesClient(cfg.Hermes.URL, cfg.Hermes.APIKey, cfg.Hermes.Insecure)
	}

	// cfg.Globals.SSHUser is always populated by ApplyGlobals (local username).
	// cfg.Defaults.SSHUser mirrors it unless explicitly set to something else.
	sshUser := cfg.Globals.SSHUser

	database, err := db.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db warning: %v (continuing without local database)\n", err)
		database = nil
	}
	if database != nil {
		defer database.Close()
	}

	// Start the embedded metrics server in the background if configured.
	if cfg.Prometheus.Listen != "" {
		go func() {
			// Log to a file, not stderr — the TUI owns the terminal's
			// alt-screen once it launches, and a log line written there
			// mid-session corrupts the display.
			logger := embeddedServerLogger()
			if err := startMetricsServer(cfg.Prometheus.Listen, database, cfg, logger); err != nil {
				logger.Printf("metrics server error: %v", err)
			}
		}()
	}

	p := tea.NewProgram(
		ui.NewApp(proxmoxClient, saltClient, nomadClient, promClient, lokiClient,
			dnsClient, caClient, claudeClient, matchboxClient, csClient, wazuhClient,
			trueNASClient, plexClient, haClient, hermesClient,
			sshUser, sshPassword, cfg.Defaults, *cfg, database, guidedSetup),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runConfigEditor launches the interactive config editor and exits; it never returns.
func runConfigEditor() {
	rawCfg, err := config.Load() // load without Keychain overlay; missing file is not an error
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus config: %v\n", err)
		fmt.Fprintln(os.Stderr, "fix the file manually or move it aside, then re-run argus config")
		os.Exit(1)
	}
	if rawCfg == nil {
		rawCfg = &config.Config{}
	}
	if err := ui.RunConfigEditor(rawCfg); err != nil {
		fmt.Fprintf(os.Stderr, "config editor error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// loadConfig loads config from file/env and overlays keyring credentials.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	gkr.Overlay(&cfg.Secret, gkr.KeyProxmoxSecret)
	gkr.Overlay(&cfg.Password, gkr.KeyProxmoxPassword)
	gkr.Overlay(&cfg.Salt.Password, gkr.KeySaltPassword)
	gkr.Overlay(&cfg.Nomad.Token, gkr.KeyNomadToken)
	gkr.Overlay(&cfg.Wazuh.Password, gkr.KeyWazuhPassword)
	gkr.Overlay(&cfg.CrowdSec.APIKey, gkr.KeyCrowdSecAPIKey)
	gkr.Overlay(&cfg.Matchbox.Token, gkr.KeyMatchboxToken)
	gkr.Overlay(&cfg.Technitium.Token, gkr.KeyTechnitiumToken)
	gkr.Overlay(&cfg.Prometheus.Token, gkr.KeyPrometheusToken)
	gkr.Overlay(&cfg.Claude.APIKey, gkr.KeyAnthropicAPIKey)
	gkr.Overlay(&cfg.TrueNAS.APIKey, gkr.KeyTrueNASAPIKey)
	gkr.Overlay(&cfg.Plex.Token, gkr.KeyPlexToken)
	gkr.Overlay(&cfg.Loki.Token, gkr.KeyLokiToken)
	gkr.Overlay(&cfg.HomeAssistant.Token, gkr.KeyHAToken)
	gkr.Overlay(&cfg.Hermes.APIKey, gkr.KeyHermesAPIKey)
	return cfg, nil
}

// embeddedServerLogger returns a logger writing to ~/.config/argus/argus.log,
// falling back to a discard logger if the file can't be opened — never to
// stderr, which the TUI owns once it launches.
func embeddedServerLogger() *log.Logger {
	home, err := os.UserHomeDir()
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	dir := filepath.Join(home, ".config", "argus")
	_ = os.MkdirAll(dir, 0o700)
	logPath := filepath.Join(dir, "argus.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	_ = f.Chmod(0o600)
	return log.New(f, "", log.LstdFlags)
}

// startMetricsServer runs an embedded Prometheus metrics HTTP server.
// This is a lighter version of cmd/metrics-server; both share the same DB.
func startMetricsServer(listen string, database *db.DB, cfg *config.Config, logger *log.Logger) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		if database == nil {
			http.Error(w, "database not available", http.StatusServiceUnavailable)
			return
		}
		snap, err := database.ComputeMetrics()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetricsText(w, snap)
	})

	logger.Printf("argus metrics listening on %s/metrics", listen)
	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return srv.ListenAndServe()
}

// writeMetricsText writes Prometheus text format to w.
func writeMetricsText(w http.ResponseWriter, s *db.MetricsSnapshot) {
	ts := s.ComputedAt.UnixMilli()

	// HELP/TYPE must appear exactly once per metric family or Prometheus
	// rejects the scrape.
	headerDone := map[string]bool{}
	g := func(name, help string, labels map[string]string, value float64) {
		if !headerDone[name] {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
			headerDone[name] = true
		}
		if len(labels) == 0 {
			fmt.Fprintf(w, "%s %g %d\n", name, value, ts)
			return
		}
		var parts []string
		for k, v := range labels {
			parts = append(parts, fmt.Sprintf(`%s=%q`, k, v))
		}
		fmt.Fprintf(w, "%s{%s} %g %d\n", name, strings.Join(parts, ","), value, ts)
	}

	g("argus_provisioned_vms_total", "Total provisioned VMs tracked", nil, float64(s.ProvisionedTotal))
	for status, n := range s.VMsByStatus {
		g("argus_provisioned_vms_by_status", "Provisioned VMs by last known status",
			map[string]string{"status": status}, float64(n))
	}
	g("argus_salt_applies_24h_total", "Salt state.apply calls (last 24h)", nil, float64(s.SaltApplies24h))
	g("argus_salt_apply_successes_24h_total", "Successful salt applies (last 24h)", nil, float64(s.SaltSuccesses24h))
	g("argus_salt_apply_failures_24h_total", "Failed salt applies (last 24h)", nil, float64(s.SaltApplies24h-s.SaltSuccesses24h))
	if s.SaltApplies24h > 0 {
		g("argus_salt_apply_success_rate", "Salt apply success rate 0-1 (last 24h)", nil,
			float64(s.SaltSuccesses24h)/float64(s.SaltApplies24h))
	}
	for minion, n := range s.SaltMinionActivity {
		g("argus_salt_minion_applies_24h_total", "Salt applies per minion (last 24h)",
			map[string]string{"minion": minion}, float64(n))
	}
	g("argus_nomad_jobs_running", "Nomad jobs with status=running", nil, float64(s.NomadJobsRunning))
	g("argus_nomad_jobs_dead", "Nomad jobs with status=dead", nil, float64(s.NomadJobsDead))
	g("argus_nomad_jobs_pending", "Nomad jobs with status=pending", nil, float64(s.NomadJobsPending))
	g("argus_nomad_job_events_today_total", "Nomad job state-change events today", nil, float64(s.NomadEventsToday))
	for _, p := range s.TopCIProfiles {
		g("argus_cloud_init_profile_vms", "VMs per cloud-init profile",
			map[string]string{"profile": p.Name}, float64(p.Count))
	}
	for _, p := range s.TopSaltProfiles {
		g("argus_salt_profile_applies", "Salt applies per salt profile",
			map[string]string{"profile": p.Name}, float64(p.Count))
	}
}
