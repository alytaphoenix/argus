package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

// CrowdSecClient talks to the CrowdSec Local API (LAPI).
// Docs: https://crowdsec.net/docs/local_api/intro
type CrowdSecClient struct {
	base   string
	apiKey string
	http   *http.Client
}

func NewCrowdSecClient(cfg config.CrowdSecConfig) *CrowdSecClient {
	if cfg.URL == "" {
		return nil
	}
	return &CrowdSecClient{
		base:   strings.TrimRight(cfg.URL, "/"),
		apiKey: cfg.APIKey,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ── Types ─────────────────────────────────────────────────────────────────────

// CrowdSecDecision is an active ban / captcha decision.
type CrowdSecDecision struct {
	ID       int    `json:"id"`
	Origin   string `json:"origin"`   // "crowdsec", "cscli", "console"
	Type     string `json:"type"`     // "ban", "captcha", "throttle"
	Scope    string `json:"scope"`    // "ip", "range", "country"
	Value    string `json:"value"`    // the IP / range / country
	Duration string `json:"duration"` // e.g. "24h0m0s"
	Scenario string `json:"scenario"` // e.g. "crowdsecurity/ssh-bf"
	Until    string `json:"until"`    // RFC3339
}

// CrowdSecAlert is one raised alert (may contain multiple decisions).
type CrowdSecAlert struct {
	ID       int    `json:"id"`
	Scenario string `json:"scenario"`
	Message  string `json:"message"`
	Source   struct {
		IP        string `json:"ip"`
		Range     string `json:"range"`
		ASName    string `json:"as_name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"source"`
	Decisions  []CrowdSecDecision `json:"decisions"`
	StartedAt  time.Time          `json:"start_at"`
	StoppedAt  time.Time          `json:"stop_at"`
	CreatedAt  time.Time          `json:"created_at"`
	EventsCount int               `json:"events_count"`
}

// CrowdSecMachine is a registered machine (agent) or bouncer.
type CrowdSecMachine struct {
	MachineID  string    `json:"machineId"`
	LastPush   time.Time `json:"last_push"`
	IsValidated bool     `json:"isValidated"`
}

// CrowdSecBouncer is a registered bouncer (enforcement component).
type CrowdSecBouncer struct {
	Name        string    `json:"name"`
	IPAddress   string    `json:"ip_address"`
	Type        string    `json:"type"`   // "firewall", "nginx", etc.
	LastPull    time.Time `json:"last_pull"`
	IsDisabled  bool      `json:"is_disabled"`
}

// CrowdSecSummary bundles counts for the TUI header.
type CrowdSecSummary struct {
	ActiveBans   int
	ActiveAlerts int
	Bouncers     int
	Machines     int
}

// ── API methods ───────────────────────────────────────────────────────────────

// Decisions returns all active decisions (bans/captchas).
func (c *CrowdSecClient) Decisions(ctx context.Context) ([]CrowdSecDecision, error) {
	body, err := c.get(ctx, "/v1/decisions")
	if err != nil {
		return nil, err
	}
	// null = no decisions
	if string(body) == "null" {
		return nil, nil
	}
	var out []CrowdSecDecision
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("crowdsec decisions: %w", err)
	}
	return out, nil
}

// Alerts returns recent alerts (default: last 100).
func (c *CrowdSecClient) Alerts(ctx context.Context) ([]CrowdSecAlert, error) {
	body, err := c.get(ctx, "/v1/alerts?limit=100")
	if err != nil {
		return nil, err
	}
	if string(body) == "null" {
		return nil, nil
	}
	var out []CrowdSecAlert
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("crowdsec alerts: %w", err)
	}
	return out, nil
}

// Bouncers returns registered bouncers.
func (c *CrowdSecClient) Bouncers(ctx context.Context) ([]CrowdSecBouncer, error) {
	body, err := c.get(ctx, "/v1/bouncers")
	if err != nil {
		return nil, err
	}
	if string(body) == "null" {
		return nil, nil
	}
	var out []CrowdSecBouncer
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("crowdsec bouncers: %w", err)
	}
	return out, nil
}

// DeleteDecision removes a decision by ID.
func (c *CrowdSecClient) DeleteDecision(ctx context.Context, id int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/v1/decisions/%d", c.base, id), nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("crowdsec delete decision: HTTP %s", resp.Status)
	}
	return nil
}

// Ping checks if the LAPI is reachable.
func (c *CrowdSecClient) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/v1/decisions")
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (c *CrowdSecClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("crowdsec: HTTP %s: %s", resp.Status, truncateBody(body))
	}
	return body, nil
}

func (c *CrowdSecClient) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
}
