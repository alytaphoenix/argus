// metrics-server exposes argus's SQLite database as a Prometheus metrics
// endpoint and serves a generated Prometheus scrape-config snippet.
//
// Run alongside argus:
//
//	argus-metrics-server   # reads config from ~/.config/argus/config.toml
//
// Prometheus scrape endpoint: GET /metrics
// Generated scrape config:    GET /scrape-config
// Health check:               GET /healthz
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	listen := cfg.Prometheus.Listen
	if listen == "" {
		listen = ":9099"
	}

	database, err := db.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		snap, err := database.ComputeMetrics()
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, snap)
	})

	mux.HandleFunc("/scrape-config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, generateScrapeConfig(cfg))
	})

	log.Printf("argus metrics server listening on %s", listen)
	log.Printf("  /metrics       — Prometheus scrape endpoint")
	log.Printf("  /scrape-config — Generated prometheus.yml snippet")
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

// writeMetrics renders a MetricsSnapshot in Prometheus text exposition format.
func writeMetrics(w http.ResponseWriter, s *db.MetricsSnapshot) {
	ts := s.ComputedAt.UnixMilli()
	line := func(name, help, typ string, labels map[string]string, value float64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
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
	repeat := func(name, help, typ string, labelKey string, m map[string]int) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for k, v := range m {
			fmt.Fprintf(w, "%s{%s=%q} %d %d\n", name, labelKey, k, v, ts)
		}
	}

	// ── Provisioned VMs ──────────────────────────────────────────────────
	line("argus_provisioned_vms_total",
		"Total number of VMs tracked by argus", "gauge",
		nil, float64(s.ProvisionedTotal))

	repeat("argus_provisioned_vms_by_status",
		"Number of provisioned VMs by last known status", "gauge",
		"status", s.VMsByStatus)

	// ── Salt ─────────────────────────────────────────────────────────────
	line("argus_salt_applies_24h_total",
		"Salt state.apply invocations in the last 24 hours", "gauge",
		nil, float64(s.SaltApplies24h))

	line("argus_salt_apply_successes_24h_total",
		"Successful salt state.apply invocations in the last 24 hours", "gauge",
		nil, float64(s.SaltSuccesses24h))

	failures := s.SaltApplies24h - s.SaltSuccesses24h
	line("argus_salt_apply_failures_24h_total",
		"Failed salt state.apply invocations in the last 24 hours", "gauge",
		nil, float64(failures))

	if s.SaltApplies24h > 0 {
		rate := float64(s.SaltSuccesses24h) / float64(s.SaltApplies24h)
		line("argus_salt_apply_success_rate",
			"Fraction of salt applies that succeeded in the last 24 hours (0–1)", "gauge",
			nil, rate)
	}

	repeat("argus_salt_minion_applies_24h_total",
		"Salt state.apply count per minion in the last 24 hours", "gauge",
		"minion", s.SaltMinionActivity)

	// ── Nomad ─────────────────────────────────────────────────────────────
	fmt.Fprintf(w, "# HELP argus_nomad_jobs_by_status Number of Nomad jobs by last known status\n")
	fmt.Fprintf(w, "# TYPE argus_nomad_jobs_by_status gauge\n")
	fmt.Fprintf(w, "argus_nomad_jobs_by_status{status=\"running\"} %d %d\n", s.NomadJobsRunning, ts)
	fmt.Fprintf(w, "argus_nomad_jobs_by_status{status=\"dead\"} %d %d\n", s.NomadJobsDead, ts)
	fmt.Fprintf(w, "argus_nomad_jobs_by_status{status=\"pending\"} %d %d\n", s.NomadJobsPending, ts)

	line("argus_nomad_job_events_today_total",
		"Number of Nomad job state-change events recorded today", "gauge",
		nil, float64(s.NomadEventsToday))

	// ── Profiles ──────────────────────────────────────────────────────────
	fmt.Fprintf(w, "# HELP argus_cloud_init_profile_vms Number of VMs using each cloud-init profile\n")
	fmt.Fprintf(w, "# TYPE argus_cloud_init_profile_vms gauge\n")
	for _, p := range s.TopCIProfiles {
		fmt.Fprintf(w, "argus_cloud_init_profile_vms{profile=%q} %d %d\n", p.Name, p.Count, ts)
	}

	fmt.Fprintf(w, "# HELP argus_salt_profile_applies Number of salt applies using each salt profile\n")
	fmt.Fprintf(w, "# TYPE argus_salt_profile_applies gauge\n")
	for _, p := range s.TopSaltProfiles {
		fmt.Fprintf(w, "argus_salt_profile_applies{profile=%q} %d %d\n", p.Name, p.Count, ts)
	}

	// ── Scrape timestamp ──────────────────────────────────────────────────
	line("argus_last_scrape_timestamp_seconds",
		"Unix timestamp of the last metrics scrape", "gauge",
		nil, float64(s.ComputedAt.Unix()))
}

// generateScrapeConfig builds a prometheus.yml scrape_configs snippet for all
// services that argus knows about.
func generateScrapeConfig(cfg *config.Config) string {
	var sb strings.Builder

	sb.WriteString("# Prometheus scrape configuration generated by argus\n")
	sb.WriteString("# Add these entries under scrape_configs: in your prometheus.yml\n")
	sb.WriteString("#\n")
	sb.WriteString("# Generated: " + time.Now().Format(time.RFC3339) + "\n\n")

	sb.WriteString("scrape_configs:\n\n")

	// ── Argus metrics server ─────────────────────────────────────────────
	listen := cfg.Prometheus.Listen
	if listen == "" {
		listen = ":9099"
	}
	host := "localhost" + listen
	if strings.HasPrefix(listen, ":") {
		host = "localhost" + listen
	}
	sb.WriteString("  # Argus homelab metrics (provisioned VMs, salt apply stats, nomad job history)\n")
	sb.WriteString("  - job_name: argus\n")
	sb.WriteString("    static_configs:\n")
	sb.WriteString("      - targets: [" + host + "]\n\n")

	// ── Proxmox via pve-exporter ───────────────────────────────────────────
	// Strip protocol from PVE host for display
	pveHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Host, "https://"), "http://")
	pveHost = strings.SplitN(pveHost, "/", 2)[0]

	sb.WriteString("  # Proxmox nodes, VMs, and LXC containers\n")
	sb.WriteString("  # Requires: pip install prometheus-pve-exporter\n")
	sb.WriteString("  # Docs: https://github.com/prometheus-pve/prometheus-pve-exporter\n")
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
	sb.WriteString("        replacement: pve-exporter:9221  # update with actual pve-exporter host\n\n")

	// ── Nomad ─────────────────────────────────────────────────────────────
	if cfg.Nomad.URL != "" {
		nomadHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Nomad.URL, "https://"), "http://")
		nomadHost = strings.TrimRight(nomadHost, "/")
		sb.WriteString("  # Nomad built-in metrics endpoint\n")
		sb.WriteString("  # No exporter needed — Nomad exports Prometheus natively\n")
		sb.WriteString("  - job_name: nomad\n")
		sb.WriteString("    metrics_path: /v1/metrics\n")
		sb.WriteString("    params:\n")
		sb.WriteString("      format: [prometheus]\n")
		sb.WriteString("    static_configs:\n")
		sb.WriteString("      - targets: [" + nomadHost + "]\n")
		if cfg.Nomad.Token != "" {
			sb.WriteString("    authorization:\n")
			sb.WriteString("      # credentials masked — this endpoint is unauthenticated; copy the real\n")
			sb.WriteString("      # token from your argus config/keychain into your Prometheus config directly\n")
			sb.WriteString("      credentials: \"" + strings.Repeat("*", min(len(cfg.Nomad.Token), 8)) + "\"\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("  # Nomad: not configured (add [nomad] url to config.toml)\n\n")
	}

	// ── Salt via salt-exporter ─────────────────────────────────────────────
	if cfg.Salt.URL != "" {
		saltHost := strings.TrimPrefix(strings.TrimPrefix(cfg.Salt.URL, "https://"), "http://")
		saltHost = strings.SplitN(saltHost, ":", 2)[0] // strip port
		sb.WriteString("  # Salt minion status and job metrics\n")
		sb.WriteString("  # Requires: https://github.com/kpetremann/salt-exporter\n")
		sb.WriteString("  # Install: go install github.com/kpetremann/salt-exporter@latest\n")
		sb.WriteString("  - job_name: salt\n")
		sb.WriteString("    static_configs:\n")
		sb.WriteString("      - targets: [" + saltHost + ":2112]\n\n")
	} else {
		sb.WriteString("  # Salt: not configured (add [salt] url to config.toml)\n\n")
	}

	sb.WriteString("# pve-exporter config (~/.config/pve-exporter/pve.yml):\n")
	sb.WriteString("# default:\n")
	sb.WriteString("#   user: " + cfg.TokenID + "\n")
	sb.WriteString("#   token_name: <your-token-name>\n")
	sb.WriteString("#   token_value: " + strings.Repeat("*", min(len(cfg.Secret), 8)) + "\n")
	sb.WriteString("#   verify_ssl: false\n")

	return sb.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
