package main

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
	"github.com/alytaphoenix/argus/sshkey"
)

// ── Helper function tests ─────────────────────────────────────────────────────

func TestStr(t *testing.T) {
	args := map[string]any{
		"node":    "pve1",
		"num":     42.0,
		"missing": nil,
	}
	if got := str(args, "node"); got != "pve1" {
		t.Errorf("str(node) = %q, want pve1", got)
	}
	if got := str(args, "num"); got != "" {
		t.Errorf("str(num) should be empty for non-string, got %q", got)
	}
	if got := str(args, "absent"); got != "" {
		t.Errorf("str(absent) should be empty, got %q", got)
	}
}

func TestIntArg(t *testing.T) {
	args := map[string]any{
		"vmid":   float64(101),
		"exact":  42,
		"str":    "not-a-num",
		"absent": nil,
	}
	if got := intArg(args, "vmid"); got != 101 {
		t.Errorf("intArg(vmid) = %d, want 101", got)
	}
	if got := intArg(args, "exact"); got != 42 {
		t.Errorf("intArg(exact) = %d, want 42", got)
	}
	if got := intArg(args, "str"); got != 0 {
		t.Errorf("intArg(str) should be 0, got %d", got)
	}
	if got := intArg(args, "absent"); got != 0 {
		t.Errorf("intArg(absent) should be 0, got %d", got)
	}
}

func TestDnsRecordValue(t *testing.T) {
	cases := []struct {
		rec  api.DNSRecord
		want string
	}{
		{
			api.DNSRecord{Type: "A", RData: api.DNSRData{IPAddress: "10.0.0.1"}},
			"10.0.0.1",
		},
		{
			api.DNSRecord{Type: "AAAA", RData: api.DNSRData{IPAddress: "::1"}},
			"::1",
		},
		{
			api.DNSRecord{Type: "CNAME", RData: api.DNSRData{CName: "origin.example.com"}},
			"origin.example.com",
		},
		{
			api.DNSRecord{Type: "TXT", RData: api.DNSRData{Text: "v=spf1 ~all"}},
			"v=spf1 ~all",
		},
		{
			api.DNSRecord{Type: "MX", RData: api.DNSRData{Preference: 10, Exchange: "mail.example.com"}},
			"10 mail.example.com",
		},
		{
			api.DNSRecord{Type: "SRV"},
			"",
		},
	}
	for _, tc := range cases {
		got := dnsRecordValue(tc.rec)
		if got != tc.want {
			t.Errorf("dnsRecordValue(%s) = %q, want %q", tc.rec.Type, got, tc.want)
		}
	}
}

func TestServarrNames(t *testing.T) {
	m := map[string]*api.ServarrClient{
		"sonarr": nil,
		"radarr": nil,
	}
	got := servarrNames(m)
	if got == "" {
		t.Error("servarrNames should not be empty")
	}
	// Both names should appear somewhere in the output.
	if len(got) < 6 {
		t.Errorf("servarrNames too short: %q", got)
	}
}

// ── Tool catalogue tests ──────────────────────────────────────────────────────

func TestAllTools_Empty(t *testing.T) {
	// With only proxmox wired (nil everything else), only proxmox tools appear.
	c := &mcpClients{}
	tools := allTools(c)
	if len(tools) == 0 {
		t.Error("expected at least proxmox tools with empty clients")
	}
	for _, tool := range tools {
		// No optional-integration tools should appear.
		for _, prefix := range []string{"salt_", "nomad_", "prom_", "loki_", "truenas_",
			"crowdsec_", "wazuh_", "dns_", "plex_", "servarr_", "ca_", "matchbox_", "hass_"} {
			if len(tool.Name) >= len(prefix) && tool.Name[:len(prefix)] == prefix {
				t.Errorf("tool %q should not appear when client is nil", tool.Name)
			}
		}
	}
}

func TestAllTools_Names_Unique(t *testing.T) {
	// Build a full client bundle with all non-nil markers (nil pointers are fine —
	// we only test the catalogue, not execution).
	c := &mcpClients{
		salt:     &api.SaltClient{},
		nomad:    &api.NomadClient{},
		prom:     &api.PrometheusClient{},
		loki:     &api.LokiClient{},
		truenas:  &api.TrueNASClient{},
		crowdsec: &api.CrowdSecClient{},
		wazuh:    &api.WazuhClient{},
		dns:      &api.TechnitiumClient{},
		plex:     &api.PlexClient{},
		servarr:  map[string]*api.ServarrClient{"sonarr": nil},
		ca:       &api.StepCAClient{},
		matchbox: &api.MatchboxClient{},
		hass:     &api.HAClient{},
	}
	tools := allTools(c)
	seen := make(map[string]bool)
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name: %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.InputSchema.Type == "" {
			t.Errorf("tool %q has empty InputSchema.Type", tool.Name)
		}
	}
}

// ── proxmox_clone_vm SSH key resolution ─────────────────────────────────────

// fakeProxmox answers the clone/cloud-init endpoints and records the key each
// cloud-init call carried.
type fakeProxmox struct {
	t        *testing.T
	requests int
	nextID   int
	clones   []int
	sshKeys  map[int]string
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
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/config"):
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("cloud-init body: %v", err)
		}
		encoded, ok := body["sshkeys"]
		if !ok {
			f.t.Errorf("cloud-init carried no sshkeys: %+v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decoded, err := url.QueryUnescape(encoded)
		if err != nil {
			f.t.Errorf("unescape sshkeys: %v", err)
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		vmid, err := strconv.Atoi(parts[len(parts)-2])
		if err != nil {
			f.t.Errorf("vmid in %q: %v", r.URL.Path, err)
		}
		f.sshKeys[vmid] = decoded
		w.Write([]byte(`{"data":null}`))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func cloneFixture(t *testing.T, defaults config.DefaultsConfig) (*mcpClients, *fakeProxmox) {
	t.Helper()
	fake := &fakeProxmox{t: t, sshKeys: map[int]string{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &mcpClients{
		proxmox:  api.NewClient(&config.Config{Host: srv.URL, TokenID: "user@pam!tok", Secret: "sec"}),
		defaults: defaults,
	}, fake
}

func cloneArgs(extra map[string]any) map[string]any {
	args := map[string]any{"node": "pve1", "template_id": float64(9000), "name": "web"}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func mcpKeyFixture(t *testing.T) string {
	t.Helper()
	publicKey, err := sshkey.Generate(t.TempDir()+"/key", false)
	if err != nil {
		t.Fatalf("fixture key: %v", err)
	}
	return publicKey
}

func TestCloneVMUsesConfiguredDefaultKey(t *testing.T) {
	defaultKey := mcpKeyFixture(t)
	c, fake := cloneFixture(t, config.DefaultsConfig{CISSHKey: defaultKey})

	if _, err := callTool("proxmox_clone_vm", cloneArgs(nil), c); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(fake.clones) != 1 {
		t.Fatalf("cloned %v, want one VM", fake.clones)
	}
	if got := fake.sshKeys[1]; got != defaultKey {
		t.Errorf("sshkeys = %q, want the configured default %q", got, defaultKey)
	}
}

func TestCloneVMExplicitKeyWinsOverDefault(t *testing.T) {
	defaultKey := mcpKeyFixture(t)
	explicit := mcpKeyFixture(t)
	c, fake := cloneFixture(t, config.DefaultsConfig{CISSHKey: defaultKey})

	if _, err := callTool("proxmox_clone_vm", cloneArgs(map[string]any{"ci_ssh_keys": "  " + explicit + " "}), c); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if got := fake.sshKeys[1]; got != explicit {
		t.Errorf("sshkeys = %q, want the explicit %q", got, explicit)
	}
}

func TestCloneVMRejectsMissingKeyBeforeAnyRequest(t *testing.T) {
	c, fake := cloneFixture(t, config.DefaultsConfig{})

	_, err := callTool("proxmox_clone_vm", cloneArgs(map[string]any{"ci_ssh_keys": "   "}), c)
	if err == nil {
		t.Fatal("cloning without a key must fail")
	}
	if err.Error() != "SSH public key is required; configure defaults.ci_ssh_key or pass ci_ssh_keys" {
		t.Errorf("err = %q, want the missing-key message", err)
	}
	if fake.requests != 0 {
		t.Errorf("made %d Proxmox requests, want 0", fake.requests)
	}
}

func TestCloneVMRejectsMalformedKeyBeforeAnyRequest(t *testing.T) {
	c, fake := cloneFixture(t, config.DefaultsConfig{CISSHKey: "garbage"})

	_, err := callTool("proxmox_clone_vm", cloneArgs(nil), c)
	if err == nil {
		t.Fatal("cloning with a malformed key must fail")
	}
	if !strings.HasPrefix(err.Error(), "invalid SSH public key: ") {
		t.Errorf("err = %q, want an invalid-key message", err)
	}
	if fake.requests != 0 {
		t.Errorf("made %d Proxmox requests, want 0", fake.requests)
	}
}
