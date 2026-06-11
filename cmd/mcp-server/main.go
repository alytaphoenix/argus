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
	Type       string               `json:"type"`
	Properties map[string]schemaProp `json:"properties,omitempty"`
	Required   []string             `json:"required,omitempty"`
}

type schemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
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

// ── Tool catalogue ───────────────────────────────────────────────────────────

func allTools(hasSalt, hasNomad bool) []toolDef {
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
				Type: "object",
				Properties: map[string]schemaProp{
					"node": {Type: "string", Description: "Proxmox node name"},
				},
				Required: []string{"node"},
			},
		},
		{
			Name:        "proxmox_guest_status",
			Description: "Get detailed status of a single VM or container.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"node":  {Type: "string", Description: "Proxmox node name"},
					"type":  {Type: "string", Description: "Guest type", Enum: []string{"qemu", "lxc"}},
					"vmid":  {Type: "integer", Description: "VM/CT ID"},
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
	}

	if hasSalt {
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
		)
	}

	if hasNomad {
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

	return tools
}

// ── Tool execution ───────────────────────────────────────────────────────────

func callTool(name string, args map[string]any, proxmox *api.Client, salt *api.SaltClient, nomad *api.NomadClient) (toolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch name {
	case "proxmox_list_nodes":
		nodes, err := proxmox.Nodes(ctx)
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
		node := str(args, "node")
		guests, err := proxmox.Guests(ctx, node)
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
		node := str(args, "node")
		gtype := str(args, "type")
		vmid := intArg(args, "vmid")
		guest, err := proxmox.GuestStatus(ctx, node, gtype, vmid)
		if err != nil {
			return text(""), err
		}
		out := fmt.Sprintf("Name: %s\nVMID: %d\nType: %s\nStatus: %s\nNode: %s\nUptime: %ds\nCPU: %.2f\nvCPUs: %d\nMem: %d/%d\nDisk: %d/%d\n",
			guest.Name, guest.VMID, guest.Type, guest.Status, guest.Node,
			guest.Uptime, guest.CPU, guest.CPUs, guest.Mem, guest.MaxMem,
			guest.Disk, guest.MaxDisk)
		return text(out), nil

	case "proxmox_guest_action":
		node := str(args, "node")
		gtype := str(args, "type")
		vmid := intArg(args, "vmid")
		action := str(args, "action")
		g := api.Guest{Node: node, Type: gtype, VMID: vmid}
		if err := proxmox.GuestAction(ctx, g, action); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Action %q sent to %s/%d.", action, node, vmid)), nil

	case "proxmox_next_vmid":
		id, err := proxmox.NextVMID(ctx)
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("%d", id)), nil

	case "proxmox_clone_vm":
		node := str(args, "node")
		templateID := intArg(args, "template_id")
		name := str(args, "name")
		newVMID := intArg(args, "new_vmid")
		storage := str(args, "storage")

		if newVMID == 0 {
			id, err := proxmox.NextVMID(ctx)
			if err != nil {
				return text(""), fmt.Errorf("nextid: %w", err)
			}
			newVMID = id
		}

		taskID, err := proxmox.CloneVM(ctx, api.CloneOptions{
			SourceNode: node,
			SourceVMID: templateID,
			NewVMID:    newVMID,
			Name:       name,
			TargetNode: node,
			Storage:    storage,
			Full:       storage != "",
		})
		if err != nil {
			return text(""), fmt.Errorf("clone: %w", err)
		}

		ciUser := str(args, "ci_user")
		ciPass := str(args, "ci_password")
		ciSSH := str(args, "ci_ssh_keys")
		ciIP := str(args, "ci_ip_config")
		if ciUser != "" || ciPass != "" || ciSSH != "" || ciIP != "" {
			if err := proxmox.SetCloudInit(ctx, node, newVMID, api.CloudInitOptions{
				User:     ciUser,
				Password: ciPass,
				SSHKeys:  ciSSH,
				IPConfig: ciIP,
			}); err != nil {
				return text(fmt.Sprintf("VM %d cloned (task %s) but cloud-init failed: %v", newVMID, taskID, err)), nil
			}
		}
		return text(fmt.Sprintf("VM %d cloned successfully. Task: %s", newVMID, taskID)), nil

	case "salt_list_minions":
		if salt == nil {
			return text("Salt not configured."), nil
		}
		minions, err := salt.Minions(ctx)
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
		if salt == nil {
			return text("Salt not configured."), nil
		}
		target := str(args, "target")
		jid, err := salt.ApplyState(ctx, target, "highstate")
		if err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Highstate dispatched. JID: %s", jid)), nil

	case "nomad_list_jobs":
		if nomad == nil {
			return text("Nomad not configured."), nil
		}
		jobs, err := nomad.Jobs(ctx)
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
		if nomad == nil {
			return text("Nomad not configured."), nil
		}
		jobID := str(args, "job_id")
		allocs, err := nomad.JobAllocations(ctx, jobID)
		if err != nil {
			return text(""), err
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%-10s %-20s %-10s  %s\n", "ID", "TASK GROUP", "STATUS", "NODE"))
		for _, a := range allocs {
			sb.WriteString(fmt.Sprintf("%-10s %-20s %-10s  %s\n",
				a.ID[:8], a.TaskGroup, a.ClientStatus, a.NodeName))
		}
		return text(sb.String()), nil

	case "nomad_stop_job":
		if nomad == nil {
			return text("Nomad not configured."), nil
		}
		jobID := str(args, "job_id")
		if err := nomad.StopJob(ctx, jobID); err != nil {
			return text(""), err
		}
		return text(fmt.Sprintf("Job %q stopped.", jobID)), nil

	default:
		return text(""), fmt.Errorf("unknown tool: %s", name)
	}
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("[ratataskr-mcp] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Overlay keyring values
	if cfg.Secret == "" {
		if v, e := gkr.Load(gkr.KeyProxmoxSecret); e == nil {
			cfg.Secret = v
		}
	}
	if cfg.Salt.Password == "" {
		if v, e := gkr.Load(gkr.KeySaltPassword); e == nil {
			cfg.Salt.Password = v
		}
	}
	if cfg.Nomad.Token == "" {
		if v, e := gkr.Load(gkr.KeyNomadToken); e == nil {
			cfg.Nomad.Token = v
		}
	}

	proxmox := api.NewClient(cfg)

	var salt *api.SaltClient
	if cfg.Salt.URL != "" {
		if sc, err := api.NewSaltClient(cfg.Salt); err == nil {
			salt = sc
		} else {
			log.Printf("salt unavailable: %v", err)
		}
	}

	var nomad *api.NomadClient
	if cfg.Nomad.URL != "" {
		nomad = api.NewNomadClient(cfg.Nomad)
	}

	log.Printf("started (salt=%v, nomad=%v)", salt != nil, nomad != nil)

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
			// notification — no response

		case "tools/list":
			resp = ok(req.ID, map[string]any{"tools": allTools(salt != nil, nomad != nil)})

		case "tools/call":
			var p toolCallParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp = errResp(req.ID, -32602, "invalid params: "+err.Error())
				break
			}
			result, err := callTool(p.Name, p.Arguments, proxmox, salt, nomad)
			if err != nil {
				result = text("error: " + err.Error())
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
