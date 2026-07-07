package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

type Client struct {
	base     string
	tokenID  string
	secret   string
	username string
	password string
	http     *http.Client

	mu        sync.Mutex
	ticket    string
	csrf      string
	ticketExp time.Time
}

func NewClient(cfg *config.Config) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec
	}
	return &Client{
		base:     strings.TrimRight(cfg.Host, "/") + "/api2/json",
		tokenID:  cfg.TokenID,
		secret:   cfg.Secret,
		username: cfg.Username,
		password: cfg.Password,
		// Individual Proxmox API calls return quickly (long jobs hand back a
		// UPID to poll); the timeout guards against hung connections.
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// setAuth attaches credentials to req. With username/password configured it
// uses PVE ticket auth (cookie + CSRF token for write methods); otherwise the
// PVEAPIToken header.
func (c *Client) setAuth(ctx context.Context, req *http.Request) error {
	if c.username == "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.tokenID+"="+c.secret)
		return nil
	}
	ticket, csrf, err := c.ensureTicket(ctx)
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})
	if req.Method != http.MethodGet {
		req.Header.Set("CSRFPreventionToken", csrf)
	}
	return nil
}

// ensureTicket re-logs-in when the cached ticket is missing or near expiry.
// PVE tickets are valid for 2h; refresh a few minutes early.
func (c *Client) ensureTicket(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket != "" && time.Now().Before(c.ticketExp.Add(-5*time.Minute)) {
		return c.ticket, c.csrf, nil
	}
	if err := c.login(ctx); err != nil {
		return "", "", fmt.Errorf("proxmox login: %w", err)
	}
	return c.ticket, c.csrf, nil
}

// login must be called with c.mu held.
func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	var wrapper struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return err
	}
	if wrapper.Data.Ticket == "" {
		return fmt.Errorf("no ticket in login response")
	}
	c.ticket = wrapper.Data.Ticket
	c.csrf = wrapper.Data.CSRF
	c.ticketExp = time.Now().Add(2 * time.Hour)
	return nil
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return err
	}
	return json.Unmarshal(wrapper.Data, out)
}

func (c *Client) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// postForm sends a POST with form-encoded body and ignores the response body.
func (c *Client) postForm(ctx context.Context, path string, params url.Values) error {
	var body *strings.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, body)
	if err != nil {
		return err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// postGetStr sends a POST and returns the response data field as a string (e.g. UPID).
func (c *Client) postGetStr(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(""))
	if err != nil {
		return "", err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	var wrapper struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return "", err
	}
	return wrapper.Data, nil
}

// postFormGetStr sends a POST with form-encoded body and returns the response data field as a string (UPID).
func (c *Client) postFormGetStr(ctx context.Context, path string, params url.Values) (string, error) {
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, body)
	if err != nil {
		return "", err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	var wrapper struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return "", err
	}
	return wrapper.Data, nil
}

// deleteGetStr sends a DELETE request and returns the response data field as a string (UPID).
func (c *Client) deleteGetStr(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+path, nil)
	if err != nil {
		return "", err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	var wrapper struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return "", err
	}
	return wrapper.Data, nil
}

// putForm sends a PUT request with form-encoded body (used for VM config updates).
func (c *Client) putForm(ctx context.Context, path string, params url.Values) error {
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, body)
	if err != nil {
		return err
	}
	if err := c.setAuth(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}
