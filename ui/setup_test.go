package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/sshkey"
)

func TestSetupFocusCycling(t *testing.T) {
	m := NewSetupModel("")
	if m.focus != setupFieldHost {
		t.Fatalf("initial focus = %d, want host", m.focus)
	}
	next, _ := m.Update(keyMsg("tab"))
	m = next.(SetupModel)
	if m.focus != setupFieldUsername {
		t.Fatalf("focus after tab = %d, want username", m.focus)
	}
	next, _ = m.Update(keyMsg("shift+tab"))
	m = next.(SetupModel)
	if m.focus != setupFieldHost {
		t.Fatalf("focus after shift+tab = %d, want host", m.focus)
	}
}

func TestSetupEnterAdvancesFields(t *testing.T) {
	m := NewSetupModel("")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(SetupModel)
	if m.focus != setupFieldUsername {
		t.Fatalf("focus = %d, want username after enter on host", m.focus)
	}
}

func TestSetupRequiresAllFields(t *testing.T) {
	m := NewSetupModel("")
	m = m.setFocus(setupFieldPassword)
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(SetupModel)
	if cmd != nil {
		t.Error("expected no connect cmd with empty fields")
	}
	if m.errMsg == "" {
		t.Error("expected validation error with empty fields")
	}
	if m.testing {
		t.Error("should not enter testing state with empty fields")
	}
}

func TestSetupConnectFiresWithAllFields(t *testing.T) {
	m := NewSetupModel("")
	m.inputs[setupFieldHost].SetValue("pve.test:8006")
	m.inputs[setupFieldPassword].SetValue("hunter2")
	m = m.setFocus(setupFieldPassword)
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(SetupModel)
	if cmd == nil {
		t.Fatal("expected connect cmd")
	}
	if !m.testing {
		t.Error("expected testing state")
	}
}

func TestSetupInsecureToggle(t *testing.T) {
	m := NewSetupModel("")
	if !m.insecure {
		t.Fatal("insecure should default to true for homelab self-signed certs")
	}
	next, _ := m.Update(keyMsg("ctrl+t"))
	m = next.(SetupModel)
	if m.insecure {
		t.Error("ctrl+t should toggle insecure off")
	}
}

func TestSetupResultError(t *testing.T) {
	m := NewSetupModel("")
	m.testing = true
	next, _ := m.Update(setupResultMsg{err: errors.New("boom")})
	m = next.(SetupModel)
	if m.testing {
		t.Error("testing should clear on result")
	}
	if m.errMsg == "" {
		t.Error("expected error message")
	}
	if m.Done() {
		t.Error("should not be done on error")
	}
}

func TestSetupResultSuccess(t *testing.T) {
	m := NewSetupModel("")
	m.testing = true
	next, cmd := m.Update(setupResultMsg{})
	m = next.(SetupModel)
	if !m.Done() {
		t.Error("expected done on success")
	}
	if cmd == nil {
		t.Error("expected quit cmd")
	}
}

func TestSetupSSHKeyFieldIsOptional(t *testing.T) {
	m := NewSetupModel("")
	field := m.inputs[setupFieldSSHKey]
	if field.CharLimit != 4096 {
		t.Errorf("ssh key field CharLimit = %d, want 4096", field.CharLimit)
	}
	if !strings.Contains(field.Placeholder, "blank") {
		t.Errorf("ssh key placeholder = %q, want it to advertise the blank default", field.Placeholder)
	}
	if field.EchoMode != 0 {
		t.Error("ssh public key field must be readable, not masked")
	}
}

func TestSetupCyclesAllFourFields(t *testing.T) {
	m := NewSetupModel("")
	want := []int{setupFieldUsername, setupFieldPassword, setupFieldSSHKey, setupFieldHost}
	for i, expected := range want {
		next, _ := m.Update(keyMsg("tab"))
		m = next.(SetupModel)
		if m.focus != expected {
			t.Fatalf("focus after tab %d = %d, want %d", i+1, m.focus, expected)
		}
	}
	next, _ := m.Update(keyMsg("shift+tab"))
	m = next.(SetupModel)
	if m.focus != setupFieldSSHKey {
		t.Fatalf("shift+tab wrapped to %d, want %d", m.focus, setupFieldSSHKey)
	}
}

func TestSetupEnterFromSSHKeyFieldConnects(t *testing.T) {
	m := NewSetupModel("")
	m.inputs[setupFieldHost].SetValue("pve.test:8006")
	m.inputs[setupFieldPassword].SetValue("hunter2")
	m = m.setFocus(setupFieldSSHKey)
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(SetupModel)
	if cmd == nil {
		t.Fatal("expected a connect cmd with the optional ssh key left blank")
	}
	if m.errMsg != "" {
		t.Errorf("blank ssh key reported %q", m.errMsg)
	}
	if !m.testing {
		t.Error("expected testing state")
	}
}

// fakePVE answers the ticket login plus one successful GET /nodes, counting
// requests so a test can prove nothing was sent.
func fakePVE(t *testing.T, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		switch r.URL.Path {
		case "/api2/json/access/ticket":
			w.Write([]byte(`{"data":{"ticket":"PVE:root@pam:TICKET","CSRFPreventionToken":"CSRF"}}`))
		case "/api2/json/nodes":
			w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupEnv isolates the test from the developer's config file, HOME (and
// therefore the generated key location) and real Keychain.
func setupEnv(t *testing.T) (home, cfgFile string) {
	t.Helper()
	home = t.TempDir()
	cfgFile = filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("HOME", home)
	t.Setenv("ARGUS_CONFIG", cfgFile)
	keyring.MockInit()
	return home, cfgFile
}

func TestSetupTestCmdBlankKeyGeneratesPairAndConfiguresIt(t *testing.T) {
	home, cfgFile := setupEnv(t)
	requests := 0
	srv := fakePVE(t, &requests)

	msg := setupTestCmd(context.Background(), srv.URL, "root@pam", "hunter2", "", false)()

	res, ok := msg.(setupResultMsg)
	if !ok {
		t.Fatalf("msg = %T, want setupResultMsg", msg)
	}
	if res.err != nil {
		t.Fatalf("setupTestCmd: %v", res.err)
	}
	if requests == 0 {
		t.Error("expected the connection test to run")
	}

	privatePath := filepath.Join(home, ".ssh", "argus_ed25519")
	info, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("generated private key missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %o, want 600", info.Mode().Perm())
	}
	// Re-running Ensure proves both halves exist and belong together.
	publicKey, err := sshkey.Ensure(privatePath)
	if err != nil {
		t.Fatalf("generated pair does not validate: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.CISSHKey != publicKey {
		t.Errorf("Defaults.CISSHKey = %q, want the generated %q", cfg.Defaults.CISSHKey, publicKey)
	}
	if cfg.Globals.SSHKey != privatePath {
		t.Errorf("Globals.SSHKey = %q, want %q", cfg.Globals.SSHKey, privatePath)
	}
	written, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(written), "PRIVATE KEY") {
		t.Error("config must reference the key path only, never the private key")
	}
}

func TestSetupTestCmdPastedKeyWritesNoPrivatePair(t *testing.T) {
	home, cfgFile := setupEnv(t)
	requests := 0
	srv := fakePVE(t, &requests)

	pasted, err := sshkey.Generate(filepath.Join(t.TempDir(), "operator_key"), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	msg := setupTestCmd(context.Background(), srv.URL, "root@pam", "hunter2", "  "+pasted+"  ", false)()

	if res, ok := msg.(setupResultMsg); !ok || res.err != nil {
		t.Fatalf("msg = %#v, want a successful setupResultMsg", msg)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.CISSHKey != pasted {
		t.Errorf("Defaults.CISSHKey = %q, want %q", cfg.Defaults.CISSHKey, pasted)
	}
	if cfg.Globals.SSHKey != "" {
		t.Errorf("Globals.SSHKey = %q, want empty: argus did not generate this key", cfg.Globals.SSHKey)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "argus_ed25519")); !os.IsNotExist(err) {
		t.Error("a pasted public key must not create a managed private keypair")
	}
	written, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(written), "PRIVATE KEY") {
		t.Error("config must never contain private key material")
	}
}

func TestSetupTestCmdRejectsMalformedKeyBeforeConnecting(t *testing.T) {
	_, cfgFile := setupEnv(t)
	requests := 0
	srv := fakePVE(t, &requests)

	msg := setupTestCmd(context.Background(), srv.URL, "root@pam", "hunter2", "definitely not a public key", false)()

	res, ok := msg.(setupResultMsg)
	if !ok {
		t.Fatalf("msg = %T, want setupResultMsg", msg)
	}
	if res.err == nil {
		t.Fatal("expected a validation error")
	}
	if requests != 0 {
		t.Errorf("made %d HTTP requests with an invalid key, want 0", requests)
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Error("a rejected key must not write a config file")
	}
}
