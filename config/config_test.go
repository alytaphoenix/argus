package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates the package from a real ~/.config/ratataskr/config.toml.
// Tests that need a file set RATATASKR_CONFIG to their own temp path.
func TestMain(m *testing.M) {
	os.Setenv("RATATASKR_CONFIG", filepath.Join(os.TempDir(), "ratataskr-test-nonexistent.toml"))
	os.Exit(m.Run())
}

// ── ApplyGlobals ──────────────────────────────────────────────────────────────

func TestApplyGlobalsDefaultSSHUser(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyGlobals()
	if cfg.Globals.SSHUser != "alyta" {
		t.Errorf("SSHUser = %q, want alyta", cfg.Globals.SSHUser)
	}
	if cfg.Defaults.SSHUser != "alyta" {
		t.Errorf("Defaults.SSHUser = %q, want alyta", cfg.Defaults.SSHUser)
	}
}

func TestApplyGlobalsCustomSSHUser(t *testing.T) {
	cfg := &Config{}
	cfg.Globals.SSHUser = "custom"
	cfg.ApplyGlobals()
	if cfg.Globals.SSHUser != "custom" {
		t.Errorf("SSHUser = %q, want custom", cfg.Globals.SSHUser)
	}
	if cfg.Defaults.SSHUser != "custom" {
		t.Errorf("Defaults.SSHUser = %q, want custom", cfg.Defaults.SSHUser)
	}
}

func TestApplyGlobalsDoesNotOverrideExplicitDefaultSSHUser(t *testing.T) {
	cfg := &Config{}
	cfg.Globals.SSHUser = "global-user"
	cfg.Defaults.SSHUser = "defaults-user"
	cfg.ApplyGlobals()
	// Defaults.SSHUser should NOT be overwritten because it was already set.
	if cfg.Defaults.SSHUser != "defaults-user" {
		t.Errorf("Defaults.SSHUser = %q, want defaults-user (should not be overwritten)", cfg.Defaults.SSHUser)
	}
}

func TestApplyGlobalsDomainPropagation(t *testing.T) {
	cfg := &Config{}
	cfg.Globals.Domain = "homelab.local"
	cfg.ApplyGlobals()
	if cfg.Defaults.SearchDomain != "homelab.local" {
		t.Errorf("Defaults.SearchDomain = %q, want homelab.local", cfg.Defaults.SearchDomain)
	}
}

func TestApplyGlobalsDomainDoesNotOverrideExplicit(t *testing.T) {
	cfg := &Config{}
	cfg.Globals.Domain = "homelab.local"
	cfg.Defaults.SearchDomain = "explicit.local"
	cfg.ApplyGlobals()
	if cfg.Defaults.SearchDomain != "explicit.local" {
		t.Errorf("Defaults.SearchDomain = %q, want explicit.local (should not be overwritten)", cfg.Defaults.SearchDomain)
	}
}

func TestApplyGlobalsTimezoneDefault(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyGlobals()
	if cfg.Globals.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", cfg.Globals.Timezone)
	}
}

func TestApplyGlobalsTimezoneCustom(t *testing.T) {
	cfg := &Config{}
	cfg.Globals.Timezone = "America/New_York"
	cfg.ApplyGlobals()
	if cfg.Globals.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want America/New_York", cfg.Globals.Timezone)
	}
}

// ── Load with environment variables ───────────────────────────────────────────

// setEnv sets environment variables and returns a cleanup function.
func setEnv(t *testing.T, pairs ...string) func() {
	t.Helper()
	var originals []struct{ key, val string; found bool }
	for i := 0; i < len(pairs)-1; i += 2 {
		k, v := pairs[i], pairs[i+1]
		orig, found := os.LookupEnv(k)
		originals = append(originals, struct{ key, val string; found bool }{k, orig, found})
		os.Setenv(k, v)
	}
	return func() {
		for _, o := range originals {
			if o.found {
				os.Setenv(o.key, o.val)
			} else {
				os.Unsetenv(o.key)
			}
		}
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://pve.test:8006",
		"PROXMOX_TOKEN_ID", "user@pam!tok",
		"PROXMOX_SECRET", "supersecret",
		"NOMAD_URL", "http://nomad.test:4646",
		"PROMETHEUS_URL", "http://prom.test:9090",
		"ANTHROPIC_API_KEY", "sk-ant-test",
		"TECHNITIUM_URL", "http://dns.test:5380",
		"CROWDSEC_URL", "http://cs.test:8080",
		"TRUENAS_URL", "https://nas.test",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "https://pve.test:8006" {
		t.Errorf("Host = %q, want https://pve.test:8006", cfg.Host)
	}
	if cfg.TokenID != "user@pam!tok" {
		t.Errorf("TokenID = %q, want user@pam!tok", cfg.TokenID)
	}
	if cfg.Secret != "supersecret" {
		t.Errorf("Secret = %q, want supersecret", cfg.Secret)
	}
	if cfg.Nomad.URL != "http://nomad.test:4646" {
		t.Errorf("Nomad.URL = %q", cfg.Nomad.URL)
	}
	if cfg.Prometheus.URL != "http://prom.test:9090" {
		t.Errorf("Prometheus.URL = %q", cfg.Prometheus.URL)
	}
	if cfg.Claude.APIKey != "sk-ant-test" {
		t.Errorf("Claude.APIKey = %q", cfg.Claude.APIKey)
	}
	if cfg.Technitium.URL != "http://dns.test:5380" {
		t.Errorf("Technitium.URL = %q", cfg.Technitium.URL)
	}
	if cfg.CrowdSec.URL != "http://cs.test:8080" {
		t.Errorf("CrowdSec.URL = %q", cfg.CrowdSec.URL)
	}
	if cfg.TrueNAS.URL != "https://nas.test" {
		t.Errorf("TrueNAS.URL = %q", cfg.TrueNAS.URL)
	}
}

func TestLoadFailsWithoutProxmoxHost(t *testing.T) {
	// Unset all env vars that could satisfy the check.
	cleanup := setEnv(t,
		"PROXMOX_HOST", "",
		"PROXMOX_TOKEN_ID", "user@pam!tok",
		"PROXMOX_SECRET", "secret",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected Validate error when PROXMOX_HOST is empty, got nil")
	}
}

func TestLoadFailsWithoutCredentials(t *testing.T) {
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://pve.test:8006",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected Validate error when token/secret are empty, got nil")
	}
}

func TestLoadInsecureFlag(t *testing.T) {
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://pve.test:8006",
		"PROXMOX_TOKEN_ID", "user@pam!tok",
		"PROXMOX_SECRET", "secret",
		"PROXMOX_INSECURE", "1",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Insecure {
		t.Error("Insecure = false, want true when PROXMOX_INSECURE=1")
	}
}

func TestLoadGlobalsEnvOverrides(t *testing.T) {
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://pve.test:8006",
		"PROXMOX_TOKEN_ID", "user@pam!tok",
		"PROXMOX_SECRET", "secret",
		"RATATASKR_SSH_USER", "envuser",
		"RATATASKR_DOMAIN", "env.homelab",
		"RATATASKR_TIMEZONE", "Europe/London",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Globals.SSHUser != "envuser" {
		t.Errorf("Globals.SSHUser = %q, want envuser", cfg.Globals.SSHUser)
	}
	if cfg.Globals.Domain != "env.homelab" {
		t.Errorf("Globals.Domain = %q, want env.homelab", cfg.Globals.Domain)
	}
	if cfg.Globals.Timezone != "Europe/London" {
		t.Errorf("Globals.Timezone = %q, want Europe/London", cfg.Globals.Timezone)
	}
	// Domain should propagate to Defaults.SearchDomain.
	if cfg.Defaults.SearchDomain != "env.homelab" {
		t.Errorf("Defaults.SearchDomain = %q, want env.homelab", cfg.Defaults.SearchDomain)
	}
}

// ── Load from TOML file ───────────────────────────────────────────────────────

func TestLoadFromFile(t *testing.T) {
	// Write a minimal config to a temp file, then point Load at it via HOME override.
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "ratataskr")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgFile := filepath.Join(cfgDir, "config.toml")

	content := `
host     = "https://pve.file:8006"
token_id = "user@pam!file"
secret   = "filesecret"
insecure = true

[globals]
ssh_user = "fileuser"
domain   = "file.homelab"
timezone = "America/Chicago"

[plex]
url      = "http://plex.file:32400"
token    = "plextoken"
insecure = false
vmid     = 100
vm_node  = "pve1"

[servarr.sonarr]
url     = "http://sonarr.file:8989"
api_key = "sonarrkey"

[servarr.radarr]
url     = "http://radarr.file:7878"
api_key = "radarrkey"
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Point Load at our test config; ensure no env vars interfere.
	cleanup := setEnv(t,
		"RATATASKR_CONFIG", cfgFile,
		"PROXMOX_HOST", "",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Proxmox
	if cfg.Host != "https://pve.file:8006" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.TokenID != "user@pam!file" {
		t.Errorf("TokenID = %q", cfg.TokenID)
	}
	if cfg.Secret != "filesecret" {
		t.Errorf("Secret = %q", cfg.Secret)
	}
	if !cfg.Insecure {
		t.Error("Insecure = false, want true")
	}

	// Globals
	if cfg.Globals.SSHUser != "fileuser" {
		t.Errorf("Globals.SSHUser = %q, want fileuser", cfg.Globals.SSHUser)
	}
	if cfg.Globals.Timezone != "America/Chicago" {
		t.Errorf("Globals.Timezone = %q, want America/Chicago", cfg.Globals.Timezone)
	}
	if cfg.Defaults.SearchDomain != "file.homelab" {
		t.Errorf("Defaults.SearchDomain = %q, want file.homelab", cfg.Defaults.SearchDomain)
	}

	// Plex
	if cfg.Plex.URL != "http://plex.file:32400" {
		t.Errorf("Plex.URL = %q", cfg.Plex.URL)
	}
	if cfg.Plex.Token != "plextoken" {
		t.Errorf("Plex.Token = %q", cfg.Plex.Token)
	}
	if cfg.Plex.VMID != 100 {
		t.Errorf("Plex.VMID = %d, want 100", cfg.Plex.VMID)
	}
	if cfg.Plex.VMNode != "pve1" {
		t.Errorf("Plex.VMNode = %q, want pve1", cfg.Plex.VMNode)
	}

	// Servarr
	if cfg.Servarr.Sonarr.URL != "http://sonarr.file:8989" {
		t.Errorf("Servarr.Sonarr.URL = %q", cfg.Servarr.Sonarr.URL)
	}
	if cfg.Servarr.Sonarr.APIKey != "sonarrkey" {
		t.Errorf("Servarr.Sonarr.APIKey = %q", cfg.Servarr.Sonarr.APIKey)
	}
	if cfg.Servarr.Radarr.URL != "http://radarr.file:7878" {
		t.Errorf("Servarr.Radarr.URL = %q", cfg.Servarr.Radarr.URL)
	}
}

func TestSaltWiringConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyGlobals()
	if cfg.Salt.MasterMinion != "" {
		t.Errorf("MasterMinion = %q, want empty", cfg.Salt.MasterMinion)
	}
	if cfg.Salt.PillarBase != "" {
		t.Errorf("PillarBase = %q, want empty (ServicesModel fills default /srv/pillar)", cfg.Salt.PillarBase)
	}
	if cfg.Salt.StateBase != "" {
		t.Errorf("StateBase = %q, want empty (ServicesModel fills default /srv/salt)", cfg.Salt.StateBase)
	}
}

func TestSaltWiringConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "ratataskr")
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
host     = "https://pve:8006"
token_id = "root@pam!tok"
secret   = "secret"

[salt]
url           = "https://salt.local:8000"
username      = "saltuser"
password      = "saltpass"
master_minion = "salt-master"
pillar_base   = "/data/pillar"
state_base    = "/data/salt"
`), 0o600)

	cleanup := setEnv(t,
		"RATATASKR_CONFIG", filepath.Join(cfgDir, "config.toml"),
		"PROXMOX_HOST", "", "PROXMOX_TOKEN_ID", "", "PROXMOX_SECRET", "",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Salt.MasterMinion != "salt-master" {
		t.Errorf("MasterMinion = %q, want salt-master", cfg.Salt.MasterMinion)
	}
	if cfg.Salt.PillarBase != "/data/pillar" {
		t.Errorf("PillarBase = %q, want /data/pillar", cfg.Salt.PillarBase)
	}
	if cfg.Salt.StateBase != "/data/salt" {
		t.Errorf("StateBase = %q, want /data/salt", cfg.Salt.StateBase)
	}
}

func TestSaveProxmoxSetup(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	cleanup := setEnv(t,
		"RATATASKR_CONFIG", cfgFile,
		"PROXMOX_HOST", "", "PROXMOX_TOKEN_ID", "", "PROXMOX_SECRET", "",
		"PROXMOX_USERNAME", "", "PROXMOX_PASSWORD", "",
	)
	defer cleanup()

	if err := SaveProxmoxSetup("https://pve.test:8006", "root@pam", true); err != nil {
		t.Fatalf("SaveProxmoxSetup: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "https://pve.test:8006" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.Username != "root@pam" {
		t.Errorf("Username = %q", cfg.Username)
	}
	if !cfg.Insecure {
		t.Error("Insecure should be true")
	}
	if cfg.Password != "" {
		t.Error("password must never be written to the config file")
	}
	info, err := os.Stat(cfgFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", info.Mode().Perm())
	}
}

// ── SaveConfig round-trip ─────────────────────────────────────────────────────

func TestSaveConfigRoundTrip(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	cleanup := setEnv(t,
		"RATATASKR_CONFIG", cfgFile,
		"PROXMOX_HOST", "", "PROXMOX_TOKEN_ID", "", "PROXMOX_SECRET", "",
		"PROXMOX_USERNAME", "", "PROXMOX_PASSWORD", "",
	)
	defer cleanup()

	// Build a rich config with non-default values in every section.
	wazuhInsecure := true
	orig := &Config{
		Host:     "https://pve.test:8006",
		TokenID:  "user@pam!tok",
		Insecure: true,
		Globals: GlobalsConfig{
			SSHUser:    "testuser",
			SSHKey:     "/home/testuser/.ssh/id_ed25519",
			Domain:     "lab.test",
			Timezone:   "America/New_York",
			AdminEmail: "admin@lab.test",
		},
		Defaults: DefaultsConfig{
			TemplateID: 9001,
			CPUCores:   4,
			MemoryMB:   4096,
			DiskGB:     50,
			Storage:    "ssd-pool",
			Bridge:     "vmbr1",
			OSType:     "l26",
			CIUser:     "ci-user",
			CIIPConfig: "ip=dhcp",
			Nameserver: "8.8.8.8",
		},
		Salt: SaltConfig{
			URL:          "https://salt.lab.test:8000",
			Username:     "saltapi",
			Eauth:        "pam",
			Insecure:     true,
			MasterMinion: "salt-master",
			PillarBase:   "/data/pillar",
			StateBase:    "/data/salt",
		},
		Nomad: NomadConfig{
			URL: "http://nomad.lab.test:4646",
		},
		Prometheus: PrometheusConfig{
			URL:    "http://prom.lab.test:9090",
			Listen: ":9099",
		},
		Loki: LokiConfig{
			URL:      "http://loki.lab.test:3100",
			Insecure: false,
		},
		CrowdSec: CrowdSecConfig{
			URL: "http://localhost:8080",
		},
		Wazuh: WazuhConfig{
			URL:      "https://wazuh.lab.test:55000",
			Username: "wazuh-wui",
			Insecure: &wazuhInsecure,
		},
		TrueNAS: TrueNASConfig{
			URL:      "https://nas.lab.test",
			Insecure: true,
		},
		Technitium: TechnitiumConfig{
			URL: "http://dns.lab.test:5380",
		},
		StepCA: StepCAConfig{
			URL:         "https://ca.lab.test:8443",
			Fingerprint: "abc123def456",
		},
		Matchbox: MatchboxConfig{
			URL: "http://matchbox.lab.test:8080",
		},
		HomeAssistant: HomeAssistantConfig{
			URL:      "http://ha.lab.test:8123",
			Insecure: false,
		},
		Hermes: HermesConfig{
			URL:         "http://hermes.lab.test:8080",
			Insecure:    false,
			SaltTarget:  "hermes-minion",
			SignalPhone: "+15555550100",
		},
		Claude: ClaudeConfig{
			Model: "claude-sonnet-4-6",
		},
		Plex: PlexConfig{
			URL:      "http://192.168.1.50:32400",
			Insecure: false,
			VMID:     200,
			VMNode:   "pve2",
		},
		Servarr: ServarrConfigs{
			Sonarr: ServarrAppConfig{URL: "http://192.168.1.50:8989", APIKey: "sonarr-key"},
			Radarr: ServarrAppConfig{URL: "http://192.168.1.50:7878", APIKey: "radarr-key"},
		},
	}

	if err := SaveConfig(orig); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after SaveConfig: %v", err)
	}

	// Proxmox
	if loaded.Host != orig.Host {
		t.Errorf("Host: got %q want %q", loaded.Host, orig.Host)
	}
	if loaded.TokenID != orig.TokenID {
		t.Errorf("TokenID: got %q want %q", loaded.TokenID, orig.TokenID)
	}
	if loaded.Insecure != orig.Insecure {
		t.Errorf("Insecure: got %v want %v", loaded.Insecure, orig.Insecure)
	}

	// Globals
	if loaded.Globals.SSHUser != orig.Globals.SSHUser {
		t.Errorf("Globals.SSHUser: got %q want %q", loaded.Globals.SSHUser, orig.Globals.SSHUser)
	}
	if loaded.Globals.SSHKey != orig.Globals.SSHKey {
		t.Errorf("Globals.SSHKey: got %q want %q", loaded.Globals.SSHKey, orig.Globals.SSHKey)
	}
	if loaded.Globals.Domain != orig.Globals.Domain {
		t.Errorf("Globals.Domain: got %q want %q", loaded.Globals.Domain, orig.Globals.Domain)
	}
	if loaded.Globals.Timezone != orig.Globals.Timezone {
		t.Errorf("Globals.Timezone: got %q want %q", loaded.Globals.Timezone, orig.Globals.Timezone)
	}
	if loaded.Globals.AdminEmail != orig.Globals.AdminEmail {
		t.Errorf("Globals.AdminEmail: got %q want %q", loaded.Globals.AdminEmail, orig.Globals.AdminEmail)
	}

	// Defaults
	if loaded.Defaults.TemplateID != orig.Defaults.TemplateID {
		t.Errorf("Defaults.TemplateID: got %d want %d", loaded.Defaults.TemplateID, orig.Defaults.TemplateID)
	}
	if loaded.Defaults.CPUCores != orig.Defaults.CPUCores {
		t.Errorf("Defaults.CPUCores: got %d want %d", loaded.Defaults.CPUCores, orig.Defaults.CPUCores)
	}
	if loaded.Defaults.Storage != orig.Defaults.Storage {
		t.Errorf("Defaults.Storage: got %q want %q", loaded.Defaults.Storage, orig.Defaults.Storage)
	}
	if loaded.Defaults.CIUser != orig.Defaults.CIUser {
		t.Errorf("Defaults.CIUser: got %q want %q", loaded.Defaults.CIUser, orig.Defaults.CIUser)
	}

	// Salt
	if loaded.Salt.URL != orig.Salt.URL {
		t.Errorf("Salt.URL: got %q want %q", loaded.Salt.URL, orig.Salt.URL)
	}
	if loaded.Salt.MasterMinion != orig.Salt.MasterMinion {
		t.Errorf("Salt.MasterMinion: got %q want %q", loaded.Salt.MasterMinion, orig.Salt.MasterMinion)
	}
	if loaded.Salt.PillarBase != orig.Salt.PillarBase {
		t.Errorf("Salt.PillarBase: got %q want %q", loaded.Salt.PillarBase, orig.Salt.PillarBase)
	}

	// Optional sections
	if loaded.Nomad.URL != orig.Nomad.URL {
		t.Errorf("Nomad.URL: got %q want %q", loaded.Nomad.URL, orig.Nomad.URL)
	}
	if loaded.Prometheus.URL != orig.Prometheus.URL {
		t.Errorf("Prometheus.URL: got %q want %q", loaded.Prometheus.URL, orig.Prometheus.URL)
	}
	if loaded.Prometheus.Listen != orig.Prometheus.Listen {
		t.Errorf("Prometheus.Listen: got %q want %q", loaded.Prometheus.Listen, orig.Prometheus.Listen)
	}
	if loaded.Loki.URL != orig.Loki.URL {
		t.Errorf("Loki.URL: got %q want %q", loaded.Loki.URL, orig.Loki.URL)
	}
	if loaded.CrowdSec.URL != orig.CrowdSec.URL {
		t.Errorf("CrowdSec.URL: got %q want %q", loaded.CrowdSec.URL, orig.CrowdSec.URL)
	}
	if loaded.Wazuh.URL != orig.Wazuh.URL {
		t.Errorf("Wazuh.URL: got %q want %q", loaded.Wazuh.URL, orig.Wazuh.URL)
	}
	if loaded.Wazuh.Username != orig.Wazuh.Username {
		t.Errorf("Wazuh.Username: got %q want %q", loaded.Wazuh.Username, orig.Wazuh.Username)
	}
	if loaded.Wazuh.Insecure == nil || *loaded.Wazuh.Insecure != *orig.Wazuh.Insecure {
		t.Errorf("Wazuh.Insecure: got %v want %v", loaded.Wazuh.Insecure, orig.Wazuh.Insecure)
	}
	if loaded.TrueNAS.URL != orig.TrueNAS.URL {
		t.Errorf("TrueNAS.URL: got %q want %q", loaded.TrueNAS.URL, orig.TrueNAS.URL)
	}
	if loaded.TrueNAS.Insecure != orig.TrueNAS.Insecure {
		t.Errorf("TrueNAS.Insecure: got %v want %v", loaded.TrueNAS.Insecure, orig.TrueNAS.Insecure)
	}
	if loaded.Technitium.URL != orig.Technitium.URL {
		t.Errorf("Technitium.URL: got %q want %q", loaded.Technitium.URL, orig.Technitium.URL)
	}
	if loaded.StepCA.URL != orig.StepCA.URL {
		t.Errorf("StepCA.URL: got %q want %q", loaded.StepCA.URL, orig.StepCA.URL)
	}
	if loaded.StepCA.Fingerprint != orig.StepCA.Fingerprint {
		t.Errorf("StepCA.Fingerprint: got %q want %q", loaded.StepCA.Fingerprint, orig.StepCA.Fingerprint)
	}
	if loaded.Matchbox.URL != orig.Matchbox.URL {
		t.Errorf("Matchbox.URL: got %q want %q", loaded.Matchbox.URL, orig.Matchbox.URL)
	}
	if loaded.HomeAssistant.URL != orig.HomeAssistant.URL {
		t.Errorf("HomeAssistant.URL: got %q want %q", loaded.HomeAssistant.URL, orig.HomeAssistant.URL)
	}
	if loaded.Hermes.URL != orig.Hermes.URL {
		t.Errorf("Hermes.URL: got %q want %q", loaded.Hermes.URL, orig.Hermes.URL)
	}
	if loaded.Hermes.SaltTarget != orig.Hermes.SaltTarget {
		t.Errorf("Hermes.SaltTarget: got %q want %q", loaded.Hermes.SaltTarget, orig.Hermes.SaltTarget)
	}
	if loaded.Hermes.SignalPhone != orig.Hermes.SignalPhone {
		t.Errorf("Hermes.SignalPhone: got %q want %q", loaded.Hermes.SignalPhone, orig.Hermes.SignalPhone)
	}
	if loaded.Claude.Model != orig.Claude.Model {
		t.Errorf("Claude.Model: got %q want %q", loaded.Claude.Model, orig.Claude.Model)
	}
	if loaded.Plex.URL != orig.Plex.URL {
		t.Errorf("Plex.URL: got %q want %q", loaded.Plex.URL, orig.Plex.URL)
	}
	if loaded.Plex.VMID != orig.Plex.VMID {
		t.Errorf("Plex.VMID: got %d want %d", loaded.Plex.VMID, orig.Plex.VMID)
	}
	if loaded.Plex.VMNode != orig.Plex.VMNode {
		t.Errorf("Plex.VMNode: got %q want %q", loaded.Plex.VMNode, orig.Plex.VMNode)
	}
	if loaded.Servarr.Sonarr.URL != orig.Servarr.Sonarr.URL {
		t.Errorf("Servarr.Sonarr.URL: got %q want %q", loaded.Servarr.Sonarr.URL, orig.Servarr.Sonarr.URL)
	}
	if loaded.Servarr.Sonarr.APIKey != orig.Servarr.Sonarr.APIKey {
		t.Errorf("Servarr.Sonarr.APIKey: got %q want %q", loaded.Servarr.Sonarr.APIKey, orig.Servarr.Sonarr.APIKey)
	}
	if loaded.Servarr.Radarr.URL != orig.Servarr.Radarr.URL {
		t.Errorf("Servarr.Radarr.URL: got %q want %q", loaded.Servarr.Radarr.URL, orig.Servarr.Radarr.URL)
	}
	if loaded.Servarr.Radarr.APIKey != orig.Servarr.Radarr.APIKey {
		t.Errorf("Servarr.Radarr.APIKey: got %q want %q", loaded.Servarr.Radarr.APIKey, orig.Servarr.Radarr.APIKey)
	}

	// Secrets must never appear in the saved file.
	raw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	for _, forbidden := range []string{"proxmox_secret", "proxmox_password", "salt_password", "plex_token"} {
		if strings.Contains(string(raw), `secret   = `) || strings.Contains(string(raw), forbidden+`"`) {
			t.Errorf("saved config must not contain secret value for key %q", forbidden)
		}
	}
}

func TestSaveConfigMinimalRoundTrip(t *testing.T) {
	// A minimal save (just Proxmox host) must not emit [globals]/[defaults] noise.
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	cleanup := setEnv(t,
		"RATATASKR_CONFIG", cfgFile,
		"PROXMOX_HOST", "", "PROXMOX_TOKEN_ID", "", "PROXMOX_SECRET", "",
		"PROXMOX_USERNAME", "", "PROXMOX_PASSWORD", "",
	)
	defer cleanup()

	minimal := &Config{Host: "https://pve.test:8006", Username: "root@pam", Insecure: true}
	minimal.ApplyGlobals() // simulates what LoadConfig does before editor opens

	if err := SaveConfig(minimal); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	raw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(raw)

	if strings.Contains(content, "[globals]") {
		t.Error("minimal save should not emit [globals] block for default values")
	}
	if strings.Contains(content, "[defaults]") {
		t.Error("minimal save should not emit [defaults] block for propagated defaults")
	}
	if strings.Contains(content, "[salt]") || strings.Contains(content, "[nomad]") {
		t.Error("minimal save should not emit optional sections")
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Host != "https://pve.test:8006" {
		t.Errorf("Host: got %q", loaded.Host)
	}
}

func TestValidateTicketAuth(t *testing.T) {
	cfg := &Config{Host: "https://pve.test:8006", Username: "root@pam", Password: "pw"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate with username/password: %v", err)
	}
	cfg.Password = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for username without password")
	}
}

func TestLoadEnvOverridesFileValues(t *testing.T) {
	// Write a config file, then override with env vars.
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "ratataskr")
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
host     = "https://file-host:8006"
token_id = "file-token"
secret   = "file-secret"
`), 0o600)

	cleanup := setEnv(t,
		"RATATASKR_CONFIG", filepath.Join(cfgDir, "config.toml"),
		"PROXMOX_HOST", "https://env-host:8006",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Env should override file value for Host.
	if cfg.Host != "https://env-host:8006" {
		t.Errorf("Host = %q, env override should take priority", cfg.Host)
	}
	// File values should still fill in token/secret since env is empty.
	if cfg.TokenID != "file-token" {
		t.Errorf("TokenID = %q, want file-token (from file)", cfg.TokenID)
	}
}
