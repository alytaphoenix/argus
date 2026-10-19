package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── VM configuration (current allocation) ────────────────────────────────────

// VMConfig is the current resource allocation for a VM or container.
type VMConfig struct {
	Cores   int          // vCPUs (logical)
	Sockets int          // sockets (QEMU; 1 for LXC)
	Memory  int          // MB
	Disks   []DiskConfig // detected disk devices
}

// TotalCores returns Cores × Sockets.
func (c VMConfig) TotalCores() int {
	s := c.Sockets
	if s == 0 {
		s = 1
	}
	return c.Cores * s
}

// DiskConfig holds one disk device's key and parsed byte size.
type DiskConfig struct {
	Key      string // "scsi0", "virtio0", "rootfs", …
	SizeGB   int    // gigabytes (0 = could not parse)
	RawValue string // raw string from config for display
}

// VMConfigFetch fetches the current hardware config for a VM or container.
func (c *Client) VMConfigFetch(ctx context.Context, node, vmType string, vmid int) (*VMConfig, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/config", node, vmType, vmid)
	var raw map[string]json.RawMessage
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("vm config %d: %w", vmid, err)
	}
	cfg := &VMConfig{}
	jsonInt(raw["cores"], &cfg.Cores)
	jsonInt(raw["memory"], &cfg.Memory)
	jsonInt(raw["sockets"], &cfg.Sockets)
	if cfg.Cores == 0 {
		cfg.Cores = 1
	}
	if cfg.Sockets == 0 {
		cfg.Sockets = 1
	}
	if cfg.Memory == 0 {
		cfg.Memory = 512
	}

	// Detect disk devices by key prefix.
	for k, v := range raw {
		if !isDiskKey(k) {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || s == "" || s == "none" {
			continue
		}
		cfg.Disks = append(cfg.Disks, DiskConfig{
			Key:      k,
			SizeGB:   parseDiskSizeGB(s),
			RawValue: s,
		})
	}
	sort.Slice(cfg.Disks, func(i, j int) bool { return cfg.Disks[i].Key < cfg.Disks[j].Key })
	return cfg, nil
}

// ResizeVMConfig applies a new core/memory config to a running or stopped VM.
// Pass 0 for a field to leave it unchanged.
func (c *Client) ResizeVMConfig(ctx context.Context, node, vmType string, vmid int, cores, memMB int) error {
	path := fmt.Sprintf("/nodes/%s/%s/%d/config", node, vmType, vmid)
	params := url.Values{}
	if cores > 0 {
		params.Set("cores", strconv.Itoa(cores))
	}
	if memMB > 0 {
		params.Set("memory", strconv.Itoa(memMB))
	}
	if len(params) == 0 {
		return nil
	}
	return c.putForm(ctx, path, params)
}

// ResizeDisk expands a disk to an absolute size (e.g. "40G").
// Use "+" prefix for relative increase, e.g. "+10G".
func (c *Client) ResizeDisk(ctx context.Context, node, vmType string, vmid int, disk, size string) error {
	path := fmt.Sprintf("/nodes/%s/%s/%d/resize", node, vmType, vmid)
	return c.putForm(ctx, path, url.Values{"disk": {disk}, "size": {size}})
}

// ── RRD time-series data ──────────────────────────────────────────────────────

// RRDStat is one data point from Proxmox's built-in RRD store.
// Fields may be NaN (nil pointer) when data is absent.
type RRDStat struct {
	Time   time.Time
	CPU    *float64 // 0..1 fraction of ALL allocated CPUs in use
	Mem    *float64 // bytes used
	MaxMem *float64 // bytes allocated (may differ from config if balloon active)
	DiskRd *float64 // bytes/s
	DiskWr *float64 // bytes/s
	NetIn  *float64 // bytes/s
	NetOut *float64 // bytes/s
}

// rrdRaw mirrors the JSON shape of each RRD data point.
type rrdRaw struct {
	Time   float64  `json:"time"`
	CPU    *float64 `json:"cpu"`
	Mem    *float64 `json:"mem"`
	MaxMem *float64 `json:"maxmem"`
	DiskRd *float64 `json:"diskread"`
	DiskWr *float64 `json:"diskwrite"`
	NetIn  *float64 `json:"netin"`
	NetOut *float64 `json:"netout"`
}

// VMRRDData fetches Proxmox RRD statistics for a VM.
// timeframe is "hour", "day", "week", "month", or "year".
// cf is the consolidation function: "AVERAGE", "MAX".
func (c *Client) VMRRDData(ctx context.Context, node, vmType string, vmid int, timeframe, cf string) ([]RRDStat, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/rrddata?timeframe=%s&cf=%s", node, vmType, vmid, timeframe, cf)
	var raw []rrdRaw
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("rrd %d: %w", vmid, err)
	}
	stats := make([]RRDStat, 0, len(raw))
	for _, r := range raw {
		stats = append(stats, RRDStat{
			Time:   time.Unix(int64(r.Time), 0),
			CPU:    r.CPU,
			Mem:    r.Mem,
			MaxMem: r.MaxMem,
			DiskRd: r.DiskRd,
			DiskWr: r.DiskWr,
			NetIn:  r.NetIn,
			NetOut: r.NetOut,
		})
	}
	return stats, nil
}

// NodeStats is the live resource summary of a Proxmox node.
type NodeStats struct {
	CPUUsage float64 // 0..1
	MaxCPU   int
	MemUsed  int64
	MaxMem   int64
}

// NodeStatsFetch returns the current resource usage for a PVE node.
func (c *Client) NodeStatsFetch(ctx context.Context, node string) (*NodeStats, error) {
	var raw struct {
		CPU    float64 `json:"cpu"`
		MaxCPU int     `json:"maxcpu"`
		Mem    struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"memory"`
	}
	if err := c.get(ctx, fmt.Sprintf("/nodes/%s/status", node), &raw); err != nil {
		return nil, err
	}
	return &NodeStats{
		CPUUsage: raw.CPU,
		MaxCPU:   raw.MaxCPU,
		MemUsed:  raw.Mem.Used,
		MaxMem:   raw.Mem.Total,
	}, nil
}

// ── Recommendation engine ─────────────────────────────────────────────────────

// SizingStatus represents whether a resource is over-, well-, or under-provisioned.
type SizingStatus string

const (
	StatusOverProvisioned  SizingStatus = "over"
	StatusWellSized        SizingStatus = "ok"
	StatusUnderProvisioned SizingStatus = "under"
	StatusNoData           SizingStatus = "nodata"
)

// VMSizingAnalysis holds the result of analysing one VM's usage against its config.
type VMSizingAnalysis struct {
	Guest      Guest
	Config     VMConfig
	DataPoints int

	// Usage statistics (week P95 / average)
	CPUAvg   float64 // 0..1 of allocated cores
	CPUP95   float64
	MemAvgMB int
	MemP95MB int

	// Disk usage (from current guest status)
	DiskUsedGB int
	DiskTotGB  int

	// Recommendations
	RecCores int
	RecMemMB int

	// Per-resource status
	CPUStatus SizingStatus
	MemStatus SizingStatus

	// Sparkline data (8 points, for display)
	CPUSpark []float64
	MemSpark []float64
}

// OverallStatus returns the most severe status across CPU and memory.
func (a VMSizingAnalysis) OverallStatus() SizingStatus {
	if a.CPUStatus == StatusUnderProvisioned || a.MemStatus == StatusUnderProvisioned {
		return StatusUnderProvisioned
	}
	if a.CPUStatus == StatusOverProvisioned || a.MemStatus == StatusOverProvisioned {
		return StatusOverProvisioned
	}
	if a.CPUStatus == StatusNoData {
		return StatusNoData
	}
	return StatusWellSized
}

// AnalyzeUsage derives sizing recommendations from one week of RRD data.
func AnalyzeUsage(guest Guest, cfg VMConfig, rrd []RRDStat) VMSizingAnalysis {
	a := VMSizingAnalysis{
		Guest:     guest,
		Config:    cfg,
		CPUStatus: StatusNoData,
		MemStatus: StatusNoData,
		RecCores:  cfg.TotalCores(),
		RecMemMB:  cfg.Memory,
	}

	// Disk usage from live guest stats.
	if guest.MaxDisk > 0 {
		a.DiskUsedGB = int(guest.Disk / (1024 * 1024 * 1024))
		a.DiskTotGB = int(guest.MaxDisk / (1024 * 1024 * 1024))
	}

	if len(rrd) == 0 {
		return a
	}

	// Collect valid samples.
	var cpuSamples, memFracSamples []float64
	for _, s := range rrd {
		if s.CPU != nil && !math.IsNaN(*s.CPU) {
			cpuSamples = append(cpuSamples, *s.CPU)
		}
		if s.Mem != nil && s.MaxMem != nil && *s.MaxMem > 0 &&
			!math.IsNaN(*s.Mem) && !math.IsNaN(*s.MaxMem) {
			memFracSamples = append(memFracSamples, *s.Mem / *s.MaxMem)
		}
	}
	a.DataPoints = len(cpuSamples)
	if a.DataPoints == 0 {
		return a
	}

	// ── CPU ──────────────────────────────────────────────────────────────────
	sort.Float64s(cpuSamples)
	cpuAvg := mean(cpuSamples)
	cpuP95 := percentileSorted(cpuSamples, 95)
	a.CPUAvg = cpuAvg
	a.CPUP95 = cpuP95

	// Target P95 load at 60% of allocated cores → rec = P95_used_cores / 0.60
	usedCoresP95 := cpuP95 * float64(cfg.TotalCores())
	recCores := int(math.Ceil(usedCoresP95 / 0.60))
	if recCores < 1 {
		recCores = 1
	}
	// Keep sane upper bound: don't recommend more than 2× current or 32.
	maxRec := cfg.TotalCores() * 2
	if maxRec > 32 {
		maxRec = 32
	}
	if recCores > maxRec {
		recCores = maxRec
	}
	a.RecCores = recCores

	switch {
	case cpuP95 < 0.15 && cfg.TotalCores() > 1:
		a.CPUStatus = StatusOverProvisioned
	case cpuP95 > 0.80:
		a.CPUStatus = StatusUnderProvisioned
	default:
		a.CPUStatus = StatusWellSized
	}

	// ── Memory ───────────────────────────────────────────────────────────────
	if len(memFracSamples) > 0 {
		sort.Float64s(memFracSamples)
		memAvgFrac := mean(memFracSamples)
		memP95Frac := percentileSorted(memFracSamples, 95)

		a.MemAvgMB = int(memAvgFrac * float64(cfg.Memory))
		a.MemP95MB = int(memP95Frac * float64(cfg.Memory))

		// Recommend P95 usage + 30% headroom, rounded up to nearest 256 MB.
		recMem := roundUpTo(int(float64(a.MemP95MB)*1.30), 256)
		if recMem < 256 {
			recMem = 256
		}
		a.RecMemMB = recMem

		switch {
		case memP95Frac < 0.20 && cfg.Memory > 512:
			a.MemStatus = StatusOverProvisioned
		case memP95Frac > 0.85:
			a.MemStatus = StatusUnderProvisioned
		default:
			a.MemStatus = StatusWellSized
		}
	}

	// ── Sparklines (downsample to 24 points) ─────────────────────────────────
	a.CPUSpark = downsample(cpuSamples, 24)
	if len(memFracSamples) > 0 {
		a.MemSpark = downsample(memFracSamples, 24)
	}

	return a
}

// ── Batch loader ─────────────────────────────────────────────────────────────

// AnalysisResult bundles one VM's full analysis for loading in the UI.
type AnalysisResult struct {
	Analysis VMSizingAnalysis
	Err      error
}

// LoadAllAnalyses concurrently fetches config + RRD data for each guest and
// returns one AnalysisResult per guest (nil RRD data = StatusNoData).
func LoadAllAnalyses(ctx context.Context, c *Client, guests []Guest) []AnalysisResult {
	results := make([]AnalysisResult, len(guests))
	var wg sync.WaitGroup
	for i, g := range guests {
		wg.Add(1)
		go func(idx int, guest Guest) {
			defer wg.Done()
			innerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()

			cfg, cfgErr := c.VMConfigFetch(innerCtx, guest.Node, guest.Type, guest.VMID)
			if cfgErr != nil {
				results[idx] = AnalysisResult{Err: cfgErr}
				return
			}
			rrd, _ := c.VMRRDData(innerCtx, guest.Node, guest.Type, guest.VMID, "week", "AVERAGE")
			results[idx] = AnalysisResult{Analysis: AnalyzeUsage(guest, *cfg, rrd)}
		}(i, g)
	}
	wg.Wait()
	return results
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func isDiskKey(k string) bool {
	for _, prefix := range []string{"scsi", "virtio", "ide", "sata", "rootfs"} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func parseDiskSizeGB(s string) int {
	// Format: "local-lvm:vm-100-disk-0,size=20G,..." or "rootfs: ...,size=8G"
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "size=") {
			v := strings.TrimPrefix(part, "size=")
			return parseSizeStrGB(v)
		}
	}
	return 0
}

func parseSizeStrGB(s string) int {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0
	}
	suffix := s[len(s)-1]
	num := s[:len(s)-1]
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch suffix {
	case 'G', 'g':
		return int(n)
	case 'M', 'm':
		return int(n / 1024)
	case 'T', 't':
		return int(n * 1024)
	}
	return 0
}

func jsonInt(raw json.RawMessage, out *int) {
	if raw == nil {
		return
	}
	json.Unmarshal(raw, out) //nolint:errcheck
}

func mean(sorted []float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return sum / float64(len(sorted))
}

func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*p/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func roundUpTo(n, multiple int) int {
	if multiple <= 0 {
		return n
	}
	return ((n + multiple - 1) / multiple) * multiple
}

func downsample(data []float64, target int) []float64 {
	if len(data) <= target {
		return data
	}
	out := make([]float64, target)
	for i := range out {
		start := i * len(data) / target
		end := (i + 1) * len(data) / target
		if end > len(data) {
			end = len(data)
		}
		out[i] = mean(data[start:end])
	}
	return out
}
