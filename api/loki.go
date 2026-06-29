package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LokiClient queries a Grafana Loki instance.
type LokiClient struct {
	base  string
	token string
	http  *http.Client
}

// LokiEntry is a single log line from Loki.
type LokiEntry struct {
	Timestamp time.Time
	Labels    map[string]string
	Line      string
}

// NewLokiClient creates a LokiClient from the given base URL, bearer token, and TLS config.
func NewLokiClient(baseURL, token string, insecure bool) *LokiClient {
	var transport http.RoundTripper = &http.Transport{}
	if insecure {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}
	return &LokiClient{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// QueryRange runs a LogQL query over the given time range and returns log lines sorted by time.
func (c *LokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]LokiEntry, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(limit))
	params.Set("direction", "backward")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/loki/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("loki HTTP %s", resp.Status)
	}

	var result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"` // [["ts_ns", "line"], ...]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("loki decode: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("loki: %s", result.Error)
	}

	var entries []LokiEntry
	for _, stream := range result.Data.Result {
		for _, v := range stream.Values {
			if len(v) < 2 {
				continue
			}
			nsec, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			entries = append(entries, LokiEntry{
				Timestamp: time.Unix(0, nsec),
				Labels:    stream.Stream,
				Line:      v[1],
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	return entries, nil
}

// Labels returns the set of label names known to Loki.
func (c *LokiClient) Labels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/loki/api/v1/labels", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("loki HTTP %s", resp.Status)
	}
	var result struct {
		Status string   `json:"status"`
		Error  string   `json:"error"`
		Data   []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("loki labels: %s", result.Error)
	}
	return result.Data, nil
}
