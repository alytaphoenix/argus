package api

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)


// WazuhClient talks to the Wazuh REST API.
// Docs: https://documentation.wazuh.com/current/user-manual/api/reference.html
type WazuhClient struct {
	base     string
	username string
	password string
	mu       sync.Mutex
	token    string
	tokenExp time.Time
	http     *http.Client
}

func NewWazuhClient(cfg config.WazuhConfig) *WazuhClient {
	if cfg.URL == "" {
		return nil
	}
	// Wazuh ships with a self-signed cert, so skip verification unless the
	// operator explicitly sets insecure = false.
	insecure := true
	if cfg.Insecure != nil {
		insecure = *cfg.Insecure
	}
	return &WazuhClient{
		base:     strings.TrimRight(cfg.URL, "/"),
		username: cfg.Username,
		password: cfg.Password,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
			},
		},
	}
}

// ── Types ─────────────────────────────────────────────────────────────────────

// WazuhAgent is a Wazuh monitoring agent installed on a node.
type WazuhAgent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	IP           string    `json:"ip"`
	Status       string    `json:"status"` // "active", "disconnected", "never_connected"
	Version      string    `json:"version"`
	OS           WazuhOS   `json:"os"`
	LastKeepAlive string   `json:"lastKeepAlive"`
	RegisteredAt string    `json:"dateAdd"`
}

// WazuhOS holds OS info for an agent.
type WazuhOS struct {
	Platform string `json:"platform"` // "linux", "windows"
	Name     string `json:"name"`     // "Ubuntu"
	Version  string `json:"version"`
}

// WazuhAlert is a security alert.
type WazuhAlert struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	AgentName   string    `json:"agent_name"`
	Rule        WazuhRule `json:"rule"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
}

// WazuhRule is the matched rule that fired the alert.
type WazuhRule struct {
	ID          int      `json:"id"`
	Level       int      `json:"level"` // 0-15, severity
	Description string   `json:"description"`
	Groups      []string `json:"groups"`
}

// WazuhSummary is high-level stats for the TUI header.
type WazuhSummary struct {
	TotalAgents       int
	ActiveAgents      int
	DisconnectedAgents int
	AlertsLast24h     int
	CriticalAlerts    int // level >= 10
}

// ── API methods ───────────────────────────────────────────────────────────────

// Agents returns all registered agents.
func (c *WazuhClient) Agents(ctx context.Context) ([]WazuhAgent, error) {
	body, err := c.get(ctx, "/agents?limit=500&offset=0")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			AffectedItems []WazuhAgent `json:"affected_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("wazuh agents: %w", err)
	}
	return resp.Data.AffectedItems, nil
}

// AgentStats returns alert counts per agent for the last 24h.
// This uses a simplified approach: query alerts filtered by date.
func (c *WazuhClient) RecentAlerts(ctx context.Context) ([]WazuhAlert, error) {
	// Query last 24h of alerts, levels 3+.
	body, err := c.get(ctx, "/security/alerts?limit=200&level=3&sort=-timestamp")
	if err != nil {
		// Wazuh versions differ; try alternate endpoint
		body, err = c.get(ctx, "/alerts?limit=200")
		if err != nil {
			return nil, err
		}
	}
	var resp struct {
		Data struct {
			AffectedItems []WazuhAlert `json:"affected_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Data.AffectedItems, nil
}

// Ping checks if Wazuh is reachable.
func (c *WazuhClient) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/")
	return err
}

// SummariseAgents computes WazuhSummary from an agent list.
func SummariseAgents(agents []WazuhAgent, alerts []WazuhAlert) WazuhSummary {
	s := WazuhSummary{TotalAgents: len(agents)}
	for _, a := range agents {
		if a.Status == "active" {
			s.ActiveAgents++
		} else {
			s.DisconnectedAgents++
		}
	}
	since := time.Now().Add(-24 * time.Hour)
	for _, al := range alerts {
		if al.Timestamp.After(since) {
			s.AlertsLast24h++
			if al.Rule.Level >= 10 {
				s.CriticalAlerts++
			}
		}
	}
	return s
}

// ── Auth ──────────────────────────────────────────────────────────────────────

// ensureToken obtains or refreshes the JWT token.
func (c *WazuhClient) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	// POST /security/user/authenticate with basic auth
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/security/user/authenticate", nil)
	if err != nil {
		return "", err
	}
	creds := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
	req.Header.Set("Authorization", "Basic "+creds)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("wazuh auth: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("wazuh auth: HTTP %s", resp.Status)
	}
	var authResp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", fmt.Errorf("wazuh auth: decode: %w", err)
	}
	c.token = authResp.Data.Token
	c.tokenExp = time.Now().Add(14 * time.Minute) // tokens last 15min
	return c.token, nil
}

func (c *WazuhClient) get(ctx context.Context, path string) ([]byte, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("wazuh: HTTP %s: %s", resp.Status, truncateBody(body))
	}
	return body, nil
}
