package wiring

import (
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
)

func TestSlugAndDNSLabel(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Grafana", "grafana"},
		{"Proxmox Web UI", "proxmox-web-ui"},
		{"signal-cli REST API", "signal-cli-rest-api"},
		{"  leading/trailing  ", "leading-trailing"},
		{"foo--bar", "foo-bar"},
		{"!@#$%", ""},
	}
	for _, tc := range cases {
		if got := Slug(tc.input); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	long := strings.Repeat("a", 80)
	if got := DNSLabel(long); len(got) > 63 {
		t.Errorf("DNSLabel length = %d, want <= 63", len(got))
	}
}

func TestPillarIncludesTraefikAndACME(t *testing.T) {
	opts := Options{
		Domain:    "home.arpa",
		DNSURL:    "http://dns.home.arpa:5380",
		DNSToken:  "secret-token",
		IngressIP: "192.168.1.10",
		ACMEURL:   "https://ca.home.arpa/acme/acme/directory",
	}
	services := []db.Service{
		{Name: "Grafana", Host: "192.168.1.20", Port: 3000, Scheme: "http", Enabled: true},
		{Name: "Postgres", Host: "192.168.1.21", Port: 5432, Scheme: "tcp", Enabled: true},
		{Name: "Old API", Host: "192.168.1.22", Port: 8080, Scheme: "http", Enabled: false},
	}
	got := Pillar(services, opts)

	for _, want := range []string{
		"dns_zone: \"home.arpa\"",
		"technitium_token_file:",
		"traefik_ingress: \"192.168.1.10\"",
		"stepca_cert_resolver: \"stepca\"",
		"certificatesResolvers:",
		"  grafana:",
		"    fqdn: \"grafana.home.arpa\"",
		"    dns_type: \"A\"",
		"    dns_value: \"192.168.1.10\"",
		"    traefik: true",
		"    cert: true",
		"  postgres:",
		"    dns_value: \"192.168.1.21\"",
		"    traefik: false",
		"    cert: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Pillar missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old-api") {
		t.Errorf("disabled service must not be wired:\n%s", got)
	}
	if strings.Contains(got, "secret-token") {
		t.Errorf("token must not appear in the pillar:\n%s", got)
	}
}

func TestPillarUsesCNAMEForHostnames(t *testing.T) {
	opts := Options{Domain: "home.arpa", IngressIP: "192.168.1.10"}
	got := Pillar([]db.Service{
		{Name: "NAS", Host: "nas.lan", Port: 2049, Scheme: "tcp", Enabled: true},
	}, opts)
	if !strings.Contains(got, "dns_type: \"CNAME\"") {
		t.Fatalf("expected CNAME for hostname target:\n%s", got)
	}
	if !strings.Contains(got, "dns_value: \"nas.lan\"") {
		t.Fatalf("expected CNAME target:\n%s", got)
	}
}

func TestStaticConfigACME(t *testing.T) {
	withACME := StaticConfig(Options{ACMEURL: "https://ca/acme/acme/directory"})
	if !strings.Contains(withACME, "certificatesResolvers:") ||
		!strings.Contains(withACME, "caServer: \"https://ca/acme/acme/directory\"") {
		t.Fatalf("static config missing ACME resolver:\n%s", withACME)
	}
	withoutACME := StaticConfig(Options{})
	if strings.Contains(withoutACME, "certificatesResolvers") {
		t.Fatalf("static config should omit ACME when unconfigured:\n%s", withoutACME)
	}
}

func TestFromConfigDefaultsIngressAndACME(t *testing.T) {
	cfg := &config.Config{
		Host: "https://192.168.1.10:8006",
		Globals: config.GlobalsConfig{
			Domain: "home.arpa",
		},
		Salt: config.SaltConfig{MasterMinion: "proxmox"},
		StepCA: config.StepCAConfig{
			URL:             "https://ca.home.arpa:8443/",
			ACMEProvisioner: "acme",
		},
	}
	got := FromConfig(cfg)
	if got.IngressIP != "192.168.1.10" {
		t.Errorf("IngressIP = %q, want 192.168.1.10", got.IngressIP)
	}
	if got.ACMEURL != "https://ca.home.arpa:8443/acme/acme/directory" {
		t.Errorf("ACMEURL = %q", got.ACMEURL)
	}
}
