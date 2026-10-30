package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/sshkey"
)

// Confirmation actions owned by the VM cloud-init section.
const (
	vmKeyConfirmClear  = "clear"
	vmKeyConfirmRotate = "rotate"
)

// vmKeySavedMsg reports the result of generating, importing or clearing the
// default cloud-init SSH public key. The root App handles it (before screen
// delegation) because the new config has to land in a.cfg and a.defaults too —
// otherwise the next provisioning form would still offer the old key.
type vmKeySavedMsg struct {
	cfg    config.Config
	notice string
	err    error
}

// updateVMKey owns every key the VM cloud-init section handles. Confirmation
// and text-input state are shared with the other sections and therefore
// intercepted earlier, in CredentialsModel.Update.
func (m CredentialsModel) updateVMKey(msg tea.KeyMsg) (CredentialsModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "b":
		return m, func() tea.Msg { return backMsg{} }
	case "left", "right", "h", "l":
		m.section = credSectionGlobal
		m.cursor = 0
		m.notice, m.err = "", nil
		return m, nil
	case "enter", "i":
		if m.vmKeySaving {
			return m, nil
		}
		ti := textinput.New()
		ti.Placeholder = "ssh-ed25519 AAAA... comment"
		ti.CharLimit = 4096
		ti.Width = 64
		ti.SetValue(m.cfg.Defaults.CISSHKey)
		ti.Focus()
		m.input = ti
		m.editing = true
		m.notice, m.err = "", nil
		return m, textinput.Blink
	case "g":
		if m.vmKeySaving {
			return m, nil
		}
		return m.requestVMKeyGenerate()
	case "d":
		if m.vmKeySaving {
			return m, nil
		}
		if strings.TrimSpace(m.cfg.Defaults.CISSHKey) == "" {
			m.notice = "no default cloud-init SSH key configured"
			m.err = nil
			return m, nil
		}
		m.confirmDelete = true
		m.confirmVMKey = vmKeyConfirmClear
		m.err = nil
		return m, nil
	}
	return m, nil
}

// finishVMKeyImport closes the paste dialog and starts the save. A blank
// submission is an error, never a delete: clearing is the explicit "d" action,
// so an accidental enter cannot silently disable cloud-init key injection.
func (m CredentialsModel) finishVMKeyImport(val string) (CredentialsModel, tea.Cmd) {
	m.editing = false
	m.nodeEditMode = ""
	if m.vmKeySaving {
		return m, nil
	}
	m.vmKeySaving = true
	return m, vmKeyImportCmd(m.cfg, val)
}

// requestVMKeyGenerate replaces the app-managed keypair. Replacing keys is
// destructive for anything that trusts the old public key, so an existing
// global key, stored path or on-disk pair asks for confirmation first.
func (m CredentialsModel) requestVMKeyGenerate() (CredentialsModel, tea.Cmd) {
	path, err := sshkey.DefaultPrivateKeyPath()
	if err != nil {
		m.err = err
		return m, nil
	}
	m.err = nil
	_, privErr := os.Stat(path)
	_, pubErr := os.Stat(path + ".pub")
	if strings.TrimSpace(m.cfg.Defaults.CISSHKey) != "" || strings.TrimSpace(m.cfg.Globals.SSHKey) != "" ||
		privErr == nil || pubErr == nil {
		m.confirmDelete = true
		m.confirmVMKey = vmKeyConfirmRotate
		return m, nil
	}
	m.vmKeySaving = true
	return m, vmKeyGenerateCmd(m.cfg, path)
}

// runVMKeyConfirm turns an answered confirmation into its command. The caller
// in CredentialsModel.Update has already cleared the confirmation flags.
func (m CredentialsModel) runVMKeyConfirm(action string) (CredentialsModel, tea.Cmd) {
	switch action {
	case vmKeyConfirmRotate:
		path, err := sshkey.DefaultPrivateKeyPath()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.vmKeySaving = true
		return m, vmKeyGenerateCmd(m.cfg, path)
	case vmKeyConfirmClear:
		m.vmKeySaving = true
		return m, vmKeyClearCmd(m.cfg)
	}
	return m, nil
}

// vmKeyGenerateCmd writes a fresh Ed25519 pair at the app-managed path and
// persists both the public key and the path. The files are written before the
// config: if the config write then fails, the operator is told the new files
// are on disk rather than being left with a config that silently still points
// at the old key. Neither path touches an existing VM — cloud-init only runs
// when a freshly cloned VM boots.
func vmKeyGenerateCmd(cfg config.Config, path string) tea.Cmd {
	return func() tea.Msg {
		publicKey, err := sshkey.Generate(path, true)
		if err != nil {
			return vmKeySavedMsg{err: fmt.Errorf("generate ssh key: %w", err)}
		}
		next := cfg
		next.Defaults.CISSHKey = publicKey
		// [globals] ssh_key is the operator's node SSH / bootstrap key, not a
		// cloud-init setting. Claim it only when nothing was chosen yet, or it
		// already is the managed pair: repointing a working admin key at a key
		// no host authorizes would break SSH for an action whose promise is
		// "future VMs only".
		if next.Globals.SSHKey == "" || next.Globals.SSHKey == path {
			next.Globals.SSHKey = path
		}
		next.ApplyGlobals()
		if err := config.SaveConfig(&next); err != nil {
			return vmKeySavedMsg{err: fmt.Errorf(
				"new keypair is on disk at %s but the config was not updated: %w", path, err)}
		}
		return vmKeySavedMsg{
			cfg:    next,
			notice: "generated ed25519 keypair at " + path + " (future VMs only)",
		}
	}
}

// vmKeyImportCmd stores a pasted public key as the provisioning default. It
// deliberately leaves globals.ssh_key alone: that key authenticates the
// operator's SSH sessions to existing hosts and is unrelated to cloud-init.
func vmKeyImportCmd(cfg config.Config, raw string) tea.Cmd {
	return func() tea.Msg {
		normalized, err := sshkey.NormalizePublicKeys(raw)
		if err != nil {
			return vmKeySavedMsg{err: fmt.Errorf("SSH public key: %w", err)}
		}
		next := cfg
		next.Defaults.CISSHKey = normalized
		next.ApplyGlobals()
		if err := config.SaveConfig(&next); err != nil {
			return vmKeySavedMsg{err: fmt.Errorf("write config: %w", err)}
		}
		return vmKeySavedMsg{cfg: next, notice: "saved cloud-init SSH public key"}
	}
}

// vmKeyClearCmd empties defaults.ci_ssh_key only. The private files and
// globals.ssh_key stay put: deleting them could lock the operator out of every
// host that key authenticates to; provisioning is blocked by the missing
// default instead.
func vmKeyClearCmd(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		next := cfg
		next.Defaults.CISSHKey = ""
		next.ApplyGlobals()
		if err := config.SaveConfig(&next); err != nil {
			return vmKeySavedMsg{err: fmt.Errorf("write config: %w", err)}
		}
		return vmKeySavedMsg{cfg: next, notice: "cleared the default cloud-init SSH public key"}
	}
}

// vmKeyView is the rendered description of the configured default cloud-init
// key. It never carries private material — only public-key fingerprints.
type vmKeyView struct {
	label  string   // row label; plural when several keys are configured
	value  string   // fingerprint, key count, "not configured", or the parse error
	kind   string   // "none" | "ok" | "multi" | "bad"
	detail []string // per-key fingerprints, only when several keys are configured
}

// vmKeySummary describes defaults.ci_ssh_key the way the Keys screen shows it.
func vmKeySummary(cfg config.Config) vmKeyView {
	raw := strings.TrimSpace(cfg.Defaults.CISSHKey)
	if raw == "" {
		return vmKeyView{label: "Cloud-init public key", value: "not configured", kind: "none"}
	}
	normalized, err := sshkey.NormalizePublicKeys(raw)
	if err != nil {
		return vmKeyView{label: "Cloud-init public key", value: "invalid key: " + err.Error(), kind: "bad"}
	}
	lines := strings.Split(normalized, "\n")
	if len(lines) > 1 {
		view := vmKeyView{
			label: "Cloud-init public keys",
			value: fmt.Sprintf("%d public keys", len(lines)),
			kind:  "multi",
		}
		for _, line := range lines {
			if fingerprint, err := sshkey.Fingerprint(line); err == nil {
				view.detail = append(view.detail, fingerprint)
			}
		}
		return view
	}
	fingerprint, err := sshkey.Fingerprint(lines[0])
	if err != nil {
		return vmKeyView{label: "Cloud-init public key", value: "invalid key: " + err.Error(), kind: "bad"}
	}
	return vmKeyView{label: "Cloud-init public key", value: fingerprint, kind: "ok"}
}

// vmKeyManagedPath reports the private key file argus created for the key it
// injects. [globals] ssh_key normally points at it, but when the operator keeps
// their own admin key there (which generating deliberately leaves alone) the
// managed pair is still the one being injected, so recognise it by its public
// half. Any other path belongs to the operator's SSH usage and stays hidden.
func vmKeyManagedPath(cfg config.Config) string {
	path, err := sshkey.DefaultPrivateKeyPath()
	if err != nil {
		return ""
	}
	if cfg.Globals.SSHKey == path {
		return path
	}
	if strings.TrimSpace(cfg.Defaults.CISSHKey) == "" {
		return ""
	}
	onDisk, err := os.ReadFile(path + ".pub")
	if err != nil {
		return ""
	}
	managed, err := sshkey.NormalizePublicKeys(string(onDisk))
	if err != nil {
		return ""
	}
	injected, err := sshkey.NormalizePublicKeys(cfg.Defaults.CISSHKey)
	if err != nil || managed != injected {
		return ""
	}
	return path
}

func (m CredentialsModel) viewVMKey(sb *strings.Builder) {
	// Newlines stay outside Render: lipgloss pads the blank lines a trailing
	// "\n" creates to the width of the block, shoving every following line to
	// the right.
	sb.WriteString(styleDim.Render("  Public key injected into every new QEMU VM through Proxmox cloud-init.") + "\n")
	sb.WriteString(styleDim.Render("  Existing VMs are never changed. Provisioning refuses to clone without a key.") + "\n\n")

	sb.WriteString(styleDim.Render(fmt.Sprintf("  %-24s  %s", "SETTING", "VALUE")) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 44))) + "\n")

	view := vmKeySummary(m.cfg)
	styled := styleRunning.Render(view.value)
	switch view.kind {
	case "none":
		styled = styleDim.Render(view.value)
	case "bad":
		styled = styleError.Render(view.value)
	}
	sb.WriteString(fmt.Sprintf("  %-24s  %s\n", view.label, styled))
	for _, fingerprint := range view.detail {
		sb.WriteString(fmt.Sprintf("  %-24s  %s\n", "", styleSubtle.Render(fingerprint)))
	}
	if path := vmKeyManagedPath(m.cfg); path != "" {
		sb.WriteString(fmt.Sprintf("  %-24s  %s\n", "Managed private key", styleSubtle.Render(path)))
	}

	if m.vmKeySaving {
		sb.WriteString("\n  " + stylePaused.Render("working on SSH keys ...") + "\n")
	}
}
