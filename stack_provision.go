package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/bootstrap"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/sshkey"
	"github.com/alytaphoenix/argus/stack"
)

const (
	provisionSSHTimeout    = 5 * time.Minute
	provisionIPTimeout     = 3 * time.Minute
	provisionKeyTimeout    = 2 * time.Minute
	provisionMinionTimeout = 2 * time.Minute
	provisionPollInterval  = 5 * time.Second
	provisionProbeTimeout  = 20 * time.Second
)

var (
	minionIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	masterAddrPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:\[\]-]*$`)
	cpuTypePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9,._+-]*$`)
)

// ensureStackTarget provisions the guest a stack declares in [provision] and
// makes it a Salt minion. It is a no-op when the target minion's key is
// already accepted, so applying the same stack twice never recreates a VM.
// Returned lines are human-readable log entries; they never contain secrets.
func ensureStackTarget(ctx context.Context, mgr *bootstrap.Manager, cfg *config.Config, def stack.Definition) ([]string, error) {
	if def.Provision == nil {
		return nil, nil
	}
	if mgr == nil {
		return nil, fmt.Errorf("provision: bootstrap manager is not configured")
	}
	if mgr.Salt == nil {
		return nil, fmt.Errorf("provision: Salt API is not configured; cannot verify or accept the key for minion %q", def.Target)
	}

	keys, err := mgr.Salt.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("provision: list Salt keys: %w", err)
	}
	if saltKeyAccepted(keys.Accepted, def.Target) {
		return []string{"target minion already accepted"}, nil
	}

	p := effectiveProvision(def, cfg)
	if p.MinionID != "" && saltKeyAccepted(keys.Accepted, p.MinionID) {
		return []string{"minion " + p.MinionID + " already accepted"}, nil
	}
	if err := p.Validate(def.Target); err != nil {
		return nil, fmt.Errorf("provision: %w", err)
	}
	if mgr.Proxmox == nil {
		return nil, fmt.Errorf("provision: Proxmox client is not configured")
	}
	if p.Cores <= 0 || p.MemoryMB <= 0 {
		return nil, fmt.Errorf("provision: cores and memory_mb must be set in [provision] or [defaults]")
	}

	vmid := p.VMID
	if vmid == 0 {
		id, err := mgr.Proxmox.NextVMID(ctx)
		if err != nil {
			return nil, fmt.Errorf("provision: allocate VMID: %w", err)
		}
		vmid = id
	}

	var log []string
	guests, err := mgr.Proxmox.Guests(ctx, p.Node)
	if err != nil {
		return log, fmt.Errorf("provision: list guests on %s: %w", p.Node, err)
	}
	var existing *api.Guest
	for i := range guests {
		if guests[i].VMID == vmid {
			existing = &guests[i]
			break
		}
	}

	switch {
	case existing != nil:
		if existing.Type != "" && existing.Type != "qemu" {
			return log, fmt.Errorf("provision: VMID %d on %s is a %s guest, not a VM", vmid, p.Node, existing.Type)
		}
		if existing.Status == "running" {
			log = append(log, fmt.Sprintf("vm %d already exists on %s and is running", vmid, p.Node))
		} else {
			if err := mgr.Proxmox.GuestAction(ctx, *existing, "start"); err != nil {
				return log, fmt.Errorf("provision: start vm %d: %w", vmid, err)
			}
			log = append(log, fmt.Sprintf("vm %d already exists on %s; started it", vmid, p.Node))
		}
	case p.CloudImage != "":
		if mgr.SSH == nil {
			return log, fmt.Errorf("provision: SSH access to the Proxmox host is required to import %s", p.CloudImage)
		}
		script, err := provisionShellScript(p, vmid)
		if err != nil {
			return log, err
		}
		log = append(log, fmt.Sprintf("creating vm %d (%s) on %s from %s", vmid, p.Name, p.Node, p.CloudImage))
		res, err := mgr.SSH.Run(ctx, script)
		if err != nil {
			return log, fmt.Errorf("provision: create vm %d: %w", vmid, err)
		}
		if !strings.Contains(res.Stdout, "ARGUS_PROVISION=ok") {
			detail := strings.TrimSpace(strings.TrimSpace(res.Stdout) + " " + strings.TrimSpace(res.Stderr))
			if detail == "" {
				detail = "no output from qm"
			}
			return log, fmt.Errorf("provision: vm %d did not report success: %s", vmid, detail)
		}
		log = append(log, fmt.Sprintf("imported %s and started vm %d", p.CloudImage, vmid))
	default:
		if err := cloneProvisionTemplate(ctx, mgr.Proxmox, p, vmid); err != nil {
			return log, err
		}
		log = append(log, fmt.Sprintf("cloned template %d to vm %d (%s) on %s", p.Template, vmid, p.Name, p.Node))
		log = append(log, fmt.Sprintf("configured cloud-init on vm %d and started it", vmid))
	}

	ip := staticIPFromIPConfig(p.IPConfig)
	if ip == "" {
		if !ipConfigUsesDHCP(p.IPConfig) {
			return log, fmt.Errorf("provision: ipconfig %q has no static address; use ip=dhcp or ip=<address>/<prefix>", p.IPConfig)
		}
		log = append(log, fmt.Sprintf("waiting for vm %d to receive a DHCP address", vmid))
		ip = waitForGuestIP(ctx, mgr.Proxmox, p.Node, vmid, provisionIPTimeout)
		if ip == "" {
			return log, fmt.Errorf("provision: vm %d did not report an address through the guest agent; set a static provisioning.ipconfig", vmid)
		}
	}

	runner, err := bootstrap.NewSSHRunner(ip, p.CIUser, p.SSHPrivateKey, "")
	if err != nil {
		return log, fmt.Errorf("provision: ssh to %s: %w", ip, err)
	}
	log = append(log, "waiting for ssh on "+ip)
	if err := waitForSSH(ctx, runner, provisionSSHTimeout); err != nil {
		return log, fmt.Errorf("provision: vm %d (%s): %w", vmid, ip, err)
	}
	log = append(log, "ssh on "+ip+" is ready")

	master, err := provisionMasterAddress(mgr)
	if err != nil {
		return log, err
	}
	script, err := saltMinionScript(p.MinionID, master)
	if err != nil {
		return log, err
	}
	log = append(log, fmt.Sprintf("installing salt-minion %s on %s (master %s)", p.MinionID, ip, master))
	if _, err := runner.Run(ctx, script); err != nil {
		return log, fmt.Errorf("provision: install salt-minion on %s: %w", ip, err)
	}
	log = append(log, "salt-minion installed and started")

	if err := acceptMinionKey(ctx, mgr.Salt, p.MinionID, provisionKeyTimeout); err != nil {
		return log, fmt.Errorf("provision: %w", err)
	}
	log = append(log, "accepted salt key for "+p.MinionID)

	if err := waitForMinion(ctx, mgr.Salt, p.MinionID, provisionMinionTimeout); err != nil {
		return log, fmt.Errorf("provision: %w", err)
	}
	log = append(log, "minion "+p.MinionID+" is responding")
	return log, nil
}

// stackProvisionDryRun describes what apply would create without changing
// anything. It returns "" when the target is already a Salt minion.
func stackProvisionDryRun(ctx context.Context, mgr *bootstrap.Manager, def stack.Definition) (string, error) {
	if def.Provision == nil || mgr == nil {
		return "", nil
	}
	p := *def.Provision
	note := ""
	if mgr.Salt == nil {
		note = "; Salt API is not configured, so the target key cannot be verified"
	} else if keys, err := mgr.Salt.ListKeys(ctx); err == nil {
		minionID := strings.TrimSpace(def.Provision.MinionID)
		if minionID == "" {
			minionID = def.Target
		}
		if saltKeyAccepted(keys.Accepted, def.Target) || saltKeyAccepted(keys.Accepted, minionID) {
			return "", nil
		}
	} else {
		note = "; Salt key state is unavailable"
	}
	vmid := p.VMID
	if vmid == 0 && mgr.Proxmox != nil {
		if id, err := mgr.Proxmox.NextVMID(ctx); err == nil {
			vmid = id
		}
	}
	id := "auto"
	if vmid > 0 {
		id = strconv.Itoa(vmid)
	}
	return fmt.Sprintf("would provision %s on %s (vmid %s)%s", p.Name, p.Node, id, note), nil
}

// effectiveProvision applies [defaults] to a stack's [provision] block. A
// decoded stack already passes validation, but definitions built in code may
// rely on config defaults for sizing and cloud-init values.
func effectiveProvision(def stack.Definition, cfg *config.Config) stack.Provision {
	var p stack.Provision
	if def.Provision != nil {
		p = *def.Provision
	}
	var d config.DefaultsConfig
	if cfg != nil {
		d = cfg.Defaults
	}
	for _, field := range []*string{
		&p.Node, &p.Name, &p.CloudImage, &p.Storage, &p.Bridge, &p.CIUser,
		&p.IPConfig, &p.Nameserver, &p.SearchDomain, &p.SSHPublicKey,
		&p.SSHPrivateKey, &p.MinionID,
	} {
		*field = strings.TrimSpace(*field)
	}
	if p.Cores == 0 {
		p.Cores = d.CPUCores
	}
	if p.MemoryMB == 0 {
		p.MemoryMB = d.MemoryMB
	}
	if p.DiskGB == 0 {
		p.DiskGB = d.DiskGB
	}
	if p.Storage == "" {
		p.Storage = strings.TrimSpace(d.Storage)
	}
	if p.Bridge == "" {
		p.Bridge = strings.TrimSpace(d.Bridge)
	}
	if p.Bridge == "" {
		p.Bridge = "vmbr0"
	}
	if p.CIUser == "" {
		p.CIUser = strings.TrimSpace(d.CIUser)
	}
	if p.CIUser == "" {
		p.CIUser = "ubuntu"
	}
	if p.Nameserver == "" {
		p.Nameserver = strings.TrimSpace(d.Nameserver)
	}
	if p.SearchDomain == "" {
		p.SearchDomain = strings.TrimSpace(d.SearchDomain)
	}
	if p.SSHPublicKey == "" {
		p.SSHPublicKey = strings.TrimSpace(d.CISSHKey)
	}
	if p.SSHPrivateKey == "" && cfg != nil {
		p.SSHPrivateKey = strings.TrimSpace(cfg.Globals.SSHKey)
	}
	if p.MinionID == "" {
		p.MinionID = strings.TrimSpace(def.Target)
	}
	return p
}

// provisionShellScript renders the qm commands that create a VM from a
// cloud image. Every interpolated value is validated by Provision.Validate
// first; the SSH key is base64-encoded so no key material appears inline in
// the shell command. The script is sent to the Proxmox host on stdin.
func provisionShellScript(p stack.Provision, vmid int) (string, error) {
	target := p.MinionID
	if target == "" {
		target = p.Name
	}
	if err := p.Validate(target); err != nil {
		return "", fmt.Errorf("provision: %w", err)
	}
	if p.CloudImage == "" {
		return "", fmt.Errorf("provision: cloud_image is required for the importdisk path")
	}
	if p.Cores <= 0 || p.MemoryMB <= 0 {
		return "", fmt.Errorf("provision: cores and memory_mb must be positive")
	}
	cpuType := strings.TrimSpace(p.CPUType)
	if cpuType == "" {
		cpuType = "host"
	}
	if !cpuTypePattern.MatchString(cpuType) {
		return "", fmt.Errorf("provision: invalid cpu_type %q", cpuType)
	}
	if vmid <= 0 {
		return "", fmt.Errorf("provision: vmid must be positive")
	}
	key, err := normalizeProvisionPublicKey(p.SSHPublicKey)
	if err != nil {
		return "", err
	}
	bridge := p.Bridge
	if bridge == "" {
		bridge = "vmbr0"
	}

	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "qm create %d --name %s --memory %d --cores %d --cpu %s --scsihw virtio-scsi-single --ostype l26 --agent enabled=1 --serial0 socket --onboot 1 --net0 %s\n",
		vmid, shQuote(p.Name), p.MemoryMB, p.Cores, shQuote(cpuType), shQuote("virtio,bridge="+bridge+",firewall=0"))
	fmt.Fprintf(&b, "img=%s\n", cloudImageShellArg(p.CloudImage))
	fmt.Fprintf(&b, "qm importdisk %d \"$img\" %s\n", vmid, shQuote(p.Storage))
	fmt.Fprintf(&b, "qm set %d --scsi0 %s\n", vmid,
		shQuote(fmt.Sprintf("%s:vm-%d-disk-0,discard=on,iothread=1", p.Storage, vmid)))
	fmt.Fprintf(&b, "qm set %d --ide2 %s\n", vmid, shQuote(p.Storage+":cloudinit"))
	fmt.Fprintf(&b, "qm set %d --boot order=scsi0\n", vmid)
	if p.DiskGB > 0 {
		fmt.Fprintf(&b, "qm resize %d scsi0 %dG\n", vmid, p.DiskGB)
	}

	b.WriteString("key=\"$(mktemp /tmp/argus-ci-key.XXXXXX)\"\n")
	b.WriteString("trap 'rm -f \"$key\"' EXIT\n")
	b.WriteString("chmod 600 \"$key\"\n")
	fmt.Fprintf(&b, "printf '%%s' %s | base64 -d > \"$key\"\n",
		shQuote(base64.StdEncoding.EncodeToString([]byte(key))))

	fmt.Fprintf(&b, "qm set %d --ciuser %s --sshkeys \"$key\" --ipconfig0 %s",
		vmid, shQuote(p.CIUser), shQuote(p.IPConfig))
	if p.Nameserver != "" {
		fmt.Fprintf(&b, " --nameserver %s", shQuote(p.Nameserver))
	}
	if p.SearchDomain != "" {
		fmt.Fprintf(&b, " --searchdomain %s", shQuote(p.SearchDomain))
	}
	b.WriteString(" --ciupgrade 0\n")
	b.WriteString("rm -f \"$key\"\n")
	b.WriteString("trap - EXIT\n")
	fmt.Fprintf(&b, "qm start %d\n", vmid)
	b.WriteString("echo ARGUS_PROVISION=ok\n")
	return b.String(), nil
}

// saltMinionScript renders the guest-side installer. minionID and masterIP
// are validated before interpolation; both are single tokens by construction.
func saltMinionScript(minionID, masterIP string) (string, error) {
	if !minionIDPattern.MatchString(minionID) {
		return "", fmt.Errorf("provision: invalid Salt minion ID %q", minionID)
	}
	if !masterAddrPattern.MatchString(masterIP) {
		return "", fmt.Errorf("provision: invalid Salt master address %q", masterIP)
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("if ! command -v curl >/dev/null 2>&1; then\n")
	b.WriteString("  sudo apt-get update -y\n")
	b.WriteString("  sudo apt-get install -y curl ca-certificates\n")
	b.WriteString("fi\n")
	b.WriteString("if ! command -v salt-minion >/dev/null 2>&1; then\n")
	fmt.Fprintf(&b, "  curl -fsSL https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh | sudo -E sh -s -- -i %s -A %s\n",
		shQuote(minionID), shQuote(masterIP))
	b.WriteString("fi\n")
	b.WriteString("sudo mkdir -p /etc/salt/minion.d\n")
	fmt.Fprintf(&b, "printf '%%s\\n' %s | sudo tee /etc/salt/minion.d/master.conf >/dev/null\n",
		shQuote("master: "+masterIP))
	fmt.Fprintf(&b, "printf '%%s\\n' %s | sudo tee /etc/salt/minion.d/id.conf >/dev/null\n",
		shQuote("id: "+minionID))
	b.WriteString("sudo systemctl enable --now salt-minion\n")
	b.WriteString("sudo systemctl restart salt-minion\n")
	b.WriteString("echo ARGUS_MINION=ok\n")
	return b.String(), nil
}

// staticIPFromIPConfig extracts the address from a Proxmox ipconfig0 value
// such as "ip=192.168.0.104/24,gw=192.168.0.1". It returns "" for DHCP and
// invalid values.
func staticIPFromIPConfig(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "ip=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(part, "ip="))
		if value == "" || strings.EqualFold(value, "dhcp") {
			return ""
		}
		host := value
		if idx := strings.IndexByte(host, '/'); idx >= 0 {
			host = host[:idx]
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsUnspecified() {
			return ""
		}
		return ip.String()
	}
	return ""
}

// ipConfigUsesDHCP reports whether ipconfig0 asks Proxmox for a DHCP lease.
func ipConfigUsesDHCP(raw string) bool {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "ip=") {
			return strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(part, "ip=")), "dhcp")
		}
	}
	return false
}

func normalizeProvisionPublicKey(raw string) (string, error) {
	key, err := sshkey.NormalizePublicKeys(raw)
	if err != nil {
		return "", fmt.Errorf("provision: ssh_public_key: %w", err)
	}
	return key, nil
}

func cloneProvisionTemplate(ctx context.Context, client *api.Client, p stack.Provision, vmid int) error {
	upid, err := client.CloneVM(ctx, api.CloneOptions{
		SourceNode: p.Node,
		SourceVMID: p.Template,
		NewVMID:    vmid,
		Name:       p.Name,
		TargetNode: p.Node,
		Storage:    p.Storage,
		Full:       true,
	})
	if err != nil {
		return fmt.Errorf("provision: clone template %d: %w", p.Template, err)
	}
	exit, err := client.WaitForTask(ctx, p.Node, upid)
	if err != nil {
		return fmt.Errorf("provision: wait for clone of template %d: %w", p.Template, err)
	}
	if exit != "" && !strings.EqualFold(exit, "OK") {
		return fmt.Errorf("provision: clone template %d failed: %s", p.Template, exit)
	}
	if p.Cores > 0 || p.MemoryMB > 0 {
		if err := client.ResizeVMConfig(ctx, p.Node, "qemu", vmid, p.Cores, p.MemoryMB); err != nil {
			return fmt.Errorf("provision: resize vm %d: %w", vmid, err)
		}
	}
	key, err := normalizeProvisionPublicKey(p.SSHPublicKey)
	if err != nil {
		return err
	}
	if err := client.SetCloudInit(ctx, p.Node, vmid, api.CloudInitOptions{
		User:         p.CIUser,
		SSHKeys:      key,
		IPConfig:     p.IPConfig,
		Nameserver:   p.Nameserver,
		SearchDomain: p.SearchDomain,
	}); err != nil {
		return fmt.Errorf("provision: cloud-init vm %d: %w", vmid, err)
	}
	guest := api.Guest{Node: p.Node, Type: "qemu", VMID: vmid}
	if current, err := client.GuestStatus(ctx, p.Node, "qemu", vmid); err == nil && current.Status == "running" {
		return nil
	}
	if err := client.GuestAction(ctx, guest, "start"); err != nil {
		return fmt.Errorf("provision: start vm %d: %w", vmid, err)
	}
	return nil
}

func provisionMasterAddress(mgr *bootstrap.Manager) (string, error) {
	if host := strings.TrimSpace(mgr.HostIP); host != "" {
		return host, nil
	}
	if raw := strings.TrimSpace(mgr.SaltAPIURL); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return u.Hostname(), nil
		}
	}
	if id := strings.TrimSpace(mgr.MasterID); id != "" && (net.ParseIP(id) != nil || strings.Contains(id, ".")) {
		return id, nil
	}
	return "", fmt.Errorf("provision: Salt master address is unknown; configure [bootstrap] host")
}

func saltKeyAccepted(accepted []string, id string) bool {
	return saltKeyIn(accepted, id)
}

func saltKeyIn(list []string, id string) bool {
	for _, item := range list {
		if strings.EqualFold(item, id) {
			return true
		}
	}
	return false
}

func acceptMinionKey(ctx context.Context, salt *api.SaltClient, minionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		keys, err := salt.ListKeys(ctx)
		if err != nil {
			return fmt.Errorf("list Salt keys: %w", err)
		}
		if saltKeyAccepted(keys.Accepted, minionID) {
			return nil
		}
		if saltKeyIn(keys.Rejected, minionID) || saltKeyIn(keys.Denied, minionID) {
			return fmt.Errorf("Salt key for %s is rejected or denied on the master; delete it before retrying", minionID)
		}
		if saltKeyIn(keys.Pending, minionID) {
			if err := salt.AcceptKey(ctx, minionID); err != nil {
				return err
			}
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("minion %s did not present a Salt key within %s", minionID, timeout)
		}
		if err := sleepContext(ctx, provisionPollInterval); err != nil {
			return err
		}
	}
}

func waitForMinion(ctx context.Context, salt *api.SaltClient, minionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := salt.RunCmd(ctx, minionID, "true")
		if err == nil && strings.Contains(out, minionID+":") {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("minion %s is not responding: %w", minionID, lastErr)
			}
			return fmt.Errorf("minion %s is not responding to cmd.run", minionID)
		}
		if err := sleepContext(ctx, provisionPollInterval); err != nil {
			return err
		}
	}
}

func waitForGuestIP(ctx context.Context, client *api.Client, node string, vmid int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if ip := client.GuestPrimaryIP(ctx, node, "qemu", vmid); ip != "" {
			return ip
		}
		if time.Now().After(deadline) {
			return ""
		}
		if err := sleepContext(ctx, provisionPollInterval); err != nil {
			return ""
		}
	}
}

func waitForSSH(ctx context.Context, runner *bootstrap.SSHRunner, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, provisionProbeTimeout)
		err := runner.Probe(probeCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("ssh did not become available within %s: %w", timeout, lastErr)
		}
		if err := sleepContext(ctx, provisionPollInterval); err != nil {
			return err
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cloudImageShellArg resolves a Proxmox volume ID (storage:type/name) through
// pvesm, because qm importdisk takes a filesystem path.
func cloudImageShellArg(image string) string {
	return "\"$(pvesm path " + shQuote(image) + ")\""
}

// shQuote wraps s in single quotes, which is safe for every value argus
// passes to qm and to the minion installer.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
