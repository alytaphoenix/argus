package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TrueNASClient communicates with the TrueNAS SCALE/CORE REST API v2.
type TrueNASClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewTrueNASClient returns a client for the given TrueNAS instance.
// insecure=true skips TLS certificate verification (useful for self-signed certs).
func NewTrueNASClient(url, apiKey string, insecure bool) *TrueNASClient {
	if url == "" || apiKey == "" {
		return nil
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
	return &TrueNASClient{
		baseURL: strings.TrimRight(url, "/") + "/api/v2.0",
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout:   20 * time.Second,
			Transport: tr,
		},
	}
}

func (c *TrueNASClient) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("truenas: %s %s → %d: %s", "GET", path, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// ── Data types ────────────────────────────────────────────────────────────────

// TrueNASPool represents one ZFS storage pool.
type TrueNASPool struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`   // "ONLINE", "DEGRADED", "OFFLINE", "FAULTED"
	Healthy   bool   `json:"healthy"`
	Size      int64  `json:"size"`
	Allocated int64  `json:"allocated"`
	Free      int64  `json:"free"`
	ScanState string // derived from scan.state: "SCANNING", "FINISHED", "CANCELED", ""
}

// TrueNASDataset is a top-level ZFS dataset (pool root).
type TrueNASDataset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UsedBytes int64  // parsed from used.parsed
	AvailBytes int64  // parsed from available.parsed
}

// TrueNASAlert is an active system alert.
type TrueNASAlert struct {
	UUID      string `json:"uuid"`
	Level     string `json:"level"`     // "INFO", "WARNING", "CRITICAL"
	Formatted string `json:"formatted"` // human-readable message
	Dismissed bool   `json:"dismissed"`
}

// TrueNASService tracks a running NAS service.
type TrueNASService struct {
	ID      int    `json:"id"`
	Service string `json:"service"` // "cifs", "nfs", "ftp", "ssh", etc.
	Enable  bool   `json:"enable"`
	State   string `json:"state"` // "RUNNING" | "STOPPED"
}

// TrueNASSystemInfo holds general system information.
type TrueNASSystemInfo struct {
	Hostname      string  `json:"hostname"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	SystemReady   bool    `json:"system_ready"`
}

// TrueNASStatus is all data fetched in one call.
type TrueNASStatus struct {
	SysInfo  TrueNASSystemInfo
	Pools    []TrueNASPool
	Datasets []TrueNASDataset
	Alerts   []TrueNASAlert
	Services []TrueNASService
}

// ── API methods ───────────────────────────────────────────────────────────────

// SystemInfo fetches basic system information.
func (c *TrueNASClient) SystemInfo(ctx context.Context) (*TrueNASSystemInfo, error) {
	var info TrueNASSystemInfo
	return &info, c.get(ctx, "/system/info", &info)
}

// Pools returns all ZFS storage pools.
func (c *TrueNASClient) Pools(ctx context.Context) ([]TrueNASPool, error) {
	// Raw pool response includes nested scan state
	var raw []struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		Healthy   bool   `json:"healthy"`
		Size      int64  `json:"size"`
		Allocated int64  `json:"allocated"`
		Free      int64  `json:"free"`
		Scan      struct {
			State string `json:"state"`
		} `json:"scan"`
	}
	if err := c.get(ctx, "/pool?limit=50", &raw); err != nil {
		return nil, err
	}
	pools := make([]TrueNASPool, len(raw))
	for i, r := range raw {
		pools[i] = TrueNASPool{
			ID:        r.ID,
			Name:      r.Name,
			Status:    r.Status,
			Healthy:   r.Healthy,
			Size:      r.Size,
			Allocated: r.Allocated,
			Free:      r.Free,
			ScanState: r.Scan.State,
		}
	}
	return pools, nil
}

// Datasets returns top-level ZFS datasets (one per pool root, limited set).
func (c *TrueNASClient) Datasets(ctx context.Context) ([]TrueNASDataset, error) {
	// Only fetch root-level datasets (no children) to keep response small
	var raw []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Used struct {
			Parsed int64 `json:"parsed"`
		} `json:"used"`
		Available struct {
			Parsed int64 `json:"parsed"`
		} `json:"available"`
	}
	if err := c.get(ctx, "/pool/dataset?limit=50&extra={%22flat%22:false}", &raw); err != nil {
		// Fall back to basic endpoint on older versions
		if err2 := c.get(ctx, "/pool/dataset?limit=50", &raw); err2 != nil {
			return nil, err
		}
	}
	out := make([]TrueNASDataset, 0, len(raw))
	for _, r := range raw {
		// Only show root-level datasets (no "/" in name after the pool)
		if strings.Count(r.Name, "/") <= 1 {
			out = append(out, TrueNASDataset{
				ID:         r.ID,
				Name:       r.Name,
				UsedBytes:  r.Used.Parsed,
				AvailBytes: r.Available.Parsed,
			})
		}
	}
	return out, nil
}

// Alerts returns all non-dismissed active alerts.
func (c *TrueNASClient) Alerts(ctx context.Context) ([]TrueNASAlert, error) {
	var alerts []TrueNASAlert
	if err := c.get(ctx, "/alert/list", &alerts); err != nil {
		return nil, err
	}
	var active []TrueNASAlert
	for _, a := range alerts {
		if !a.Dismissed {
			active = append(active, a)
		}
	}
	return active, nil
}

// Services returns all configured services and their run state.
func (c *TrueNASClient) Services(ctx context.Context) ([]TrueNASService, error) {
	var svcs []TrueNASService
	return svcs, c.get(ctx, "/service?limit=50", &svcs)
}

// FetchAll gathers everything in one call (errors on sub-calls are non-fatal).
func (c *TrueNASClient) FetchAll(ctx context.Context) (*TrueNASStatus, error) {
	var s TrueNASStatus
	var err error

	if info, e := c.SystemInfo(ctx); e != nil {
		return nil, fmt.Errorf("truenas system/info: %w", e)
	} else {
		s.SysInfo = *info
	}

	s.Pools, err = c.Pools(ctx)
	if err != nil {
		return nil, fmt.Errorf("truenas pools: %w", err)
	}

	// Non-fatal calls
	s.Datasets, _ = c.Datasets(ctx)
	s.Alerts, _ = c.Alerts(ctx)
	s.Services, _ = c.Services(ctx)

	return &s, nil
}

// TrueNASUptimeStr formats uptime_seconds as a human-readable string.
func TrueNASUptimeStr(secs float64) string {
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}
