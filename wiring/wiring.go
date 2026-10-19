// Package wiring turns tracked services into a Salt pillar + state that
// publishes DNS records, Traefik routes, and automatic TLS certificates.
package wiring

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
)

const (
	// DefaultTraefikDir is where argus writes dynamic Traefik configuration.
	DefaultTraefikDir = "/etc/traefik/dynamic"
	// DefaultACMEDir holds Traefik's ACME account and certificate state.
	DefaultACMEDir = "/etc/traefik/acme"
	// DefaultStaticPath is Traefik's static configuration file.
	DefaultStaticPath = "/etc/traefik/traefik.yml"
	// DefaultTokenPath is the root-only Technitium token file on the master.
	DefaultTokenPath = "/etc/argus/technitium.token"
	// CertResolverName is the Traefik certificate resolver argus configures.
	CertResolverName = "stepca"
)

// Options describes where and how service wiring is applied.
type Options struct {
	MasterMinion string
	PillarBase   string
	StateBase    string

	Domain    string
	DNSURL    string
	DNSToken  string
	IngressIP string

	// ACMEURL is the step-ca ACME directory. When set, Traefik obtains and
	// renews certificates for every wired service automatically.
	ACMEURL string

	TraefikDir string
	ACMEDir    string
	StaticPath string
	TokenPath  string
}

// FromConfig builds wiring options from the loaded argus configuration.
// IngressIP defaults to the Proxmox host, which is where Traefik is normally
// installed by the built-in formula.
func FromConfig(cfg *config.Config) Options {
	if cfg == nil {
		return Options{}
	}
	ingress := strings.TrimSpace(cfg.Traefik.IngressIP)
	if ingress == "" {
		ingress = hostFromURL(cfg.Host)
	}
	return Options{
		MasterMinion: cfg.Salt.MasterMinion,
		PillarBase:   cfg.Salt.PillarBase,
		StateBase:    cfg.Salt.StateBase,
		Domain:       cfg.Globals.Domain,
		DNSURL:       strings.TrimSpace(cfg.Technitium.URL),
		DNSToken:     strings.TrimSpace(cfg.Technitium.Token),
		IngressIP:    ingress,
		ACMEURL:      cfg.StepCA.ACMEEndpoint(),
	}
}

// withDefaults fills unset paths.
func (o Options) withDefaults() Options {
	if o.PillarBase == "" {
		o.PillarBase = "/srv/pillar"
	}
	if o.StateBase == "" {
		o.StateBase = "/srv/salt"
	}
	if o.TraefikDir == "" {
		o.TraefikDir = DefaultTraefikDir
	}
	if o.ACMEDir == "" {
		o.ACMEDir = DefaultACMEDir
	}
	if o.StaticPath == "" {
		o.StaticPath = DefaultStaticPath
	}
	if o.TokenPath == "" {
		o.TokenPath = DefaultTokenPath
	}
	return o
}

// Enabled reports whether a Salt master is configured for wiring.
func (o Options) Enabled() bool { return strings.TrimSpace(o.MasterMinion) != "" }

// AutoEnabled reports whether automatic wiring has enough information to be
// useful: a master, a DNS zone, and an ingress address. Manual `w` still works
// with a partial configuration.
func (o Options) AutoEnabled() bool {
	return o.Enabled() && o.Domain != "" && o.IngressIP != ""
}

// DNSEnabled reports whether DNS records can be published.
func (o Options) DNSEnabled() bool {
	return o.DNSURL != "" && o.DNSToken != "" && o.Domain != ""
}

// CertResolver returns the Traefik resolver name when ACME is configured.
func (o Options) CertResolver() string {
	if o.ACMEURL == "" || o.Domain == "" || o.IngressIP == "" {
		return ""
	}
	return CertResolverName
}

type serviceEntry struct {
	slug     string
	host     string
	port     int
	scheme   string
	fqdn     string
	zone     string
	dnsType  string
	dnsValue string
	traefik  bool
	cert     bool
}

// serviceEntries selects and normalises the services that should be wired.
func serviceEntries(services []db.Service, o Options) []serviceEntry {
	resolver := o.CertResolver()
	var out []serviceEntry
	for _, svc := range sortedServices(services) {
		if !svc.Enabled || strings.TrimSpace(svc.Name) == "" || strings.TrimSpace(svc.Host) == "" {
			continue
		}
		slug := Slug(svc.Name)
		if slug == "" {
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(svc.Scheme))
		if scheme == "" {
			scheme = "http"
		}
		port := svc.Port
		if port == 0 {
			if scheme == "https" {
				port = 443
			} else {
				port = 80
			}
		}
		fqdn := slug
		if o.Domain != "" {
			fqdn = slug + "." + o.Domain
		}
		useTraefik := scheme == "http" || scheme == "https"

		// Clients reach a Traefik-fronted service through the ingress. For
		// direct services the DNS record points at the service itself.
		dnsTarget := svc.Host
		if useTraefik && o.IngressIP != "" {
			dnsTarget = o.IngressIP
		}
		dnsType := "A"
		if net.ParseIP(dnsTarget) == nil {
			dnsType = "CNAME"
		}
		out = append(out, serviceEntry{
			slug:     slug,
			host:     svc.Host,
			port:     port,
			scheme:   scheme,
			fqdn:     fqdn,
			zone:     o.Domain,
			dnsType:  dnsType,
			dnsValue: dnsTarget,
			traefik:  useTraefik,
			cert:     useTraefik && resolver != "" && o.IngressIP != "" && o.Domain != "",
		})
	}
	return out
}

// PillarData is the inline pillar passed to state.apply. Using an inline
// override avoids requiring argus_services in /srv/pillar/top.sls.
func PillarData(services []db.Service, o Options) map[string]interface{} {
	o = o.withDefaults()
	tokenFile := ""
	if o.DNSToken != "" {
		tokenFile = o.TokenPath
	}
	entries := serviceEntries(services, o)
	svcMap := make(map[string]interface{}, len(entries))
	for _, e := range entries {
		svcMap[e.slug] = map[string]interface{}{
			"host":      e.host,
			"port":      e.port,
			"scheme":    e.scheme,
			"fqdn":      e.fqdn,
			"dns_zone":  e.zone,
			"dns_type":  e.dnsType,
			"dns_value": e.dnsValue,
			"traefik":   e.traefik,
			"cert":      e.cert,
		}
	}
	return map[string]interface{}{
		"technitium_url":        o.DNSURL,
		"technitium_token_file": tokenFile,
		"traefik_ingress":       o.IngressIP,
		"traefik_dir":           o.TraefikDir,
		"traefik_acme_dir":      o.ACMEDir,
		"traefik_static_path":   o.StaticPath,
		"stepca_cert_resolver":  o.CertResolver(),
		"traefik_static_config": StaticConfig(o),
		"services":              svcMap,
	}
}

// Pillar renders /srv/pillar/argus_services.sls for operators who prefer the
// conventional top.sls include. Apply uses PillarData inline.
func Pillar(services []db.Service, o Options) string {
	o = o.withDefaults()
	var b strings.Builder
	b.WriteString("# argus-managed — do not edit; regenerated automatically\n")
	data := PillarData(services, o)
	fmt.Fprintf(&b, "technitium_url: %q\n", data["technitium_url"])
	fmt.Fprintf(&b, "technitium_token_file: %q\n", data["technitium_token_file"])
	fmt.Fprintf(&b, "traefik_ingress: %q\n", data["traefik_ingress"])
	fmt.Fprintf(&b, "traefik_dir: %q\n", data["traefik_dir"])
	fmt.Fprintf(&b, "traefik_acme_dir: %q\n", data["traefik_acme_dir"])
	fmt.Fprintf(&b, "traefik_static_path: %q\n", data["traefik_static_path"])
	fmt.Fprintf(&b, "stepca_cert_resolver: %q\n", data["stepca_cert_resolver"])
	b.WriteString("traefik_static_config: |\n")
	for _, line := range strings.Split(strings.TrimRight(StaticConfig(o), "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	b.WriteString("services:\n")
	for _, e := range serviceEntries(services, o) {
		fmt.Fprintf(&b, "  %s:\n", e.slug)
		fmt.Fprintf(&b, "    host: %q\n", e.host)
		fmt.Fprintf(&b, "    port: %d\n", e.port)
		fmt.Fprintf(&b, "    scheme: %q\n", e.scheme)
		fmt.Fprintf(&b, "    fqdn: %q\n", e.fqdn)
		fmt.Fprintf(&b, "    dns_zone: %q\n", e.zone)
		fmt.Fprintf(&b, "    dns_type: %q\n", e.dnsType)
		fmt.Fprintf(&b, "    dns_value: %q\n", e.dnsValue)
		fmt.Fprintf(&b, "    traefik: %v\n", e.traefik)
		fmt.Fprintf(&b, "    cert: %v\n", e.cert)
	}
	return b.String()
}

// StaticConfig renders Traefik's static configuration. The ACME resolver is
// only present when a step-ca ACME directory is configured.
func StaticConfig(o Options) string {
	o = o.withDefaults()
	var b strings.Builder
	b.WriteString("entryPoints:\n")
	b.WriteString("  web:\n    address: \":80\"\n")
	b.WriteString("  websecure:\n    address: \":443\"\n")
	b.WriteString("providers:\n")
	b.WriteString("  file:\n")
	fmt.Fprintf(&b, "    directory: %q\n", o.TraefikDir)
	b.WriteString("    watch: true\n")
	if o.ACMEURL != "" {
		b.WriteString("certificatesResolvers:\n")
		fmt.Fprintf(&b, "  %s:\n", CertResolverName)
		b.WriteString("    acme:\n")
		fmt.Fprintf(&b, "      caServer: %q\n", o.ACMEURL)
		fmt.Fprintf(&b, "      storage: %q\n", path.Join(o.ACMEDir, "acme.json"))
		b.WriteString("      httpChallenge:\n")
		b.WriteString("        entryPoint: web\n")
	}
	return b.String()
}

// Apply writes the pillar, state, Traefik static config, and Technitium token
// to the Salt master, then applies services.wiring. It returns the Salt JID.
func Apply(ctx context.Context, salt *api.SaltClient, services []db.Service, o Options) (string, error) {
	if salt == nil {
		return "", fmt.Errorf("salt client is not configured")
	}
	o = o.withDefaults()
	if !o.Enabled() {
		return "", fmt.Errorf("set [salt] master_minion to enable service wiring")
	}

	pillarPath := path.Join(o.PillarBase, "argus_services.sls")
	statePath := path.Join(o.StateBase, "services", "wiring.sls")

	// The pillar holds the static Traefik config and DNS/table metadata. It is
	// root-readable only because it can reference the Technitium token path.
	if err := salt.WriteFileMode(ctx, o.MasterMinion, pillarPath, Pillar(services, o), 0o600); err != nil {
		return "", fmt.Errorf("write pillar: %w", err)
	}
	if err := salt.WriteFileMode(ctx, o.MasterMinion, statePath, api.WiringStateTemplate, 0o644); err != nil {
		return "", fmt.Errorf("write state: %w", err)
	}
	if o.DNSToken != "" {
		if err := salt.WriteFileMode(ctx, o.MasterMinion, o.TokenPath, o.DNSToken, 0o600); err != nil {
			return "", fmt.Errorf("write dns token: %w", err)
		}
	}
	jid, err := salt.ApplyStateWithPillar(ctx, o.MasterMinion, "services.wiring", PillarData(services, o))
	if err != nil {
		return "", fmt.Errorf("apply services.wiring: %w", err)
	}
	return jid, nil
}

// Slug converts a human service name into a DNS label.
func Slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// DNSLabel converts a VM name into a single DNS label (max 63 chars).
func DNSLabel(name string) string {
	s := Slug(name)
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

func sortedServices(services []db.Service) []db.Service {
	out := make([]db.Service, len(services))
	copy(out, services)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	s := raw
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
