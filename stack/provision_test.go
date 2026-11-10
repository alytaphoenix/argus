package stack

import (
	"strings"
	"testing"
)

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl argus"

func stackWithProvision(provision string) string {
	return `name = "hermes"
target = "phoenix-hermes"
install_root = "/opt/argus/hermes"

` + provision + `

[[service]]
id = "hermes"
image = "example/hermes:1"
`
}

const cloudImageProvision = `[provision]
node = "proxmox"
name = "phoenix-hermes"
cloud_image = "local:iso/noble-server-cloudimg-amd64.img"
storage = "local-lvm"
cores = 2
memory_mb = 4096
disk_gb = 32
bridge = "vmbr0"
ci_user = "ubuntu"
ipconfig = "ip=192.168.0.104/24,gw=192.168.0.1"
nameserver = "192.168.0.42"
searchdomain = "phoenix.arpa"
ssh_public_key = "` + testPublicKey + `"
ssh_private_key = "/Users/alytaphoenix/.ssh/argus_bootstrap"
`

func TestDecodeProvisionCloudImage(t *testing.T) {
	def, err := Decode(stackWithProvision(cloudImageProvision))
	if err != nil {
		t.Fatalf("valid provision rejected: %v", err)
	}
	p := def.Provision
	if p == nil {
		t.Fatal("provision block was dropped")
	}
	if p.MinionID != "phoenix-hermes" {
		t.Fatalf("minion_id = %q, want the target", p.MinionID)
	}
	if p.Name != "phoenix-hermes" || p.Node != "proxmox" || p.Storage != "local-lvm" {
		t.Fatalf("unexpected provision values: %+v", *p)
	}
	if p.Template != 0 || p.Cores != 2 || p.MemoryMB != 4096 || p.DiskGB != 32 {
		t.Fatalf("unexpected sizing: %+v", *p)
	}
}

func TestDecodeProvisionTemplate(t *testing.T) {
	body := `[provision]
node = "pve"
name = "media01"
template = 9000
storage = "local-lvm"
ipconfig = "ip=dhcp"
ssh_public_key = "` + testPublicKey + `"
minion_id = "media01"
`
	def, err := Decode(stackWithProvision(body))
	if err != nil {
		t.Fatalf("template provision rejected: %v", err)
	}
	if def.Provision.Template != 9000 || def.Provision.CloudImage != "" {
		t.Fatalf("unexpected template provision: %+v", *def.Provision)
	}
	if def.Provision.MinionID != "media01" {
		t.Fatalf("minion_id = %q", def.Provision.MinionID)
	}
}

func TestProvisionNormalizes(t *testing.T) {
	body := `[provision]
node = " proxmox "
name = " phoenix-hermes "
template = 9000
storage = " local-lvm "
ipconfig = " ip=dhcp "
ssh_public_key = " ` + testPublicKey + ` "
`
	def, err := Decode(stackWithProvision(body))
	if err != nil {
		t.Fatalf("provision with padding rejected: %v", err)
	}
	if def.Provision.Node != "proxmox" || def.Provision.Name != "phoenix-hermes" ||
		def.Provision.Storage != "local-lvm" || def.Provision.IPConfig != "ip=dhcp" {
		t.Fatalf("values not trimmed: %+v", *def.Provision)
	}
}

func TestProvisionRoundTrip(t *testing.T) {
	def, err := Decode(stackWithProvision(cloudImageProvision))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	encoded, err := Encode(def)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(encoded, "[provision]") {
		t.Fatalf("encoded TOML lost the provision block:\n%s", encoded)
	}
	again, err := Decode(encoded)
	if err != nil {
		t.Fatalf("round-trip Decode: %v\n%s", err, encoded)
	}
	if again.Provision == nil {
		t.Fatal("round-trip lost provision")
	}
	if *again.Provision != *def.Provision {
		t.Fatalf("round-trip changed provision:\n got %+v\nwant %+v", *again.Provision, *def.Provision)
	}
}

func TestProvisionUnknownKeyRejected(t *testing.T) {
	body := cloudImageProvision + "typo_key = 1\n"
	if _, err := Decode(stackWithProvision(body)); err == nil {
		t.Fatal("unknown [provision] key was accepted")
	}
}

func TestProvisionValidation(t *testing.T) {
	replace := func(old, new string) string {
		return strings.Replace(stackWithProvision(cloudImageProvision), old, new, 1)
	}
	cases := map[string]string{
		"missing node":               replace(`node = "proxmox"`+"\n", ""),
		"missing name":               replace(`name = "phoenix-hermes"`+"\n", ""),
		"missing storage":            replace(`storage = "local-lvm"`+"\n", ""),
		"missing public key":         replace(`ssh_public_key = "`+testPublicKey+`"`+"\n", ""),
		"missing ipconfig":           replace(`ipconfig = "ip=192.168.0.104/24,gw=192.168.0.1"`+"\n", ""),
		"template and image":         replace(`storage = "local-lvm"`, "template = 9000\nstorage = \"local-lvm\""),
		"neither template nor image": replace(`cloud_image = "local:iso/noble-server-cloudimg-amd64.img"`+"\n", ""),
		"negative template":          replace(`storage = "local-lvm"`, "template = -1\nstorage = \"local-lvm\""),
		"vmid too large":             replace(`cores = 2`, "vmid = 1000000000\ncores = 2"),
		"negative cores":             replace(`cores = 2`, "cores = -1"),
		"negative memory":            replace(`memory_mb = 4096`, "memory_mb = -1"),
		"negative disk":              replace(`disk_gb = 32`, "disk_gb = -1"),
		"node whitespace":            replace(`node = "proxmox"`, `node = "prox mox"`),
		"name whitespace":            replace(`name = "phoenix-hermes"`, `name = "phoenix hermes"`),
		"storage whitespace":         replace(`storage = "local-lvm"`, `storage = "local lvm"`),
		"bridge whitespace":          replace(`bridge = "vmbr0"`, `bridge = "vm br0"`),
		"minion whitespace":          replace(`cloud_image = "local:iso/noble-server-cloudimg-amd64.img"`, "minion_id = \"bad id\"\ncloud_image = \"local:iso/noble-server-cloudimg-amd64.img\""),
		"image whitespace":           replace(`local:iso/noble-server-cloudimg-amd64.img`, `local:iso/noble server.img`),
		"image missing storage":      replace(`local:iso/noble-server-cloudimg-amd64.img`, `noble-server-cloudimg-amd64.img`),
		"image traversal":            replace(`local:iso/noble-server-cloudimg-amd64.img`, `local:iso/../../etc/passwd`),
		"relative private key":       replace(`/Users/alytaphoenix/.ssh/argus_bootstrap`, `~/.ssh/argus_bootstrap`),
		"ipconfig whitespace":        replace(`ip=192.168.0.104/24,gw=192.168.0.1`, `ip=192.168.0.104/24, gw=192.168.0.1`),
		"bad ci user":                replace(`ci_user = "ubuntu"`, `ci_user = "Admin"`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(input); err == nil {
				t.Fatalf("invalid provision accepted:\n%s", input)
			}
		})
	}
}

func TestProvisionMinionDefaultsToTarget(t *testing.T) {
	body := strings.Replace(cloudImageProvision, `ssh_private_key = "/Users/alytaphoenix/.ssh/argus_bootstrap"`+"\n", "", 1)
	def := stackWithProvision(body)
	// Change the target and prove the default follows it.
	def = strings.Replace(def, `target = "phoenix-hermes"`, `target = "media01"`, 1)
	decoded, err := Decode(def)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Provision.MinionID != "media01" {
		t.Fatalf("minion_id = %q, want media01", decoded.Provision.MinionID)
	}
}
