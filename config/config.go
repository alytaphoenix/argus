package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Host          string              `toml:"host"`
	TokenID       string              `toml:"token_id"`
	Secret        string              `toml:"secret"`
	Username      string              `toml:"username"` // ticket auth (e.g. "root@pam"); takes priority over token
	Password      string              `toml:"password"`
	Insecure      bool                `toml:"insecure"`
	Globals       GlobalsConfig       `toml:"globals"`
	Salt          SaltConfig          `toml:"salt"`
	Nomad         NomadConfig         `toml:"nomad"`
	Defaults      DefaultsConfig      `toml:"defaults"`
	Prometheus    PrometheusConfig    `toml:"prometheus"`
	Technitium    TechnitiumConfig    `toml:"technitium"`
	StepCA        StepCAConfig        `toml:"stepca"`
	TrueNAS       TrueNASConfig       `toml:"truenas"`
	Claude        ClaudeConfig        `toml:"claude"`
	Matchbox      MatchboxConfig      `toml:"matchbox"`
	CrowdSec      CrowdSecConfig      `toml:"crowdsec"`
	Wazuh         WazuhConfig         `toml:"wazuh"`
	Plex          PlexConfig          `toml:"plex"`
	Servarr       ServarrConfigs      `toml:"servarr"`
	Loki          LokiConfig          `toml:"loki"`
	HomeAssistant HomeAssistantConfig `toml:"homeassistant"`
	Hermes        HermesConfig        `toml:"hermes"`
}

// PlexConfig configures the Plex Media Server integration.
type PlexConfig struct {
	URL      string `toml:"url"`      // e.g. http://192.168.1.50:32400
	Token    string `toml:"token"`    // X-Plex-Token; get from plex.tv/claim or prefs XML
	Insecure bool   `toml:"insecure"` // skip TLS verify for self-signed certs
	VMID     int    `toml:"vmid"`     // Proxmox VMID if Plex runs in a VM (for status badge)
	VMNode   string `toml:"vm_node"`  // Proxmox node name for the Plex VM
}

// ServarrConfigs holds per-app settings for the *arr family.
type ServarrConfigs struct {
	Sonarr    ServarrAppConfig `toml:"sonarr"`
	Radarr    ServarrAppConfig `toml:"radarr"`
	Lidarr    ServarrAppConfig `toml:"lidarr"`
	Readarr   ServarrAppConfig `toml:"readarr"`
	Prowlarr  ServarrAppConfig `toml:"prowlarr"`
	Bazarr    ServarrAppConfig `toml:"bazarr"`
	Overseerr ServarrAppConfig `toml:"overseerr"`
	Tautulli  ServarrAppConfig `toml:"tautulli"`
}

// ServarrAppConfig is config for one *arr app instance.
type ServarrAppConfig struct {
	URL      string `toml:"url"`
	APIKey   string `toml:"api_key"`
	Insecure bool   `toml:"insecure"`
	VMID     int    `toml:"vmid"` // Proxmox VMID if the service runs in a VM
	VMNode   string `toml:"vm_node"`
}

// GlobalsConfig sets cross-cutting defaults applied throughout argus.
// Values here are used as the base; more specific sections (e.g. [defaults])
// override them where they overlap.
type GlobalsConfig struct {
	// SSHUser is the default SSH username for all node and VM connections.
	// Overrides defaults.ssh_user if set. Default: "alyta"
	SSHUser string `toml:"ssh_user"`

	// SSHKey is the path to an SSH private key file (e.g. ~/.ssh/id_ed25519).
	// If empty, the SSH agent / default key is used.
	SSHKey string `toml:"ssh_key"`

	// Domain is the homelab base domain (e.g. "homelab.local").
	// Used as the default searchdomain and for service hostname resolution.
	Domain string `toml:"domain"`

	// Timezone is used for log display and scheduled tasks (IANA format, e.g. "America/New_York").
	Timezone string `toml:"timezone"`

	// AdminEmail is the operator contact address for notifications / CA certs.
	AdminEmail string `toml:"admin_email"`
}

// MatchboxConfig configures the Matchbox PXE provisioning server.
type MatchboxConfig struct {
	URL   string `toml:"url"`   // e.g. http://matchbox.local:8080
	Token string `toml:"token"` // optional bearer token
}

// CrowdSecConfig configures the CrowdSec Local API.
type CrowdSecConfig struct {
	URL    string `toml:"url"`     // e.g. http://localhost:8080
	APIKey string `toml:"api_key"` // LAPI key from cscli bouncers add
}

// WazuhConfig configures the Wazuh REST API.
type WazuhConfig struct {
	URL      string `toml:"url"`      // e.g. https://wazuh-manager:55000
	Username string `toml:"username"` // wazuh-wui or similar
	Password string `toml:"password"`
	// Insecure skips TLS verification. Wazuh ships with a self-signed cert,
	// so this defaults to true when unset (see NewWazuhClient).
	Insecure *bool `toml:"insecure"`
}

// ClaudeConfig configures the Anthropic Claude API for AI-powered recommendations.
type ClaudeConfig struct {
	APIKey string `toml:"api_key"` // Anthropic API key; also read from ANTHROPIC_API_KEY
	Model  string `toml:"model"`   // defaults to claude-sonnet-4-6
}

// TrueNASConfig configures TrueNAS SCALE/CORE monitoring.
type TrueNASConfig struct {
	URL      string `toml:"url"`      // e.g. https://truenas.local
	APIKey   string `toml:"api_key"`  // TrueNAS UI → System → API Keys
	Insecure bool   `toml:"insecure"` // skip TLS verify for self-signed certs
}

// TechnitiumConfig configures the Technitium DNS server integration.
type TechnitiumConfig struct {
	URL   string `toml:"url"`   // e.g. http://dns.local:5380
	Token string `toml:"token"` // API token from Technitium admin panel
}

// StepCAConfig configures the Smallstep step-ca integration.
type StepCAConfig struct {
	URL         string `toml:"url"`         // e.g. https://ca.local:8443
	Fingerprint string `toml:"fingerprint"` // root cert fingerprint for verification
}

// HomeAssistantConfig configures the Home Assistant integration.
type HomeAssistantConfig struct {
	URL      string `toml:"url"`      // e.g. http://homeassistant.local:8123
	Token    string `toml:"token"`    // Long-lived access token
	Insecure bool   `toml:"insecure"` // skip TLS verify for self-signed certs
}

// HermesConfig configures the Hermes agent integration for AI-powered automation.
type HermesConfig struct {
	URL         string `toml:"url"`          // e.g. http://hermes.local:8080
	APIKey      string `toml:"api_key"`      // Bearer token if auth enabled
	Insecure    bool   `toml:"insecure"`     // skip TLS verify
	SaltTarget  string `toml:"salt_target"`  // minion ID where Hermes + signal-cli run
	SignalPhone string `toml:"signal_phone"` // registered Signal account number, e.g. +15555550100
}

// LokiConfig configures optional Grafana Loki log browsing in the Metrics screen.
type LokiConfig struct {
	URL      string `toml:"url"`      // e.g. http://loki:3100
	Token    string `toml:"token"`    // Bearer token (leave empty if auth disabled)
	Insecure bool   `toml:"insecure"` // skip TLS verify for self-signed certs
}

// PrometheusConfig configures optional Prometheus integration.
type PrometheusConfig struct {
	// URL is the Prometheus server to query (e.g. http://localhost:9090).
	// Leave empty to disable Prometheus-backed panels in the metrics screen.
	URL string `toml:"url"`
	// Token is an optional Bearer token for Prometheus with auth enabled.
	Token string `toml:"token"`
	// Listen is the address for argus's own metrics server
	// (e.g. ":9099"). Leave empty to disable.
	Listen string `toml:"listen"`
}

type DefaultsConfig struct {
	TemplateID    int    `toml:"template_id"`
	CPUCores      int    `toml:"cpu_cores"`
	MemoryMB      int    `toml:"memory_mb"`
	DiskGB        int    `toml:"disk_gb"`
	Storage       string `toml:"storage"`
	Bridge        string `toml:"bridge"`
	OSType        string `toml:"ostype"`
	SSHUser       string `toml:"ssh_user"`
	CIUser        string `toml:"ci_user"`
	CISSHKey      string `toml:"ci_ssh_key"`
	CIIPConfig    string `toml:"ci_ipconfig"`
	Nameserver    string `toml:"nameserver"`
	SearchDomain  string `toml:"searchdomain"`
	BackupStorage string `toml:"backup_storage"` // Proxmox storage ID for backup listing, e.g. "pbs"
}

type NomadConfig struct {
	URL   string `toml:"url"`
	Token string `toml:"token"` // ACL token; leave empty if ACLs are disabled
}

type SaltConfig struct {
	URL          string `toml:"url"`
	Username     string `toml:"username"`
	Password     string `toml:"password"`
	Eauth        string `toml:"eauth"`         // defaults to "pam"
	Insecure     bool   `toml:"insecure"`      // skip TLS verify
	MasterMinion string `toml:"master_minion"` // minion ID of the salt-master host (for file writes)
	PillarBase   string `toml:"pillar_base"`   // pillar root on master, default /srv/pillar
	StateBase    string `toml:"state_base"`    // state root on master, default /srv/salt
}

// envAny returns the first non-empty value among the given env var names,
// checked in order. Used to prefer the current ARGUS_* name while still
// honoring the pre-rename RATATASKR_* name for anyone with it already set.
func envAny(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func Load() (*Config, error) {
	cfg := &Config{}

	if path, err := defaultPath(); err == nil {
		migrateLegacyConfig(path)
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", path, err)
			}
		}
	}

	// Environment variables override file values
	if v := os.Getenv("PROXMOX_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("PROXMOX_TOKEN_ID"); v != "" {
		cfg.TokenID = v
	}
	if v := os.Getenv("PROXMOX_SECRET"); v != "" {
		cfg.Secret = v
	}
	if v := os.Getenv("PROXMOX_USERNAME"); v != "" {
		cfg.Username = v
	}
	if v := os.Getenv("PROXMOX_PASSWORD"); v != "" {
		cfg.Password = v
	}
	if os.Getenv("PROXMOX_INSECURE") == "1" {
		cfg.Insecure = true
	}
	if v := os.Getenv("SALT_URL"); v != "" {
		cfg.Salt.URL = v
	}
	if v := os.Getenv("SALT_USERNAME"); v != "" {
		cfg.Salt.Username = v
	}
	if v := os.Getenv("SALT_PASSWORD"); v != "" {
		cfg.Salt.Password = v
	}
	if v := os.Getenv("NOMAD_URL"); v != "" {
		cfg.Nomad.URL = v
	}
	if v := os.Getenv("NOMAD_TOKEN"); v != "" {
		cfg.Nomad.Token = v
	}
	if v := os.Getenv("PROMETHEUS_URL"); v != "" {
		cfg.Prometheus.URL = v
	}
	if v := os.Getenv("PROMETHEUS_TOKEN"); v != "" {
		cfg.Prometheus.Token = v
	}
	if v := envAny("ARGUS_METRICS_LISTEN", "RATATASKR_METRICS_LISTEN"); v != "" {
		cfg.Prometheus.Listen = v
	}
	if v := os.Getenv("TECHNITIUM_URL"); v != "" {
		cfg.Technitium.URL = v
	}
	if v := os.Getenv("TECHNITIUM_TOKEN"); v != "" {
		cfg.Technitium.Token = v
	}
	if v := os.Getenv("STEPCA_URL"); v != "" {
		cfg.StepCA.URL = v
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.Claude.APIKey = v
	}
	if v := os.Getenv("MATCHBOX_URL"); v != "" {
		cfg.Matchbox.URL = v
	}
	if v := os.Getenv("MATCHBOX_TOKEN"); v != "" {
		cfg.Matchbox.Token = v
	}
	if v := os.Getenv("CROWDSEC_URL"); v != "" {
		cfg.CrowdSec.URL = v
	}
	if v := os.Getenv("CROWDSEC_API_KEY"); v != "" {
		cfg.CrowdSec.APIKey = v
	}
	if v := os.Getenv("WAZUH_URL"); v != "" {
		cfg.Wazuh.URL = v
	}
	if v := os.Getenv("WAZUH_USERNAME"); v != "" {
		cfg.Wazuh.Username = v
	}
	if v := os.Getenv("WAZUH_PASSWORD"); v != "" {
		cfg.Wazuh.Password = v
	}
	if v := os.Getenv("TRUENAS_URL"); v != "" {
		cfg.TrueNAS.URL = v
	}
	if v := os.Getenv("TRUENAS_API_KEY"); v != "" {
		cfg.TrueNAS.APIKey = v
	}

	// Globals env overrides
	if v := envAny("ARGUS_SSH_USER", "RATATASKR_SSH_USER"); v != "" {
		cfg.Globals.SSHUser = v
	}
	if v := envAny("ARGUS_SSH_KEY", "RATATASKR_SSH_KEY"); v != "" {
		cfg.Globals.SSHKey = v
	}
	if v := envAny("ARGUS_DOMAIN", "RATATASKR_DOMAIN"); v != "" {
		cfg.Globals.Domain = v
	}
	if v := envAny("ARGUS_TIMEZONE", "RATATASKR_TIMEZONE"); v != "" {
		cfg.Globals.Timezone = v
	}

	// Apply globals → propagate to sub-sections if not already set.
	cfg.ApplyGlobals()

	return cfg, nil
}

// Validate checks that the required Proxmox credentials are present.
// Called after all sources (file, env, keyring overlay) have been merged,
// so secrets stored only in the macOS Keychain are accepted.
func (cfg *Config) Validate() error {
	if cfg.Host == "" {
		p, _ := defaultPath()
		return fmt.Errorf("PROXMOX_HOST not set and no config at %s", p)
	}
	if cfg.Username != "" {
		if cfg.Password == "" {
			return fmt.Errorf("PROXMOX_PASSWORD required for ticket auth with username %q (config, env, or Keychain)", cfg.Username)
		}
		return nil
	}
	if cfg.TokenID == "" || cfg.Secret == "" {
		return fmt.Errorf("PROXMOX_TOKEN_ID and PROXMOX_SECRET (or username/password for ticket auth) required (config, env, or Keychain)")
	}
	return nil
}

// ApplyGlobals propagates [globals] settings into sub-sections that support them.
// Called after all config sources (file + env) are merged. Sub-section values
// that are already set explicitly are never overwritten.
func (cfg *Config) ApplyGlobals() {
	g := &cfg.Globals

	// Default SSH user: globals → defaults.ssh_user → "alyta"
	if g.SSHUser == "" {
		g.SSHUser = "alyta"
	}
	if cfg.Defaults.SSHUser == "" {
		cfg.Defaults.SSHUser = g.SSHUser
	}

	// Default domain → defaults.searchdomain
	if g.Domain != "" && cfg.Defaults.SearchDomain == "" {
		cfg.Defaults.SearchDomain = g.Domain
	}

	// Default timezone
	if g.Timezone == "" {
		g.Timezone = "UTC"
	}
}

func defaultPath() (string, error) {
	if v := os.Getenv("ARGUS_CONFIG"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "argus", "config.toml"), nil
}

// legacyPath returns the pre-rename config location (~/.config/ratataskr/config.toml).
func legacyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ratataskr", "config.toml"), nil
}

// migrateLegacyConfig copies a config.toml found at the pre-rename location
// into path, if path doesn't exist yet. Best-effort; the old file is left in
// place. This lets existing ratataskr installs pick up seamlessly under the
// argus config dir without losing their Proxmox setup.
func migrateLegacyConfig(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	old, err := legacyPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(old)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// SaveProxmoxSetup writes a minimal config file from the first-run setup
// screen. The password is stored in the Keychain, never in the file. Other
// integrations are introspected at runtime or added to the file by hand.
func SaveProxmoxSetup(host, username string, insecure bool) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(`# Generated by argus first-run setup.
# Password is stored in the macOS Keychain (key: proxmox_password).
# See config.toml.example for optional integrations (salt, nomad, plex, ...).
host     = %q
username = %q
insecure = %v
`, host, username, insecure)
	return os.WriteFile(path, []byte(content), 0o600)
}
