package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	keyring "github.com/zalando/go-keyring"

	"github.com/alytaphoenix/argus/config"
	gkr "github.com/alytaphoenix/argus/keyring"
	"github.com/alytaphoenix/argus/sshkey"
)

// TestMain keeps every ui test off the developer's real keychain: gkr calls go
// to go-keyring's in-memory mock instead of shelling
// `security find-generic-password`, which pops a macOS authorization prompt.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// vmKeyEnv isolates the config file and the managed key location (HOME), and
// returns both paths.
func vmKeyEnv(t *testing.T) (home, cfgFile string) {
	t.Helper()
	home = t.TempDir()
	cfgFile = filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("HOME", home)
	t.Setenv("ARGUS_CONFIG", cfgFile)
	return home, cfgFile
}

func vmKeyFixture(t *testing.T) string {
	t.Helper()
	publicKey, err := sshkey.Generate(filepath.Join(t.TempDir(), "fixture"), false)
	if err != nil {
		t.Fatalf("fixture key: %v", err)
	}
	return publicKey
}

func runVMKeyCmd(t *testing.T, cmd tea.Cmd) vmKeySavedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(vmKeySavedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want vmKeySavedMsg", msg)
	}
	return msg
}

func newVMKeyModel(cfg config.Config) CredentialsModel {
	m := NewCredentialsModel(nil, cfg)
	m.section = credSectionVMKey
	return m
}

func TestVMKeyGeneratePersistsKeyAndPath(t *testing.T) {
	home, cfgFile := vmKeyEnv(t)
	m := newVMKeyModel(config.Config{Host: "https://pve.test:8006"})

	next, cmd := m.Update(keyMsg("g"))
	m = next
	if m.confirmDelete {
		t.Fatal("a clean install should generate without asking")
	}
	if !m.vmKeySaving {
		t.Error("expected vmKeySaving while the command is in flight")
	}

	msg := runVMKeyCmd(t, cmd)
	if msg.err != nil {
		t.Fatalf("generate: %v", msg.err)
	}

	privatePath := filepath.Join(home, ".ssh", "argus_ed25519")
	if msg.cfg.Globals.SSHKey != privatePath {
		t.Errorf("Globals.SSHKey = %q, want %q", msg.cfg.Globals.SSHKey, privatePath)
	}
	publicKey, err := sshkey.Ensure(privatePath)
	if err != nil {
		t.Fatalf("generated pair invalid: %v", err)
	}
	if msg.cfg.Defaults.CISSHKey != publicKey {
		t.Errorf("Defaults.CISSHKey = %q, want the on-disk public key %q", msg.cfg.Defaults.CISSHKey, publicKey)
	}
	info, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("private key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %o, want 600", info.Mode().Perm())
	}

	written, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if strings.Contains(string(written), "PRIVATE KEY") {
		t.Error("config must store the key path, never the private key")
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Defaults.CISSHKey != publicKey || loaded.Globals.SSHKey != privatePath {
		t.Errorf("persisted config = %+v/%+v, want the generated values",
			loaded.Defaults.CISSHKey, loaded.Globals.SSHKey)
	}

	// The rendered section must describe the key without exposing it.
	m.cfg = msg.cfg
	view := m.View()
	if !strings.Contains(view, "SHA256:") {
		t.Error("view should show the public key fingerprint")
	}
	if strings.Contains(view, "PRIVATE KEY") {
		t.Error("view must never render private key material")
	}
}

func TestVMKeyGenerateAsksBeforeReplacingExistingKey(t *testing.T) {
	home, cfgFile := vmKeyEnv(t)
	cfg := config.Config{Host: "https://pve.test:8006", Defaults: config.DefaultsConfig{CISSHKey: vmKeyFixture(t)}}
	if err := config.SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	before, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	m := newVMKeyModel(cfg)
	next, cmd := m.Update(keyMsg("g"))
	m = next
	if cmd != nil {
		t.Error("rotation must not run before confirmation")
	}
	if !m.confirmDelete || m.confirmVMKey != vmKeyConfirmRotate {
		t.Fatalf("confirm state = %v/%q, want a pending rotate", m.confirmDelete, m.confirmVMKey)
	}
	if !strings.Contains(m.View(), "future VMs") {
		t.Error("confirmation must say only future VMs change")
	}

	// Declining changes nothing on disk and leaves the config intact.
	next, cmd = m.Update(keyMsg("n"))
	m = next
	if cmd != nil || m.vmKeySaving {
		t.Error("declining must not start any work")
	}
	if after, err := os.ReadFile(cfgFile); err != nil || string(after) != string(before) {
		t.Error("declining rotation must not rewrite the config")
	}

	// Confirming replaces the pair.
	next, _ = m.Update(keyMsg("g"))
	m = next
	_, cmd = m.Update(keyMsg("enter"))
	msg := runVMKeyCmd(t, cmd)
	if msg.err != nil {
		t.Fatalf("confirmed rotate: %v", msg.err)
	}
	if msg.cfg.Defaults.CISSHKey == cfg.Defaults.CISSHKey {
		t.Error("rotation kept the old public key")
	}
	if _, err := sshkey.Ensure(filepath.Join(home, ".ssh", "argus_ed25519")); err != nil {
		t.Errorf("rotated pair invalid: %v", err)
	}
}

func TestVMKeyImportCanonicalizesAndKeepsPrivatePath(t *testing.T) {
	vmKeyEnv(t)
	const operatorPrivateKey = "/home/op/.ssh/id_ed25519"
	cfg := config.Config{
		Host:     "https://pve.test:8006",
		Globals:  config.GlobalsConfig{SSHKey: operatorPrivateKey, SSHUser: "op"},
		Defaults: config.DefaultsConfig{CISSHKey: vmKeyFixture(t)},
	}
	if err := config.SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	m := newVMKeyModel(cfg)
	next, _ := m.Update(keyMsg("i"))
	m = next
	if !m.editing {
		t.Fatal("i should open the paste dialog")
	}
	if m.input.Value() != cfg.Defaults.CISSHKey {
		t.Errorf("paste dialog = %q, want the current key prefilled", m.input.Value())
	}
	if m.input.CharLimit != 4096 {
		t.Errorf("paste dialog CharLimit = %d, want 4096", m.input.CharLimit)
	}

	pasted := vmKeyFixture(t) + "  laptop@desk"
	m.input.SetValue(pasted)
	next, cmd := m.Update(keyMsg("enter"))
	m = next
	if m.editing {
		t.Error("enter should close the paste dialog")
	}

	msg := runVMKeyCmd(t, cmd)
	if msg.err != nil {
		t.Fatalf("import: %v", msg.err)
	}
	want := strings.Fields(pasted)[0] + " " + strings.Fields(pasted)[1]
	if msg.cfg.Defaults.CISSHKey != want {
		t.Errorf("Defaults.CISSHKey = %q, want the canonical %q", msg.cfg.Defaults.CISSHKey, want)
	}
	if msg.cfg.Globals.SSHKey != operatorPrivateKey {
		t.Errorf("Globals.SSHKey = %q, want the operator's own key %q untouched",
			msg.cfg.Globals.SSHKey, operatorPrivateKey)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Defaults.CISSHKey != want || loaded.Globals.SSHKey != operatorPrivateKey {
		t.Error("import did not persist the expected values")
	}
}

func TestVMKeyImportRejectsBlankWithoutTouchingConfig(t *testing.T) {
	_, cfgFile := vmKeyEnv(t)
	existing := vmKeyFixture(t)
	cfg := config.Config{Host: "https://pve.test:8006", Defaults: config.DefaultsConfig{CISSHKey: existing}}
	if err := config.SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	before, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	m := newVMKeyModel(cfg)
	next, _ := m.Update(keyMsg("enter"))
	m = next
	m.input.SetValue("   ")
	next, cmd := m.Update(keyMsg("enter"))
	m = next

	msg := runVMKeyCmd(t, cmd)
	if msg.err == nil {
		t.Fatal("a blank paste must be an error, not a delete")
	}
	if msg.cfg != (config.Config{}) {
		t.Errorf("a failed import must not hand the App a new config: %+v", msg.cfg)
	}
	if loaded, err := config.Load(); err != nil || loaded.Defaults.CISSHKey != existing {
		t.Errorf("rejected import changed the stored key to %q (%v)", loaded.Defaults.CISSHKey, err)
	}
	if after, err := os.ReadFile(cfgFile); err != nil || string(after) != string(before) {
		t.Error("a rejected import must not rewrite the config")
	}
}

func TestVMKeyImportRejectsMalformedKey(t *testing.T) {
	_, cfgFile := vmKeyEnv(t)
	cfg := config.Config{Host: "https://pve.test:8006", Defaults: config.DefaultsConfig{CISSHKey: vmKeyFixture(t)}}
	if err := config.SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	before, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	msg := runVMKeyCmd(t, vmKeyImportCmd(cfg, "not a public key at all"))
	if msg.err == nil {
		t.Fatal("expected a validation error")
	}
	if after, err := os.ReadFile(cfgFile); err != nil || string(after) != string(before) {
		t.Error("a malformed key must not rewrite the config")
	}
}

func TestVMKeyCancelLeavesEverythingAlone(t *testing.T) {
	home, cfgFile := vmKeyEnv(t)
	cfg := config.Config{Host: "https://pve.test:8006", Defaults: config.DefaultsConfig{CISSHKey: vmKeyFixture(t)}}
	if err := config.SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	before, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	m := newVMKeyModel(cfg)
	next, _ := m.Update(keyMsg("i"))
	m = next
	m.input.SetValue("ssh-rsa AAAA nonsense")
	next, cmd := m.Update(keyMsg("esc"))
	m = next
	if m.editing {
		t.Error("esc should close the dialog")
	}
	if cmd != nil {
		t.Error("cancel must not start work")
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "argus_ed25519")); !os.IsNotExist(err) {
		t.Error("cancel must not generate a keypair")
	}
	if after, err := os.ReadFile(cfgFile); err != nil || string(after) != string(before) {
		t.Error("cancel must not rewrite the config")
	}
}

func TestVMKeyClearNeedsConfirmationAndKeepsPrivateFiles(t *testing.T) {
	home, _ := vmKeyEnv(t)
	privatePath := filepath.Join(home, ".ssh", "argus_ed25519")
	publicKey, err := sshkey.Generate(privatePath, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cfg := config.Config{
		Host:     "https://pve.test:8006",
		Globals:  config.GlobalsConfig{SSHKey: privatePath},
		Defaults: config.DefaultsConfig{CISSHKey: publicKey},
	}

	m := newVMKeyModel(cfg)
	next, cmd := m.Update(keyMsg("d"))
	m = next
	if cmd != nil {
		t.Error("clearing must wait for confirmation")
	}
	if !m.confirmDelete || m.confirmVMKey != vmKeyConfirmClear {
		t.Fatalf("confirm state = %v/%q, want a pending clear", m.confirmDelete, m.confirmVMKey)
	}

	next, cmd = m.Update(keyMsg("y"))
	m = next
	msg := runVMKeyCmd(t, cmd)
	if msg.err != nil {
		t.Fatalf("clear: %v", msg.err)
	}
	if msg.cfg.Defaults.CISSHKey != "" {
		t.Errorf("Defaults.CISSHKey = %q, want empty", msg.cfg.Defaults.CISSHKey)
	}
	if msg.cfg.Globals.SSHKey != privatePath {
		t.Errorf("Globals.SSHKey = %q, want the private path left alone", msg.cfg.Globals.SSHKey)
	}
	if _, err := os.Stat(privatePath); err != nil {
		t.Errorf("clearing the default key deleted the private key file: %v", err)
	}
	if _, err := os.Stat(privatePath + ".pub"); err != nil {
		t.Errorf("clearing the default key deleted the public key file: %v", err)
	}
}

func TestVMKeyClearWithoutKeyIsANotice(t *testing.T) {
	vmKeyEnv(t)
	m := newVMKeyModel(config.Config{Host: "https://pve.test:8006"})
	next, cmd := m.Update(keyMsg("d"))
	m = next
	if cmd != nil || m.confirmDelete {
		t.Error("nothing to clear: expected a notice, not a confirmation")
	}
	if m.notice == "" {
		t.Error("expected a notice explaining there is no key")
	}
}

func TestVMKeySectionCyclingAndStatus(t *testing.T) {
	vmKeyEnv(t)
	fixture := vmKeyFixture(t)
	m := newVMKeyModel(config.Config{Defaults: config.DefaultsConfig{CISSHKey: fixture}})

	for _, step := range []struct {
		key  string
		want credSection
	}{
		{"right", credSectionGlobal},
		{"right", credSectionNodes},
		{"right", credSectionVMKey},
		{"left", credSectionGlobal},
	} {
		next, _ := m.Update(keyMsg(step.key))
		m = next
		if m.section != step.want {
			t.Fatalf("after %q section = %v, want %v", step.key, m.section, step.want)
		}
	}

	view := vmKeySummary(config.Config{Defaults: config.DefaultsConfig{CISSHKey: fixture}})
	if view.kind != "ok" || !strings.HasPrefix(view.value, "SHA256:") {
		t.Errorf("single key summary = %+v, want a fingerprint", view)
	}
	view = vmKeySummary(config.Config{})
	if view.kind != "none" || view.value != "not configured" {
		t.Errorf("empty summary = %+v, want not configured", view)
	}
	view = vmKeySummary(config.Config{Defaults: config.DefaultsConfig{CISSHKey: "garbage key"}})
	if view.kind != "bad" || !strings.Contains(view.value, "invalid key") {
		t.Errorf("invalid summary = %+v, want an invalid-key state", view)
	}
	view = vmKeySummary(config.Config{Defaults: config.DefaultsConfig{CISSHKey: fixture + "\n" + vmKeyFixture(t)}})
	if view.kind != "multi" || len(view.detail) != 2 || !strings.Contains(view.value, "2 public keys") {
		t.Errorf("multi summary = %+v, want two fingerprints", view)
	}
}

func TestVMKeySavedMsgUpdatesAppDefaults(t *testing.T) {
	vmKeyEnv(t)
	fixture := vmKeyFixture(t)
	newCfg := config.Config{
		Host:     "https://pve.test:8006",
		Defaults: config.DefaultsConfig{CISSHKey: fixture, TemplateID: 9001},
	}

	a := App{screen: screenCredentials}
	a.defaults = config.DefaultsConfig{TemplateID: 1}
	a.credentials = newVMKeyModel(config.Config{Defaults: config.DefaultsConfig{TemplateID: 1}})
	a.credentials.vmKeySaving = true

	updated, _ := a.Update(vmKeySavedMsg{cfg: newCfg, notice: "saved"})
	got := updated.(App)
	if got.defaults.CISSHKey != fixture {
		t.Errorf("App.defaults.CISSHKey = %q, want the imported key", got.defaults.CISSHKey)
	}
	if got.cfg.Defaults.CISSHKey != fixture {
		t.Errorf("App.cfg.Defaults.CISSHKey = %q, want the imported key", got.cfg.Defaults.CISSHKey)
	}
	if got.credentials.cfg.Defaults.CISSHKey != fixture || got.credentials.vmKeySaving ||
		got.credentials.notice != "saved" || got.credentials.err != nil {
		t.Errorf("credentials model not synced: %+v", got.credentials)
	}

	// A failure keeps the previous config and reports the error instead.
	failing := App{screen: screenCredentials}
	failing.defaults = config.DefaultsConfig{TemplateID: 7}
	failing.cfg = config.Config{Defaults: config.DefaultsConfig{TemplateID: 7}}
	failing.credentials.vmKeySaving = true
	updated, _ = failing.Update(vmKeySavedMsg{err: os.ErrPermission})
	got = updated.(App)
	if got.defaults.TemplateID != 7 || got.cfg.Defaults.TemplateID != 7 {
		t.Error("a failed key save must leave the running config untouched")
	}
	if got.credentials.vmKeySaving {
		t.Error("vmKeySaving must clear even on failure")
	}
	if got.credentials.err == nil {
		t.Error("expected the error to surface on the Keys screen")
	}
}

func TestScreenCapturesTabCredentialsVMKey(t *testing.T) {
	vmKeyEnv(t)
	a := App{screen: screenCredentials}
	a.credentials = newVMKeyModel(config.Config{})
	if a.screenCapturesTab() {
		t.Error("the key list must let tab switch sections")
	}
	a.credentials.editing = true
	if !a.screenCapturesTab() {
		t.Error("the paste dialog must own tab")
	}
	a.credentials.editing = false
	a.credentials.confirmDelete = true
	if !a.screenCapturesTab() {
		t.Error("a pending confirmation must own tab")
	}
	a.credentials.confirmDelete = false
	a.credentials.vmKeySaving = true
	if !a.screenCapturesTab() {
		t.Error("in-flight key work must own tab")
	}
}

// lipgloss pads the blank lines that a "\n" *inside* Render() creates to the
// width of the styled block, shoving every following line to the right, so
// newlines stay outside Render(). Only the sections whose header lines were
// affected are rendered here.
func TestCredentialsSectionsStayLeftAligned(t *testing.T) {
	vmKeyEnv(t)
	m := newVMKeyModel(config.Config{Defaults: config.DefaultsConfig{
		CISSHKey: vmKeyFixture(t) + "\n" + vmKeyFixture(t),
	}})

	for _, section := range []credSection{credSectionNodes, credSectionVMKey} {
		m.section = section
		for _, line := range strings.Split(m.View(), "\n") {
			if line != "" && strings.Trim(line, " ") == "" {
				t.Errorf("%v section emitted a padded blank line (%d spaces)", section, len(line))
			}
		}
	}

	m.section = credSectionVMKey
	vmView := m.View()
	for _, want := range []string{
		"\n  Existing VMs are never changed.",
		"\n  SETTING",
		"\n  Cloud-init public keys",
	} {
		if !strings.Contains(vmView, want) {
			t.Errorf("VM cloud-init view lost its column-2 line %q:\n%s", want, vmView)
		}
	}

	m.section = credSectionNodes
	if view := m.View(); !strings.Contains(view, "\n  Per-node SSH overrides loaded from keychain") {
		t.Errorf("Per-node SSH header is not at column 2:\n%s", view)
	}
}

// One dialog answers every delete, so a cancelled rotate must not follow the
// operator into the Global section and replace the row they chose.
func TestCancelledVMKeyPromptCannotHijackARowDelete(t *testing.T) {
	vmKeyEnv(t)
	m := newVMKeyModel(config.Config{Defaults: config.DefaultsConfig{CISSHKey: vmKeyFixture(t)}})

	next, _ := m.Update(keyMsg("g"))
	m = next
	if !m.confirmDelete || m.confirmVMKey != vmKeyConfirmRotate {
		t.Fatalf("expected a pending rotate, got %v/%q", m.confirmDelete, m.confirmVMKey)
	}
	if next, _ = m.Update(keyMsg("n")); next.confirmDelete || next.confirmVMKey != "" {
		t.Fatalf("canceling left %v/%q", next.confirmDelete, next.confirmVMKey)
	}
	m = next

	if err := gkr.Save(gkr.KeyProxmoxSecret, "s3cret"); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}
	m.section = credSectionGlobal
	m.cursor = 0
	if next, _ = m.Update(keyMsg("d")); !next.confirmDelete || next.confirmVMKey != "" {
		t.Fatalf("row delete carried VM-key state: %v/%q", next.confirmDelete, next.confirmVMKey)
	}
	m = next
	if next, _ = m.Update(keyMsg("y")); next.confirmVMKey != "" {
		t.Fatalf("confirming re-armed %q", next.confirmVMKey)
	}
	m = next

	if gkr.IsSet(gkr.KeyProxmoxSecret) {
		t.Error("the selected keychain row was not deleted")
	}
	if m.cfg.Defaults.CISSHKey == "" {
		t.Error("the confirm ran the cancelled cloud-init clear instead of the row delete")
	}
}

// [globals] ssh_key is the operator's node SSH / bootstrap key. Generating the
// cloud-init pair must not silently repoint it at a key no host authorizes.
func TestVMKeyGenerateLeavesOperatorSSHKeyAlone(t *testing.T) {
	home, cfgFile := vmKeyEnv(t)
	const operatorKey = "/home/op/.ssh/id_ed25519"
	m := newVMKeyModel(config.Config{
		Host:      "https://pve.test:8006",
		Globals:   config.GlobalsConfig{SSHKey: operatorKey, SSHUser: "op"},
		Bootstrap: config.BootstrapConfig{SSHKey: operatorKey},
	})

	next, cmd := m.Update(keyMsg("g"))
	m = next
	if cmd != nil {
		t.Fatal("an existing operator key must ask before generating")
	}
	if next, cmd = m.Update(keyMsg("y")); cmd == nil {
		t.Fatal("confirming must start the generation")
	}
	msg := runVMKeyCmd(t, cmd)
	if msg.err != nil {
		t.Fatalf("generate: %v", msg.err)
	}

	managed := filepath.Join(home, ".ssh", "argus_ed25519")
	if msg.cfg.Globals.SSHKey != operatorKey {
		t.Errorf("Globals.SSHKey = %q, want the operator key untouched", msg.cfg.Globals.SSHKey)
	}
	if msg.cfg.Bootstrap.SSHKey != operatorKey {
		t.Errorf("Bootstrap.SSHKey = %q: ApplyGlobals repointed bootstrap SSH", msg.cfg.Bootstrap.SSHKey)
	}
	if msg.cfg.Defaults.CISSHKey == "" {
		t.Error("the new public key was not recorded")
	}
	if got := vmKeyManagedPath(msg.cfg); got != managed {
		t.Errorf("vmKeyManagedPath = %q, want the pair that produced the key, %q", got, managed)
	}
	saved, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), operatorKey) {
		t.Errorf("config lost the operator ssh_key:\n%s", saved)
	}
}
