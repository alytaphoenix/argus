package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/config"
	gkr "github.com/alytaphoenix/ratataskr/keyring"
)

// cfgField describes one editable row in the config editor.
type cfgField struct {
	label       string
	hint        string
	placeholder string // shown when empty; pressing enter pre-fills the input with this
	isSecret    bool   // Keychain-managed; shown grayed out, not editable here
	isBool      bool   // toggle with space/enter
	keychainKey string // for secret fields: the Keychain key name

	get func(*config.Config) string
	set func(*config.Config, string)
}

// cfgSection groups fields under a tab.
type cfgSection struct {
	title  string
	fields []*cfgField
}

// ConfigEditorModel is a Bubbletea model for editing ~/.config/ratataskr/config.toml.
type ConfigEditorModel struct {
	cfg      *config.Config
	sections []cfgSection
	section  int
	row      int
	editing  bool
	input    textinput.Model
	dirty    bool
	saved    bool
	errMsg   string
	width    int
	height   int
}

func strField(label, hint, placeholder string,
	get func(*config.Config) string,
	set func(*config.Config, string)) *cfgField {
	return &cfgField{label: label, hint: hint, placeholder: placeholder, get: get, set: set}
}

func boolField(label, hint string,
	get func(*config.Config) bool,
	set func(*config.Config, bool)) *cfgField {
	return &cfgField{
		label:  label,
		hint:   hint,
		isBool: true,
		get: func(c *config.Config) string {
			return strconv.FormatBool(get(c))
		},
		set: func(c *config.Config, v string) {
			b, _ := strconv.ParseBool(v)
			set(c, b)
		},
	}
}

func intField(label, hint, placeholder string,
	get func(*config.Config) int,
	set func(*config.Config, int)) *cfgField {
	return &cfgField{
		label:       label,
		hint:        hint,
		placeholder: placeholder,
		get: func(c *config.Config) string {
			if n := get(c); n != 0 {
				return strconv.Itoa(n)
			}
			return ""
		},
		set: func(c *config.Config, v string) {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				set(c, n)
			} else {
				set(c, 0)
			}
		},
	}
}

func secretField(label, hint, keychainKey string) *cfgField {
	return &cfgField{
		label:       label,
		hint:        hint,
		isSecret:    true,
		keychainKey: keychainKey,
		get:         func(*config.Config) string { return "" },
		set:         func(*config.Config, string) {},
	}
}

func buildCfgSections(cfg *config.Config) []cfgSection {
	_ = cfg // sections reference cfg via closures
	return []cfgSection{
		{
			title: "Proxmox",
			fields: []*cfgField{
				strField("host", "required: Proxmox VE API address", "https://pve.homelab.local:8006",
					func(c *config.Config) string { return c.Host },
					func(c *config.Config, v string) { c.Host = v }),
				strField("token_id", "API token (Datacenter→Permissions→API Tokens)", "user@pam!ratataskr",
					func(c *config.Config) string { return c.TokenID },
					func(c *config.Config, v string) { c.TokenID = v }),
				secretField("secret", "API token secret — store via Keys screen", gkr.KeyProxmoxSecret),
				strField("username", "ticket auth instead of token e.g. root@pam", "root@pam",
					func(c *config.Config) string { return c.Username },
					func(c *config.Config, v string) { c.Username = v }),
				secretField("password", "ticket auth password — store via Keys screen", gkr.KeyProxmoxPassword),
				boolField("insecure", "skip TLS verify for self-signed certs",
					func(c *config.Config) bool { return c.Insecure },
					func(c *config.Config, v bool) { c.Insecure = v }),
			},
		},
		{
			title: "Globals",
			fields: []*cfgField{
				strField("ssh_user", "default SSH username across all integrations", "alyta",
					func(c *config.Config) string { return c.Globals.SSHUser },
					func(c *config.Config, v string) { c.Globals.SSHUser = v }),
				strField("ssh_key", "path to SSH private key (empty = use ssh-agent)", "~/.ssh/id_ed25519",
					func(c *config.Config) string { return c.Globals.SSHKey },
					func(c *config.Config, v string) { c.Globals.SSHKey = v }),
				strField("domain", "homelab base domain e.g. homelab.local", "homelab.local",
					func(c *config.Config) string { return c.Globals.Domain },
					func(c *config.Config, v string) { c.Globals.Domain = v }),
				strField("timezone", "IANA timezone e.g. America/New_York", "UTC",
					func(c *config.Config) string { return c.Globals.Timezone },
					func(c *config.Config, v string) { c.Globals.Timezone = v }),
				strField("admin_email", "operator email for cert SANs / notifications", "admin@homelab.local",
					func(c *config.Config) string { return c.Globals.AdminEmail },
					func(c *config.Config, v string) { c.Globals.AdminEmail = v }),
			},
		},
		{
			title: "VM Defaults",
			fields: []*cfgField{
				intField("template_id", "Proxmox VMID of your cloud-init template", "9000",
					func(c *config.Config) int { return c.Defaults.TemplateID },
					func(c *config.Config, v int) { c.Defaults.TemplateID = v }),
				intField("cpu_cores", "default vCPU count for new VMs", "2",
					func(c *config.Config) int { return c.Defaults.CPUCores },
					func(c *config.Config, v int) { c.Defaults.CPUCores = v }),
				intField("memory_mb", "default RAM in MiB", "2048",
					func(c *config.Config) int { return c.Defaults.MemoryMB },
					func(c *config.Config, v int) { c.Defaults.MemoryMB = v }),
				intField("disk_gb", "default disk size in GiB", "20",
					func(c *config.Config) int { return c.Defaults.DiskGB },
					func(c *config.Config, v int) { c.Defaults.DiskGB = v }),
				strField("storage", "Proxmox storage pool ID", "local-lvm",
					func(c *config.Config) string { return c.Defaults.Storage },
					func(c *config.Config, v string) { c.Defaults.Storage = v }),
				strField("bridge", "network bridge", "vmbr0",
					func(c *config.Config) string { return c.Defaults.Bridge },
					func(c *config.Config, v string) { c.Defaults.Bridge = v }),
				strField("ostype", "guest OS type", "l26",
					func(c *config.Config) string { return c.Defaults.OSType },
					func(c *config.Config, v string) { c.Defaults.OSType = v }),
				strField("ci_user", "cloud-init username", "alyta",
					func(c *config.Config) string { return c.Defaults.CIUser },
					func(c *config.Config, v string) { c.Defaults.CIUser = v }),
				strField("ci_ssh_key", "cloud-init public key (paste ssh-rsa / ed25519 pub key)", "",
					func(c *config.Config) string { return c.Defaults.CISSHKey },
					func(c *config.Config, v string) { c.Defaults.CISSHKey = v }),
				strField("ci_ipconfig", "IP config for new VMs", "ip=dhcp",
					func(c *config.Config) string { return c.Defaults.CIIPConfig },
					func(c *config.Config, v string) { c.Defaults.CIIPConfig = v }),
				strField("nameserver", "DNS server for new VMs", "1.1.1.1",
					func(c *config.Config) string { return c.Defaults.Nameserver },
					func(c *config.Config, v string) { c.Defaults.Nameserver = v }),
				strField("backup_storage", "Proxmox storage ID for backup listing e.g. pbs", "pbs",
					func(c *config.Config) string { return c.Defaults.BackupStorage },
					func(c *config.Config, v string) { c.Defaults.BackupStorage = v }),
			},
		},
		{
			title: "Salt",
			fields: []*cfgField{
				strField("url", "salt-api endpoint", "https://salt.homelab.local:8000",
					func(c *config.Config) string { return c.Salt.URL },
					func(c *config.Config, v string) { c.Salt.URL = v }),
				strField("username", "salt-api user", "saltapi",
					func(c *config.Config) string { return c.Salt.Username },
					func(c *config.Config, v string) { c.Salt.Username = v }),
				secretField("password", "salt-api password — store via Keys screen", gkr.KeySaltPassword),
				strField("eauth", "auth backend: pam, ldap, or file", "pam",
					func(c *config.Config) string { return c.Salt.Eauth },
					func(c *config.Config, v string) { c.Salt.Eauth = v }),
				boolField("insecure", "skip TLS verify",
					func(c *config.Config) bool { return c.Salt.Insecure },
					func(c *config.Config, v bool) { c.Salt.Insecure = v }),
				strField("master_minion", "minion ID of the salt-master (for pillar writes)", "salt-master",
					func(c *config.Config) string { return c.Salt.MasterMinion },
					func(c *config.Config, v string) { c.Salt.MasterMinion = v }),
				strField("pillar_base", "pillar root on master", "/srv/pillar",
					func(c *config.Config) string { return c.Salt.PillarBase },
					func(c *config.Config, v string) { c.Salt.PillarBase = v }),
				strField("state_base", "state root on master", "/srv/salt",
					func(c *config.Config) string { return c.Salt.StateBase },
					func(c *config.Config, v string) { c.Salt.StateBase = v }),
			},
		},
		{
			title: "Nomad",
			fields: []*cfgField{
				strField("url", "Nomad API endpoint", "http://nomad.homelab.local:4646",
					func(c *config.Config) string { return c.Nomad.URL },
					func(c *config.Config, v string) { c.Nomad.URL = v }),
				secretField("token", "ACL token (leave unset if ACLs are disabled)", gkr.KeyNomadToken),
			},
		},
		{
			title: "Metrics",
			fields: []*cfgField{
				strField("prometheus url", "Prometheus query endpoint", "http://prometheus.homelab.local:9090",
					func(c *config.Config) string { return c.Prometheus.URL },
					func(c *config.Config, v string) { c.Prometheus.URL = v }),
				secretField("prometheus token", "Bearer token if auth enabled", gkr.KeyPrometheusToken),
				strField("prometheus listen", "bind address for ratataskr's own /metrics endpoint", ":9099",
					func(c *config.Config) string { return c.Prometheus.Listen },
					func(c *config.Config, v string) { c.Prometheus.Listen = v }),
				strField("loki url", "Loki log query endpoint", "http://loki.homelab.local:3100",
					func(c *config.Config) string { return c.Loki.URL },
					func(c *config.Config, v string) { c.Loki.URL = v }),
				secretField("loki token", "Bearer token if Loki is behind an auth proxy", gkr.KeyLokiToken),
				boolField("loki insecure", "skip TLS verify for Loki",
					func(c *config.Config) bool { return c.Loki.Insecure },
					func(c *config.Config, v bool) { c.Loki.Insecure = v }),
			},
		},
		{
			title: "Security",
			fields: []*cfgField{
				strField("crowdsec url", "CrowdSec LAPI address", "http://localhost:8080",
					func(c *config.Config) string { return c.CrowdSec.URL },
					func(c *config.Config, v string) { c.CrowdSec.URL = v }),
				secretField("crowdsec api_key", "cscli bouncers add ratataskr", gkr.KeyCrowdSecAPIKey),
				strField("wazuh url", "Wazuh REST API", "https://wazuh-manager.homelab.local:55000",
					func(c *config.Config) string { return c.Wazuh.URL },
					func(c *config.Config, v string) { c.Wazuh.URL = v }),
				strField("wazuh username", "Wazuh API user", "wazuh-wui",
					func(c *config.Config) string { return c.Wazuh.Username },
					func(c *config.Config, v string) { c.Wazuh.Username = v }),
				secretField("wazuh password", "Wazuh API password — store via Keys screen", gkr.KeyWazuhPassword),
			},
		},
		{
			title: "Services",
			fields: []*cfgField{
				strField("truenas url", "TrueNAS REST API", "https://truenas.homelab.local",
					func(c *config.Config) string { return c.TrueNAS.URL },
					func(c *config.Config, v string) { c.TrueNAS.URL = v }),
				secretField("truenas api_key", "TrueNAS → System → API Keys → Add", gkr.KeyTrueNASAPIKey),
				boolField("truenas insecure", "skip TLS verify for TrueNAS",
					func(c *config.Config) bool { return c.TrueNAS.Insecure },
					func(c *config.Config, v bool) { c.TrueNAS.Insecure = v }),
				strField("technitium url", "Technitium DNS admin API", "http://dns.homelab.local:5380",
					func(c *config.Config) string { return c.Technitium.URL },
					func(c *config.Config, v string) { c.Technitium.URL = v }),
				secretField("technitium token", "Technitium → Settings → API Keys", gkr.KeyTechnitiumToken),
				strField("stepca url", "step-ca API", "https://ca.homelab.local:8443",
					func(c *config.Config) string { return c.StepCA.URL },
					func(c *config.Config, v string) { c.StepCA.URL = v }),
				strField("stepca fingerprint", "run: step ca fingerprint", "",
					func(c *config.Config) string { return c.StepCA.Fingerprint },
					func(c *config.Config, v string) { c.StepCA.Fingerprint = v }),
				strField("matchbox url", "Matchbox PXE server", "http://matchbox.homelab.local:8080",
					func(c *config.Config) string { return c.Matchbox.URL },
					func(c *config.Config, v string) { c.Matchbox.URL = v }),
				secretField("matchbox token", "optional Bearer token if Matchbox is behind auth", gkr.KeyMatchboxToken),
				strField("homeassistant url", "Home Assistant API", "http://homeassistant.local:8123",
					func(c *config.Config) string { return c.HomeAssistant.URL },
					func(c *config.Config, v string) { c.HomeAssistant.URL = v }),
				secretField("homeassistant token", "HA → Profile → Long-Lived Access Tokens", gkr.KeyHAToken),
				boolField("homeassistant insecure", "skip TLS verify for HA",
					func(c *config.Config) bool { return c.HomeAssistant.Insecure },
					func(c *config.Config, v bool) { c.HomeAssistant.Insecure = v }),
				strField("hermes url", "Hermes agent API", "http://hermes.homelab.local:8080",
					func(c *config.Config) string { return c.Hermes.URL },
					func(c *config.Config, v string) { c.Hermes.URL = v }),
				secretField("hermes api_key", "Bearer token if Hermes auth is enabled", gkr.KeyHermesAPIKey),
				strField("hermes salt_target", "minion ID where Hermes + signal-cli run", "hermes",
					func(c *config.Config) string { return c.Hermes.SaltTarget },
					func(c *config.Config, v string) { c.Hermes.SaltTarget = v }),
				strField("hermes signal_phone", "Signal account in E.164 format", "+15555550100",
					func(c *config.Config) string { return c.Hermes.SignalPhone },
					func(c *config.Config, v string) { c.Hermes.SignalPhone = v }),
			},
		},
		{
			title: "Media",
			fields: []*cfgField{
				strField("plex url", "Plex Media Server", "http://192.168.1.50:32400",
					func(c *config.Config) string { return c.Plex.URL },
					func(c *config.Config, v string) { c.Plex.URL = v }),
				secretField("plex token", "X-Plex-Token — store via Keys screen (key: plex_token)", gkr.KeyPlexToken),
				boolField("plex insecure", "skip TLS verify for Plex",
					func(c *config.Config) bool { return c.Plex.Insecure },
					func(c *config.Config, v bool) { c.Plex.Insecure = v }),
				intField("plex vmid", "Proxmox VMID of the Plex VM (for status badge)", "0",
					func(c *config.Config) int { return c.Plex.VMID },
					func(c *config.Config, v int) { c.Plex.VMID = v }),
				strField("plex vm_node", "Proxmox node hosting Plex", "pve1",
					func(c *config.Config) string { return c.Plex.VMNode },
					func(c *config.Config, v string) { c.Plex.VMNode = v }),
				strField("sonarr url", "Sonarr", "http://192.168.1.50:8989",
					func(c *config.Config) string { return c.Servarr.Sonarr.URL },
					func(c *config.Config, v string) { c.Servarr.Sonarr.URL = v }),
				strField("sonarr api_key", "Sonarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Sonarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Sonarr.APIKey = v }),
				strField("radarr url", "Radarr", "http://192.168.1.50:7878",
					func(c *config.Config) string { return c.Servarr.Radarr.URL },
					func(c *config.Config, v string) { c.Servarr.Radarr.URL = v }),
				strField("radarr api_key", "Radarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Radarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Radarr.APIKey = v }),
				strField("lidarr url", "Lidarr", "http://192.168.1.50:8686",
					func(c *config.Config) string { return c.Servarr.Lidarr.URL },
					func(c *config.Config, v string) { c.Servarr.Lidarr.URL = v }),
				strField("lidarr api_key", "Lidarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Lidarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Lidarr.APIKey = v }),
				strField("readarr url", "Readarr", "http://192.168.1.50:8787",
					func(c *config.Config) string { return c.Servarr.Readarr.URL },
					func(c *config.Config, v string) { c.Servarr.Readarr.URL = v }),
				strField("readarr api_key", "Readarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Readarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Readarr.APIKey = v }),
				strField("prowlarr url", "Prowlarr", "http://192.168.1.50:9696",
					func(c *config.Config) string { return c.Servarr.Prowlarr.URL },
					func(c *config.Config, v string) { c.Servarr.Prowlarr.URL = v }),
				strField("prowlarr api_key", "Prowlarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Prowlarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Prowlarr.APIKey = v }),
				strField("bazarr url", "Bazarr", "http://192.168.1.50:6767",
					func(c *config.Config) string { return c.Servarr.Bazarr.URL },
					func(c *config.Config, v string) { c.Servarr.Bazarr.URL = v }),
				strField("bazarr api_key", "Bazarr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Bazarr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Bazarr.APIKey = v }),
				strField("overseerr url", "Overseerr", "http://192.168.1.50:5055",
					func(c *config.Config) string { return c.Servarr.Overseerr.URL },
					func(c *config.Config, v string) { c.Servarr.Overseerr.URL = v }),
				strField("overseerr api_key", "Overseerr → Settings → General → API Key", "",
					func(c *config.Config) string { return c.Servarr.Overseerr.APIKey },
					func(c *config.Config, v string) { c.Servarr.Overseerr.APIKey = v }),
				strField("tautulli url", "Tautulli", "http://192.168.1.50:8181",
					func(c *config.Config) string { return c.Servarr.Tautulli.URL },
					func(c *config.Config, v string) { c.Servarr.Tautulli.URL = v }),
				strField("tautulli api_key", "Tautulli → Settings → Web Interface → API Key", "",
					func(c *config.Config) string { return c.Servarr.Tautulli.APIKey },
					func(c *config.Config, v string) { c.Servarr.Tautulli.APIKey = v }),
			},
		},
		{
			title: "AI",
			fields: []*cfgField{
				secretField("anthropic api_key", "store via Keys screen or set ANTHROPIC_API_KEY env", gkr.KeyAnthropicAPIKey),
				strField("model", "leave empty for default (claude-sonnet-4-6)", "claude-sonnet-4-6",
					func(c *config.Config) string { return c.Claude.Model },
					func(c *config.Config, v string) { c.Claude.Model = v }),
			},
		},
	}
}

// NewConfigEditorModel creates the editor, loading sections from cfg.
func NewConfigEditorModel(cfg *config.Config) ConfigEditorModel {
	in := textinput.New()
	in.Width = 52
	in.CharLimit = 1024
	return ConfigEditorModel{
		cfg:      cfg,
		sections: buildCfgSections(cfg),
		input:    in,
	}
}

func (m ConfigEditorModel) Init() tea.Cmd { return textinput.Blink }

func (m *ConfigEditorModel) currentField() *cfgField {
	if m.section < 0 || m.section >= len(m.sections) {
		return nil
	}
	sec := m.sections[m.section]
	if m.row < 0 || m.row >= len(sec.fields) {
		return nil
	}
	return sec.fields[m.row]
}

func (m ConfigEditorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.editing {
			return m.handleEditKey(msg)
		}
		return m.handleNavKey(msg)
	}
	return m, nil
}

func (m ConfigEditorModel) handleNavKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "right", "tab", "l":
		m.section = (m.section + 1) % len(m.sections)
		m.row = 0
		m.errMsg = ""
		return m, nil

	case "left", "shift+tab", "h":
		m.section = (m.section + len(m.sections) - 1) % len(m.sections)
		m.row = 0
		m.errMsg = ""
		return m, nil

	case "down", "j":
		sec := m.sections[m.section]
		m.row = (m.row + 1) % len(sec.fields)
		return m, nil

	case "up", "k":
		sec := m.sections[m.section]
		m.row = (m.row + len(sec.fields) - 1) % len(sec.fields)
		return m, nil

	case " ", "enter", "e":
		f := m.currentField()
		if f == nil || f.isSecret {
			return m, nil
		}
		if f.isBool {
			cur := f.get(m.cfg)
			if cur == "true" {
				f.set(m.cfg, "false")
			} else {
				f.set(m.cfg, "true")
			}
			m.dirty = true
			m.saved = false
			return m, nil
		}
		// Start text editing: pre-fill with current value, or placeholder if empty.
		cur := f.get(m.cfg)
		if cur == "" {
			cur = f.placeholder
		}
		m.input.SetValue(cur)
		m.input.CursorEnd()
		m.input.Focus()
		m.editing = true
		m.errMsg = ""
		return m, textinput.Blink

	case "d":
		// Clear the current field.
		f := m.currentField()
		if f == nil || f.isSecret || f.isBool {
			return m, nil
		}
		f.set(m.cfg, "")
		m.dirty = true
		m.saved = false
		return m, nil

	case "s":
		m.errMsg = ""
		if err := config.SaveConfig(m.cfg); err != nil {
			m.errMsg = "save failed: " + err.Error()
			return m, nil
		}
		m.dirty = false
		m.saved = true
		return m, nil
	}
	return m, nil
}

func (m ConfigEditorModel) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.input.Blur()
		m.editing = false
		return m, nil
	case "enter":
		f := m.currentField()
		if f != nil {
			f.set(m.cfg, m.input.Value())
			m.dirty = true
			m.saved = false
		}
		m.input.Blur()
		m.editing = false
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m ConfigEditorModel) View() string {
	var b strings.Builder

	// ── Title bar ───────────────────────────────────────────────────────────────
	b.WriteString(styleTitle.Render("ratataskr config editor"))
	if m.dirty {
		b.WriteString("  " + stylePaused.Render("● unsaved"))
	} else if m.saved {
		b.WriteString("  " + styleRunning.Render("✓ saved"))
	}
	b.WriteString("\n\n")

	// ── Tab bar ─────────────────────────────────────────────────────────────────
	for i, sec := range m.sections {
		if i > 0 {
			b.WriteString(styleDim.Render(" │ "))
		}
		if i == m.section {
			b.WriteString(styleTabActive.Render(sec.title))
		} else {
			b.WriteString(styleTabInactive.Render(sec.title))
		}
	}
	b.WriteString("\n\n")

	// ── Field list ──────────────────────────────────────────────────────────────
	sec := m.sections[m.section]
	maxLabel := 0
	for _, f := range sec.fields {
		if len(f.label) > maxLabel {
			maxLabel = len(f.label)
		}
	}

	for i, f := range sec.fields {
		selected := i == m.row
		pad := strings.Repeat(" ", maxLabel-len(f.label)+2)

		var labelStr string
		if f.isSecret {
			labelStr = styleDim.Render(f.label)
		} else if selected {
			labelStr = styleCyan.Render(f.label)
		} else {
			labelStr = f.label
		}

		var valStr string
		switch {
		case f.isSecret:
			if f.keychainKey != "" && gkr.IsSet(f.keychainKey) {
				valStr = styleRunning.Render("● set") + styleDim.Render(" (Keychain: "+f.keychainKey+")")
			} else {
				valStr = styleDim.Render("○ not set") + styleDim.Render(" (Keychain: "+f.keychainKey+")")
			}
		case m.editing && selected:
			valStr = m.input.View()
		case f.isBool:
			cur := f.get(m.cfg)
			if cur == "true" {
				valStr = styleRunning.Render("true")
			} else {
				valStr = styleDim.Render("false")
			}
		default:
			cur := f.get(m.cfg)
			if cur == "" {
				if f.placeholder != "" {
					valStr = styleDim.Render("(empty — default: "+f.placeholder+")")
				} else {
					valStr = styleDim.Render("(empty)")
				}
			} else {
				valStr = cur
			}
		}

		cursor := "  "
		if selected {
			cursor = styleCyan.Render("▶ ")
		}

		line := cursor + labelStr + pad + valStr

		// Show hint inline on the selected row.
		if selected && f.hint != "" {
			available := m.width - 4
			lineLen := len([]rune(fmt.Sprintf("  %s%s%s", f.label, pad, valStr)))
			if lineLen < available-len(f.hint)-4 {
				line += "  " + styleDim.Render("# "+f.hint)
			}
		}

		b.WriteString(line + "\n")
	}

	// ── Status / error ──────────────────────────────────────────────────────────
	b.WriteString("\n")
	if m.errMsg != "" {
		b.WriteString(styleError.Render("  "+m.errMsg) + "\n\n")
	}

	// ── Help bar ────────────────────────────────────────────────────────────────
	if m.editing {
		b.WriteString(styleHelp.Render("enter: confirm · esc: cancel · ctrl+c: quit"))
	} else {
		b.WriteString(styleHelp.Render(
			"↑↓/jk: field · ←→/hl or tab: section · enter/e: edit · space: toggle · d: clear · s: save · q: quit",
		))
	}

	return b.String()
}

// RunConfigEditor launches the full-screen config editor. cfg may be partially
// populated (e.g. from a first-run load); the editor pre-fills fields from
// placeholder defaults for any empty values.
func RunConfigEditor(cfg *config.Config) error {
	p := tea.NewProgram(NewConfigEditorModel(cfg), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
