package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

type SaltClient struct {
	base     string
	username string
	password string
	eauth    string
	http     *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

type MinionGrains struct {
	OSFullName string   `json:"osfullname"`
	OSRelease  string   `json:"osrelease"`
	Kernel     string   `json:"kernel"`
	Arch       string   `json:"cpuarch"`
	Roles      []string `json:"roles"`
	FQDN       string   `json:"fqdn"`
	IPv4       []string `json:"ipv4"`
}

type Minion struct {
	ID     string
	Online bool
	Grains MinionGrains
}

type Job struct {
	JID       string
	Function  string
	Target    string
	StartTime string
}

func NewSaltClient(cfg config.SaltConfig) (*SaltClient, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec
	}
	eauth := cfg.Eauth
	if eauth == "" {
		eauth = "pam"
	}
	c := &SaltClient{
		base:     strings.TrimRight(cfg.URL, "/"),
		username: cfg.Username,
		password: cfg.Password,
		eauth:    eauth,
		http:     &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.login(ctx); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// ensureToken re-logs-in when the cached token is missing or near expiry.
// salt-api tokens expire (default 12h), so long-lived sessions must refresh.
func (c *SaltClient) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp.Add(-time.Minute)) {
		return c.token, nil
	}
	if err := c.login(ctx); err != nil {
		return "", fmt.Errorf("salt re-login: %w", err)
	}
	return c.token, nil
}

// login must be called with c.mu held (or before the client is shared).
func (c *SaltClient) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{
		"username": c.username,
		"password": c.password,
		"eauth":    c.eauth,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	var result struct {
		Return []struct {
			Token  string  `json:"token"`
			Expire float64 `json:"expire"` // epoch seconds
		} `json:"return"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Return) == 0 || result.Return[0].Token == "" {
		return fmt.Errorf("no token in login response")
	}
	c.token = result.Return[0].Token
	if exp := result.Return[0].Expire; exp > 0 {
		c.tokenExp = time.Unix(int64(exp), 0)
	} else {
		c.tokenExp = time.Now().Add(11 * time.Hour) // salt default is 12h
	}
	return nil
}

func (c *SaltClient) saltGet(ctx context.Context, path string) (json.RawMessage, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}

	var result struct {
		Return []json.RawMessage `json:"return"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Return) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return result.Return[0], nil
}

func (c *SaltClient) saltRun(ctx context.Context, cmd map[string]interface{}) (json.RawMessage, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal([]interface{}{cmd})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}

	var result struct {
		Return []json.RawMessage `json:"return"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Return) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return result.Return[0], nil
}

// Minions returns all known minions with cached grains and live online status.
func (c *SaltClient) Minions(ctx context.Context) ([]Minion, error) {
	grainsRaw, err := c.saltGet(ctx, "/minions")
	if err != nil {
		return nil, fmt.Errorf("minions: %w", err)
	}
	var grains map[string]MinionGrains
	if err := json.Unmarshal(grainsRaw, &grains); err != nil {
		return nil, fmt.Errorf("parsing grains: %w", err)
	}

	// Best-effort ping to determine live status; don't fail if this errors.
	pingRaw, _ := c.saltRun(ctx, map[string]interface{}{
		"client":  "local",
		"tgt":     "*",
		"fun":     "test.ping",
		"timeout": 5,
	})
	online := map[string]bool{}
	if pingRaw != nil {
		json.Unmarshal(pingRaw, &online) //nolint:errcheck
	}

	minions := make([]Minion, 0, len(grains))
	for id, g := range grains {
		minions = append(minions, Minion{ID: id, Online: online[id], Grains: g})
	}
	sort.Slice(minions, func(i, j int) bool {
		return minions[i].ID < minions[j].ID
	})
	return minions, nil
}

// ApplyState dispatches state.apply asynchronously and returns the JID.
// Pass an empty state to run highstate.
func (c *SaltClient) ApplyState(ctx context.Context, target, state string) (string, error) {
	cmd := map[string]interface{}{
		"client": "local_async",
		"tgt":    target,
		"fun":    "state.apply",
	}
	if state != "" {
		cmd["arg"] = []string{state}
	}
	raw, err := c.saltRun(ctx, cmd)
	if err != nil {
		return "", err
	}
	var result struct {
		JID string `json:"jid"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	return result.JID, nil
}

func (c *SaltClient) Jobs(ctx context.Context) ([]Job, error) {
	raw, err := c.saltGet(ctx, "/jobs")
	if err != nil {
		return nil, err
	}
	var jobsMap map[string]struct {
		Function  string `json:"Function"`
		Target    string `json:"Target"`
		StartTime string `json:"StartTime"`
	}
	if err := json.Unmarshal(raw, &jobsMap); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(jobsMap))
	for jid, j := range jobsMap {
		jobs = append(jobs, Job{JID: jid, Function: j.Function, Target: j.Target, StartTime: j.StartTime})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].JID > jobs[j].JID })
	if len(jobs) > 20 {
		jobs = jobs[:20]
	}
	return jobs, nil
}

// JobResult fetches the return value of a completed async job.
// done==false means the job has not yet returned (still running on the minion).
// failedStates contains the state IDs whose "result" field was false.
func (c *SaltClient) JobResult(ctx context.Context, jid string) (done bool, success bool, failedStates []string, err error) {
	raw, err := c.saltGet(ctx, "/jobs/"+jid)
	if err != nil {
		return false, false, nil, err
	}

	// When pending the Salt API returns {} or {"jid":"…"} with no minion keys.
	// A completed highstate result looks like:
	//   { "minion_id": { "state_id": { "result": true/false, … }, … } }
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer) == 0 {
		return false, false, nil, nil // not done yet
	}

	allSuccess := true
	var failed []string
	for _, minionRaw := range outer {
		// Each minion value is either a map of state results or a string (error).
		var states map[string]struct {
			Result bool `json:"result"`
		}
		if err := json.Unmarshal(minionRaw, &states); err != nil {
			// Not a map — possibly an error string from the minion.
			allSuccess = false
			failed = append(failed, "minion error")
			continue
		}
		for id, s := range states {
			if !s.Result {
				allSuccess = false
				failed = append(failed, id)
			}
		}
	}
	return true, allSuccess, failed, nil
}

// validUnitName matches safe systemd unit names — the unit is interpolated
// into a shell command on the minion, so anything else is rejected.
var validUnitName = regexp.MustCompile(`^[A-Za-z0-9_.@:\\-]+$`)

// JournalLogs fetches recent systemd journal lines from a minion via cmd.run.
// unit is a systemd unit name (e.g. "nginx") or empty for system journal.
// Returns up to lines log lines, newest last.
func (c *SaltClient) JournalLogs(ctx context.Context, target, unit string, lines int) ([]string, error) {
	if unit != "" && !validUnitName.MatchString(unit) {
		return nil, fmt.Errorf("invalid unit name %q", unit)
	}
	cmd := "journalctl -n " + fmt.Sprintf("%d", lines) + " --no-pager -o short-iso"
	if unit != "" {
		cmd += " -u '" + unit + "'"
	}
	raw, err := c.saltRun(ctx, map[string]interface{}{
		"client": "local",
		"tgt":    target,
		"fun":    "cmd.run",
		"arg":    []string{cmd},
	})
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("journal: parse: %w", err)
	}
	for _, v := range result {
		lines := strings.Split(strings.TrimSpace(v), "\n")
		return lines, nil
	}
	return nil, nil
}

// RunCmd runs an arbitrary shell command on the target via cmd.run and
// returns the combined output from each responding minion.
func (c *SaltClient) RunCmd(ctx context.Context, target, command string) (string, error) {
	raw, err := c.saltRun(ctx, map[string]interface{}{
		"client":  "local",
		"tgt":     target,
		"fun":     "cmd.run",
		"arg":     []string{command},
		"timeout": 30,
	})
	if err != nil {
		return "", err
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return string(raw), nil
	}
	var parts []string
	for minion, out := range result {
		parts = append(parts, minion+": "+out)
	}
	return strings.Join(parts, "\n"), nil
}

func (c *SaltClient) JobsForMinion(ctx context.Context, minionID string) ([]Job, error) {
	jobs, err := c.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	var filtered []Job
	for _, j := range jobs {
		if j.Target == minionID || j.Target == "*" {
			filtered = append(filtered, j)
		}
	}
	if len(filtered) > 10 {
		filtered = filtered[:10]
	}
	return filtered, nil
}

// SaltKeys holds the key lists returned by wheel key.list_all.
type SaltKeys struct {
	Accepted []string
	Pending  []string
	Rejected []string
	Denied   []string
}

// saltKeyData is the inner struct from wheel key.list_all responses.
type saltKeyData struct {
	Minions  []string `json:"minions"`
	Pre      []string `json:"minions_pre"`
	Rejected []string `json:"minions_rejected"`
	Denied   []string `json:"minions_denied"`
}

func parseKeyData(raw json.RawMessage) *saltKeyData {
	var kd saltKeyData
	if err := json.Unmarshal(raw, &kd); err == nil && (len(kd.Minions) > 0 || len(kd.Pre) > 0 || len(kd.Rejected) > 0) {
		return &kd
	}
	// Newer salt-api wraps wheel results: {"data": {"return": {...}}}
	var wrapper struct {
		Data struct {
			Return saltKeyData `json:"return"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		return &wrapper.Data.Return
	}
	return &saltKeyData{}
}

// ListKeys returns all minion key lists via the Salt wheel client.
func (c *SaltClient) ListKeys(ctx context.Context) (*SaltKeys, error) {
	raw, err := c.saltRun(ctx, map[string]interface{}{
		"client": "wheel",
		"fun":    "key.list_all",
	})
	if err != nil {
		return nil, fmt.Errorf("ListKeys: %w", err)
	}
	kd := parseKeyData(raw)
	return &SaltKeys{
		Accepted: kd.Minions,
		Pending:  kd.Pre,
		Rejected: kd.Rejected,
		Denied:   kd.Denied,
	}, nil
}

// AcceptKey accepts a pending minion key by ID.
func (c *SaltClient) AcceptKey(ctx context.Context, minionID string) error {
	_, err := c.saltRun(ctx, map[string]interface{}{
		"client": "wheel",
		"fun":    "key.accept",
		"match":  minionID,
	})
	if err != nil {
		return fmt.Errorf("AcceptKey %s: %w", minionID, err)
	}
	return nil
}

// RejectKey rejects a pending minion key by ID.
func (c *SaltClient) RejectKey(ctx context.Context, minionID string) error {
	_, err := c.saltRun(ctx, map[string]interface{}{
		"client": "wheel",
		"fun":    "key.reject",
		"match":  minionID,
	})
	if err != nil {
		return fmt.Errorf("RejectKey %s: %w", minionID, err)
	}
	return nil
}

// DeleteKey deletes a minion key (accepted or rejected) by ID.
func (c *SaltClient) DeleteKey(ctx context.Context, minionID string) error {
	_, err := c.saltRun(ctx, map[string]interface{}{
		"client": "wheel",
		"fun":    "key.delete",
		"match":  minionID,
	})
	if err != nil {
		return fmt.Errorf("DeleteKey %s: %w", minionID, err)
	}
	return nil
}

// WritePillar writes a Salt pillar file to the master. name is a relative path
// within pillarBase (e.g. "ratataskr_cluster" or "ratataskr_profiles/myprofile").
// The .sls extension is appended automatically.
func (c *SaltClient) WritePillar(ctx context.Context, masterMinion, pillarBase, name, content string) error {
	if pillarBase == "" {
		pillarBase = "/srv/pillar"
	}
	return c.WriteFile(ctx, masterMinion, path.Join(pillarBase, name+".sls"), content)
}

// WriteFile writes content to a file on the named minion using cmd.run + base64.
// masterMinion must be a minion whose grain id matches the salt-master host.
// The path must be absolute; its parent directory is created with mkdir -p.
func (c *SaltClient) WriteFile(ctx context.Context, masterMinion, filePath, content string) error {
	if strings.ContainsAny(filePath, "'\n") {
		return fmt.Errorf("invalid file path %q", filePath)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	dir := path.Dir(filePath)
	// base64 output is [A-Za-z0-9+/=] — safe to single-quote without escaping;
	// filePath is validated above to contain no quotes.
	cmd := fmt.Sprintf("mkdir -p '%s' && echo '%s' | base64 -d > '%s'", dir, encoded, filePath)
	raw, err := c.saltRun(ctx, map[string]interface{}{
		"client":  "local",
		"tgt":     masterMinion,
		"fun":     "cmd.run",
		"arg":     []string{cmd},
		"timeout": 15,
	})
	if err != nil {
		return err
	}
	var result map[string]string
	if jsonErr := json.Unmarshal(raw, &result); jsonErr != nil {
		return nil // unparseable → assume success (e.g. minion returned raw string)
	}
	for _, v := range result {
		if strings.HasPrefix(strings.TrimSpace(v), "Error") {
			return fmt.Errorf("remote: %s", v)
		}
	}
	return nil
}
