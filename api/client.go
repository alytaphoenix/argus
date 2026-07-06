package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

type Client struct {
	base    string
	tokenID string
	secret  string
	http    *http.Client
}

func NewClient(cfg *config.Config) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec
	}
	return &Client{
		base:    strings.TrimRight(cfg.Host, "/") + "/api2/json",
		tokenID: cfg.TokenID,
		secret:  cfg.Secret,
		// Individual Proxmox API calls return quickly (long jobs hand back a
		// UPID to poll); the timeout guards against hung connections.
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *Client) authHeader() string {
	return "PVEAPIToken=" + c.tokenID + "=" + c.secret
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authHeader())

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
	req.Header.Set("Authorization", c.authHeader())

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
	req.Header.Set("Authorization", c.authHeader())
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
	req.Header.Set("Authorization", c.authHeader())
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
	req.Header.Set("Authorization", c.authHeader())
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
	req.Header.Set("Authorization", c.authHeader())
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
	req.Header.Set("Authorization", c.authHeader())
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
