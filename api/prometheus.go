package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

// PrometheusClient queries a Prometheus HTTP API.
type PrometheusClient struct {
	base  string
	token string
	http  *http.Client
}

// NewPrometheusClient creates a client from config. Returns nil when URL is empty.
func NewPrometheusClient(cfg config.PrometheusConfig) *PrometheusClient {
	if cfg.URL == "" {
		return nil
	}
	return &PrometheusClient{
		base:  strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// PromSample is one time-series sample returned by an instant query.
type PromSample struct {
	Labels map[string]string
	Value  float64
	Time   time.Time
}

// LabelString returns a concise display string for the labels of a sample,
// omitting __name__ and any labels listed in drop.
func (s PromSample) LabelString(drop ...string) string {
	skip := map[string]bool{"__name__": true}
	for _, d := range drop {
		skip[d] = true
	}
	var parts []string
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+s.Labels[k])
	}
	if len(parts) == 0 {
		if name := s.Labels["__name__"]; name != "" {
			return name
		}
		return "(no labels)"
	}
	return strings.Join(parts, " ")
}

// PromRangePoint is one (timestamp, value) pair in a range series.
type PromRangePoint struct {
	Time  time.Time
	Value float64
}

// PromRangeSeries is one labelled time-series returned by a range query.
type PromRangeSeries struct {
	Labels map[string]string
	Points []PromRangePoint
}

// Query executes an instant PromQL query and returns the result vector.
func (c *PrometheusClient) Query(ctx context.Context, expr string) ([]PromSample, error) {
	params := url.Values{
		"query": {expr},
		"time":  {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	raw, err := c.get(ctx, "/api/v1/query?"+params.Encode())
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]json.RawMessage `json:"value"` // [timestamp, "value"]
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", resp.Error)
	}

	out := make([]PromSample, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		var ts float64
		var valStr string
		json.Unmarshal(r.Value[0], &ts)   //nolint:errcheck
		json.Unmarshal(r.Value[1], &valStr) //nolint:errcheck
		val, _ := strconv.ParseFloat(valStr, 64)
		out = append(out, PromSample{
			Labels: r.Metric,
			Value:  val,
			Time:   time.Unix(int64(ts), 0),
		})
	}
	// Sort by label string for deterministic display.
	sort.Slice(out, func(i, j int) bool {
		return out[i].LabelString() < out[j].LabelString()
	})
	return out, nil
}

// QueryRange executes a range PromQL query and returns a matrix of series.
func (c *PrometheusClient) QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]PromRangeSeries, error) {
	params := url.Values{
		"query": {expr},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10) + "s"},
	}
	raw, err := c.get(ctx, "/api/v1/query_range?"+params.Encode())
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode range: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", resp.Error)
	}

	out := make([]PromRangeSeries, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		pts := make([]PromRangePoint, 0, len(r.Values))
		for _, v := range r.Values {
			var ts float64
			var valStr string
			json.Unmarshal(v[0], &ts)     //nolint:errcheck
			json.Unmarshal(v[1], &valStr) //nolint:errcheck
			val, _ := strconv.ParseFloat(valStr, 64)
			pts = append(pts, PromRangePoint{Time: time.Unix(int64(ts), 0), Value: val})
		}
		out = append(out, PromRangeSeries{Labels: r.Metric, Points: pts})
	}
	return out, nil
}

// PromAlertRule is one alerting rule from Prometheus.
type PromAlertRule struct {
	Name     string
	State    string // "inactive", "pending", "firing"
	Expr     string
	Severity string
	Summary  string
}

// AlertRules fetches active alerting rules from the Prometheus rules API.
func (c *PrometheusClient) AlertRules(ctx context.Context) ([]PromAlertRule, error) {
	raw, err := c.get(ctx, "/api/v1/rules?type=alert")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name   string            `json:"name"`
					State  string            `json:"state"`
					Query  string            `json:"query"`
					Labels map[string]string `json:"labels"`
					Annotations map[string]string `json:"annotations"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", resp.Error)
	}
	var out []PromAlertRule
	for _, g := range resp.Data.Groups {
		for _, r := range g.Rules {
			out = append(out, PromAlertRule{
				Name:     r.Name,
				State:    r.State,
				Expr:     r.Query,
				Severity: r.Labels["severity"],
				Summary:  r.Annotations["summary"],
			})
		}
	}
	return out, nil
}

func (c *PrometheusClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
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
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
