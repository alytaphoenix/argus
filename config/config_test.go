package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

	_, err := Load()
	if err == nil {
		t.Error("expected error when PROXMOX_HOST is empty, got nil")
	}
}

func TestLoadFailsWithoutCredentials(t *testing.T) {
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://pve.test:8006",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer cleanup()

	_, err := Load()
	if err == nil {
		t.Error("expected error when token/secret are empty, got nil")
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

	// Override HOME so Load finds our test config.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	// Ensure no env vars interfere.
	cleanup := setEnv(t,
		"PROXMOX_HOST", "",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer func() {
		os.Setenv("HOME", origHome)
		cleanup()
	}()

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

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	cleanup := setEnv(t, "PROXMOX_HOST", "", "PROXMOX_TOKEN_ID", "", "PROXMOX_SECRET", "")
	defer func() {
		os.Setenv("HOME", origHome)
		cleanup()
	}()

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

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	cleanup := setEnv(t,
		"PROXMOX_HOST", "https://env-host:8006",
		"PROXMOX_TOKEN_ID", "",
		"PROXMOX_SECRET", "",
	)
	defer func() {
		os.Setenv("HOME", origHome)
		cleanup()
	}()

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
