package api

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"
)

type Node struct {
	Name    string  `json:"node"`
	Status  string  `json:"status"`
	CPU     float64 `json:"cpu"`
	MaxCPU  int     `json:"maxcpu"`
	Mem     int64   `json:"mem"`
	MaxMem  int64   `json:"maxmem"`
	Disk    int64   `json:"disk"`
	MaxDisk int64   `json:"maxdisk"`
	Uptime  int64   `json:"uptime"`
}

type Guest struct {
	VMID    int     `json:"vmid"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	CPU     float64 `json:"cpu"`
	CPUs    int     `json:"cpus"`
	Mem     int64   `json:"mem"`
	MaxMem  int64   `json:"maxmem"`
	Disk    int64   `json:"disk"`
	MaxDisk int64   `json:"maxdisk"`
	Uptime  int64   `json:"uptime"`
	NetIn   int64   `json:"netin"`
	NetOut  int64   `json:"netout"`
	Type    string  // "qemu" or "lxc", set client-side
	Node    string  // node name, set client-side
}

func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var nodes []Node
	if err := c.get(ctx, "/nodes", &nodes); err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
	return nodes, nil
}

func (c *Client) Guests(ctx context.Context, node string) ([]Guest, error) {
	var vms, lxcs []Guest

	if err := c.get(ctx, fmt.Sprintf("/nodes/%s/qemu", node), &vms); err != nil {
		return nil, fmt.Errorf("qemu: %w", err)
	}
	if err := c.get(ctx, fmt.Sprintf("/nodes/%s/lxc", node), &lxcs); err != nil {
		return nil, fmt.Errorf("lxc: %w", err)
	}

	for i := range vms {
		vms[i].Type = "qemu"
		vms[i].Node = node
	}
	for i := range lxcs {
		lxcs[i].Type = "lxc"
		lxcs[i].Node = node
	}

	all := append(vms, lxcs...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].VMID < all[j].VMID
	})
	return all, nil
}

func (c *Client) GuestAction(ctx context.Context, g Guest, action string) error {
	path := fmt.Sprintf("/nodes/%s/%s/%d/status/%s", g.Node, g.Type, g.VMID, action)
	return c.post(ctx, path)
}

func (c *Client) GuestStatus(ctx context.Context, node, guestType string, vmid int) (*Guest, error) {
	var g Guest
	path := fmt.Sprintf("/nodes/%s/%s/%d/status/current", node, guestType, vmid)
	if err := c.get(ctx, path, &g); err != nil {
		return nil, err
	}
	g.Type = guestType
	g.Node = node
	g.VMID = vmid
	return &g, nil
}

// ClusterNode is one member of the Proxmox cluster.
type ClusterNode struct {
	Name   string `json:"name"`
	Online int    `json:"online"` // 1 = online
	NodeID int    `json:"nodeid"`
	IP     string `json:"ip"`
	Level  string `json:"level"` // "master", "node", ""
}

// ClusterStatus returns the current cluster members and quorum state.
func (c *Client) ClusterStatus(ctx context.Context) ([]ClusterNode, error) {
	var raw []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Online int    `json:"online"`
		NodeID int    `json:"nodeid"`
		IP     string `json:"ip"`
		Level  string `json:"level"`
	}
	if err := c.get(ctx, "/cluster/status", &raw); err != nil {
		return nil, err
	}
	var nodes []ClusterNode
	for _, r := range raw {
		if r.Type == "node" {
			nodes = append(nodes, ClusterNode{
				Name:   r.Name,
				Online: r.Online,
				NodeID: r.NodeID,
				IP:     r.IP,
				Level:  r.Level,
			})
		}
	}
	return nodes, nil
}

// VMFirewallEnabled returns whether the VM-level Proxmox firewall is enabled.
func (c *Client) VMFirewallEnabled(ctx context.Context, node, vmType string, vmid int) (bool, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/firewall/options", node, vmType, vmid)
	var out struct {
		Enable int `json:"enable"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return false, err
	}
	return out.Enable == 1, nil
}

// SetVMFirewall enables or disables the VM-level Proxmox firewall.
func (c *Client) SetVMFirewall(ctx context.Context, node, vmType string, vmid int, enable bool) error {
	path := fmt.Sprintf("/nodes/%s/%s/%d/firewall/options", node, vmType, vmid)
	v := url.Values{}
	if enable {
		v.Set("enable", "1")
	} else {
		v.Set("enable", "0")
	}
	return c.putForm(ctx, path, v)
}

// NodeVersion returns the Proxmox version string for a node (pveversion + release).
func (c *Client) NodeVersion(ctx context.Context, node string) (string, error) {
	var out struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	if err := c.get(ctx, fmt.Sprintf("/nodes/%s/version", node), &out); err != nil {
		return "", err
	}
	if out.Release != "" {
		return out.Version + "-" + out.Release, nil
	}
	return out.Version, nil
}

// HAResources returns the HA-managed resources and their current state.
func (c *Client) HAResources(ctx context.Context) ([]HAResource, error) {
	var out []HAResource
	if err := c.get(ctx, "/cluster/ha/resources", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// HAResource is one HA-managed resource (VM or CT).
type HAResource struct {
	SID     string `json:"sid"`       // e.g. "vm:100"
	State   string `json:"state"`     // "started"|"stopped"|"migrating"|...
	Node    string `json:"node"`      // current home node
	Group   string `json:"group"`
	MaxRelocate int `json:"max_relocate"`
	MaxRestart  int `json:"max_restart"`
}

// NodeInMaintenanceMode puts a Proxmox node into HA maintenance mode so VMs are migrated away.
// This uses PUT /cluster/ha/groups/{group} or more correctly, the node maintenance endpoint.
// Proxmox doesn't have a direct "maintenance mode" API; we migrate all HA VMs off the node instead.
func (c *Client) MigrateHAResourcesOffNode(ctx context.Context, node string, haResources []HAResource) []error {
	var errs []error
	for _, r := range haResources {
		if r.Node != node {
			continue
		}
		// POST /cluster/ha/resources/{sid}/migrate to move to another node
		// We pass no target node — Proxmox will pick a suitable one
		path := fmt.Sprintf("/cluster/ha/resources/%s/migrate", url.PathEscape(r.SID))
		v := url.Values{}
		if err := c.postForm(ctx, path, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.SID, err))
		}
	}
	return errs
}

// NodeAPTUpdate triggers apt cache refresh + upgrade on a Proxmox node.
// Returns the UPID of the upgrade task (empty string if the API doesn't return one).
func (c *Client) NodeAPTUpdate(ctx context.Context, node string) (string, error) {
	// POST /nodes/{node}/apt/update refreshes the package cache
	updatePath := fmt.Sprintf("/nodes/%s/apt/update", node)
	if err := c.postForm(ctx, updatePath, nil); err != nil {
		// Non-fatal — Proxmox may return non-2xx if cache is fresh
		_ = err
	}
	// POST /nodes/{node}/apt/upgrade runs the actual upgrade and returns a UPID
	upgPath := fmt.Sprintf("/nodes/%s/apt/upgrade", node)
	upid, err := c.postGetStr(ctx, upgPath)
	return upid, err
}

// WaitForTask polls a Proxmox task until it completes or the context expires.
// Returns the exitstatus string (e.g. "OK" or an error message).
func (c *Client) WaitForTask(ctx context.Context, node, upid string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var status struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		path := fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid))
		if err := c.get(ctx, path, &status); err != nil {
			return "", err
		}
		if status.Status == "stopped" {
			return status.ExitStatus, nil
		}
		time.Sleep(3 * time.Second)
	}
}

// NodeReboot sends a reboot command to a Proxmox node via the API.
func (c *Client) NodeReboot(ctx context.Context, node string) error {
	path := fmt.Sprintf("/nodes/%s/status", node)
	v := url.Values{}
	v.Set("command", "reboot")
	return c.putForm(ctx, path, v)
}

// Snapshot is a VM/LXC snapshot entry.
type Snapshot struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Snaptime    int64   `json:"snaptime"`
	Parent      string  `json:"parent"`
	VMState     float64 `json:"vmstate"` // 1 if RAM state was saved
}

// Snapshots returns all snapshots for a guest. "current" pseudo-snapshot is filtered out.
func (c *Client) Snapshots(ctx context.Context, node, vmType string, vmid int) ([]Snapshot, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot", node, vmType, vmid)
	var snaps []Snapshot
	if err := c.get(ctx, path, &snaps); err != nil {
		return nil, err
	}
	filtered := snaps[:0]
	for _, s := range snaps {
		if s.Name != "current" {
			filtered = append(filtered, s)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Snaptime > filtered[j].Snaptime
	})
	return filtered, nil
}

// CreateSnapshot creates a snapshot and returns the UPID for polling.
func (c *Client) CreateSnapshot(ctx context.Context, node, vmType string, vmid int, name, desc string) (string, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot", node, vmType, vmid)
	v := url.Values{}
	v.Set("snapname", name)
	if desc != "" {
		v.Set("description", desc)
	}
	return c.postFormGetStr(ctx, path, v)
}

// DeleteSnapshot deletes a snapshot and returns the UPID for polling.
func (c *Client) DeleteSnapshot(ctx context.Context, node, vmType string, vmid int, snapname string) (string, error) {
	path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot/%s", node, vmType, vmid, snapname)
	return c.deleteGetStr(ctx, path)
}

// BackupEntry is a backup volume in Proxmox storage.
type BackupEntry struct {
	VolID  string `json:"volid"`
	Size   int64  `json:"size"`
	CTime  int64  `json:"ctime"`
	Notes  string `json:"notes"`
	Format string `json:"format"`
}

// VMBackups lists backup entries for a VM in the given storage.
func (c *Client) VMBackups(ctx context.Context, node, storage string, vmid int) ([]BackupEntry, error) {
	path := fmt.Sprintf("/nodes/%s/storage/%s/content?content=backup&vmid=%d", node, storage, vmid)
	var entries []BackupEntry
	if err := c.get(ctx, path, &entries); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CTime > entries[j].CTime
	})
	return entries, nil
}

