package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CloneOptions controls how a VM is cloned from a template.
type CloneOptions struct {
	SourceNode string
	SourceVMID int
	NewVMID    int    // 0 = use NextVMID
	Name       string
	TargetNode string // empty = same as source node
	Storage    string // empty = same as source storage
	Full       bool   // true = full clone, false = linked clone
}

// CloudInitOptions configures cloud-init on a QEMU VM after cloning.
type CloudInitOptions struct {
	User         string
	Password     string // plain text; Proxmox hashes it
	SSHKeys      string // authorized_keys content
	IPConfig     string // "ip=dhcp" or "ip=x.x.x.x/24,gw=..."
	Nameserver   string
	SearchDomain string
}

// NextVMID returns the next available VM/CT ID from the cluster.
func (c *Client) NextVMID(ctx context.Context) (int, error) {
	var id int
	if err := c.get(ctx, "/cluster/nextid", &id); err != nil {
		return 0, err
	}
	return id, nil
}

// CloneVM clones a QEMU VM from a template. Returns the Proxmox task UPID.
func (c *Client) CloneVM(ctx context.Context, opts CloneOptions) (string, error) {
	if opts.TargetNode == "" {
		opts.TargetNode = opts.SourceNode
	}

	body := map[string]interface{}{
		"newid":  opts.NewVMID,
		"name":   opts.Name,
		"full":   boolInt(opts.Full),
		"target": opts.TargetNode,
	}
	if opts.Storage != "" {
		body["storage"] = opts.Storage
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/clone", opts.SourceNode, opts.SourceVMID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}

	var result struct {
		Data string `json:"data"` // UPID
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Data, nil
}

// SetCloudInit applies cloud-init configuration to a QEMU VM.
func (c *Client) SetCloudInit(ctx context.Context, node string, vmid int, opts CloudInitOptions) error {
	body := map[string]interface{}{}
	if opts.User != "" {
		body["ciuser"] = opts.User
	}
	if opts.Password != "" {
		body["cipassword"] = opts.Password
	}
	if opts.SSHKeys != "" {
		// Proxmox expects URL-encoded SSH keys
		body["sshkeys"] = strings.ReplaceAll(opts.SSHKeys, "\n", "%0a")
	}
	if opts.IPConfig != "" {
		body["ipconfig0"] = opts.IPConfig
	}
	if opts.Nameserver != "" {
		body["nameserver"] = opts.Nameserver
	}
	if opts.SearchDomain != "" {
		body["searchdomain"] = opts.SearchDomain
	}
	if len(body) == 0 {
		return nil
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// GuestPrimaryIP returns the first non-loopback IPv4 address of a guest.
// For QEMU VMs it uses the QEMU guest agent; for LXC it uses the interfaces endpoint.
// Returns an empty string (not an error) when no IP is found.
func (c *Client) GuestPrimaryIP(ctx context.Context, node, guestType string, vmid int) string {
	if guestType == "qemu" {
		var result struct {
			Result []struct {
				Name        string `json:"name"`
				IPAddresses []struct {
					IPAddress string `json:"ip-address"`
					Type      string `json:"ip-address-type"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid)
		if err := c.get(ctx, path, &result); err == nil {
			for _, iface := range result.Result {
				if iface.Name == "lo" {
					continue
				}
				for _, addr := range iface.IPAddresses {
					if addr.Type == "ipv4" && addr.IPAddress != "127.0.0.1" {
						return addr.IPAddress
					}
				}
			}
		}
	} else {
		var ifaces []struct {
			Name string `json:"name"`
			Inet string `json:"inet"`
		}
		path := fmt.Sprintf("/nodes/%s/lxc/%d/interfaces", node, vmid)
		if err := c.get(ctx, path, &ifaces); err == nil {
			for _, iface := range ifaces {
				if iface.Name != "lo" && iface.Inet != "" {
					return strings.SplitN(iface.Inet, "/", 2)[0]
				}
			}
		}
	}
	return ""
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
