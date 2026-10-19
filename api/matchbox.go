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

	"github.com/alytaphoenix/argus/config"
)

// MatchboxClient talks to a Matchbox PXE provisioning server.
// Docs: https://matchbox.psdn.io/api.html
type MatchboxClient struct {
	base  string
	token string // optional bearer auth
	http  *http.Client
}

func NewMatchboxClient(cfg config.MatchboxConfig) *MatchboxClient {
	if cfg.URL == "" {
		return nil
	}
	return &MatchboxClient{
		base:  strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ── Types ─────────────────────────────────────────────────────────────────────

// MatchboxProfile is a PXE boot profile (kernel + initrd + args).
type MatchboxProfile struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Boot        MatchboxBoot `json:"boot"`
	CloudConfig string       `json:"cloud_config_url,omitempty"`
	Ignition    string       `json:"ignition_id,omitempty"`
}

// MatchboxBoot holds the kernel/initrd/args for a profile.
type MatchboxBoot struct {
	Kernel string   `json:"kernel"`
	Initrd []string `json:"initrd"`
	Args   []string `json:"args"`
}

// MatchboxGroup assigns a profile to machines matched by selector.
type MatchboxGroup struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Profile  string            `json:"profile"`
	Selector map[string]string `json:"selector,omitempty"` // {"mac":"aa:bb:cc:dd:ee:ff"}
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ── API methods ───────────────────────────────────────────────────────────────

func (c *MatchboxClient) Profiles(ctx context.Context) ([]MatchboxProfile, error) {
	body, err := c.get(ctx, "/api/profiles")
	if err != nil {
		return nil, err
	}
	var out []MatchboxProfile
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("matchbox profiles: %w", err)
	}
	return out, nil
}

func (c *MatchboxClient) CreateProfile(ctx context.Context, p MatchboxProfile) error {
	return c.postJSON(ctx, "/api/profiles", p)
}

func (c *MatchboxClient) DeleteProfile(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/profiles/"+id)
}

func (c *MatchboxClient) Groups(ctx context.Context) ([]MatchboxGroup, error) {
	body, err := c.get(ctx, "/api/groups")
	if err != nil {
		return nil, err
	}
	var out []MatchboxGroup
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("matchbox groups: %w", err)
	}
	return out, nil
}

func (c *MatchboxClient) CreateGroup(ctx context.Context, g MatchboxGroup) error {
	return c.postJSON(ctx, "/api/groups", g)
}

func (c *MatchboxClient) DeleteGroup(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/groups/"+id)
}

// Ping checks if Matchbox is reachable.
func (c *MatchboxClient) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/")
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (c *MatchboxClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	return c.do(req)
}

func (c *MatchboxClient) postJSON(ctx context.Context, path string, v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	_, err = c.do(req)
	return err
}

func (c *MatchboxClient) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	_, err = c.do(req)
	return err
}

func (c *MatchboxClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *MatchboxClient) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("matchbox: HTTP %s: %s", resp.Status, truncateBody(body))
	}
	return body, nil
}

// ── Built-in profile templates ────────────────────────────────────────────────

// PXETemplate is a pre-defined boot profile template.
type PXETemplate struct {
	Key         string
	Name        string
	Description string
	Kernel      string
	Initrd      []string
	Args        string
}

// PXETemplates is the built-in catalog of common netboot profiles.
var PXETemplates = []PXETemplate{
	{
		Key:         "netboot.xyz",
		Name:        "Netboot.xyz (chain loader)",
		Description: "iPXE chain loader — pick any OS interactively at boot",
		Kernel:      "http://boot.netboot.xyz/ipxe/netboot.xyz.lkrn",
		Args:        "",
	},
	{
		Key:         "ubuntu-24.04",
		Name:        "Ubuntu 24.04 LTS",
		Description: "Ubuntu 24.04 (Noble) netboot installer via subiquity",
		Kernel:      "http://archive.ubuntu.com/ubuntu/dists/noble/main/installer-amd64/current/legacy-images/netboot/ubuntu-installer/amd64/linux",
		Initrd:      []string{"http://archive.ubuntu.com/ubuntu/dists/noble/main/installer-amd64/current/legacy-images/netboot/ubuntu-installer/amd64/initrd.gz"},
		Args:        "auto=true priority=critical",
	},
	{
		Key:         "ubuntu-22.04",
		Name:        "Ubuntu 22.04 LTS",
		Description: "Ubuntu 22.04 (Jammy) netboot installer",
		Kernel:      "http://archive.ubuntu.com/ubuntu/dists/jammy/main/installer-amd64/current/legacy-images/netboot/ubuntu-installer/amd64/linux",
		Initrd:      []string{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/installer-amd64/current/legacy-images/netboot/ubuntu-installer/amd64/initrd.gz"},
		Args:        "auto=true priority=critical",
	},
	{
		Key:         "debian-12",
		Name:        "Debian 12 (Bookworm)",
		Description: "Debian 12 netboot installer",
		Kernel:      "http://ftp.debian.org/debian/dists/bookworm/main/installer-amd64/current/images/netboot/debian-installer/amd64/linux",
		Initrd:      []string{"http://ftp.debian.org/debian/dists/bookworm/main/installer-amd64/current/images/netboot/debian-installer/amd64/initrd.gz"},
		Args:        "auto=true priority=critical",
	},
	{
		Key:         "alpine-3.19",
		Name:        "Alpine Linux 3.19",
		Description: "Minimal Alpine Linux live image",
		Kernel:      "http://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/netboot/vmlinuz-lts",
		Initrd:      []string{"http://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/netboot/initramfs-lts"},
		Args:        "modules=loop,squashfs,sd-mod,usb-storage quiet nomodeset alpine_repo=http://dl-cdn.alpinelinux.org/alpine/v3.19/main",
	},
	{
		Key:         "talos-v1.7",
		Name:        "Talos Linux v1.7",
		Description: "Kubernetes-focused immutable OS",
		Kernel:      "https://factory.talos.dev/image/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4b/v1.7.0/kernel-amd64",
		Initrd:      []string{"https://factory.talos.dev/image/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4b/v1.7.0/initramfs-amd64.xz"},
		Args:        "talos.platform=metal slab_nomerge pti=on",
	},
	{
		Key:         "flatcar-stable",
		Name:        "Flatcar Container Linux",
		Description: "Container-optimised immutable OS (CoreOS successor)",
		Kernel:      "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_pxe.vmlinuz",
		Initrd:      []string{"https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_pxe_image.cpio.gz"},
		Args:        "flatcar.first_boot=1 initrd=flatcar_production_pxe_image.cpio.gz",
	},
	{
		Key:         "memtest",
		Name:        "Memtest86+",
		Description: "RAM diagnostic tool",
		Kernel:      "http://boot.netboot.xyz/memtest/memtest86-7.0.0",
		Args:        "",
	},
}
