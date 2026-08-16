package ui

import (
	"testing"

	"github.com/alytaphoenix/argus/api"
)

func TestServiceSlug(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Grafana", "grafana"},
		{"Proxmox Web UI", "proxmox-web-ui"},
		{"signal-cli REST API", "signal-cli-rest-api"},
		{"MinIO S3 API", "minio-s3-api"},
		{"Hermes API Server", "hermes-api-server"},
		{"  leading/trailing  ", "leading-trailing"},
		{"ALL CAPS", "all-caps"},
		{"foo--bar", "foo-bar"},
		{"123service", "123service"},
		{"!@#$%", ""},
		{"a", "a"},
	}
	for _, tc := range cases {
		got := serviceSlug(tc.input)
		if got != tc.want {
			t.Errorf("serviceSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── lazyLoadTab guard tests ───────────────────────────────────────────────────

func TestLazyLoadTab_DNS_FirstVisit(t *testing.T) {
	m := ServicesModel{}
	cmd := m.lazyLoadTab(svcTabDNS)
	if cmd == nil {
		t.Error("first visit to DNS tab should return a load command")
	}
	if !m.dnsLoading {
		t.Error("dnsLoading should be true after first DNS tab visit")
	}
}

func TestLazyLoadTab_DNS_AlreadyLoading(t *testing.T) {
	m := ServicesModel{dnsLoading: true}
	cmd := m.lazyLoadTab(svcTabDNS)
	if cmd != nil {
		t.Error("should not fire a second load when dnsLoading is already true")
	}
}

func TestLazyLoadTab_DNS_AlreadyLoaded(t *testing.T) {
	m := ServicesModel{zones: []api.DNSZone{{Name: "homelab.local"}}}
	cmd := m.lazyLoadTab(svcTabDNS)
	if cmd != nil {
		t.Error("should not fire a load when zones are already populated")
	}
}

func TestLazyLoadTab_TrueNAS_FirstVisit(t *testing.T) {
	m := ServicesModel{}
	cmd := m.lazyLoadTab(svcTabTrueNAS)
	if cmd == nil {
		t.Error("first visit to TrueNAS tab should return a load command")
	}
	if !m.trueNASLoading {
		t.Error("trueNASLoading should be true after first TrueNAS tab visit")
	}
}

func TestLazyLoadTab_TrueNAS_AlreadyLoading(t *testing.T) {
	m := ServicesModel{trueNASLoading: true}
	cmd := m.lazyLoadTab(svcTabTrueNAS)
	if cmd != nil {
		t.Error("should not fire a second load when trueNASLoading is already true")
	}
}

func TestLazyLoadTab_TrueNAS_AlreadyChecked(t *testing.T) {
	m := ServicesModel{trueNASChecked: true}
	cmd := m.lazyLoadTab(svcTabTrueNAS)
	if cmd != nil {
		t.Error("should not fire a load when trueNASChecked is true")
	}
}

func TestLazyLoadTab_HA_FirstVisit(t *testing.T) {
	m := ServicesModel{}
	cmd := m.lazyLoadTab(svcTabHomeAssistant)
	if cmd == nil {
		t.Error("first visit to HA tab should return a load command")
	}
	if !m.haLoading {
		t.Error("haLoading should be true after first HA tab visit")
	}
}

func TestLazyLoadTab_HA_AlreadyLoading(t *testing.T) {
	m := ServicesModel{haLoading: true}
	cmd := m.lazyLoadTab(svcTabHomeAssistant)
	if cmd != nil {
		t.Error("should not fire a second load when haLoading is already true")
	}
}

func TestLazyLoadTab_HA_AlreadyChecked(t *testing.T) {
	m := ServicesModel{haChecked: true}
	cmd := m.lazyLoadTab(svcTabHomeAssistant)
	if cmd != nil {
		t.Error("should not fire a load when haChecked is true")
	}
}

func TestLazyLoadTab_Services_NoLoad(t *testing.T) {
	m := ServicesModel{}
	cmd := m.lazyLoadTab(svcTabServices)
	if cmd != nil {
		t.Error("Services tab needs no lazy load")
	}
}

func TestLazyLoadTab_Certs_NoLoad(t *testing.T) {
	m := ServicesModel{}
	cmd := m.lazyLoadTab(svcTabCerts)
	if cmd != nil {
		t.Error("Certs tab needs no lazy load")
	}
}
