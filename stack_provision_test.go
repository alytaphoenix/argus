package main

import (
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/bootstrap"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/stack"
)

const mainTestPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl argus"

func testProvision() stack.Provision {
	return stack.Provision{
		Node:         "proxmox",
		VMID:         104,
		Name:         "phoenix-hermes",
		CloudImage:   "local:iso/noble-server-cloudimg-amd64.img",
		Storage:      "local-lvm",
		Cores:        2,
		MemoryMB:     4096,
		DiskGB:       32,
		Bridge:       "vmbr0",
		CIUser:       "ubuntu",
		IPConfig:     "ip=192.168.0.104/24,gw=192.168.0.1",
		Nameserver:   "192.168.0.42",
		SearchDomain: "phoenix.arpa",
		SSHPublicKey: mainTestPublicKey,
		MinionID:     "phoenix-hermes",
	}
}

func TestProvisionShellScript(t *testing.T) {
	script, err := provisionShellScript(testProvision(), 104)
	if err != nil {
		t.Fatalf("provisionShellScript: %v", err)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"qm create 104 --name 'phoenix-hermes' --memory 4096 --cores 2 --cpu 'host'",
		"--scsihw virtio-scsi-single --ostype l26 --agent enabled=1 --serial0 socket --onboot 1",
		"--net0 'virtio,bridge=vmbr0,firewall=0'",
		`img="$(pvesm path 'local:iso/noble-server-cloudimg-amd64.img')"`,
		`qm importdisk 104 "$img" 'local-lvm'`,
		"qm set 104 --scsi0 'local-lvm:vm-104-disk-0,discard=on,iothread=1'",
		"qm set 104 --ide2 'local-lvm:cloudinit'",
		"qm set 104 --boot order=scsi0",
		"qm resize 104 scsi0 32G",
		"base64 -d",
		"--ciuser 'ubuntu' --sshkeys \"$key\" --ipconfig0 'ip=192.168.0.104/24,gw=192.168.0.1'",
		"--nameserver '192.168.0.42' --searchdomain 'phoenix.arpa' --ciupgrade 0",
		"rm -f \"$key\"",
		"qm start 104",
		"ARGUS_PROVISION=ok",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n%s", want, script)
		}
	}
	if strings.Contains(script, "ssh-ed25519") {
		t.Error("raw public key material leaked into the shell script")
	}
	if strings.Count(script, "qm create") != 1 {
		t.Error("script must create exactly one VM")
	}
}

func TestProvisionShellScriptSkipsResize(t *testing.T) {
	p := testProvision()
	p.DiskGB = 0
	script, err := provisionShellScript(p, 104)
	if err != nil {
		t.Fatalf("provisionShellScript: %v", err)
	}
	if strings.Contains(script, "qm resize") {
		t.Fatalf("disk_gb=0 must not resize:\n%s", script)
	}
}

func TestProvisionShellScriptOmitsEmptyCloudInitValues(t *testing.T) {
	p := testProvision()
	p.Nameserver = ""
	p.SearchDomain = ""
	script, err := provisionShellScript(p, 104)
	if err != nil {
		t.Fatalf("provisionShellScript: %v", err)
	}
	if strings.Contains(script, "--nameserver") || strings.Contains(script, "--searchdomain") {
		t.Fatalf("empty cloud-init values must be omitted:\n%s", script)
	}
}

func TestProvisionShellScriptRejectsUnsafeInput(t *testing.T) {
	if _, err := provisionShellScript(testProvision(), 0); err == nil {
		t.Fatal("vmid 0 was accepted")
	}
	templateOnly := testProvision()
	templateOnly.CloudImage = ""
	templateOnly.Template = 9000
	if _, err := provisionShellScript(templateOnly, 104); err == nil {
		t.Fatal("template-only provision reached the importdisk path")
	}
	badName := testProvision()
	badName.Name = "phoenix-hermes; reboot"
	if _, err := provisionShellScript(badName, 104); err == nil {
		t.Fatal("unsafe VM name was accepted")
	}
	badKey := testProvision()
	badKey.SSHPublicKey = "not-a-key"
	if _, err := provisionShellScript(badKey, 104); err == nil {
		t.Fatal("invalid public key was accepted")
	}
	noCores := testProvision()
	noCores.Cores = 0
	if _, err := provisionShellScript(noCores, 104); err == nil {
		t.Fatal("zero cores was accepted")
	}
}

func TestSaltMinionScript(t *testing.T) {
	script, err := saltMinionScript("phoenix-hermes", "192.168.0.42")
	if err != nil {
		t.Fatalf("saltMinionScript: %v", err)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"export DEBIAN_FRONTEND=noninteractive",
		"sudo apt-get install -y curl ca-certificates",
		"https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh",
		"-i 'phoenix-hermes' -A '192.168.0.42'",
		"/etc/salt/minion.d/master.conf",
		"'master: 192.168.0.42'",
		"/etc/salt/minion.d/id.conf",
		"'id: phoenix-hermes'",
		"sudo systemctl enable --now salt-minion",
		"sudo systemctl restart salt-minion",
		"ARGUS_MINION=ok",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n%s", want, script)
		}
	}
}

func TestSaltMinionScriptRejectsUnsafeInput(t *testing.T) {
	if _, err := saltMinionScript("bad id", "192.168.0.42"); err == nil {
		t.Fatal("minion ID with whitespace was accepted")
	}
	if _, err := saltMinionScript("phoenix-hermes", "192.168.0.42; rm -rf /"); err == nil {
		t.Fatal("master address with shell metacharacters was accepted")
	}
	if _, err := saltMinionScript("", "192.168.0.42"); err == nil {
		t.Fatal("empty minion ID was accepted")
	}
}

func TestStaticIPFromIPConfig(t *testing.T) {
	cases := map[string]string{
		"ip=192.168.0.104/24,gw=192.168.0.1": "192.168.0.104",
		"ip=192.168.0.104":                   "192.168.0.104",
		"ip=10.0.0.5/8":                      "10.0.0.5",
		"ip=fd00::5/64":                      "fd00::5",
		"ip=dhcp":                            "",
		"ip=DHCP,gw=192.168.0.1":             "",
		"gw=192.168.0.1":                     "",
		"ip=not-an-ip/24":                    "",
		"ip=":                                "",
		"":                                   "",
	}
	for raw, want := range cases {
		if got := staticIPFromIPConfig(raw); got != want {
			t.Errorf("staticIPFromIPConfig(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestIPConfigUsesDHCP(t *testing.T) {
	for raw, want := range map[string]bool{
		"ip=dhcp":                  true,
		"ip=DHCP,gw=192.168.0.1":   true,
		"ip=192.168.0.104/24,gw=x": false,
		"gw=192.168.0.1":           false,
		"":                         false,
	} {
		if got := ipConfigUsesDHCP(raw); got != want {
			t.Errorf("ipConfigUsesDHCP(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestEffectiveProvisionAppliesDefaults(t *testing.T) {
	def := stack.Definition{
		Name:   "hermes",
		Target: "phoenix-hermes",
		Provision: &stack.Provision{
			Node:     "proxmox",
			Name:     "phoenix-hermes",
			Template: 9000,
			Storage:  "local-lvm",
			IPConfig: "ip=dhcp",
		},
	}
	cfg := &config.Config{
		Globals: config.GlobalsConfig{SSHKey: "/home/argus/.ssh/id_ed25519"},
		Defaults: config.DefaultsConfig{
			CPUCores:     4,
			MemoryMB:     8192,
			DiskGB:       64,
			Storage:      "local-zfs",
			Bridge:       "vmbr1",
			CIUser:       "debian",
			CISSHKey:     mainTestPublicKey,
			Nameserver:   "1.1.1.1",
			SearchDomain: "phoenix.arpa",
		},
	}
	p := effectiveProvision(def, cfg)
	if p.Cores != 4 || p.MemoryMB != 8192 || p.DiskGB != 64 {
		t.Fatalf("sizing defaults not applied: %+v", p)
	}
	if p.Bridge != "vmbr1" || p.CIUser != "debian" || p.Nameserver != "1.1.1.1" || p.SearchDomain != "phoenix.arpa" {
		t.Fatalf("cloud-init defaults not applied: %+v", p)
	}
	if p.Storage != "local-lvm" {
		t.Fatalf("explicit storage was overridden: %q", p.Storage)
	}
	if p.SSHPublicKey != mainTestPublicKey || p.SSHPrivateKey != "/home/argus/.ssh/id_ed25519" {
		t.Fatalf("key defaults not applied: %+v", p)
	}
	if p.MinionID != "phoenix-hermes" {
		t.Fatalf("minion_id = %q, want target", p.MinionID)
	}
	if def.Provision.Cores != 0 || def.Provision.Bridge != "" {
		t.Fatal("effectiveProvision mutated the stack definition")
	}
}

func TestEffectiveProvisionSafeFallbacks(t *testing.T) {
	def := stack.Definition{
		Name:   "hermes",
		Target: "phoenix-hermes",
		Provision: &stack.Provision{
			Node:         "proxmox",
			Name:         "phoenix-hermes",
			CloudImage:   "local:iso/noble.img",
			Storage:      "local-lvm",
			IPConfig:     "ip=dhcp",
			SSHPublicKey: mainTestPublicKey,
		},
	}
	p := effectiveProvision(def, nil)
	if p.Bridge != "vmbr0" {
		t.Fatalf("bridge fallback = %q", p.Bridge)
	}
	if p.CIUser != "ubuntu" {
		t.Fatalf("ci_user fallback = %q", p.CIUser)
	}
}

func TestStackProvisionDryRun(t *testing.T) {
	def := stack.Definition{
		Name:      "hermes",
		Target:    "phoenix-hermes",
		Provision: &stack.Provision{Node: "proxmox", VMID: 104, Name: "phoenix-hermes"},
	}
	line, err := stackProvisionDryRun(nil, &bootstrap.Manager{}, def)
	if err != nil {
		t.Fatalf("stackProvisionDryRun: %v", err)
	}
	if !strings.HasPrefix(line, "would provision phoenix-hermes on proxmox (vmid 104)") {
		t.Fatalf("unexpected dry-run line: %q", line)
	}
	none, err := stackProvisionDryRun(nil, &bootstrap.Manager{}, stack.Definition{Name: "plain"})
	if err != nil || none != "" {
		t.Fatalf("stack without provision returned %q, %v", none, err)
	}
}
