package ui

import (
	"testing"

	"github.com/alytaphoenix/argus/api"
)

func TestMatchBootstrapService(t *testing.T) {
	cases := []struct {
		guestName string
		want      string
	}{
		{"dns-server", "dns"},
		{"technitium01", "dns"},
		{"step-ca-01", "certs"},
		{"certs-vm", "certs"},
		{"truenas", "truenas"},
		{"plex-media", "plex"},
		{"sonarr", "servarr"},
		{"hermes-agent", "hermes"},
		{"signal-gateway", "signal"},
		// Should NOT false-positive on short/generic substrings.
		{"cache-01", ""},
		{"mediacat", ""},
		{"nasty-vm", ""},
		{"web-frontend", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := matchBootstrapService(tc.guestName)
		if got != tc.want {
			t.Errorf("matchBootstrapService(%q) = %q, want %q", tc.guestName, got, tc.want)
		}
	}
}

// TestMatchBootstrapServiceDeterministicOnDualMatch locks the priority order
// when a hostname's keywords span two services — matchBootstrapService must
// check bootstrapServiceKeywordOrder in slice order (not a map), or this
// result would vary randomly from call to call.
func TestMatchBootstrapServiceDeterministicOnDualMatch(t *testing.T) {
	const guestName = "hermes-signal-01" // contains both "hermes" and "signal"
	want := matchBootstrapService(guestName)
	if want != "hermes" {
		t.Fatalf("matchBootstrapService(%q) = %q, want %q (hermes precedes signal in the catalog order)", guestName, want, "hermes")
	}
	for i := 0; i < 20; i++ {
		if got := matchBootstrapService(guestName); got != want {
			t.Fatalf("matchBootstrapService(%q) returned %q on call %d, want stable %q", guestName, got, i, want)
		}
	}
}

func TestMatchAcceptedMinion(t *testing.T) {
	m := OrchestrateModel{keys: &api.SaltKeys{Accepted: []string{"dns-server"}}}
	if got := m.matchAcceptedMinion("DNS-Server"); got != "dns-server" {
		t.Errorf("matchAcceptedMinion(case-mismatch) = %q, want %q", got, "dns-server")
	}
	if got := m.matchAcceptedMinion("dns-server"); got != "dns-server" {
		t.Errorf("matchAcceptedMinion(exact) = %q, want %q", got, "dns-server")
	}
	if got := m.matchAcceptedMinion("nonexistent"); got != "" {
		t.Errorf("matchAcceptedMinion(no match) = %q, want empty", got)
	}
}

func TestDiscoveredGuestForServicePrefersMinion(t *testing.T) {
	m := OrchestrateModel{
		keys: &api.SaltKeys{Accepted: []string{"dns-new"}},
		clusterGuests: []discoveredGuest{
			{Guest: api.Guest{Name: "dns-old", VMID: 100, Node: "pve1"}},
			{Guest: api.Guest{Name: "dns-new", VMID: 200, Node: "pve2"}},
		},
	}
	dg, minionID, found := m.discoveredGuestForService("dns")
	if !found {
		t.Fatal("expected a match")
	}
	if minionID != "dns-new" {
		t.Errorf("minionID = %q, want %q (should prefer the accepted-minion guest over the earlier non-minion match)", minionID, "dns-new")
	}
	if dg.Guest.Name != "dns-new" {
		t.Errorf("matched guest = %q, want %q", dg.Guest.Name, "dns-new")
	}
}

func TestDiscoveredGuestForServiceFallsBackToFirstMatch(t *testing.T) {
	m := OrchestrateModel{
		clusterGuests: []discoveredGuest{
			{Guest: api.Guest{Name: "dns-old", VMID: 100, Node: "pve1"}},
		},
	}
	dg, minionID, found := m.discoveredGuestForService("dns")
	if !found || minionID != "" || dg.Guest.Name != "dns-old" {
		t.Errorf("got (dg=%+v, minionID=%q, found=%v), want (dns-old, \"\", true)", dg, minionID, found)
	}
}

func TestDiscoveredGuestForServiceSkipsArgusManaged(t *testing.T) {
	m := OrchestrateModel{
		clusterGuests: []discoveredGuest{
			{Guest: api.Guest{Name: "dns-managed", VMID: 1}, ArgusManaged: true},
		},
	}
	if _, _, found := m.discoveredGuestForService("dns"); found {
		t.Error("should not surface argus-managed guests as discovery candidates")
	}
}

func TestDiscoveredGuestForServiceNoMatch(t *testing.T) {
	m := OrchestrateModel{
		clusterGuests: []discoveredGuest{
			{Guest: api.Guest{Name: "web-frontend", VMID: 1}},
		},
	}
	if _, _, found := m.discoveredGuestForService("dns"); found {
		t.Error("should not match an unrelated hostname")
	}
}

func TestCanonicalizeHostname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"step-ca-01", "stepca01"},
		{"DNS_Server", "dnsserver"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := canonicalizeHostname(tc.in); got != tc.want {
			t.Errorf("canonicalizeHostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJoinAdvisory(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"a", "", "a"},
		{"", "b", "b"},
		{"a", "b", "a; b"},
	}
	for _, tc := range cases {
		if got := joinAdvisory(tc.a, tc.b); got != tc.want {
			t.Errorf("joinAdvisory(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}
