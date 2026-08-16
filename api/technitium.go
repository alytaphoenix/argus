package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/config"
)

// TechnitiumClient talks to the Technitium DNS Server REST API.
// Docs: https://github.com/TechnitiumSoftware/DnsServer/blob/master/APIDOCS.md
type TechnitiumClient struct {
	base  string
	token string
	http  *http.Client
}

// NewTechnitiumClient creates a client. Returns nil if URL is empty.
func NewTechnitiumClient(cfg config.TechnitiumConfig) *TechnitiumClient {
	if cfg.URL == "" {
		return nil
	}
	return &TechnitiumClient{
		base:  strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ── Types ─────────────────────────────────────────────────────────────────────

// DNSZone is a zone returned by Technitium.
type DNSZone struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "Primary", "Secondary", "Stub", "Forwarder"
	Disabled bool   `json:"disabled"`
}

// DNSRecord is a record returned by Technitium.
type DNSRecord struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // A, AAAA, CNAME, MX, TXT, PTR, …
	TTL     int    `json:"ttl"`
	RData   DNSRData
	Disable bool `json:"disable"`
}

// DNSRData holds the record-type-specific data.
type DNSRData struct {
	// A / AAAA
	IPAddress string `json:"ipAddress"`
	// CNAME
	CName string `json:"cname"`
	// MX
	Exchange   string `json:"exchange"`
	Preference int    `json:"preference"`
	// TXT
	Text string `json:"text"`
}

// DNSAddOptions is used to create or update a record.
type DNSAddOptions struct {
	Zone       string
	Domain     string // FQDN or relative name
	Type       string // A, AAAA, CNAME, TXT, MX
	TTL        int    // 0 = use zone default (300)
	IPAddress  string // A / AAAA
	CName      string // CNAME target (FQDN)
	Text       string // TXT value
	Exchange   string // MX hostname
	Preference int    // MX priority
	Overwrite  bool   // replace existing record with same name+type
}

// ── API methods ───────────────────────────────────────────────────────────────

// Zones lists all zones on the server.
func (c *TechnitiumClient) Zones(ctx context.Context) ([]DNSZone, error) {
	raw, err := c.get(ctx, "/api/zones/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			Zones []DNSZone `json:"zones"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("zones: decode: %w", err)
	}
	return resp.Response.Zones, nil
}

// Records returns all records in a zone (or for a specific domain if domain != "").
func (c *TechnitiumClient) Records(ctx context.Context, zone, domain string) ([]DNSRecord, error) {
	params := url.Values{"zone": {zone}}
	if domain != "" {
		params.Set("domain", domain)
	}
	raw, err := c.get(ctx, "/api/zones/records/get", params)
	if err != nil {
		return nil, err
	}

	// Technitium returns a wrapped response whose rdata format varies by type.
	var outer struct {
		Response struct {
			Records []json.RawMessage `json:"records"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("records: decode: %w", err)
	}

	var out []DNSRecord
	for _, raw := range outer.Response.Records {
		var base struct {
			Name    string          `json:"name"`
			Type    string          `json:"type"`
			TTL     int             `json:"ttl"`
			Disable bool            `json:"disable"`
			RData   json.RawMessage `json:"rData"`
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			continue
		}
		rec := DNSRecord{Name: base.Name, Type: base.Type, TTL: base.TTL, Disable: base.Disable}
		json.Unmarshal(base.RData, &rec.RData) //nolint:errcheck — best-effort
		out = append(out, rec)
	}
	return out, nil
}

// AddRecord creates a new DNS record. Uses /api/zones/records/add (POST).
func (c *TechnitiumClient) AddRecord(ctx context.Context, opts DNSAddOptions) error {
	params := url.Values{
		"zone":   {opts.Zone},
		"domain": {opts.Domain},
		"type":   {opts.Type},
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = 300
	}
	params.Set("ttl", fmt.Sprintf("%d", ttl))
	if opts.Overwrite {
		params.Set("overwrite", "true")
	}

	switch strings.ToUpper(opts.Type) {
	case "A", "AAAA":
		params.Set("ipAddress", opts.IPAddress)
	case "CNAME":
		params.Set("cname", opts.CName)
	case "TXT":
		params.Set("text", opts.Text)
	case "MX":
		params.Set("exchange", opts.Exchange)
		params.Set("preference", fmt.Sprintf("%d", opts.Preference))
	}

	_, err := c.post(ctx, "/api/zones/records/add", params)
	return err
}

// UpdateRecord updates an existing record (delete + add).
// Technitium's update endpoint requires the old values; using overwrite=true on Add
// is simpler and achieves the same result.
func (c *TechnitiumClient) UpdateRecord(ctx context.Context, opts DNSAddOptions) error {
	opts.Overwrite = true
	return c.AddRecord(ctx, opts)
}

// DeleteRecord removes a DNS record.
func (c *TechnitiumClient) DeleteRecord(ctx context.Context, zone, domain, recType, value string) error {
	params := url.Values{
		"zone":   {zone},
		"domain": {domain},
		"type":   {recType},
	}
	switch strings.ToUpper(recType) {
	case "A", "AAAA":
		params.Set("ipAddress", value)
	case "CNAME":
		params.Set("cname", value)
	case "TXT":
		params.Set("text", value)
	case "MX":
		// value is the exchange hostname; Technitium also needs preference
		// but accepts the exchange alone for deletion matching.
		params.Set("exchange", value)
	}
	_, err := c.post(ctx, "/api/zones/records/delete", params)
	return err
}

// Ping checks whether the Technitium API is reachable.
func (c *TechnitiumClient) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/api/user/login", url.Values{
		"user":     {"check"},
		"pass":     {"check"},
		"includeInfo": {"false"},
	})
	// A 401/forbidden is still "reachable"; only network errors mean down.
	if err != nil && strings.Contains(err.Error(), "HTTP") {
		return nil
	}
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (c *TechnitiumClient) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("token", c.token)
	u := c.base + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *TechnitiumClient) post(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("token", c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+path, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

func (c *TechnitiumClient) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, truncateBody(body))
	}

	// Technitium wraps errors in {"status":"error","errorMessage":"…"}
	var status struct {
		Status string `json:"status"`
		Error  string `json:"errorMessage"`
	}
	if json.Unmarshal(body, &status) == nil && status.Status == "error" {
		return nil, fmt.Errorf("technitium: %s", status.Error)
	}
	return body, nil
}

func truncateBody(b []byte) string {
	s := string(b)
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
