package api

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScanFinding is a single finding from a port scan or VM firewall audit.
type ScanFinding struct {
	Target      string // IP or hostname
	Node        string // Proxmox node name (for VM findings)
	VMID        int    // VM ID (for VM findings; 0 = not a VM)
	ScanType    string // "port" | "vm_firewall"
	Check       string // e.g. "tcp:22" | "vm_firewall"
	Status      string // "pass" | "warn" | "fail" | "info"
	Detail      string // human-readable finding
	Severity    string // "info" | "low" | "medium" | "high" | "critical"
	Remediation string // human-readable suggestion
	RemAction   string // programmatic: "enable_vm_firewall" | ""
}

// CommonPorts is the set of TCP ports probed by default.
var CommonPorts = []int{
	21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 443, 445,
	993, 995, 1433, 1521, 2049, 3306, 3389, 5432, 5900, 6379,
	8080, 8443, 8888, 9200, 27017,
}

// PortScan dials each port on target concurrently and returns findings for
// every open port it discovers. The context deadline controls overall scan time.
func PortScan(ctx context.Context, target string, ports []int) []ScanFinding {
	type result struct {
		port int
		open bool
	}

	ch := make(chan result, len(ports))
	var wg sync.WaitGroup

	for _, port := range ports {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			addr := fmt.Sprintf("%s:%d", target, p)
			d := net.Dialer{Timeout: 600 * time.Millisecond}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
				ch <- result{p, true}
			} else {
				ch <- result{p, false}
			}
		}(port)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	openSet := make(map[int]bool)
	for r := range ch {
		if r.open {
			openSet[r.port] = true
		}
	}

	// Sort for deterministic output
	var open []int
	for p := range openSet {
		open = append(open, p)
	}
	sort.Ints(open)

	var findings []ScanFinding
	for _, port := range open {
		svc := wellKnownService(port)
		f := ScanFinding{
			Target:   target,
			ScanType: "port",
			Check:    fmt.Sprintf("tcp:%d", port),
			Status:   "info",
			Detail:   fmt.Sprintf("Port %d open (%s)", port, svc),
			Severity: "info",
		}
		switch port {
		case 23: // telnet
			f.Severity = "critical"
			f.Remediation = "Disable telnet immediately — use SSH instead"
		case 21: // ftp
			f.Severity = "high"
			f.Remediation = "Replace FTP with SFTP/SCP"
		case 111, 2049: // rpcbind / NFS
			f.Severity = "medium"
			f.Remediation = "Restrict NFS/RPC to trusted networks only"
		case 135, 139, 445: // Windows SMB/NetBIOS
			f.Severity = "medium"
			f.Remediation = "Restrict SMB to trusted networks or VPN"
		case 3389: // RDP
			f.Severity = "medium"
			f.Remediation = "Restrict RDP to VPN/trusted IPs; consider disabling if unused"
		case 5900: // VNC
			f.Severity = "high"
			f.Remediation = "Expose VNC only over an encrypted SSH tunnel"
		case 6379: // Redis
			f.Severity = "medium"
			f.Remediation = "Bind Redis to loopback and enable AUTH password"
		case 9200, 9300: // Elasticsearch
			f.Severity = "high"
			f.Remediation = "Enable Elasticsearch auth/TLS and restrict network access"
		case 27017: // MongoDB
			f.Severity = "high"
			f.Remediation = "Enable MongoDB auth and bind to loopback or trusted interface"
		}
		findings = append(findings, f)
	}
	return findings
}

func wellKnownService(port int) string {
	m := map[int]string{
		21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp",
		53: "dns", 80: "http", 110: "pop3", 111: "rpcbind",
		135: "msrpc", 139: "netbios-ssn", 143: "imap", 443: "https",
		445: "smb", 993: "imaps", 995: "pop3s",
		1433: "mssql", 1521: "oracle", 2049: "nfs",
		3306: "mysql", 3389: "rdp", 5432: "postgres",
		5900: "vnc", 6379: "redis", 8080: "http-alt",
		8443: "https-alt", 8888: "jupyter", 9200: "elasticsearch",
		9300: "elasticsearch-cluster", 27017: "mongodb",
	}
	if s, ok := m[port]; ok {
		return s
	}
	return "unknown"
}

// VMFirewallScan checks whether the Proxmox VM-level firewall is enabled for
// each running guest and returns a finding per VM.
func VMFirewallScan(ctx context.Context, client *Client, guests []Guest) []ScanFinding {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var findings []ScanFinding

	for _, g := range guests {
		if g.Status != "running" {
			continue
		}
		wg.Add(1)
		go func(g Guest) {
			defer wg.Done()

			enabled, err := client.VMFirewallEnabled(ctx, g.Node, g.Type, g.VMID)
			if err != nil {
				// Firewall endpoint may not exist for older Proxmox versions; skip silently.
				return
			}

			f := ScanFinding{
				Target:   g.Name,
				Node:     g.Node,
				VMID:     g.VMID,
				ScanType: "vm_firewall",
				Check:    "vm_firewall",
			}
			if enabled {
				f.Status = "pass"
				f.Detail = fmt.Sprintf("%s (VMID %d): VM firewall enabled", g.Name, g.VMID)
				f.Severity = "info"
			} else {
				f.Status = "fail"
				f.Detail = fmt.Sprintf("%s (VMID %d): VM firewall disabled — traffic unfiltered", g.Name, g.VMID)
				f.Severity = "medium"
				f.Remediation = "Enable VM firewall in Proxmox to filter inbound/outbound traffic"
				f.RemAction = "enable_vm_firewall"
			}

			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		}(g)
	}

	wg.Wait()

	// Sort: failing first, then by VM name
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Status != findings[j].Status {
			return findings[i].Status == "fail"
		}
		return findings[i].Target < findings[j].Target
	})
	return findings
}

// DiscoverHost runs a port scan and returns a terse service map string and findings.
// The summary string is suitable for storing in service_hosts.discovered_ports.
func DiscoverHost(ctx context.Context, host string) (summary string, findings []ScanFinding) {
	findings = PortScan(ctx, host, CommonPorts)
	var parts []string
	for _, f := range findings {
		parts = append(parts, f.Check)
	}
	if len(parts) > 0 {
		summary = strings.Join(parts, ",")
	}
	return summary, findings
}

// ScanSeverityOrder returns a numeric weight for severity (higher = more severe).
func ScanSeverityOrder(sev string) int {
	switch sev {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	}
	return 0
}
