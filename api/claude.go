package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

const (
	claudeAPIURL       = "https://api.anthropic.com/v1/messages"
	claudeAPIVersion   = "2023-06-01"
	claudeDefaultModel = "claude-sonnet-4-6"
	claudeMaxTokens    = 1024
)

// ClaudeClient calls the Anthropic Messages API.
type ClaudeClient struct {
	apiKey string
	model  string
	http   *http.Client
}

// NewClaudeClient creates a client. Returns nil if APIKey is empty.
func NewClaudeClient(cfg config.ClaudeConfig) *ClaudeClient {
	if cfg.APIKey == "" {
		return nil
	}
	model := cfg.Model
	if model == "" {
		model = claudeDefaultModel
	}
	return &ClaudeClient{
		apiKey: cfg.APIKey,
		model:  model,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// VMResizeExplanation is what Claude returns after analysing a VM's sizing.
type VMResizeExplanation struct {
	Summary    string   // one-sentence headline
	Detail     string   // full paragraph explanation
	Changes    []string // bullet list: each proposed change as a short string
	Caveats    []string // e.g. "reboot required", "balloon driver needed"
	RiskLevel  string   // "low", "medium", "high"
}

// ExplainVMResize asks Claude to explain the sizing analysis and proposed
// changes in plain English. The returned explanation is shown to the user
// before they confirm the resize.
func (c *ClaudeClient) ExplainVMResize(ctx context.Context, a VMSizingAnalysis, nodeStats *NodeStats) (*VMResizeExplanation, error) {
	prompt := buildResizePrompt(a, nodeStats)

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":      c.model,
		"max_tokens": claudeMaxTokens,
		"system":     resizeSystemPrompt,
		"messages":   []message{{Role: "user", Content: prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", claudeAPIVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(body, &errResp) //nolint:errcheck
		if errResp.Error.Message != "" {
			return nil, fmt.Errorf("claude API: %s", errResp.Error.Message)
		}
		return nil, fmt.Errorf("claude API: HTTP %s", resp.Status)
	}

	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("claude: decode: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("claude: empty response")
	}

	text := apiResp.Content[0].Text
	return parseExplanation(text), nil
}

// ── Prompt helpers ────────────────────────────────────────────────────────────

const resizeSystemPrompt = `You are an expert infrastructure engineer advising on VM right-sizing for a Proxmox homelab.
Given VM usage metrics, produce a concise, actionable recommendation.

Respond ONLY with valid JSON matching this schema:
{
  "summary": "<one sentence headline, max 100 chars>",
  "detail": "<2-3 sentence explanation of the sizing situation>",
  "changes": ["<change 1>", "<change 2>"],
  "caveats": ["<caveat 1>"],
  "risk_level": "low|medium|high"
}

Use plain language. Be specific about numbers. Keep changes as short imperative sentences like "Reduce CPU from 4 to 1 core". If no change is recommended, say so in changes with "No changes recommended". Risk is low for reductions (worst case: minor slowdown), medium if increasing resources significantly, high only if data is insufficient or the change might cause instability.`

func buildResizePrompt(a VMSizingAnalysis, ns *NodeStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VM: %q (VMID %d, %s)\n", a.Guest.Name, a.Guest.VMID, a.Guest.Type)
	fmt.Fprintf(&b, "Node: %s\n", a.Guest.Node)
	fmt.Fprintf(&b, "Current config: %d vCPU, %d MB RAM\n", a.Config.TotalCores(), a.Config.Memory)
	fmt.Fprintf(&b, "Measurement period: 7 days (%d data points)\n", a.DataPoints)
	fmt.Fprintf(&b, "\nCPU usage:\n")
	fmt.Fprintf(&b, "  Average: %.1f%% of %d vCPU (%.2f cores used on avg)\n",
		a.CPUAvg*100, a.Config.TotalCores(), a.CPUAvg*float64(a.Config.TotalCores()))
	fmt.Fprintf(&b, "  P95:     %.1f%% of %d vCPU (%.2f cores used at P95)\n",
		a.CPUP95*100, a.Config.TotalCores(), a.CPUP95*float64(a.Config.TotalCores()))
	fmt.Fprintf(&b, "  Status: %s\n", a.CPUStatus)
	fmt.Fprintf(&b, "\nMemory usage:\n")
	fmt.Fprintf(&b, "  Average: %d MB used of %d MB allocated (%.0f%%)\n",
		a.MemAvgMB, a.Config.Memory, safePercent(float64(a.MemAvgMB), float64(a.Config.Memory)))
	fmt.Fprintf(&b, "  P95:     %d MB used of %d MB allocated (%.0f%%)\n",
		a.MemP95MB, a.Config.Memory, safePercent(float64(a.MemP95MB), float64(a.Config.Memory)))
	fmt.Fprintf(&b, "  Status: %s\n", a.MemStatus)
	if a.DiskTotGB > 0 {
		fmt.Fprintf(&b, "\nDisk: %d GB used of %d GB total (%.0f%%)\n",
			a.DiskUsedGB, a.DiskTotGB, safePercent(float64(a.DiskUsedGB), float64(a.DiskTotGB)))
	}
	fmt.Fprintf(&b, "\nAlgorithmic recommendation:\n")
	fmt.Fprintf(&b, "  CPU: %d vCPU → %d vCPU\n", a.Config.TotalCores(), a.RecCores)
	fmt.Fprintf(&b, "  RAM: %d MB → %d MB\n", a.Config.Memory, a.RecMemMB)
	if ns != nil {
		fmt.Fprintf(&b, "\nNode capacity:\n")
		fmt.Fprintf(&b, "  CPU: %d logical CPUs, %.0f%% currently used\n",
			ns.MaxCPU, ns.CPUUsage*100)
		fmt.Fprintf(&b, "  RAM: %d GB total, %d GB used\n",
			ns.MaxMem/1024/1024/1024, ns.MemUsed/1024/1024/1024)
	}
	b.WriteString("\nProvide your recommendation.")
	return b.String()
}

// parseExplanation parses the JSON response from Claude into a
// VMResizeExplanation. Falls back to a plain-text wrapper if JSON is malformed.
func parseExplanation(text string) *VMResizeExplanation {
	// Claude might wrap JSON in markdown fences — strip them.
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "{"); idx > 0 {
		text = text[idx:]
	}
	if idx := strings.LastIndex(text, "}"); idx >= 0 && idx < len(text)-1 {
		text = text[:idx+1]
	}

	var raw struct {
		Summary   string   `json:"summary"`
		Detail    string   `json:"detail"`
		Changes   []string `json:"changes"`
		Caveats   []string `json:"caveats"`
		RiskLevel string   `json:"risk_level"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		// Best-effort fallback — treat whole text as detail.
		return &VMResizeExplanation{
			Summary:   "Claude recommendation",
			Detail:    text,
			RiskLevel: "unknown",
		}
	}
	return &VMResizeExplanation{
		Summary:   raw.Summary,
		Detail:    raw.Detail,
		Changes:   raw.Changes,
		Caveats:   raw.Caveats,
		RiskLevel: raw.RiskLevel,
	}
}

// NaturalLanguageQuery sends a freeform question with a system context snapshot
// and returns the plain-text response. The caller provides a context string
// describing current homelab state (nodes, alerts, etc.) to inject as system.
func (c *ClaudeClient) NaturalLanguageQuery(ctx context.Context, systemContext, question string) (string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	system := "You are a helpful homelab assistant with access to the following live state:\n\n" + systemContext +
		"\n\nAnswer questions concisely and accurately based on this data. Use plain text, no markdown."

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":      c.model,
		"max_tokens": 512,
		"system":     system,
		"messages":   []message{{Role: "user", Content: question}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", claudeAPIVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error struct{ Message string } `json:"error"`
		}
		json.Unmarshal(body, &errResp) //nolint:errcheck
		if errResp.Error.Message != "" {
			return "", fmt.Errorf("claude API: %s", errResp.Error.Message)
		}
		return "", fmt.Errorf("claude API: HTTP %s", resp.Status)
	}

	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("claude: decode: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("claude: empty response")
	}
	return apiResp.Content[0].Text, nil
}

func safePercent(used, total float64) float64 {
	if total == 0 {
		return 0
	}
	return used / total * 100
}
