package stack

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Provision describes the Proxmox guest a stack runs on. A stack with a
// [provision] block is self-contained: `argus stack apply` creates the VM,
// installs a Salt minion on it, accepts its key, and only then applies the
// stack. Leaving the block out keeps the old behaviour (an existing minion).
type Provision struct {
	Node          string `toml:"node"`
	VMID          int    `toml:"vmid"`
	Name          string `toml:"name"`
	Template      int    `toml:"template"`
	CloudImage    string `toml:"cloud_image"`
	Storage       string `toml:"storage"`
	Cores         int    `toml:"cores"`
	MemoryMB      int    `toml:"memory_mb"`
	CPUType       string `toml:"cpu_type"`
	DiskGB        int    `toml:"disk_gb"`
	Bridge        string `toml:"bridge"`
	CIUser        string `toml:"ci_user"`
	IPConfig      string `toml:"ipconfig"`
	Nameserver    string `toml:"nameserver"`
	SearchDomain  string `toml:"searchdomain"`
	SSHPublicKey  string `toml:"ssh_public_key"`
	SSHPrivateKey string `toml:"ssh_private_key"`
	MinionID      string `toml:"minion_id"`
}

var (
	// provisionToken matches the identifiers argus interpolates into qm
	// commands: node, VM name, storage, bridge, and minion ID.
	provisionToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// provisionImage matches a Proxmox volume ID on the master, e.g.
	// "local:iso/noble-server-cloudimg-amd64.img". Colons and slashes are
	// required; whitespace and control characters are rejected.
	provisionImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*:[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	// provisionUser matches a Linux user name on the cloud image.
	provisionUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	// provisionIPConfig matches Proxmox ipconfig0 syntax without spaces.
	provisionIPConfig = regexp.MustCompile(`^[A-Za-z0-9=.,:/-]+$`)
	// provisionNameserver matches one IPv4/IPv6 address or DNS name.
	provisionNameserver = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
)

// Validate checks a [provision] block before argus ever builds a qm command.
// target is the stack's target minion, used as the default minion_id.
func (p Provision) Validate(target string) error {
	if err := validProvisionToken("node", p.Node); err != nil {
		return err
	}
	if err := validProvisionToken("name", p.Name); err != nil {
		return err
	}
	if err := validProvisionToken("storage", p.Storage); err != nil {
		return err
	}
	if p.Bridge != "" {
		if err := validProvisionToken("bridge", p.Bridge); err != nil {
			return err
		}
	}
	minionID := p.MinionID
	if minionID == "" {
		minionID = target
	}
	if err := validProvisionToken("minion_id", minionID); err != nil {
		return err
	}

	if p.VMID < 0 || p.VMID > 999999999 {
		return fmt.Errorf("vmid: %d is out of range", p.VMID)
	}
	if p.Template < 0 {
		return fmt.Errorf("template: must be a positive VMID or 0")
	}
	if p.CloudImage != "" {
		if err := validCloudImage(p.CloudImage); err != nil {
			return err
		}
	}
	hasTemplate := p.Template > 0
	hasImage := p.CloudImage != ""
	if hasTemplate == hasImage {
		return fmt.Errorf("exactly one of template or cloud_image is required")
	}
	if p.Cores < 0 || p.MemoryMB < 0 || p.DiskGB < 0 {
		return fmt.Errorf("cores, memory_mb, and disk_gb must be non-negative")
	}

	if strings.TrimSpace(p.IPConfig) == "" {
		return fmt.Errorf("ipconfig: is required (e.g. ip=dhcp or ip=192.168.0.10/24,gw=192.168.0.1)")
	}
	if !provisionIPConfig.MatchString(p.IPConfig) {
		return fmt.Errorf("ipconfig: %q contains characters argus will not pass to qm", p.IPConfig)
	}
	if p.CIUser != "" && !provisionUser.MatchString(p.CIUser) {
		return fmt.Errorf("ci_user: %q must match [a-z_][a-z0-9_-]*", p.CIUser)
	}
	if p.Nameserver != "" && !provisionNameserver.MatchString(p.Nameserver) {
		return fmt.Errorf("nameserver: %q must not contain whitespace or control characters", p.Nameserver)
	}
	if p.SearchDomain != "" && !provisionToken.MatchString(p.SearchDomain) {
		return fmt.Errorf("searchdomain: %q must match [A-Za-z0-9][A-Za-z0-9._-]*", p.SearchDomain)
	}

	if strings.TrimSpace(p.SSHPublicKey) == "" {
		return fmt.Errorf("ssh_public_key: is required")
	}
	if strings.ContainsAny(p.SSHPublicKey, "\x00\r") {
		return fmt.Errorf("ssh_public_key: contains control characters")
	}
	if p.SSHPrivateKey != "" {
		if !filepath.IsAbs(p.SSHPrivateKey) {
			return fmt.Errorf("ssh_private_key: must be an absolute host path: %q", p.SSHPrivateKey)
		}
		if strings.ContainsAny(p.SSHPrivateKey, "\x00\r\n") {
			return fmt.Errorf("ssh_private_key: contains control characters")
		}
	}
	return nil
}

func validProvisionToken(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s: is required", label)
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s: must not contain whitespace", label)
	}
	if !provisionToken.MatchString(value) {
		return fmt.Errorf("%s: %q must match [A-Za-z0-9][A-Za-z0-9._-]*", label, value)
	}
	return nil
}

func validCloudImage(image string) error {
	if strings.ContainsAny(image, " \t\r\n") {
		return fmt.Errorf("cloud_image: must not contain whitespace")
	}
	if !provisionImage.MatchString(image) {
		return fmt.Errorf("cloud_image: %q must be a Proxmox volume ID like local:iso/noble-server-cloudimg-amd64.img", image)
	}
	if strings.Contains(image, "..") {
		return fmt.Errorf("cloud_image: must not contain '..'")
	}
	return nil
}
