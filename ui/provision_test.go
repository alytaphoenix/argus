package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	"github.com/alytaphoenix/argus/sshkey"
)

// fakeProxmox records the clone/cloud-init traffic a provisioning run produces
// and decodes the percent-encoded sshkeys value Proxmox would receive.
type fakeProxmox struct {
	t        *testing.T
	requests int
	nextID   int
	clones   []int
	// sshKeys maps the cloned VMID to the authorized_keys content the
	// cloud-init PUT carried.
	sshKeys map[int]string
}

func (f *fakeProxmox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
		f.nextID++
		fmt.Fprintf(w, `{"data":%d}`, f.nextID)

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/clone"):
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("clone body: %v", err)
		}
		id, _ := body["newid"].(float64)
		f.clones = append(f.clones, int(id))
		fmt.Fprintf(w, `{"data":"UPID:pve1:0000:0000:0000:clone:qemu:%d:root@pam:"}`, int(id))

	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/status"):
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))

	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/config"):
		// Resource sizing uses a form PUT to the same path; only the JSON one
		// is the cloud-init call.
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			w.Write([]byte(`{"data":null}`))
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("cloud-init body: %v", err)
		}
		encoded, ok := body["sshkeys"]
		if !ok {
			f.t.Errorf("cloud-init PUT carried no sshkeys: %+v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decoded, err := url.QueryUnescape(encoded)
		if err != nil {
			f.t.Errorf("unescape sshkeys %q: %v", encoded, err)
		}
		f.sshKeys[pathVMID(f.t, r.URL.Path)] = decoded
		w.Write([]byte(`{"data":null}`))

	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// pathVMID pulls the VMID out of /api2/json/nodes/{node}/qemu/{vmid}/config.
// It reports failures with Errorf, never Fatalf: it runs on the httptest server
// goroutine, where FailNow would abort the handler and leave the client waiting
// for a response instead of failing the test.
func pathVMID(t *testing.T, path string) int {
	t.Helper()
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-1] != "config" {
		t.Errorf("path %q is not a VM config call", path)
		return 0
	}
	vmid, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		t.Errorf("vmid in %q: %v", path, err)
		return 0
	}
	return vmid
}

// provisionFixture builds a provisioning model wired to a fake Proxmox. The
// name/resources are filled in because startSubmit refuses to clone without
// them; the caller decides what the cloud-init fields hold.
func provisionFixture(t *testing.T, defaults config.DefaultsConfig, database *db.DB) (ProvisionModel, *fakeProxmox) {
	t.Helper()
	fake := &fakeProxmox{t: t, sshKeys: map[int]string{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := api.NewClient(&config.Config{Host: srv.URL, TokenID: "user@pam!tok", Secret: "sec"})
	if defaults.TemplateID == 0 {
		defaults.TemplateID = 9000
	}
	m := NewProvisionModel(client, api.Node{Name: "pve1"}, defaults, database, nil, "")
	m.inputs[provFieldName].SetValue("web")
	m.inputs[provFieldCPU].SetValue("2")
	m.inputs[provFieldMemory].SetValue("2048")
	return m, fake
}

func provisionTestDB(t *testing.T, profiles ...db.CloudInitProfile) *db.DB {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, p := range profiles {
		if err := database.SaveCloudInitProfile(p); err != nil {
			t.Fatalf("save profile %q: %v", p.Name, err)
		}
	}
	return database
}

func provisionKeyFixture(t *testing.T) string {
	t.Helper()
	publicKey, err := sshkey.Generate(t.TempDir()+"/key", false)
	if err != nil {
		t.Fatalf("fixture key: %v", err)
	}
	return publicKey
}

// submitProvision runs the same path the enter key takes: validate through
// startSubmit, then execute the returned provisioning command.
func submitProvision(t *testing.T, m ProvisionModel) provisionDoneMsg {
	t.Helper()
	validated, cmd := m.startSubmit()
	if cmd == nil {
		t.Fatalf("startSubmit refused: %v", validated.err)
	}
	msg := validated.submit()()
	done, ok := msg.(provisionDoneMsg)
	if !ok {
		t.Fatalf("provision produced %T, want provisionDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("provision failed: %v", done.err)
	}
	return done
}

// A batch run with nothing but a global default key must inject that key into
// every VM it clones — the whole point of the global default.
func TestProvisionBatchUsesGlobalKeyForEveryVM(t *testing.T) {
	global := provisionKeyFixture(t)
	m, fake := provisionFixture(t, config.DefaultsConfig{CISSHKey: global}, nil)
	m.inputs[provFieldBatch].SetValue("2")

	done := submitProvision(t, m)

	if done.count != 2 {
		t.Errorf("count = %d, want 2", done.count)
	}
	if len(fake.clones) != 2 {
		t.Fatalf("cloned %v, want two VMs", fake.clones)
	}
	if len(fake.sshKeys) != 2 {
		t.Fatalf("cloud-init ran for %d VMs, want 2", len(fake.sshKeys))
	}
	for vmid, sent := range fake.sshKeys {
		if sent != global {
			t.Errorf("vm %d sshkeys = %q, want the global default %q", vmid, sent, global)
		}
	}
}

func TestProvisionKeyPrecedence(t *testing.T) {
	global := provisionKeyFixture(t)
	profileKey := provisionKeyFixture(t)
	formKey := provisionKeyFixture(t)

	cases := []struct {
		name        string
		profileKey  string
		formKey     string
		wantKey     string
		wantProfile bool
	}{
		{name: "blank profile falls back to global", profileKey: "", wantKey: global, wantProfile: true},
		{name: "profile key beats global", profileKey: profileKey, wantKey: profileKey, wantProfile: true},
		{name: "form key beats profile and global", profileKey: profileKey, formKey: formKey, wantKey: formKey, wantProfile: true},
		{name: "global alone when no profile is loaded", profileKey: profileKey, wantKey: global},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var database *db.DB
			if tc.wantProfile {
				database = provisionTestDB(t, db.CloudInitProfile{
					Name:     "base",
					CIUser:   "ci",
					CISSHKey: tc.profileKey,
				})
			}
			m, fake := provisionFixture(t, config.DefaultsConfig{CISSHKey: global}, database)
			if tc.wantProfile {
				m.inputs[provFieldCIProfile].SetValue("base")
				m, _ = m.loadCIProfile()
				if !strings.Contains(m.profileNotice, "loaded cloud-init profile") {
					t.Fatalf("profile notice = %q", m.profileNotice)
				}
			}
			if tc.formKey != "" {
				m.inputs[provFieldSSHKey].SetValue(tc.formKey)
			}

			submitProvision(t, m)

			if len(fake.sshKeys) != 1 {
				t.Fatalf("cloud-init ran for %d VMs, want 1", len(fake.sshKeys))
			}
			for vmid, sent := range fake.sshKeys {
				if sent != tc.wantKey {
					t.Errorf("vm %d sshkeys = %q, want %q", vmid, sent, tc.wantKey)
				}
			}
		})
	}
}

func TestProvisionRefusesWithoutAnyKey(t *testing.T) {
	m, fake := provisionFixture(t, config.DefaultsConfig{}, nil)
	m.inputs[provFieldSSHKey].SetValue("")

	validated, cmd := m.startSubmit()
	if cmd != nil {
		t.Fatal("cloning must not start without an SSH key")
	}
	if validated.err == nil || validated.err.Error() != "SSH public key is required; generate or import one in Keys" {
		t.Errorf("err = %v, want the missing-key message", validated.err)
	}
	if validated.submitting {
		t.Error("model entered the submitting state without a key")
	}
	if fake.requests != 0 {
		t.Errorf("made %d Proxmox requests, want 0", fake.requests)
	}
}

func TestProvisionRefusesMalformedKey(t *testing.T) {
	for name, value := range map[string]string{
		"form only":      "not-a-key",
		"truncated blob": "ssh-ed25519 AAAA truncated",
	} {
		t.Run(name, func(t *testing.T) {
			m, fake := provisionFixture(t, config.DefaultsConfig{}, nil)
			m.inputs[provFieldSSHKey].SetValue(value)

			validated, cmd := m.startSubmit()
			if cmd != nil {
				t.Fatal("cloning must not start with a malformed key")
			}
			if validated.err == nil || !strings.HasPrefix(validated.err.Error(), "invalid SSH public key: ") {
				t.Errorf("err = %v, want an invalid-key message", validated.err)
			}
			if fake.requests != 0 {
				t.Errorf("made %d Proxmox requests, want 0", fake.requests)
			}
		})
	}
	// A malformed global default is refused too, even with the field blank.
	m, fake := provisionFixture(t, config.DefaultsConfig{CISSHKey: "garbage"}, nil)
	m.inputs[provFieldSSHKey].SetValue("")
	if _, cmd := m.startSubmit(); cmd != nil {
		t.Error("a malformed global default must block the submit")
	}
	if fake.requests != 0 {
		t.Errorf("made %d Proxmox requests, want 0", fake.requests)
	}
}

func TestLoadCIProfileKeyFallback(t *testing.T) {
	global := provisionKeyFixture(t)
	profileKey := provisionKeyFixture(t)

	// A profile without a key must restore the global rather than clear it.
	database := provisionTestDB(t, db.CloudInitProfile{Name: "base", CIUser: "ci", CISSHKey: ""})
	m, _ := provisionFixture(t, config.DefaultsConfig{CISSHKey: global}, database)
	m.inputs[provFieldCIProfile].SetValue("base")
	m.inputs[provFieldSSHKey].SetValue("") // as if a stale profile had cleared it
	m, _ = m.loadCIProfile()
	if got := m.inputs[provFieldSSHKey].Value(); got != global {
		t.Errorf("ssh key field = %q, want the global default restored", got)
	}

	// A profile key is normalised on load so submit cannot be handed junk.
	database = provisionTestDB(t, db.CloudInitProfile{Name: "spaced", CIUser: "ci", CISSHKey: "  " + profileKey + "  \n"})
	m, _ = provisionFixture(t, config.DefaultsConfig{CISSHKey: global}, database)
	m.inputs[provFieldCIProfile].SetValue("spaced")
	m, _ = m.loadCIProfile()
	if got := m.inputs[provFieldSSHKey].Value(); got != profileKey {
		t.Errorf("ssh key field = %q, want the canonical profile key %q", got, profileKey)
	}

	// A malformed profile key is reported and loads nothing.
	database = provisionTestDB(t, db.CloudInitProfile{Name: "broken", CIUser: "ci-user", CISSHKey: "not a public key"})
	m, _ = provisionFixture(t, config.DefaultsConfig{CISSHKey: global}, database)
	m.inputs[provFieldCIProfile].SetValue("broken")
	m, _ = m.loadCIProfile()
	if !strings.Contains(m.profileNotice, "invalid SSH public key") {
		t.Errorf("notice = %q, want a validation error", m.profileNotice)
	}
	if got := m.inputs[provFieldCIUser].Value(); got == "ci-user" {
		t.Error("a broken profile must not half-load its other fields")
	}
	if got := m.inputs[provFieldSSHKey].Value(); got != global {
		t.Errorf("ssh key field = %q, want the untouched global default", got)
	}
}
