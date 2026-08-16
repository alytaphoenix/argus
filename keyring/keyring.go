package keyring

import "github.com/zalando/go-keyring"

const service = "argus"

// legacyService is the pre-rename Keychain service name. Load transparently
// falls back to it and migrates matching entries forward, so existing
// ratataskr installs don't lose stored credentials across the rename.
const legacyService = "ratataskr"

// Credential keys stored under the "argus" service.
const (
	KeyProxmoxSecret   = "proxmox_secret"
	KeyProxmoxPassword = "proxmox_password"
	KeySaltPassword    = "salt_password"
	KeyNomadToken      = "nomad_token"
	KeySSHPassword     = "ssh_password"
	KeyWazuhPassword   = "wazuh_password"
	KeyCrowdSecAPIKey  = "crowdsec_api_key"
	KeyMatchboxToken   = "matchbox_token"
	KeyTechnitiumToken = "technitium_token"
	KeyPrometheusToken = "prometheus_token"
	KeyAnthropicAPIKey = "anthropic_api_key"
	KeyTrueNASAPIKey   = "truenas_api_key"
	KeyPlexToken       = "plex_token"
	KeyHAToken         = "homeassistant_token"
	KeyLokiToken       = "loki_token"
	KeyHermesAPIKey    = "hermes_api_key"
)

// SSHNodeKey returns the keyring key for a per-node SSH password.
// The actual password for node "pve1" is stored as "ssh_node_pve1".
func SSHNodeKey(node string) string {
	return "ssh_node_" + node
}

func Save(key, value string) error {
	return keyring.Set(service, key, value)
}

// Load reads key from the Keychain. If it's missing under the current
// service, it falls back to the legacy "ratataskr" service and, on a hit,
// best-effort copies the value forward so future lookups skip the fallback.
func Load(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if err == nil {
		return v, nil
	}
	if v, legacyErr := keyring.Get(legacyService, key); legacyErr == nil {
		_ = keyring.Set(service, key, v)
		return v, nil
	}
	return "", err
}

func Delete(key string) error {
	return keyring.Delete(service, key)
}

// IsSet returns true if the key exists in the keychain (current or legacy service).
func IsSet(key string) bool {
	_, err := Load(key)
	return err == nil
}

// Overlay fills *target from the keychain when it is empty and the key exists.
// Values already set (config file or env) are never overwritten.
func Overlay(target *string, key string) {
	if *target == "" {
		if v, err := Load(key); err == nil {
			*target = v
		}
	}
}
