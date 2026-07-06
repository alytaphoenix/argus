// ratataskr-mcp-server exposes all ratataskr operations as MCP tools so Claude can
// call them directly. Run it as an MCP stdio server:
//
//	claude mcp add ratataskr -- /path/to/ratataskr-mcp-server
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/config"
	gkr "github.com/alytaphoenix/ratataskr/keyring"
)

// ── JSON-RPC 2.0 wire types ──────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── MCP protocol types ───────────────────────────────────────────────────────

type initResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    serverCaps `json:"capabilities"`
	ServerInfo      serverInfo `json:"serverInfo"`
}

type serverCaps struct {
	Tools map[string]any `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
}

type schemaProp struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func ok(id any, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errResp(id any, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func text(s string) toolResult {
	return toolResult{Content: []contentBlock{{Type: "text", Text: s}}}
}

func str(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}


// ── Client bundle ────────────────────────────────────────────────────────────

type mcpClients struct {
	proxmox  *api.Client
	salt     *api.SaltClient
	nomad    *api.NomadClient
	hass     *api.HAClient
	prom     *api.PrometheusClient
	loki     *api.LokiClient
	truenas  *api.TrueNASClient
	crowdsec *api.CrowdSecClient
	wazuh    *api.WazuhClient
	dns      *api.TechnitiumClient
	plex     *api.PlexClient
	servarr  map[string]*api.ServarrClient // keyed by app name
	ca       *api.StepCAClient
	matchbox *api.MatchboxClient
}

// ── Tool catalogue ───────────────────────────────────────────────────────────

func allTools(c *mcpClients) []toolDef {
	// ── Proxmox (always present) ─────────────────────────────────────────────
	tools := []toolDef{
		{
			Name:        "proxmox_list_nodes",
			Description: "List all Proxmox cluster nodes with CPU, memory, and status.",
			InputSchema: inputSchema{Type: "object"},
		},
		{
			Name:        "proxmox_list_guests",
			Description: "List all VMs and LXC containers on a Proxmox node.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]schemaProp{"node": {Type: "string", Description: "Proxmox node name"}},
				Required:   []string{"node"},
			},
		},
		{
			Name:        "proxmox_guest_status",
			Description: "Get detailed status of a single VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node": {Type: "string", Description: "Proxmox node name"},
					"type": {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid": {Type: "integer", Description: "VM/CT ID"},
				},
				Required: []string{"node", "type", "vmid"},
			},
		},
		{
			Name:        "proxmox_guest_action",
			Description: "Start, stop, or reboot a VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node":   {Type: "string", Description: "Proxmox node name"},
					"type":   {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid":   {Type: "integer", Description: "VM/CT ID"},
					"action": {Type: "string", Description: "Action to perform", Enum: []string{"start", "stop", "reboot", "shutdown"}},
				},
				Required: []string{"node", "type", "vmid", "action"},
			},
		},
		{
			Name:        "proxmox_next_vmid",
			Description: "Return the next available VM/CT ID from the cluster.",
			InputSchema: inputSchema{Type: "object"},
		},
		{
			Name:        "proxmox_clone_vm",
			Description: "Clone a VM from a template and optionally configure cloud-init.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node":         {Type: "string", Description: "Target Proxmox node"},
					"template_id":  {Type: "integer", Description: "Source template VMID"},
					"name":         {Type: "string", Description: "New VM name"},
					"new_vmid":     {Type: "integer", Description: "New VMID (0 = auto)"},
					"storage":      {Type: "string", Description: "Storage pool (blank = same as source)"},
					"ci_user":      {Type: "string", Description: "Cloud-init user"},
					"ci_password":  {Type: "string", Description: "Cloud-init password"},
					"ci_ssh_keys":  {Type: "string", Description: "SSH authorized_keys content"},
					"ci_ip_config": {Type: "string", Description: "IP config (e.g. ip=dhcp)"},
				},
				Required: []string{"node", "template_id", "name"},
			},
		},
		{
			Name:        "proxmox_node_version",
			Description: "Get Proxmox VE version on a specific node.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]schemaProp{"node": {Type: "string", Description: "Proxmox node name"}},
				Required:   []string{"node"},
			},
		},
		{
			Name:        "proxmox_list_snapshots",
			Description: "List snapshots for a VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node": {Type: "string", Description: "Proxmox node name"},
					"type": {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid": {Type: "integer", Description: "VM/CT ID"},
				},
				Required: []string{"node", "type", "vmid"},
			},
		},
		{
			Name:        "proxmox_create_snapshot",
			Description: "Create a snapshot of a VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node": {Type: "string", Description: "Proxmox node name"},
					"type": {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid": {Type: "integer", Description: "VM/CT ID"},
					"name": {Type: "string", Description: "Snapshot name (alphanumeric, no spaces)"},
					"desc": {Type: "string", Description: "Snapshot description (optional)"},
				},
				Required: []string{"node", "type", "vmid", "name"},
			},
		},
		{
			Name:        "proxmox_delete_snapshot",
			Description: "Delete a snapshot of a VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node":     {Type: "string", Description: "Proxmox node name"},
					"type":     {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid":     {Type: "integer", Description: "VM/CT ID"},
					"snapname": {Type: "string", Description: "Snapshot name to delete"},
				},
				Required: []string{"node", "type", "vmid", "snapname"},
			},
		},
	}

	// ── Salt ─────────────────────────────────────────────────────────────────
	if c.salt != nil {
		tools = append(tools,
			toolDef{
				Name:        "salt_list_minions",
				Description: "List all Salt minions with online status and grain data.",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "salt_apply_highstate",
				Description: "Apply Salt highstate to a target (minion ID or glob).",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"target": {Type: "string", Description: "Salt target (minion ID or glob, e.g. '*')"},
					},
					Required: []string{"target"},
				},
			},
			toolDef{
				Name:        "salt_apply_state",
				Description: "Apply a named Salt state to a target.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"target": {Type: "string", Description: "Salt target (minion ID or glob)"},
						"state":  {Type: "string", Description: "State name (e.g. nginx, services.wiring)"},
					},
					Required: []string{"target", "state"},
				},
			},
			toolDef{
				Name:        "salt_job_result",
				Description: "Poll the result of a Salt async job by JID.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"jid": {Type: "string", Description: "Job ID returned by salt_apply_highstate or salt_apply_state"},
					},
					Required: []string{"jid"},
				},
			},
			toolDef{
				Name:        "salt_journal_logs",
				Description: "Fetch systemd journal logs from a minion via Salt.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"target": {Type: "string", Description: "Minion ID"},
						"unit":   {Type: "string", Description: "Systemd unit name (e.g. nginx, docker)"},
						"lines":  {Type: "integer", Description: "Number of lines to fetch (default 50)"},
					},
					Required: []string{"target", "unit"},
				},
			},
		)
	}

	// ── Nomad ────────────────────────────────────────────────────────────────
	if c.nomad != nil {
		tools = append(tools,
			toolDef{
				Name:        "nomad_list_jobs",
				Description: "List all Nomad jobs with status and allocation counts.",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "nomad_job_allocations",
				Description: "List allocations for a specific Nomad job.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"job_id": {Type: "string", Description: "Nomad job ID"},
					},
					Required: []string{"job_id"},
				},
			},
			toolDef{
				Name:        "nomad_stop_job",
				Description: "Stop (deregister) a Nomad job.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"job_id": {Type: "string", Description: "Nomad job ID"},
					},
					Required: []string{"job_id"},
				},
			},
		)
	}

	// ── Prometheus ───────────────────────────────────────────────────────────
	if c.prom != nil {
		tools = append(tools,
			toolDef{
				Name:        "prom_query",
				Description: "Execute an instant PromQL query and return the current value vector.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"expr": {Type: "string", Description: "PromQL expression (e.g. up, node_memory_MemFree_bytes)"},
					},
					Required: []string{"expr"},
				},
			},
			toolDef{
				Name:        "prom_alerts",
				Description: "List all Prometheus alert rules that are currently firing or pending.",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── Loki ─────────────────────────────────────────────────────────────────
	if c.loki != nil {
		tools = append(tools,
			toolDef{
				Name:        "loki_query",
				Description: "Query Grafana Loki logs using a LogQL expression.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"expr":  {Type: "string", Description: "LogQL expression (e.g. {app=\"nginx\"} |= \"error\")"},
						"hours": {Type: "integer", Description: "How many hours to look back (default 1)"},
						"limit": {Type: "integer", Description: "Max log lines to return (default 100)"},
					},
					Required: []string{"expr"},
				},
			},
		)
	}

	// ── TrueNAS ──────────────────────────────────────────────────────────────
	if c.truenas != nil {
		tools = append(tools,
			toolDef{
				Name:        "truenas_status",
				Description: "Get TrueNAS pool health, active alerts, and running services.",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── CrowdSec ─────────────────────────────────────────────────────────────
	if c.crowdsec != nil {
		tools = append(tools,
			toolDef{
				Name:        "crowdsec_decisions",
				Description: "List active CrowdSec decisions (bans, throttles).",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "crowdsec_unban",
				Description: "Delete a CrowdSec decision (unban an IP).",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"id": {Type: "integer", Description: "Decision ID from crowdsec_decisions"},
					},
					Required: []string{"id"},
				},
			},
		)
	}

	// ── Wazuh ────────────────────────────────────────────────────────────────
	if c.wazuh != nil {
		tools = append(tools,
			toolDef{
				Name:        "wazuh_agents",
				Description: "List Wazuh agents with status and OS info.",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "wazuh_alerts",
				Description: "List recent Wazuh security alerts (last 24h).",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── DNS (Technitium) ─────────────────────────────────────────────────────
	if c.dns != nil {
		tools = append(tools,
			toolDef{
				Name:        "dns_list_zones",
				Description: "List all DNS zones managed by Technitium.",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "dns_list_records",
				Description: "List DNS records in a zone.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"zone": {Type: "string", Description: "DNS zone name (e.g. homelab.local)"},
					},
					Required: []string{"zone"},
				},
			},
			toolDef{
				Name:        "dns_add_record",
				Description: "Add a DNS record to a zone.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"zone":  {Type: "string", Description: "Zone name"},
						"type":  {Type: "string", Description: "Record type", Enum: []string{"A", "AAAA", "CNAME", "TXT", "MX"}},
						"name":  {Type: "string", Description: "Record name (e.g. myhost or myhost.homelab.local)"},
						"value": {Type: "string", Description: "Record value (IP for A, hostname for CNAME, etc.)"},
						"ttl":   {Type: "integer", Description: "TTL in seconds (default 300)"},
					},
					Required: []string{"zone", "type", "name", "value"},
				},
			},
			toolDef{
				Name:        "dns_delete_record",
				Description: "Delete a DNS record from a zone.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"zone":  {Type: "string", Description: "Zone name"},
						"name":  {Type: "string", Description: "Record name"},
						"type":  {Type: "string", Description: "Record type"},
						"value": {Type: "string", Description: "Record value"},
					},
					Required: []string{"zone", "name", "type", "value"},
				},
			},
		)
	}

	// ── Plex ─────────────────────────────────────────────────────────────────
	if c.plex != nil {
		tools = append(tools,
			toolDef{
				Name:        "plex_status",
				Description: "Get Plex Media Server info, active sessions, and libraries.",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── Servarr ──────────────────────────────────────────────────────────────
	if len(c.servarr) > 0 {
		names := make([]string, 0, len(c.servarr))
		for k := range c.servarr {
			names = append(names, k)
		}
		names = append(names, "all")
		tools = append(tools,
			toolDef{
				Name:        "servarr_status",
				Description: "Get health and queue status of a *arr service (Sonarr, Radarr, etc.).",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"service": {Type: "string", Description: "Service name or 'all'", Enum: names},
					},
					Required: []string{"service"},
				},
			},
		)
	}

	// ── step-ca ──────────────────────────────────────────────────────────────
	if c.ca != nil {
		tools = append(tools,
			toolDef{
				Name:        "ca_health",
				Description: "Check Smallstep step-ca health and status.",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── Matchbox PXE ─────────────────────────────────────────────────────────
	if c.matchbox != nil {
		tools = append(tools,
			toolDef{
				Name:        "matchbox_list_profiles",
				Description: "List Matchbox PXE profiles.",
				InputSchema: inputSchema{Type: "object"},
			},
			toolDef{
				Name:        "matchbox_list_groups",
				Description: "List Matchbox PXE groups (boot assignments).",
				InputSchema: inputSchema{Type: "object"},
			},
		)
	}

	// ── Home Assistant ───────────────────────────────────────────────────────
	if c.hass != nil {
		tools = append(tools,
			toolDef{
				Name:        "hass_list_entities",
				Description: "List Home Assistant entities, optionally filtered by domain (e.g. light, switch, sensor).",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"domain": {Type: "string", Description: "Entity domain filter (leave empty for all)"},
					},
				},
			},
			toolDef{
				Name:        "hass_get_state",
				Description: "Get the current state and attributes of a Home Assistant entity.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"entity_id": {Type: "string", Description: "Entity ID (e.g. light.living_room)"},
					},
					Required: []string{"entity_id"},
				},
			},
			toolDef{
				Name:        "hass_call_service",
				Description: "Call a Home Assistant service (e.g. turn a light on/off, run an automation). Pass extra service_data as a JSON object string for parameters like brightness, color_temp, transition, etc.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]schemaProp{
						"domain":       {Type: "string", Description: "Service domain (e.g. light, switch, automation)"},
						"service":      {Type: "string", Description: "Service name (e.g. turn_on, turn_off, toggle)"},
						"entity_id":    {Type: "string", Description: "Target entity ID (optional)"},
						"service_data": {Type: "string", Description: "Optional JSON object with extra service parameters (e.g. {\"brightness\": 128, \"color_temp\": 300})"},
					},
					Required: []string{"domain", "service"},
				},
			},
		)
	}

	return tools
}

// ── Tool execution ───────────────────────────────────────────────────────────

func callTool(name string, args map[string]any, c *mcpClients) (toolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch name {

	// ── Proxmox ──────────────────────────────────────────────────────────────

	case "proxmox_list_nodes":
		nodes, err := c.proxmox.Nodes(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-20s %-8s  %6s  %s\n", "NODE", "STATUS", "CPU%", "MEMORY"))
		for _, n := range nodes {
			memPct := 0.0
			if n.MaxMem > 0 {
				memPct = float64(n.Mem) / float64(n.MaxMem) * 100
			}
			sb.WriteString(fmt.Sprintf("%-20s %-8s  %5.1f%%  %.1f%%\n",
				n.Name, n.Status, n.CPU*100, memPct))
		}
		return text(sb.String()), nil

	case "proxmox_list_guests":
		guests, err := c.proxmox.Guests(ctx, str(args, "node"))
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-6s %-25s %-5s %-10s\n", "VMID", "NAME", "TYPE", "STATUS"))
		for _, g := range guests {
			sb.WriteString(fmt.Sprintf("%-6d %-25s %-5s %-10s\n", g.VMID, g.Name, g.Type, g.Status))
		}
		return text(sb.String()), nil

	case "proxmox_guest_status":
		guest, err := c.proxmox.GuestStatus(ctx, str(args, "node"), str(args, "type"), intArg(args, "vmid"))
		if err != nil {
			return text(""), err
		}
		out := fmt.Sprintf("Name: %s\nVMID: %d\nType: %s\nStatus: %s\nNode: %s\nUptime: %ds\nCPU: %.2f\nvCPUs: %d\nMem: %d/%d\nDisk: %d/%d\n",
			guest.Name, guest.VMID, guest.Type, guest.Status, guest.Node,
			guest.Uptime, guest.CPU, guest.CPUs, guest.Mem, guest.MaxMem,
			guest.Disk, guest.MaxDisk)
		return text(out), nil

	case "proxmox_guest_action":
		g := api.Guest{Node: str(args, "node"), Type: str(args, "type"), VMID: intArg(args, "vmid")}
		if err := c.proxmox.GuestAction(ctx, g, str(args, "action")); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Action %q sent to %s/%d.", str(args, "action"), str(args, "node"), intArg(args, "vmid"))), nil

	case "proxmox_next_vmid":
		id, err := c.proxmox.NextVMID(ctx)
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("%d", id)), nil

	case "proxmox_clone_vm":
		node := str(args, "node")
		templateID := intArg(args, "template_id")
		name := str(args, "name")
		newVMID := intArg(args, "new_vmid")
		if newVMID == 0 {
			id, err := c.proxmox.NextVMID(ctx)
			if err != nil {
				return text(""), fmt.Errorf("nextid: %w", err)
			}
			newVMID = id
		}
		storage := str(args, "storage")
		taskID, err := c.proxmox.CloneVM(ctx, api.CloneOptions{
			SourceNode: node, SourceVMID: templateID,
			NewVMID: newVMID, Name: name, TargetNode: node,
			Storage: storage, Full: storage != "",
		})
		if err != nil {
			return text(""), fmt.Errorf("clone: %w", err)
		}
		ciUser := str(args, "ci_user")
		ciPass := str(args, "ci_password")
		ciSSH := str(args, "ci_ssh_keys")
		ciIP := str(args, "ci_ip_config")
		if ciUser != "" || ciPass != "" || ciSSH != "" || ciIP != "" {
			// The VM stays locked until the clone task finishes; config
			// changes are rejected while the lock is held.
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer waitCancel()
			if exit, err := c.proxmox.WaitForTask(waitCtx, node, taskID); err != nil {
				return text(fmt.Sprintf("VM %d cloned (task %s) but waiting for clone failed: %v", newVMID, taskID, err)), nil
			} else if exit != "OK" {
				return text(fmt.Sprintf("VM %d clone task ended with status %q; cloud-init not applied", newVMID, exit)), nil
			}
			if err := c.proxmox.SetCloudInit(waitCtx, node, newVMID, api.CloudInitOptions{
				User: ciUser, Password: ciPass, SSHKeys: ciSSH, IPConfig: ciIP,
			}); err != nil {
				return text(fmt.Sprintf("VM %d cloned (task %s) but cloud-init failed: %v", newVMID, taskID, err)), nil
			}
		}
		return text(fmt.Sprintf("VM %d cloned. Task: %s", newVMID, taskID)), nil

	case "proxmox_node_version":
		v, err := c.proxmox.NodeVersion(ctx, str(args, "node"))
		if err != nil {
			return text(""), err
		}
		return text(v), nil

	case "proxmox_list_snapshots":
		snaps, err := c.proxmox.Snapshots(ctx, str(args, "node"), str(args, "type"), intArg(args, "vmid"))
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-20s %-12s  %s\n", "NAME", "DATE", "DESCRIPTION"))
		for _, s := range snaps {
			t := ""
			if s.Snaptime > 0 {
				t = time.Unix(s.Snaptime, 0).Format("2006-01-02 15:04")
			}
			sb.WriteString(fmt.Sprintf("%-20s %-12s  %s\n", s.Name, t, s.Description))
		}
		return text(sb.String()), nil

	case "proxmox_create_snapshot":
		upid, err := c.proxmox.CreateSnapshot(ctx,
			str(args, "node"), str(args, "type"), intArg(args, "vmid"),
			str(args, "name"), str(args, "desc"))
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Snapshot creation started. UPID: %s", upid)), nil

	case "proxmox_delete_snapshot":
		upid, err := c.proxmox.DeleteSnapshot(ctx,
			str(args, "node"), str(args, "type"), intArg(args, "vmid"),
			str(args, "snapname"))
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Snapshot deletion started. UPID: %s", upid)), nil

	// ── Salt ─────────────────────────────────────────────────────────────────

	case "salt_list_minions":
		if c.salt == nil {
			return text("Salt not configured."), nil
		}
		minions, err := c.salt.Minions(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-30s %-8s  %s\n", "MINION", "STATUS", "OS"))
		for _, m := range minions {
			status := "offline"
			if m.Online {
				status = "online"
			}
			sb.WriteString(fmt.Sprintf("%-30s %-8s  %s\n", m.ID, status, m.Grains.OSFullName))
		}
		return text(sb.String()), nil

	case "salt_apply_highstate":
		if c.salt == nil {
			return text("Salt not configured."), nil
		}
		jid, err := c.salt.ApplyState(ctx, str(args, "target"), "highstate")
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Highstate dispatched. JID: %s", jid)), nil

	case "salt_apply_state":
		if c.salt == nil {
			return text("Salt not configured."), nil
		}
		jid, err := c.salt.ApplyState(ctx, str(args, "target"), str(args, "state"))
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("State %q dispatched. JID: %s", str(args, "state"), jid)), nil

	case "salt_job_result":
		if c.salt == nil {
			return text("Salt not configured."), nil
		}
		done, success, failed, err := c.salt.JobResult(ctx, str(args, "jid"))
		if err != nil {
			return text(""), err
		}
		if !done {
			return text("Job still running."), nil
		}
		if success {
			return text("Job completed successfully."), nil
		}
		return text(fmt.Sprintf("Job failed. Failed states: %s", strings.Join(failed, ", "))), nil

	case "salt_journal_logs":
		if c.salt == nil {
			return text("Salt not configured."), nil
		}
		lines := intArg(args, "lines")
		if lines == 0 {
			lines = 50
		}
		logs, err := c.salt.JournalLogs(ctx, str(args, "target"), str(args, "unit"), lines)
		if err != nil {
			return text(""), err
		}
		return text(strings.Join(logs, "\n")), nil

	// ── Nomad ────────────────────────────────────────────────────────────────

	case "nomad_list_jobs":
		if c.nomad == nil {
			return text("Nomad not configured."), nil
		}
		jobs, err := c.nomad.Jobs(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-30s %-10s %-10s  %s\n", "JOB", "TYPE", "STATUS", "RUNNING/FAILED"))
		for _, j := range jobs {
			r, f, _ := j.Totals()
			sb.WriteString(fmt.Sprintf("%-30s %-10s %-10s  %d/%d\n", j.ID, j.Type, j.Status, r, f))
		}
		return text(sb.String()), nil

	case "nomad_job_allocations":
		if c.nomad == nil {
			return text("Nomad not configured."), nil
		}
		allocs, err := c.nomad.JobAllocations(ctx, str(args, "job_id"))
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-10s %-20s %-10s  %s\n", "ID", "TASK GROUP", "STATUS", "NODE"))
		for _, a := range allocs {
			id := a.ID
			if len(id) > 8 {
				id = id[:8]
			}
			sb.WriteString(fmt.Sprintf("%-10s %-20s %-10s  %s\n",
				id, a.TaskGroup, a.ClientStatus, a.NodeName))
		}
		return text(sb.String()), nil

	case "nomad_stop_job":
		if c.nomad == nil {
			return text("Nomad not configured."), nil
		}
		if err := c.nomad.StopJob(ctx, str(args, "job_id")); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Job %q stopped.", str(args, "job_id"))), nil

	// ── Prometheus ───────────────────────────────────────────────────────────

	case "prom_query":
		if c.prom == nil {
			return text("Prometheus not configured."), nil
		}
		samples, err := c.prom.Query(ctx, str(args, "expr"))
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		for _, s := range samples {
			sb.WriteString(fmt.Sprintf("%s = %g\n", s.LabelString(), s.Value))
		}
		if sb.Len() == 0 {
			sb.WriteString("No results.")
		}
		return text(sb.String()), nil

	case "prom_alerts":
		if c.prom == nil {
			return text("Prometheus not configured."), nil
		}
		rules, err := c.prom.AlertRules(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		firing := 0
		for _, r := range rules {
			if r.State == "firing" || r.State == "pending" {
				sb.WriteString(fmt.Sprintf("[%s] %s — %s\n", strings.ToUpper(r.State), r.Name, r.Summary))
				firing++
			}
		}
		if firing == 0 {
			sb.WriteString("No alerts firing.")
		}
		return text(sb.String()), nil

	// ── Loki ─────────────────────────────────────────────────────────────────

	case "loki_query":
		if c.loki == nil {
			return text("Loki not configured."), nil
		}
		hours := intArg(args, "hours")
		if hours == 0 {
			hours = 1
		}
		limit := intArg(args, "limit")
		if limit == 0 {
			limit = 100
		}
		end := time.Now()
		start := end.Add(-time.Duration(hours) * time.Hour)
		entries, err := c.loki.QueryRange(ctx, str(args, "expr"), start, end, limit)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("%s  %s\n", e.Timestamp.Format("15:04:05"), e.Line))
		}
		if sb.Len() == 0 {
			sb.WriteString("No log entries found.")
		}
		return text(sb.String()), nil

	// ── TrueNAS ──────────────────────────────────────────────────────────────

	case "truenas_status":
		if c.truenas == nil {
			return text("TrueNAS not configured."), nil
		}
		status, err := c.truenas.FetchAll(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Host: %s  Version: %s  Ready: %v\n\n",
			status.SysInfo.Hostname, status.SysInfo.Version, status.SysInfo.SystemReady))

		sb.WriteString("POOLS:\n")
		for _, p := range status.Pools {
			sb.WriteString(fmt.Sprintf("  %-20s  %-8s  healthy=%v\n", p.Name, p.Status, p.Healthy))
		}

		if len(status.Alerts) > 0 {
			sb.WriteString("\nACTIVE ALERTS:\n")
			for _, a := range status.Alerts {
				sb.WriteString(fmt.Sprintf("  [%s] %s\n", a.Level, a.Formatted))
			}
		}

		running := []string{}
		stopped := []string{}
		for _, svc := range status.Services {
			if svc.Enable {
				if svc.State == "RUNNING" {
					running = append(running, svc.Service)
				} else {
					stopped = append(stopped, svc.Service)
				}
			}
		}
		if len(running) > 0 {
			sb.WriteString("\nRunning services: " + strings.Join(running, ", ") + "\n")
		}
		if len(stopped) > 0 {
			sb.WriteString("Stopped services: " + strings.Join(stopped, ", ") + "\n")
		}
		return text(sb.String()), nil

	// ── CrowdSec ─────────────────────────────────────────────────────────────

	case "crowdsec_decisions":
		if c.crowdsec == nil {
			return text("CrowdSec not configured."), nil
		}
		decisions, err := c.crowdsec.Decisions(ctx)
		if err != nil {
			return text(""), err
		}
		if len(decisions) == 0 {
			return text("No active decisions."), nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-6s %-10s %-10s %-20s  %s\n", "ID", "TYPE", "SCOPE", "VALUE", "DURATION"))
		for _, d := range decisions {
			sb.WriteString(fmt.Sprintf("%-6d %-10s %-10s %-20s  %s\n",
				d.ID, d.Type, d.Scope, d.Value, d.Duration))
		}
		return text(sb.String()), nil

	case "crowdsec_unban":
		if c.crowdsec == nil {
			return text("CrowdSec not configured."), nil
		}
		if err := c.crowdsec.DeleteDecision(ctx, intArg(args, "id")); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Decision %d deleted.", intArg(args, "id"))), nil

	// ── Wazuh ────────────────────────────────────────────────────────────────

	case "wazuh_agents":
		if c.wazuh == nil {
			return text("Wazuh not configured."), nil
		}
		agents, err := c.wazuh.Agents(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-6s %-20s %-15s %-12s  %s\n", "ID", "NAME", "IP", "STATUS", "OS"))
		for _, a := range agents {
			sb.WriteString(fmt.Sprintf("%-6s %-20s %-15s %-12s  %s %s\n",
				a.ID, a.Name, a.IP, a.Status, a.OS.Platform, a.OS.Version))
		}
		return text(sb.String()), nil

	case "wazuh_alerts":
		if c.wazuh == nil {
			return text("Wazuh not configured."), nil
		}
		alerts, err := c.wazuh.RecentAlerts(ctx)
		if err != nil {
			return text(""), err
		}
		if len(alerts) == 0 {
			return text("No recent alerts."), nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-6s %-12s %-20s  %s\n", "LEVEL", "AGENT", "RULE", "DESCRIPTION"))
		for _, a := range alerts {
			sb.WriteString(fmt.Sprintf("%-6d %-12s %-20s  %s\n",
				a.Rule.Level, a.AgentName, fmt.Sprintf("id=%d", a.Rule.ID), a.Rule.Description))
		}
		return text(sb.String()), nil

	// ── DNS ──────────────────────────────────────────────────────────────────

	case "dns_list_zones":
		if c.dns == nil {
			return text("DNS not configured."), nil
		}
		zones, err := c.dns.Zones(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		for _, z := range zones {
			disabled := ""
			if z.Disabled {
				disabled = " [disabled]"
			}
			sb.WriteString(z.Name + disabled + "\n")
		}
		return text(sb.String()), nil

	case "dns_list_records":
		if c.dns == nil {
			return text("DNS not configured."), nil
		}
		records, err := c.dns.Records(ctx, str(args, "zone"), "")
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-36s %-8s %-6s  %s\n", "NAME", "TYPE", "TTL", "VALUE"))
		for _, r := range records {
			value := dnsRecordValue(r)
			sb.WriteString(fmt.Sprintf("%-36s %-8s %-6d  %s\n", r.Name, r.Type, r.TTL, value))
		}
		return text(sb.String()), nil

	case "dns_add_record":
		if c.dns == nil {
			return text("DNS not configured."), nil
		}
		ttl := intArg(args, "ttl")
		if ttl == 0 {
			ttl = 300
		}
		recType := strings.ToUpper(str(args, "type"))
		value := str(args, "value")
		opts := api.DNSAddOptions{
			Zone:   str(args, "zone"),
			Domain: str(args, "name"),
			Type:   recType,
			TTL:    ttl,
		}
		switch recType {
		case "A", "AAAA":
			opts.IPAddress = value
		case "CNAME":
			opts.CName = value
		case "TXT":
			opts.Text = value
		case "MX":
			opts.Exchange = value
			opts.Preference = 10
		}
		if err := c.dns.AddRecord(ctx, opts); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Added %s record %s → %s in %s.", recType, str(args, "name"), value, str(args, "zone"))), nil

	case "dns_delete_record":
		if c.dns == nil {
			return text("DNS not configured."), nil
		}
		if err := c.dns.DeleteRecord(ctx, str(args, "zone"), str(args, "name"), str(args, "type"), str(args, "value")); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Deleted %s record %s from %s.", str(args, "type"), str(args, "name"), str(args, "zone"))), nil

	// ── Plex ─────────────────────────────────────────────────────────────────

	case "plex_status":
		if c.plex == nil {
			return text("Plex not configured."), nil
		}
		status, err := c.plex.FetchAll(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Server: %s  v%s  (%s)\n", status.Info.FriendlyName, status.Info.Version, status.Info.Platform))
		sb.WriteString(fmt.Sprintf("Sessions: %d active\n", len(status.Sessions)))
		for _, s := range status.Sessions {
			title := s.Title
			if s.GrandparentTitle != "" {
				title = s.GrandparentTitle + " — " + s.Title
			}
			sb.WriteString(fmt.Sprintf("  [%s] %s — %s on %s\n", s.Type, title, s.User.Title, s.Player.Device))
		}
		if len(status.Libraries) > 0 {
			sb.WriteString(fmt.Sprintf("\nLibraries (%d):\n", len(status.Libraries)))
			for _, l := range status.Libraries {
				sb.WriteString(fmt.Sprintf("  %-20s  %s  (%d items)\n", l.Title, l.Type, l.Count))
			}
		}
		return text(sb.String()), nil

	// ── Servarr ──────────────────────────────────────────────────────────────

	case "servarr_status":
		if len(c.servarr) == 0 {
			return text("Servarr not configured."), nil
		}
		svcName := str(args, "service")

		type servarrResult struct {
			name   string
			status api.ServarrStatus
		}

		fetch := func(targets map[string]*api.ServarrClient) []servarrResult {
			ch := make(chan servarrResult, len(targets))
			for name, client := range targets {
				name, client := name, client
				go func() {
					sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					ch <- servarrResult{name: name, status: client.FetchStatus(sctx)}
				}()
			}
			results := make([]servarrResult, 0, len(targets))
			for range targets {
				results = append(results, <-ch)
			}
			return results
		}

		var targets map[string]*api.ServarrClient
		if svcName == "all" {
			targets = c.servarr
		} else if client, ok := c.servarr[svcName]; ok {
			targets = map[string]*api.ServarrClient{svcName: client}
		} else {
			return text(fmt.Sprintf("Unknown service %q. Available: %s", svcName, servarrNames(c.servarr))), nil
		}

		var sb strings.Builder
		for _, r := range fetch(targets) {
			health := "ok"
			if len(r.status.Health) > 0 {
				var issues []string
				for _, h := range r.status.Health {
					issues = append(issues, h.Message)
				}
				health = strings.Join(issues, "; ")
			}
			version := ""
			if r.status.Status != nil {
				version = r.status.Status.Version
			}
			sb.WriteString(fmt.Sprintf("%-12s  version=%-12s  online=%-5v  health=%s  queue=%d\n",
				r.name, version, r.status.Online, health, len(r.status.Queue)))
		}
		return text(sb.String()), nil

	// ── step-ca ──────────────────────────────────────────────────────────────

	case "ca_health":
		if c.ca == nil {
			return text("step-ca not configured."), nil
		}
		h, err := c.ca.Health(ctx)
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("step-ca status: %s\n", h.Status)), nil

	// ── Matchbox ─────────────────────────────────────────────────────────────

	case "matchbox_list_profiles":
		if c.matchbox == nil {
			return text("Matchbox not configured."), nil
		}
		profiles, err := c.matchbox.Profiles(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		for _, p := range profiles {
			sb.WriteString(fmt.Sprintf("%-20s  %s\n", p.ID, p.Name))
		}
		return text(sb.String()), nil

	case "matchbox_list_groups":
		if c.matchbox == nil {
			return text("Matchbox not configured."), nil
		}
		groups, err := c.matchbox.Groups(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		for _, g := range groups {
			sb.WriteString(fmt.Sprintf("%-20s  profile=%-20s  %s\n", g.ID, g.Profile, g.Name))
		}
		return text(sb.String()), nil

	// ── Home Assistant ───────────────────────────────────────────────────────

	case "hass_list_entities":
		if c.hass == nil {
			return text("Home Assistant not configured."), nil
		}
		domain := str(args, "domain")
		entities, err := c.hass.States(ctx)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-40s %-15s  %s\n", "ENTITY", "DOMAIN", "STATE"))
		for _, e := range entities {
			d := e.EntityID
			if idx := strings.Index(e.EntityID, "."); idx >= 0 {
				d = e.EntityID[:idx]
			}
			if domain != "" && d != domain {
				continue
			}
			sb.WriteString(fmt.Sprintf("%-40s %-15s  %s\n", e.EntityID, d, e.State))
		}
		return text(sb.String()), nil

	case "hass_get_state":
		if c.hass == nil {
			return text("Home Assistant not configured."), nil
		}
		e, err := c.hass.GetState(ctx, str(args, "entity_id"))
		if err != nil {
			return text(""), err
		}
		attrsJSON, _ := json.MarshalIndent(e.Attributes, "", "  ")
		out := fmt.Sprintf("entity_id:    %s\nstate:        %s\nlast_changed: %s\nattributes:\n%s\n",
			e.EntityID, e.State, e.LastChanged.Format(time.RFC3339), string(attrsJSON))
		return text(out), nil

	case "hass_call_service":
		if c.hass == nil {
			return text("Home Assistant not configured."), nil
		}
		domain := str(args, "domain")
		service := str(args, "service")
		entityID := str(args, "entity_id")
		var extraData map[string]any
		if raw := str(args, "service_data"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &extraData); err != nil {
				return text(""), fmt.Errorf("service_data must be a valid JSON object: %w", err)
			}
		}
		if err := c.hass.CallService(ctx, domain, service, entityID, extraData); err != nil {
			return text(""), err
		}
		msg := fmt.Sprintf("Called %s.%s", domain, service)
		if entityID != "" {
			msg += " on " + entityID
		}
		return text(msg + "."), nil

	default:
		return text(""), fmt.Errorf("unknown tool: %s", name)
	}
}

// ── Helpers for tool execution ───────────────────────────────────────────────

func dnsRecordValue(rec api.DNSRecord) string {
	switch strings.ToUpper(rec.Type) {
	case "A", "AAAA":
		return rec.RData.IPAddress
	case "CNAME":
		return rec.RData.CName
	case "TXT":
		return rec.RData.Text
	case "MX":
		return fmt.Sprintf("%d %s", rec.RData.Preference, rec.RData.Exchange)
	default:
		return ""
	}
}

func servarrNames(m map[string]*api.ServarrClient) string {
	var names []string
	for k := range m {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[ratataskr-mcp] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Overlay keyring credentials
	overlayKeyring(cfg)

	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}

	c := &mcpClients{}

	c.proxmox = api.NewClient(cfg)

	if cfg.Salt.URL != "" {
		if sc, err := api.NewSaltClient(cfg.Salt); err == nil {
			c.salt = sc
		} else {
			log.Printf("salt unavailable: %v", err)
		}
	}

	if cfg.Nomad.URL != "" {
		c.nomad = api.NewNomadClient(cfg.Nomad)
	}

	if cfg.Prometheus.URL != "" {
		c.prom = api.NewPrometheusClient(cfg.Prometheus)
	}

	if cfg.Loki.URL != "" {
		c.loki = api.NewLokiClient(cfg.Loki.URL, cfg.Loki.Token, cfg.Loki.Insecure)
	}

	if cfg.TrueNAS.URL != "" {
		c.truenas = api.NewTrueNASClient(cfg.TrueNAS.URL, cfg.TrueNAS.APIKey, cfg.TrueNAS.Insecure)
	}

	if cfg.CrowdSec.URL != "" {
		c.crowdsec = api.NewCrowdSecClient(cfg.CrowdSec)
	}

	if cfg.Wazuh.URL != "" {
		c.wazuh = api.NewWazuhClient(cfg.Wazuh)
	}

	if cfg.Technitium.URL != "" {
		c.dns = api.NewTechnitiumClient(cfg.Technitium)
	}

	if cfg.Plex.URL != "" {
		c.plex = api.NewPlexClient(cfg.Plex.URL, cfg.Plex.Token, cfg.Plex.Insecure)
	}

	c.servarr = buildServarrClients(cfg)

	if cfg.StepCA.URL != "" {
		c.ca = api.NewStepCAClient(cfg.StepCA)
	}

	if cfg.Matchbox.URL != "" {
		c.matchbox = api.NewMatchboxClient(cfg.Matchbox)
	}

	if cfg.HomeAssistant.URL != "" {
		c.hass = api.NewHAClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, cfg.HomeAssistant.Insecure)
	}

	log.Printf("started — salt=%v nomad=%v prom=%v loki=%v truenas=%v crowdsec=%v wazuh=%v dns=%v plex=%v servarr=%d ca=%v matchbox=%v hass=%v",
		c.salt != nil, c.nomad != nil, c.prom != nil, c.loki != nil,
		c.truenas != nil, c.crowdsec != nil, c.wazuh != nil, c.dns != nil,
		c.plex != nil, len(c.servarr), c.ca != nil, c.matchbox != nil, c.hass != nil)

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("parse error: %v", err)
			continue
		}

		var resp *rpcResponse

		switch req.Method {
		case "initialize":
			resp = ok(req.ID, initResult{
				ProtocolVersion: "2024-11-05",
				Capabilities:    serverCaps{Tools: map[string]any{}},
				ServerInfo:      serverInfo{Name: "ratataskr", Version: "1.0.0"},
			})

		case "notifications/initialized":
			// no response

		case "tools/list":
			resp = ok(req.ID, map[string]any{"tools": allTools(c)})

		case "tools/call":
			var p toolCallParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp = errResp(req.ID, -32602, "invalid params: "+err.Error())
				break
			}
			result, err := callTool(p.Name, p.Arguments, c)
			if err != nil {
				result = toolResult{
					Content: []contentBlock{{Type: "text", Text: "error: " + err.Error()}},
					IsError: true,
				}
			}
			resp = ok(req.ID, result)

		default:
			if req.ID != nil {
				resp = errResp(req.ID, -32601, "method not found: "+req.Method)
			}
		}

		if resp != nil {
			if err := enc.Encode(resp); err != nil {
				log.Printf("encode error: %v", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("stdin: %v", err)
	}
}

func overlayKeyring(cfg *config.Config) {
	gkr.Overlay(&cfg.Secret, gkr.KeyProxmoxSecret)
	gkr.Overlay(&cfg.Salt.Password, gkr.KeySaltPassword)
	gkr.Overlay(&cfg.Nomad.Token, gkr.KeyNomadToken)
	gkr.Overlay(&cfg.Wazuh.Password, gkr.KeyWazuhPassword)
	gkr.Overlay(&cfg.CrowdSec.APIKey, gkr.KeyCrowdSecAPIKey)
	gkr.Overlay(&cfg.Matchbox.Token, gkr.KeyMatchboxToken)
	gkr.Overlay(&cfg.Technitium.Token, gkr.KeyTechnitiumToken)
	gkr.Overlay(&cfg.Prometheus.Token, gkr.KeyPrometheusToken)
	gkr.Overlay(&cfg.TrueNAS.APIKey, gkr.KeyTrueNASAPIKey)
	gkr.Overlay(&cfg.Plex.Token, gkr.KeyPlexToken)
	gkr.Overlay(&cfg.HomeAssistant.Token, gkr.KeyHAToken)
	gkr.Overlay(&cfg.Loki.Token, gkr.KeyLokiToken)
}

func buildServarrClients(cfg *config.Config) map[string]*api.ServarrClient {
	out := make(map[string]*api.ServarrClient)
	add := func(name string, appCfg config.ServarrAppConfig) {
		if appCfg.URL != "" {
			out[name] = api.NewServarrClient(appCfg.URL, appCfg.APIKey, name, appCfg.Insecure)
		}
	}
	add("sonarr", cfg.Servarr.Sonarr)
	add("radarr", cfg.Servarr.Radarr)
	add("lidarr", cfg.Servarr.Lidarr)
	add("readarr", cfg.Servarr.Readarr)
	add("prowlarr", cfg.Servarr.Prowlarr)
	add("bazarr", cfg.Servarr.Bazarr)
	add("overseerr", cfg.Servarr.Overseerr)
	add("tautulli", cfg.Servarr.Tautulli)
	return out
}
