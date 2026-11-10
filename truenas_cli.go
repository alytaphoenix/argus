package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
)

const trueNASUsage = `argus truenas — TrueNAS configuration and management

Usage:
  argus truenas status                       Show system info, pools, alerts
  argus truenas datasets                     List ZFS datasets
  argus truenas services                     List NAS services and state
  argus truenas start <service>              Start a NAS service (nfs, smb, ssh, ...)
  argus truenas stop <service>               Stop a NAS service
  argus truenas restart <service>            Restart a NAS service
  argus truenas nfs-list                     List NFS shares
  argus truenas nfs-create <path> <network>  Create an NFS share for a path
  argus truenas nfs-delete <id>              Delete an NFS share by id
  argus truenas ds-create <name> [type]      Create a ZFS dataset (type defaults to FILESYSTEM)
  argus truenas ds-delete <name>             Delete a ZFS dataset by full id

Configuration: [truenas] url / api_key in config.toml (api_key may live in
Keychain as argus/truenas_api_key).`

func runTrueNASCLI(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(trueNASUsage)
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	client := api.NewTrueNASClient(cfg.TrueNAS.URL, cfg.TrueNAS.APIKey, cfg.TrueNAS.Insecure)
	if client == nil {
		fmt.Fprintln(os.Stderr, "truenas: not configured — set [truenas] url and api_key in config.toml")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		status, err := client.FetchAll(ctx)
		if err != nil {
			fail("status", err)
		}
		fmt.Printf("%s  v%s  up %s  ready=%t\n",
			status.SysInfo.Hostname, status.SysInfo.Version,
			api.TrueNASUptimeStr(status.SysInfo.UptimeSeconds), status.SysInfo.SystemReady)
		for _, a := range status.Alerts {
			fmt.Printf("ALERT [%s] %s\n", a.Level, a.Formatted)
		}
		fmt.Println("pools:")
		for _, p := range status.Pools {
			fmt.Printf("  %-20s %-10s free=%s\n", p.Name, p.Status, humanBytes(p.Free))
		}
		fmt.Println("datasets:")
		for _, d := range status.Datasets {
			fmt.Printf("  %-40s free=%s\n", d.ID, humanBytes(d.AvailBytes))
		}
		fmt.Println("services:")
		for _, s := range status.Services {
			fmt.Printf("  %-10s %-8s enabled=%t\n", s.Service, s.State, s.Enable)
		}

	case "datasets":
		ds, err := client.Datasets(ctx)
		if err != nil {
			fail("datasets", err)
		}
		for _, d := range ds {
			fmt.Printf("%-40s free=%s\n", d.ID, humanBytes(d.AvailBytes))
		}

	case "services":
		svcs, err := client.Services(ctx)
		if err != nil {
			fail("services", err)
		}
		for _, s := range svcs {
			fmt.Printf("%-10s %-8s enabled=%t\n", s.Service, s.State, s.Enable)
		}

	case "start", "stop", "restart":
		if len(rest) != 1 {
			fmt.Fprintf(os.Stderr, "truenas %s: exactly one service name required\n", cmd)
			os.Exit(2)
		}
		var err error
		switch cmd {
		case "start":
			err = client.StartService(ctx, rest[0])
		case "stop":
			err = client.StopService(ctx, rest[0])
		case "restart":
			err = client.RestartService(ctx, rest[0])
		}
		if err != nil {
			fail(cmd+" "+rest[0], err)
		}
		fmt.Printf("%s %s: ok\n", cmd, rest[0])

	case "nfs-list":
		shares, err := client.NFSShares(ctx)
		if err != nil {
			fail("nfs-list", err)
		}
		for _, s := range shares {
			fmt.Printf("%d  %-40s enabled=%t ro=%t networks=%s security=%s\n",
				s.ID, s.Path, s.Enabled, s.ReadOnly,
				strings.Join(s.Networks, ","), strings.Join(s.Sec, ","))
		}

	case "nfs-create":
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "truenas nfs-create: <path> <network> required (e.g. /mnt/PhoenixNest/Media 192.168.0.0/24)")
			os.Exit(2)
		}
		share, err := client.CreateNFSShare(ctx, api.NFSShare{
			Path:     rest[0],
			Networks: []string{rest[1]},
			Enabled:  true,
			Sec:      []string{"SYS"},
		})
		if err != nil {
			fail("nfs-create", err)
		}
		fmt.Printf("created NFS share id=%d path=%s networks=%s\n", share.ID, share.Path, strings.Join(share.Networks, ","))

	case "nfs-delete":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "truenas nfs-delete: <id> required")
			os.Exit(2)
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "truenas nfs-delete: invalid id %q\n", rest[0])
			os.Exit(2)
		}
		if err := client.DeleteNFSShare(ctx, id); err != nil {
			fail("nfs-delete", err)
		}
		fmt.Printf("deleted NFS share id=%d\n", id)

	case "ds-create":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "truenas ds-create: <name> [type] required (e.g. PhoenixNest/Media)")
			os.Exit(2)
		}
		dstype := "FILESYSTEM"
		if len(rest) > 1 {
			dstype = rest[1]
		}
		if err := client.CreateDataset(ctx, rest[0], dstype); err != nil {
			fail("ds-create", err)
		}
		fmt.Printf("created dataset %s (%s)\n", rest[0], dstype)

	case "ds-delete":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "truenas ds-delete: <name> required")
			os.Exit(2)
		}
		if err := client.DeleteDataset(ctx, rest[0]); err != nil {
			fail("ds-delete", err)
		}
		fmt.Printf("deleted dataset %s\n", rest[0])

	default:
		fmt.Fprintf(os.Stderr, "truenas: unknown command %q\n\n%s\n", cmd, trueNASUsage)
		os.Exit(2)
	}
}

func fail(op string, err error) {
	fmt.Fprintf(os.Stderr, "truenas %s: %v\n", op, err)
	os.Exit(1)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
