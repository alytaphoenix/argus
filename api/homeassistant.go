package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type HAClient struct {
	base  string
	token string
	http  *http.Client
}

type HAEntity struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
}

func NewHAClient(baseURL, token string, insecure bool) *HAClient {
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &HAClient{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second, Transport: tr},
	}
}

func (c *HAClient) doGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

func (c *HAClient) Ping(ctx context.Context) error {
	resp, err := c.doGet(ctx, "/api/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

type haConfigResult struct {
	Version      string `json:"version"`
	LocationName string `json:"location_name"`
}

func (c *HAClient) Version(ctx context.Context) (string, error) {
	resp, err := c.doGet(ctx, "/api/config")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var r haConfigResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Version, nil
}

func (c *HAClient) States(ctx context.Context) ([]HAEntity, error) {
	resp, err := c.doGet(ctx, "/api/states")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var entities []HAEntity
	if err := json.NewDecoder(resp.Body).Decode(&entities); err != nil {
		return nil, err
	}
	return entities, nil
}

func (c *HAClient) GetState(ctx context.Context, entityID string) (HAEntity, error) {
	resp, err := c.doGet(ctx, "/api/states/"+entityID)
	if err != nil {
		return HAEntity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HAEntity{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var e HAEntity
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return HAEntity{}, err
	}
	return e, nil
}

// CallService calls a HA service (e.g. domain=light, service=turn_on).
// entityID may be empty for domain-wide services. extraData adds extra JSON body fields.
func (c *HAClient) CallService(ctx context.Context, domain, service, entityID string, extraData map[string]any) error {
	body := map[string]any{}
	if entityID != "" {
		body["entity_id"] = entityID
	}
	for k, v := range extraData {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/services/"+domain+"/"+service,
		bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
