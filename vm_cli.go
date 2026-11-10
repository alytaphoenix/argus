package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/db"
)

const vmUsage = `Usage:
  argus vm list [--node NAME]
  argus vm delete --vmid ID [--node NAME] [--name NAME] [--type qemu|lxc] [--no-purge] --yes

Deleting a guest is permanent. The guest must be stopped first. --yes is
required; --name refuses to delete a guest whose name does not match.
`

func runVM(args []string) {
	cmd := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "list":
		runVMList(args)
	case "delete", "rm", "destroy":
		runVMDelete(args)
	case "help", "-h", "--help":
		fmt.Print(vmUsage)
	default:
		fmt.Fprintf(os.Stderr, "argus vm: unknown command %q\n\n%s", cmd, vmUsage)
		os.Exit(2)
	}
}

func vmClient() (*api.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return api.NewClient(cfg), nil
}

func runVMList(args []string) {
	fs := flag.NewFlagSet("argus vm list", flag.ExitOnError)
	node := fs.String("node", "", "Proxmox node (default: all nodes)")
	_ = fs.Parse(args)

	client, err := vmClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus vm: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	nodes, err := client.Nodes(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus vm: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%-10s %-6s %-8s %-28s %s\n", "NODE", "ID", "TYPE", "NAME", "STATUS")
	for _, n := range nodes {
		if *node != "" && n.Name != *node {
			continue
		}
		guests, err := client.Guests(ctx, n.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argus vm: %s: %v\n", n.Name, err)
			continue
		}
		for _, g := range guests {
			fmt.Printf("%-10s %-6d %-8s %-28s %s\n", g.Node, g.VMID, g.Type, truncatePlain(g.Name, 28), g.Status)
		}
	}
}

func runVMDelete(args []string) {
	fs := flag.NewFlagSet("argus vm delete", flag.ExitOnError)
	vmid := fs.Int("vmid", 0, "VMID to destroy (required)")
	node := fs.String("node", "", "Proxmox node (default: find the VMID)")
	name := fs.String("name", "", "expected guest name; refuses a mismatch")
	guestType := fs.String("type", "", "qemu or lxc (default: find)")
	purge := fs.Bool("purge", true, "remove backup/replication/HA references too")
	yes := fs.Bool("yes", false, "confirm permanent deletion")
	_ = fs.Parse(args)

	if *vmid <= 0 {
		fmt.Fprintln(os.Stderr, "argus vm delete: --vmid is required")
		os.Exit(2)
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "argus vm delete: refusing to delete without --yes")
		os.Exit(2)
	}
	if *guestType != "" && *guestType != "qemu" && *guestType != "lxc" {
		fmt.Fprintln(os.Stderr, "argus vm delete: --type must be qemu or lxc")
		os.Exit(2)
	}

	client, err := vmClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus vm: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	guest, err := findGuest(ctx, client, *node, *guestType, *vmid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus vm delete: %v\n", err)
		os.Exit(1)
	}
	if *name != "" && !strings.EqualFold(guest.Name, *name) {
		fmt.Fprintf(os.Stderr, "argus vm delete: VMID %d is %q, not %q; refusing\n", guest.VMID, guest.Name, *name)
		os.Exit(1)
	}
	if guest.Status != "stopped" {
		fmt.Fprintf(os.Stderr, "argus vm delete: VM %d %q is %s; stop it first\n", guest.VMID, guest.Name, guest.Status)
		os.Exit(1)
	}

	fmt.Printf("Deleting %s %d %q on %s (purge=%t)\n", guest.Type, guest.VMID, guest.Name, guest.Node, *purge)
	upid, err := client.DeleteGuest(ctx, guest.Node, guest.Type, guest.VMID, *purge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus vm delete: %v\n", err)
		os.Exit(1)
	}
	if upid != "" {
		status, err := client.WaitForTask(ctx, guest.Node, upid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argus vm delete: task %s: %v\n", upid, err)
			os.Exit(1)
		}
		if status != "OK" {
			fmt.Fprintf(os.Stderr, "argus vm delete: task failed: %s\n", status)
			os.Exit(1)
		}
	}
	if database, dbErr := db.Open(); dbErr == nil {
		_ = database.DeleteProvisionedVM(guest.VMID)
		_ = database.Close()
	}
	fmt.Printf("Deleted VM %d %q\n", guest.VMID, guest.Name)
}

func findGuest(ctx context.Context, client *api.Client, node, guestType string, vmid int) (api.Guest, error) {
	nodes, err := client.Nodes(ctx)
	if err != nil {
		return api.Guest{}, err
	}
	for _, n := range nodes {
		if node != "" && n.Name != node {
			continue
		}
		guests, err := client.Guests(ctx, n.Name)
		if err != nil {
			continue
		}
		for _, g := range guests {
			if g.VMID != vmid {
				continue
			}
			if guestType != "" && g.Type != guestType {
				continue
			}
			return g, nil
		}
	}
	where := "any node"
	if node != "" {
		where = node
	}
	return api.Guest{}, fmt.Errorf("VMID %d not found on %s", vmid, where)
}

func truncatePlain(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
