package main

import (
	"testing"

	"github.com/alytaphoenix/argus/api"
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
