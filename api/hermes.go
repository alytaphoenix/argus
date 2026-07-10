package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HermesClient talks to a Hermes agent API server.
type HermesClient struct {
	base   string
	apiKey string
	http   *http.Client
}

// HermesStatus is the result of a Hermes health check.
type HermesStatus struct {
	Online     bool
	Version    string
	GatewayURL string // signal-cli gateway URL if configured
}

func NewHermesClient(baseURL, apiKey string, insecure bool) *HermesClient {
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &HermesClient{
		base:   strings.TrimRight(baseURL, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 10 * time.Second, Transport: tr},
	}
}

func (c *HermesClient) doGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func (c *HermesClient) Ping(ctx context.Context) error {
	resp, err := c.doGet(ctx, "/health")
	if err != nil {
		// Fall back to root.
		resp2, err2 := c.doGet(ctx, "/")
		if err2 != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 200 && resp2.StatusCode < 400 {
			return nil
		}
		return fmt.Errorf("hermes: status %d", resp2.StatusCode)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hermes: status %d", resp.StatusCode)
	}
	return nil
}

func (c *HermesClient) Status(ctx context.Context) (*HermesStatus, error) {
	resp, err := c.doGet(ctx, "/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hermes: status %d", resp.StatusCode)
	}
	var body struct {
		Status     string `json:"status"`
		Version    string `json:"version"`
		GatewayURL string `json:"gateway_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return &HermesStatus{Online: true}, nil
	}
	online := body.Status == "" || body.Status == "ok" || body.Status == "healthy"
	return &HermesStatus{
		Online:     online,
		Version:    body.Version,
		GatewayURL: body.GatewayURL,
	}, nil
}
