package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alytaphoenix/argus/config"
)

// StepCAClient talks to a Smallstep step-ca server.
// Docs: https://smallstep.com/docs/step-ca/api-reference/
type StepCAClient struct {
	base        string
	fingerprint string
	http        *http.Client

	// The root certificate is fetched once (insecure), verified against the
	// configured SHA-256 fingerprint, then used as the RootCAs for every
	// subsequent request. This turns the fingerprint into a real trust anchor
	// instead of merely disabling TLS verification.
	mu     sync.Mutex
	pinned bool
	root   *CertInfo
}

// NewStepCAClient creates a client. Returns nil if URL is empty.
func NewStepCAClient(cfg config.StepCAConfig) *StepCAClient {
	if cfg.URL == "" {
		return nil
	}
	transport := &http.Transport{}
	if cfg.Fingerprint != "" {
		// First fetch is unauthenticated so we can obtain the CA root; it is
		// immediately checked against the fingerprint and replaced with a
		// pinned RootCAs pool before any other request is made.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &StepCAClient{
		base:        strings.TrimRight(cfg.URL, "/"),
		fingerprint: utilNormalizeFingerprint(cfg.Fingerprint),
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}
}

func utilNormalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.TrimSpace(fp))
	fp = strings.ReplaceAll(fp, ":", "")
	return fp
}

// ensurePinnedRoot fetches the CA root once, verifies its SHA-256 fingerprint,
// and installs it as the trust anchor for subsequent TLS requests.
func (c *StepCAClient) ensurePinnedRoot(ctx context.Context) error {
	if c.fingerprint == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pinned {
		return nil
	}

	// Prefer the fingerprint-scoped endpoint; fall back to /root for step-ca
	// versions that always serve the root at the unqualified path.
	paths := []string{"/root/" + url.PathEscape(c.fingerprint), "/root"}
	var lastErr error
	for _, p := range paths {
		pemBytes, info, err := c.fetchRootAt(ctx, p)
		if err != nil {
			lastErr = err
			continue
		}
		if utilNormalizeFingerprint(info.Fingerprint) != c.fingerprint {
			return fmt.Errorf("stepca root fingerprint mismatch: got %s, want %s", info.Fingerprint, c.fingerprint)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return fmt.Errorf("stepca: invalid root certificate PEM")
		}
		tr, ok := c.http.Transport.(*http.Transport)
		if !ok {
			return fmt.Errorf("stepca: unsupported transport")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
		// Drop any idle connection created with the unpinned TLS config.
		tr.CloseIdleConnections()
		c.pinned = true
		c.root = info
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("stepca: root certificate unavailable")
	}
	return lastErr
}

// StepCAHealth is the parsed response from GET /health.
type StepCAHealth struct {
	Status string `json:"status"` // "ok"
}

// CertInfo holds summarised info about an x509 certificate.
type CertInfo struct {
	CommonName  string
	DNSNames    []string
	Issuer      string
	Serial      string
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256 hex
}

// DaysUntilExpiry returns calendar days until the cert expires (negative = expired).
func (c CertInfo) DaysUntilExpiry() int {
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// ExpiryStatus returns "ok", "expiring" (≤30d), or "expired".
func (c CertInfo) ExpiryStatus() string {
	d := c.DaysUntilExpiry()
	switch {
	case d < 0:
		return "expired"
	case d <= 30:
		return "expiring"
	default:
		return "ok"
	}
}

// Health checks if the step-ca server is reachable (GET /health).
func (c *StepCAClient) Health(ctx context.Context) (*StepCAHealth, error) {
	if err := c.ensurePinnedRoot(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("stepca health: HTTP %s: %s", resp.Status, truncateBody(body))
	}

	var h StepCAHealth
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("stepca health: decode: %w", err)
	}
	return &h, nil
}

// RootCert retrieves the CA root certificate from step-ca (/root).
// step-ca returns a PEM-encoded certificate.
func (c *StepCAClient) RootCert(ctx context.Context) (*CertInfo, error) {
	if c.fingerprint != "" {
		if err := c.ensurePinnedRoot(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		root := c.root
		c.mu.Unlock()
		return root, nil
	}
	_, info, err := c.fetchRootAt(ctx, "/root")
	return info, err
}

// fetchRootAt fetches and parses the CA root at path. Callers that pin a
// fingerprint must verify the returned CertInfo before trusting it.
func (c *StepCAClient) fetchRootAt(ctx context.Context, path string) ([]byte, *CertInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("stepca root: HTTP %s: %s", resp.Status, truncateBody(body))
	}

	// Response may be JSON {"ca": "<PEM>"} or raw PEM.
	var wrapper struct {
		CA string `json:"ca"`
	}
	if json.Unmarshal(body, &wrapper) == nil && wrapper.CA != "" {
		body = []byte(wrapper.CA)
	}
	info, err := parseCertPEM(body)
	if err != nil {
		return nil, nil, err
	}
	return body, info, nil
}

// Ping returns nil if the step-ca server responds to /health.
func (c *StepCAClient) Ping(ctx context.Context) error {
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	if h.Status != "ok" {
		return fmt.Errorf("stepca: status %q", h.Status)
	}
	return nil
}

// parseCertPEM parses the first PEM block as an x509 certificate and
// returns a CertInfo summary.
func parseCertPEM(pemBytes []byte) (*CertInfo, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("stepca: no PEM block in response")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("stepca: parse cert: %w", err)
	}
	return certToInfo(cert), nil
}

// certToInfo converts an x509.Certificate to a CertInfo.
func certToInfo(cert *x509.Certificate) *CertInfo {
	sum := sha256.Sum256(cert.Raw)
	fp := fmt.Sprintf("%x", sum[:])
	return &CertInfo{
		CommonName:  cert.Subject.CommonName,
		DNSNames:    cert.DNSNames,
		Issuer:      cert.Issuer.CommonName,
		Serial:      cert.SerialNumber.String(),
		NotBefore:   cert.NotBefore,
		NotAfter:    cert.NotAfter,
		Fingerprint: fp,
	}
}
