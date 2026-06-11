package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

type SaltClient struct {
	base  string
	token string
	http  *http.Client
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
	c := &SaltClient{
		base: strings.TrimRight(cfg.URL, "/"),
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.login(ctx, cfg.Username, cfg.Password, cfg.Eauth); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

func (c *SaltClient) login(ctx context.Context, username, password, eauth string) error {
	if eauth == "" {
		eauth = "pam"
	}
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
		"eauth":    eauth,
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
			Token string `json:"token"`
		} `json:"return"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Return) == 0 || result.Return[0].Token == "" {
		return fmt.Errorf("no token in login response")
	}
	c.token = result.Return[0].Token
	return nil
}

func (c *SaltClient) saltGet(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Token", c.token)
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
	body, err := json.Marshal([]interface{}{cmd})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.token)
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

// JournalLogs fetches recent systemd journal lines from a minion via cmd.run.
// unit is a systemd unit name (e.g. "nginx") or empty for system journal.
// Returns up to lines log lines, newest last.
func (c *SaltClient) JournalLogs(ctx context.Context, target, unit string, lines int) ([]string, error) {
	cmd := "journalctl -n " + fmt.Sprintf("%d", lines) + " --no-pager -o short-iso"
	if unit != "" {
		cmd += " -u " + unit
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
