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
	"time"

	"github.com/alytaphoenix/argus/config"
)

// StepCAClient talks to a Smallstep step-ca server.
// Docs: https://smallstep.com/docs/step-ca/api-reference/
type StepCAClient struct {
	base        string
	fingerprint string
	http        *http.Client
}

// NewStepCAClient creates a client. Returns nil if URL is empty.
func NewStepCAClient(cfg config.StepCAConfig) *StepCAClient {
	if cfg.URL == "" {
		return nil
	}
	// step-ca typically uses self-signed TLS; skip verify if fingerprint is set
	// (the fingerprint itself is the trust anchor)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Fingerprint != ""},
	}
	return &StepCAClient{
		base:        cfg.URL,
		fingerprint: cfg.Fingerprint,
		http: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}
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
	// step-ca v1 API: GET /root/{fingerprint} or just GET /root
	url := c.base + "/root"
	if c.fingerprint != "" {
		url = c.base + "/root/" + c.fingerprint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("stepca root: HTTP %s: %s", resp.Status, truncateBody(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Response may be JSON {"ca": "<PEM>"} or raw PEM
	var wrapper struct {
		CA string `json:"ca"`
	}
	if json.Unmarshal(body, &wrapper) == nil && wrapper.CA != "" {
		return parseCertPEM([]byte(wrapper.CA))
	}
	return parseCertPEM(body)
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
		CommonName: cert.Subject.CommonName,
		DNSNames:   cert.DNSNames,
		Issuer:     cert.Issuer.CommonName,
		Serial:     cert.SerialNumber.String(),
		NotBefore:  cert.NotBefore,
		NotAfter:   cert.NotAfter,
		Fingerprint: fp,
	}
}
