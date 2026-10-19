package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/bootstrap"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	"github.com/alytaphoenix/argus/formula"
	gkr "github.com/alytaphoenix/argus/keyring"
)

func runBootstrap(args []string) {
	cmd := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("argus bootstrap", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	apply := fs.Bool("yes", false, "apply the plan (required for apply)")
	dryRun := fs.Bool("dry-run", false, "print the plan without changing the host")
	formulasFlag := fs.String("formulas", "", "comma-separated formula IDs (default: baseline)")
	sshHost := fs.String("ssh-host", "", "SSH host (default: Proxmox host)")
	sshUser := fs.String("ssh-user", "", "SSH user for the Proxmox host (default [bootstrap] ssh_user or root)")
	sshKey := fs.String("ssh-key", "", "SSH private key path (default: [globals] ssh_key)")
	sshPassword := fs.String("ssh-password", "", "SSH password (prefer key/agent; also reads ssh_password keychain)")
	saltMasterID := fs.String("salt-master-id", "", "Salt minion ID for the master host (default: Proxmox node name)")
	saltAPIURL := fs.String("salt-api-url", "", "Salt API URL (default: https://<host>:8000)")
	saltAPIUser := fs.String("salt-api-user", "", "Salt API PAM user (default [bootstrap] salt_api_user or saltapi)")
	saltPassword := fs.String("salt-password", "", "existing Salt API password (default: salt_password keychain)")
	_ = fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	client := api.NewClient(cfg)

	var saltClient *api.SaltClient
	if cfg.Salt.URL != "" {
		saltClient, _ = api.NewSaltClient(cfg.Salt)
	}

	host := strings.TrimSpace(*sshHost)
	if host == "" {
		host = hostFromURL(cfg.Host)
	}
	if host == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: no Proxmox host configured")
		os.Exit(1)
	}
	key := strings.TrimSpace(*sshKey)
	if key == "" {
		key = cfg.Bootstrap.SSHKey
	}
	if key == "" {
		key = cfg.Globals.SSHKey
	}
	user := strings.TrimSpace(*sshUser)
	if user == "" {
		user = cfg.Bootstrap.SSHUser
	}
	if user == "" {
		user = "root"
	}
	pass := *sshPassword
	if pass == "" {
		pass, _ = gkr.Load(gkr.KeySSHPassword)
	}
	runner, err := bootstrap.NewSSHRunner(host, user, key, pass)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}

	hostIP := resolveIP(host)
	masterID := strings.TrimSpace(*saltMasterID)
	if masterID == "" {
		masterID = cfg.Bootstrap.SaltMasterID
	}
	if masterID == "" {
		if nodes, nErr := client.Nodes(context.Background()); nErr == nil && len(nodes) > 0 {
			masterID = nodes[0].Name
		} else {
			masterID = "proxmox"
		}
	}
	apiURL := strings.TrimSpace(*saltAPIURL)
	if apiURL == "" {
		apiURL = cfg.Bootstrap.SaltAPIURL
	}
	if apiURL == "" {
		apiURL = "https://" + hostIP + ":8000"
	}
	apiUser := strings.TrimSpace(*saltAPIUser)
	if apiUser == "" {
		apiUser = cfg.Bootstrap.SaltAPIUser
	}
	if apiUser == "" {
		apiUser = "saltapi"
	}
	saltPass := *saltPassword
	if saltPass == "" {
		saltPass, _ = gkr.Load(gkr.KeySaltPassword)
	}

	registry, err := formula.BuiltinRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: formula registry: %v\n", err)
		os.Exit(1)
	}
	selected := splitCSV(*formulasFlag)

	if cmd == "formulas" {
		printFormulas(registry, *jsonOut)
		return
	}

	database, dbErr := db.Open()
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "warning: local database unavailable: %v\n", dbErr)
		database = nil
	}
	if database != nil {
		defer database.Close()
	}

	mgr, err := bootstrap.NewManager(runner, client, saltClient, registry, masterID, apiURL, apiUser, saltPass, hostIP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}
	mgr.DryRun = *dryRun

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	switch cmd {
	case "plan", "status":
		plan, err := mgr.Plan(ctx, selected)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bootstrap plan: %v\n", err)
			os.Exit(1)
		}
		if database != nil {
			if rows, dbErr := database.FormulaInstallations(); dbErr == nil {
				for i := range plan.Formulas {
					for _, row := range rows {
						if row.FormulaID == plan.Formulas[i].Formula.ID {
							plan.Formulas[i].Installed = true
							plan.Formulas[i].Detail = "applied " + row.AppliedAt.Format("2006-01-02 15:04")
						}
					}
				}
			}
			planJSON, _ := json.Marshal(plan)
			status := "blocked"
			if plan.Ready {
				status = "ready"
			}
			_ = database.SavePlatformProfile(db.PlatformProfile{
				Host: host, SaltMasterID: masterID, SaltAPIURL: apiURL,
				SaltAPIUser: apiUser, SaltVersion: "3008.x", Status: status,
				DetailsJSON: string(planJSON), LastChecked: time.Now(),
			})
		}
		printPlan(plan, *jsonOut)
	case "apply":
		if !*apply && !*dryRun {
			fmt.Fprintln(os.Stderr, "bootstrap apply requires --yes (or use --dry-run to preview)")
			os.Exit(2)
		}
		runID := int64(0)
		if database != nil && !*dryRun {
			runID, _ = database.SaveBootstrapRun(db.BootstrapRun{
				Host: host, Mode: "apply", Status: "running", StartedAt: time.Now(),
			})
		}
		run, err := mgr.Apply(ctx, selected)
		if err != nil {
			if database != nil && runID != 0 {
				_ = database.UpdateBootstrapRun(runID, "failed", "", err.Error())
			}
			fmt.Fprintf(os.Stderr, "bootstrap apply: %v\n", err)
			os.Exit(1)
		}
		if *dryRun {
			printPlan(run.Plan, *jsonOut)
			return
		}
		if database != nil && runID != 0 {
			planJSON, _ := json.Marshal(run.Plan)
			_ = database.UpdateBootstrapRun(runID, "success", strings.Join(run.Log, "\n"), "")
			_ = database.SavePlatformProfile(db.PlatformProfile{
				Host: host, SaltMasterID: masterID, SaltAPIURL: apiURL,
				SaltAPIUser: apiUser, SaltVersion: "3008.x", Status: "ready",
				DetailsJSON: string(planJSON), LastChecked: time.Now(),
			})
			for _, state := range run.Applied {
				for _, f := range registry.List() {
					if f.StateName() != state {
						continue
					}
					_ = database.SaveFormulaInstallation(db.FormulaInstallation{
						FormulaID: f.ID, Target: masterID, State: state, Version: f.Version,
						Status: "applied", AppliedAt: time.Now(),
					})
				}
			}
		}
		if run.SaltAPIPassword != "" {
			if err := gkr.Save(gkr.KeySaltPassword, run.SaltAPIPassword); err != nil {
				fmt.Fprintf(os.Stderr, "warning: store salt password in keychain: %v\n", err)
			}
		}
		cfg.Salt.URL = apiURL
		cfg.Salt.Username = apiUser
		cfg.Salt.Eauth = "pam"
		cfg.Salt.MasterMinion = masterID
		cfg.Salt.PillarBase = "/srv/pillar"
		cfg.Salt.StateBase = "/srv/salt"
		cfg.Salt.Insecure = false
		cfg.Bootstrap.SSHUser = user
		cfg.Bootstrap.SaltMasterID = masterID
		cfg.Bootstrap.SaltAPIURL = apiURL
		cfg.Bootstrap.SaltAPIUser = apiUser
		if key != "" && key != cfg.Globals.SSHKey {
			cfg.Bootstrap.SSHKey = key
		}
		if err := config.SaveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: save config: %v\n", err)
		}
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(run)
		} else {
			fmt.Printf("bootstrap complete on %s\n", host)
			for _, line := range run.Log {
				fmt.Println("  " + line)
			}
			fmt.Printf("salt API: %s (user %s)\n", apiURL, apiUser)
			fmt.Println("next: open argus → Orch → Bootstrap and press x")
		}
	default:
		fmt.Fprintf(os.Stderr, "argus bootstrap: unknown command %q (plan, apply, status, formulas)\n", cmd)
		os.Exit(2)
	}
}

func printPlan(p bootstrap.Plan, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(p)
		return
	}
	fmt.Printf("Bootstrap plan for %s (generated %s)\n\n", p.Host, p.GeneratedAt.Format(time.RFC3339))
	for _, s := range p.Steps {
		fmt.Printf("  [%-7s] %-24s %s\n", s.Status, s.Title, s.Detail)
		if len(s.Commands) > 0 {
			for _, c := range s.Commands {
				fmt.Printf("              %s\n", c)
			}
		}
	}
	if len(p.Formulas) > 0 {
		fmt.Println("\nFormulas:")
		for _, fs := range p.Formulas {
			fmt.Printf("  %-18s %-12s %s\n", fs.Formula.ID, fs.Formula.Category, fs.Detail)
		}
	}
	fmt.Printf("\nReady: %v\n", p.Ready)
}

func printFormulas(r *formula.Registry, jsonOut bool) {
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(r.List())
		return
	}
	fmt.Printf("%-18s %-12s %-10s %s\n", "ID", "CATEGORY", "KIND", "DESCRIPTION")
	for _, f := range r.List() {
		fmt.Printf("%-18s %-12s %-10s %s\n", f.ID, f.Category, f.Kind, f.Description)
	}
	fmt.Println("\nBaseline:", strings.Join(formula.BaselineIDs, ", "))
	fmt.Println("Recommended:", strings.Join(formula.RecommendedIDs, ", "))
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return strings.TrimSpace(raw)
	}
	return u.Hostname()
}

func resolveIP(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return host
	}
	ips, err := net.LookupHost(host)
	if err == nil && len(ips) > 0 {
		return ips[0]
	}
	return host
}
