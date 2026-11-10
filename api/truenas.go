package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// TrueNASClient communicates with TrueNAS.
//
// TrueNAS ≤25.10 exposes the REST API v2.0 at /api/v2.0. TrueNAS 26.0 removed
// that REST surface and serves only the new JSON-RPC 2.0 WebSocket API at
// /api/current. The client probes for REST first and falls back to WebSocket,
// so one implementation serves both generations.
type TrueNASClient struct {
	rawURL     string
	restBase   string
	host       string
	apiKey     string
	insecure   bool
	httpClient *http.Client
	ws         *websocket.Conn
	mu         sync.Mutex
	dialMu     sync.Mutex
	nextID     int
	useWS      bool
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// NewTrueNASClient returns a client for the given TrueNAS instance.
// insecure=true skips TLS certificate verification (useful for self-signed certs).
func NewTrueNASClient(url, apiKey string, insecure bool) *TrueNASClient {
	if url == "" || apiKey == "" {
		return nil
	}
	raw := strings.TrimRight(url, "/")
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
	c := &TrueNASClient{
		rawURL:     raw,
		restBase:   raw + "/api/v2.0",
		host:       stripSchemeHost(raw),
		apiKey:     apiKey,
		insecure:   insecure,
		nextID:     1,
		httpClient: &http.Client{Timeout: 20 * time.Second, Transport: tr},
	}
	if c.probeREST() {
		return c
	}
	// No REST v2.0 → TrueNAS 26.x new WebSocket API.
	c.useWS = true
	if err := c.dialWS(insecure); err != nil {
		// Leave the client usable: later calls retry the dial.
		c.ws = nil
	}
	return c
}

func stripSchemeHost(u string) string {
	base := u
	if idx := strings.Index(base, "://"); idx >= 0 {
		base = base[idx+3:]
	}
	if idx := strings.Index(base, "/"); idx >= 0 {
		base = base[:idx]
	}
	return base
}

// Host returns the hostname (or IP) of the TrueNAS server, suitable for use
// in NFS mount device strings.
func (c *TrueNASClient) Host() string { return c.host }

func (c *TrueNASClient) probeREST() bool {
	req, err := http.NewRequest("GET", c.restBase+"/system/info", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return resp.StatusCode < 500 && resp.StatusCode != http.StatusNotFound
}

func (c *TrueNASClient) dialWS(insecure bool) error {
	u := strings.Replace(c.rawURL, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	u = strings.TrimRight(u, "/") + "/api/current"
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
	conn, _, err := dialer.Dial(u, nil)
	if err != nil {
		return err
	}
	c.ws = conn
	var ok bool
	if err := c.rpc("auth.login_with_api_key", []any{c.apiKey}, &ok); err != nil {
		conn.Close()
		c.ws = nil
		return err
	}
	if !ok {
		conn.Close()
		c.ws = nil
		return fmt.Errorf("truenas: api key login rejected")
	}
	return nil
}

func (c *TrueNASClient) ensureWS() error {
	c.dialMu.Lock()
	defer c.dialMu.Unlock()
	c.mu.Lock()
	connected := c.ws != nil
	c.mu.Unlock()
	if connected {
		return nil
	}
	return c.dialWS(c.insecure)
}

func (c *TrueNASClient) rpc(method string, params []any, out any) error {
	if c.ws == nil {
		return fmt.Errorf("truenas: websocket not connected")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.ws.WriteJSON(req); err != nil {
		return err
	}
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(20 * time.Second))
		var resp rpcResponse
		if err := c.ws.ReadJSON(&resp); err != nil {
			return err
		}
		if resp.ID != id {
			continue // notification or stale response
		}
		if resp.Error != nil {
			return fmt.Errorf("truenas %s: %s", method, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

func (c *TrueNASClient) get(ctx context.Context, path string, out interface{}) error {
	if c.useWS {
		if err := c.ensureWS(); err != nil {
			return err
		}
		switch {
		case path == "/system/info":
			return c.rpc("system.info", []any{}, out)
		case strings.HasPrefix(path, "/pool/dataset"):
			return c.rpc("pool.dataset.query", []any{
				[]any{}, map[string]any{"limit": 50, "extra": map[string]any{"flat": true}},
			}, out)
		case strings.HasPrefix(path, "/pool"):
			return c.rpc("pool.query", []any{[]any{}, map[string]any{"limit": 50}}, out)
		case strings.HasPrefix(path, "/alert"):
			return c.rpc("alert.list", []any{}, out)
		case strings.HasPrefix(path, "/service"):
			return c.rpc("service.query", []any{[]any{}, map[string]any{"limit": 50}}, out)
		case strings.HasPrefix(path, "/sharing/nfs"):
			return c.rpc("sharing.nfs.query", []any{}, out)
		default:
			return fmt.Errorf("truenas: no ws mapping for %s", path)
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", c.restBase+path, nil)
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

// write performs a mutating request (POST/PUT/DELETE). It maps REST paths to
// the equivalent JSON-RPC method on the TrueNAS 26+ WebSocket API so the same
// call sites work against both API generations.
func (c *TrueNASClient) write(ctx context.Context, method, path string, body any, out any) error {
	if c.useWS {
		if err := c.ensureWS(); err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(path, "/pool/dataset"):
			switch method {
			case "POST":
				return c.rpc("pool.dataset.create", []any{body}, out)
			case "DELETE":
				id, _ := url.PathUnescape(strings.TrimPrefix(path, "/pool/dataset/"))
				return c.rpc("pool.dataset.delete", []any{id}, out)
			}
		case strings.HasPrefix(path, "/service/"):
			var action string
			switch {
			case strings.HasPrefix(path, "/service/start"):
				action = "start"
			case strings.HasPrefix(path, "/service/stop"):
				action = "stop"
			case strings.HasPrefix(path, "/service/restart"):
				action = "restart"
			}
			if action == "" {
				return fmt.Errorf("truenas: unknown service action %s", path)
			}
			m, _ := body.(map[string]any)
			service, _ := m["service"].(string)
			if service == "" {
				return fmt.Errorf("truenas: service name required for %s", path)
			}
			return c.rpc("service."+action, []any{service, map[string]any{"timeout": 30}}, out)
		case strings.HasPrefix(path, "/sharing/nfs"):
			switch method {
			case "POST":
				return c.rpc("sharing.nfs.create", []any{body}, out)
			case "DELETE":
				id, _ := strconv.Atoi(strings.TrimPrefix(path, "/sharing/nfs/"))
				return c.rpc("sharing.nfs.delete", []any{id}, out)
			}
		default:
			return fmt.Errorf("truenas: no ws write mapping for %s", path)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.restBase+path, nil)
	if err != nil {
		return err
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.ContentLength = int64(len(b))
		req.Body = io.NopCloser(strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("truenas: %s %s → %d: %s", method, path, resp.StatusCode, string(rb))
	}
	if out != nil && len(rb) > 0 {
		return json.Unmarshal(rb, out)
	}
	return nil
}

// TrueNASPool represents one ZFS storage pool.
type TrueNASPool struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"` // "ONLINE", "DEGRADED", "OFFLINE", "FAULTED"
	Healthy   bool   `json:"healthy"`
	Size      int64  `json:"size"`
	Allocated int64  `json:"allocated"`
	Free      int64  `json:"free"`
	ScanState string // derived from scan.state: "SCANNING", "FINISHED", "CANCELED", ""
}

// TrueNASDataset is a top-level ZFS dataset (pool root).
type TrueNASDataset struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	UsedBytes  int64  // parsed from used.parsed
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

// NFSShare represents a TrueNAS NFS export.
type NFSShare struct {
	ID       int      `json:"id,omitempty"`
	Path     string   `json:"path"`
	Enabled  bool     `json:"enabled"`
	Networks []string `json:"networks,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Comment  string   `json:"comment,omitempty"`
	ReadOnly bool     `json:"ro,omitempty"`
	AllDirs  bool     `json:"alldirs,omitempty"`
	Sec      []string `json:"security,omitempty"`
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
	if err := c.get(ctx, "/system/info", &info); err != nil {
		return nil, err
	}
	// TrueNAS 26+ dropped `system_ready` from system.info; readiness lives in
	// its own JSON-RPC call there.
	if c.useWS && !info.SystemReady {
		if err := c.ensureWS(); err == nil {
			var ready bool
			if err := c.rpc("system.ready", []any{}, &ready); err == nil {
				info.SystemReady = ready
			}
		}
	}
	return &info, nil
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

// Datasets returns each pool root plus its first-level children — the paths
// consumers actually export and mount (e.g. PhoenixNest/Media).
func (c *TrueNASClient) Datasets(ctx context.Context) ([]TrueNASDataset, error) {
	// flat=true is required: TrueNAS nests children inside their parent row
	// otherwise, and a roots-only response starves the Plex media-root picker
	// and the cluster pillar (both build /mnt/<name> per row).
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
	if err := c.get(ctx, "/pool/dataset?limit=50&extra={%22flat%22:true}", &raw); err != nil {
		// Fall back to basic endpoint on older versions
		if err2 := c.get(ctx, "/pool/dataset?limit=50", &raw); err2 != nil {
			return nil, err
		}
	}
	out := make([]TrueNASDataset, 0, len(raw))
	for _, r := range raw {
		// Pool root plus one level below it; deeper entries are content
		// folders, not export candidates.
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

// ── Write / management methods ────────────────────────────────────────────────

// CreateDataset creates a new ZFS dataset under an existing pool or dataset.
// name is the full path, e.g. "PhoenixNest/Media" or "PhoenixNest/Media/Movies".
// datasetType is "FILESYSTEM" (default) or "VOLUME".
func (c *TrueNASClient) CreateDataset(ctx context.Context, name, datasetType string) error {
	if datasetType == "" {
		datasetType = "FILESYSTEM"
	}
	return c.write(ctx, "POST", "/pool/dataset", map[string]any{
		"name": name,
		"type": datasetType,
	}, nil)
}

// DeleteDataset removes a ZFS dataset by its full id (e.g. "PhoenixNest/Media").
func (c *TrueNASClient) DeleteDataset(ctx context.Context, id string) error {
	return c.write(ctx, "DELETE", "/pool/dataset/"+url.PathEscape(id), nil, nil)
}

// StartService starts a NAS service (nfs, smb, ssh, iscsi, ...).
func (c *TrueNASClient) StartService(ctx context.Context, service string) error {
	return c.write(ctx, "POST", "/service/start", map[string]any{"service": service}, nil)
}

// StopService stops a NAS service.
func (c *TrueNASClient) StopService(ctx context.Context, service string) error {
	return c.write(ctx, "POST", "/service/stop", map[string]any{"service": service}, nil)
}

// RestartService restarts a NAS service.
func (c *TrueNASClient) RestartService(ctx context.Context, service string) error {
	return c.write(ctx, "POST", "/service/restart", map[string]any{"service": service}, nil)
}

// NFSShares returns all configured NFS shares.
func (c *TrueNASClient) NFSShares(ctx context.Context) ([]NFSShare, error) {
	var shares []NFSShare
	return shares, c.get(ctx, "/sharing/nfs", &shares)
}

// CreateNFSShare creates a new NFS export. The returned share carries the new id.
func (c *TrueNASClient) CreateNFSShare(ctx context.Context, opts NFSShare) (*NFSShare, error) {
	var share NFSShare
	err := c.write(ctx, "POST", "/sharing/nfs", opts, &share)
	return &share, err
}

// DeleteNFSShare removes an NFS export by id.
func (c *TrueNASClient) DeleteNFSShare(ctx context.Context, id int) error {
	return c.write(ctx, "DELETE", "/sharing/nfs/"+strconv.Itoa(id), nil, nil)
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
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
