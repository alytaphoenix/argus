package keyring

import "github.com/zalando/go-keyring"

const service = "ratataskr"

// Credential keys stored under the "ratataskr" service.
const (
	KeyProxmoxSecret   = "proxmox_secret"
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
)

// SSHNodeKey returns the keyring key for a per-node SSH password.
// The actual password for node "pve1" is stored as "ssh_node_pve1".
func SSHNodeKey(node string) string {
	return "ssh_node_" + node
}

func Save(key, value string) error {
	return keyring.Set(service, key, value)
}

func Load(key string) (string, error) {
	return keyring.Get(service, key)
}

func Delete(key string) error {
	return keyring.Delete(service, key)
}

// IsSet returns true if the key exists in the keychain.
func IsSet(key string) bool {
	_, err := keyring.Get(service, key)
	return err == nil
}
