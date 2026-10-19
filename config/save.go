package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SaveConfig writes cfg to the default config path (~/.config/argus/config.toml).
// Secrets that live in the macOS Keychain (proxmox password/secret, salt password,
// nomad/prometheus/loki/truenas/plex/ha/hermes/technitium/crowdsec/matchbox tokens,
// and the Anthropic API key) are never written to file.
// Servarr api_keys have no Keychain entry and ARE written to the file.
// Optional sections are omitted when their primary URL is empty.
func SaveConfig(cfg *Config) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderConfig(cfg)), 0o600)
}

func renderConfig(cfg *Config) string {
	var b strings.Builder
	fmt.Fprint(&b, "# argus configuration\n")
	fmt.Fprint(&b, "# Secrets live in the macOS Keychain — use the Keys screen (k) inside argus.\n")
	fmt.Fprint(&b, "# See config.toml.example for full documentation and optional sections.\n\n")

	// ── Proxmox (required) ──────────────────────────────────────────────────────
	writeStr(&b, "host", cfg.Host)
	if cfg.TokenID != "" {
		writeStr(&b, "token_id", cfg.TokenID)
		fmt.Fprint(&b, "# secret stored in Keychain (key: proxmox_secret)\n")
	}
	if cfg.Username != "" {
		writeStr(&b, "username", cfg.Username)
		fmt.Fprint(&b, "# password stored in Keychain (key: proxmox_password)\n")
	}
	fmt.Fprintf(&b, "insecure = %v\n", cfg.Insecure)

	// ── Globals ─────────────────────────────────────────────────────────────────
	g := cfg.Globals
	hasGlobals := (g.SSHUser != "" && g.SSHUser != defaultSSHUser()) ||
		g.SSHKey != "" ||
		g.Domain != "" ||
		(g.Timezone != "" && g.Timezone != "UTC") ||
		g.AdminEmail != ""
	if hasGlobals {
		fmt.Fprint(&b, "\n[globals]\n")
		if g.SSHUser != "" && g.SSHUser != defaultSSHUser() {
			writeStr(&b, "ssh_user", g.SSHUser)
		}
		writeStr(&b, "ssh_key", g.SSHKey)
		writeStr(&b, "domain", g.Domain)
		if g.Timezone != "" && g.Timezone != "UTC" {
			writeStr(&b, "timezone", g.Timezone)
		}
		writeStr(&b, "admin_email", g.AdminEmail)
	}

	// ── VM Defaults ─────────────────────────────────────────────────────────────
	d := cfg.Defaults
	// SSHUser and SearchDomain are propagated from globals; only write if explicitly different.
	defaultsSSHUser := d.SSHUser != "" && d.SSHUser != g.SSHUser
	defaultsSearchDomain := d.SearchDomain != "" && d.SearchDomain != g.Domain
	hasDefaults := d.TemplateID != 0 || d.CPUCores != 0 || d.MemoryMB != 0 ||
		d.DiskGB != 0 || d.Storage != "" || d.Bridge != "" || d.OSType != "" ||
		defaultsSSHUser || d.CIUser != "" || d.CISSHKey != "" || d.CIIPConfig != "" ||
		d.Nameserver != "" || defaultsSearchDomain || d.BackupStorage != ""
	if hasDefaults {
		fmt.Fprint(&b, "\n[defaults]\n")
		writeInt(&b, "template_id", d.TemplateID)
		writeInt(&b, "cpu_cores", d.CPUCores)
		writeInt(&b, "memory_mb", d.MemoryMB)
		writeInt(&b, "disk_gb", d.DiskGB)
		writeStr(&b, "storage", d.Storage)
		writeStr(&b, "bridge", d.Bridge)
		writeStr(&b, "ostype", d.OSType)
		if defaultsSSHUser {
			writeStr(&b, "ssh_user", d.SSHUser)
		}
		writeStr(&b, "ci_user", d.CIUser)
		writeStr(&b, "ci_ssh_key", d.CISSHKey)
		writeStr(&b, "ci_ipconfig", d.CIIPConfig)
		writeStr(&b, "nameserver", d.Nameserver)
		if defaultsSearchDomain {
			writeStr(&b, "searchdomain", d.SearchDomain)
		}
		writeStr(&b, "backup_storage", d.BackupStorage)
	}

	// ── Salt ────────────────────────────────────────────────────────────────────
	if cfg.Salt.URL != "" {
		s := cfg.Salt
		fmt.Fprint(&b, "\n[salt]\n")
		writeStr(&b, "url", s.URL)
		writeStr(&b, "username", s.Username)
		fmt.Fprint(&b, "# password stored in Keychain (key: salt_password)\n")
		writeStr(&b, "eauth", s.Eauth)
		fmt.Fprintf(&b, "insecure      = %v\n", s.Insecure)
		writeStr(&b, "master_minion", s.MasterMinion)
		writeStr(&b, "pillar_base", s.PillarBase)
		writeStr(&b, "state_base", s.StateBase)
	}

	// ── Nomad ───────────────────────────────────────────────────────────────────
	if cfg.Nomad.URL != "" {
		fmt.Fprint(&b, "\n[nomad]\n")
		writeStr(&b, "url", cfg.Nomad.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: nomad_token)\n")
	}

	// ── Prometheus ──────────────────────────────────────────────────────────────
	if cfg.Prometheus.URL != "" || cfg.Prometheus.Listen != "" {
		fmt.Fprint(&b, "\n[prometheus]\n")
		writeStr(&b, "url", cfg.Prometheus.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: prometheus_token)\n")
		writeStr(&b, "listen", cfg.Prometheus.Listen)
	}

	// ── Loki ────────────────────────────────────────────────────────────────────
	if cfg.Loki.URL != "" {
		fmt.Fprint(&b, "\n[loki]\n")
		writeStr(&b, "url", cfg.Loki.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: loki_token)\n")
		fmt.Fprintf(&b, "insecure = %v\n", cfg.Loki.Insecure)
	}

	// ── CrowdSec ────────────────────────────────────────────────────────────────
	if cfg.CrowdSec.URL != "" {
		fmt.Fprint(&b, "\n[crowdsec]\n")
		writeStr(&b, "url", cfg.CrowdSec.URL)
		fmt.Fprint(&b, "# api_key stored in Keychain (key: crowdsec_api_key)\n")
	}

	// ── Wazuh ───────────────────────────────────────────────────────────────────
	if cfg.Wazuh.URL != "" {
		fmt.Fprint(&b, "\n[wazuh]\n")
		writeStr(&b, "url", cfg.Wazuh.URL)
		writeStr(&b, "username", cfg.Wazuh.Username)
		fmt.Fprint(&b, "# password stored in Keychain (key: wazuh_password)\n")
		if cfg.Wazuh.Insecure != nil {
			fmt.Fprintf(&b, "insecure = %v\n", *cfg.Wazuh.Insecure)
		}
	}

	// ── TrueNAS ─────────────────────────────────────────────────────────────────
	if cfg.TrueNAS.URL != "" {
		fmt.Fprint(&b, "\n[truenas]\n")
		writeStr(&b, "url", cfg.TrueNAS.URL)
		fmt.Fprint(&b, "# api_key stored in Keychain (key: truenas_api_key)\n")
		fmt.Fprintf(&b, "insecure = %v\n", cfg.TrueNAS.Insecure)
	}

	// ── Technitium DNS ──────────────────────────────────────────────────────────
	if cfg.Technitium.URL != "" {
		fmt.Fprint(&b, "\n[technitium]\n")
		writeStr(&b, "url", cfg.Technitium.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: technitium_token)\n")
	}

	// ── step-ca ─────────────────────────────────────────────────────────────────
	if cfg.StepCA.URL != "" {
		fmt.Fprint(&b, "\n[stepca]\n")
		writeStr(&b, "url", cfg.StepCA.URL)
		writeStr(&b, "fingerprint", cfg.StepCA.Fingerprint)
	}

	// ── Matchbox ────────────────────────────────────────────────────────────────
	if cfg.Matchbox.URL != "" {
		fmt.Fprint(&b, "\n[matchbox]\n")
		writeStr(&b, "url", cfg.Matchbox.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: matchbox_token)\n")
	}

	// ── Home Assistant ──────────────────────────────────────────────────────────
	if cfg.HomeAssistant.URL != "" {
		fmt.Fprint(&b, "\n[homeassistant]\n")
		writeStr(&b, "url", cfg.HomeAssistant.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: homeassistant_token)\n")
		fmt.Fprintf(&b, "insecure = %v\n", cfg.HomeAssistant.Insecure)
	}

	// ── Hermes ──────────────────────────────────────────────────────────────────
	if cfg.Hermes.URL != "" {
		fmt.Fprint(&b, "\n[hermes]\n")
		writeStr(&b, "url", cfg.Hermes.URL)
		fmt.Fprint(&b, "# api_key stored in Keychain (key: hermes_api_key)\n")
		fmt.Fprintf(&b, "insecure     = %v\n", cfg.Hermes.Insecure)
		writeStr(&b, "salt_target", cfg.Hermes.SaltTarget)
		writeStr(&b, "signal_phone", cfg.Hermes.SignalPhone)
	}

	// ── Claude ──────────────────────────────────────────────────────────────────
	if cfg.Claude.Model != "" {
		fmt.Fprint(&b, "\n[claude]\n")
		fmt.Fprint(&b, "# api_key stored in Keychain (key: anthropic_api_key) or ANTHROPIC_API_KEY env\n")
		writeStr(&b, "model", cfg.Claude.Model)
	}

	// ── Plex ────────────────────────────────────────────────────────────────────
	if cfg.Plex.URL != "" {
		fmt.Fprint(&b, "\n[plex]\n")
		writeStr(&b, "url", cfg.Plex.URL)
		fmt.Fprint(&b, "# token stored in Keychain (key: plex_token)\n")
		fmt.Fprintf(&b, "insecure = %v\n", cfg.Plex.Insecure)
		if cfg.Plex.VMID != 0 {
			fmt.Fprintf(&b, "vmid     = %d\n", cfg.Plex.VMID)
		}
		writeStr(&b, "vm_node", cfg.Plex.VMNode)
	}

	// ── Servarr ─────────────────────────────────────────────────────────────────
	// api_key has no Keychain entry — it is written to the config file.
	servarrApps := []struct {
		name string
		app  ServarrAppConfig
	}{
		{"sonarr", cfg.Servarr.Sonarr},
		{"radarr", cfg.Servarr.Radarr},
		{"lidarr", cfg.Servarr.Lidarr},
		{"readarr", cfg.Servarr.Readarr},
		{"prowlarr", cfg.Servarr.Prowlarr},
		{"bazarr", cfg.Servarr.Bazarr},
		{"overseerr", cfg.Servarr.Overseerr},
		{"tautulli", cfg.Servarr.Tautulli},
	}
	for _, s := range servarrApps {
		if s.app.URL == "" {
			continue
		}
		fmt.Fprintf(&b, "\n[servarr.%s]\n", s.name)
		writeStr(&b, "url", s.app.URL)
		writeStr(&b, "api_key", s.app.APIKey)
		fmt.Fprintf(&b, "insecure = %v\n", s.app.Insecure)
		if s.app.VMID != 0 {
			fmt.Fprintf(&b, "vmid     = %d\n", s.app.VMID)
		}
		writeStr(&b, "vm_node", s.app.VMNode)
	}

	return b.String()
}

func writeStr(b *strings.Builder, key, val string) {
	if val != "" {
		fmt.Fprintf(b, "%s = %q\n", key, val)
	}
}

func writeInt(b *strings.Builder, key string, val int) {
	if val != 0 {
		fmt.Fprintf(b, "%s = %d\n", key, val)
	}
}
