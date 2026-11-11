package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/bootstrap"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	"github.com/alytaphoenix/argus/formula"
	gkr "github.com/alytaphoenix/argus/keyring"
	"github.com/alytaphoenix/argus/stack"
)

const stackUsage = `Usage:
  argus stack list [--json]
  argus stack show NAME [--compose|--sls|--toml]
  argus stack init NAME [--target MINION] [--force]
  argus stack apply NAME [--target MINION] [--dry-run] --yes

Stacks live in ~/.config/argus/stacks/*.toml (override with ARGUS_STACKS_DIR).
A stack file defines the Salt minion, install root, media mount, and one or
more [[service]] blocks. argus renders a Compose file and Salt state from it.
`

func runStack(args []string) {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Print(stackUsage)
		return
	}
	cmd := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "list", "ls":
		runStackList(args)
	case "show", "cat":
		runStackShow(args)
	case "init":
		runStackInit(args)
	case "apply":
		runStackApply(args)
	default:
		fmt.Fprintf(os.Stderr, "argus stack: unknown command %q\n\n%s", cmd, stackUsage)
		os.Exit(2)
	}
}

// reorderStackArgs moves flags before the positional stack name. Go's flag
// package stops parsing at the first non-flag, but `stack show servarr
// --compose` is the natural order.
func reorderStackArgs(args []string, valueFlags ...string) []string {
	takesValue := map[string]bool{}
	for _, name := range valueFlags {
		takesValue[name] = true
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		if takesValue[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func loadStackRegistry() (*formula.Registry, error) {
	r, err := formula.BuiltinRegistry()
	if err != nil {
		return nil, err
	}
	return formula.WithStackDir(r, stack.DefaultDir())
}

func runStackList(args []string) {
	fs := flag.NewFlagSet("argus stack list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	registry, err := loadStackRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	ids := registry.StackIDs()
	if *jsonOut {
		fmt.Print("[")
		for i, id := range ids {
			f, _ := registry.Get(id)
			if i > 0 {
				fmt.Print(",")
			}
			fmt.Printf(`{"id":%q,"target":%q,"install_root":%q,"services":%d}`,
				f.ID, f.TargetMinion, stackRoot(f), len(stackServices(f)))
		}
		fmt.Println("]")
		return
	}
	if len(ids) == 0 {
		fmt.Println("no stacks found")
		return
	}
	fmt.Printf("%-20s %-22s %-62s %s\n", "STACK", "TARGET", "SERVICES", "INSTALL ROOT")
	for _, id := range ids {
		f, _ := registry.Get(id)
		names := make([]string, 0, len(stackServices(f)))
		for _, svc := range stackServices(f) {
			names = append(names, svc.id)
		}
		target := f.TargetMinion
		if target == "" {
			target = "(not set)"
		}
		fmt.Printf("%-20s %-22s %-62s %s\n",
			truncatePlain(f.ID, 20), truncatePlain(target, 22),
			truncatePlain(strings.Join(names, ","), 62), stackRoot(f))
	}
	fmt.Printf("\nStack files: %s\n", stack.DefaultDir())
}

func runStackShow(args []string) {
	fs := flag.NewFlagSet("argus stack show", flag.ExitOnError)
	compose := fs.Bool("compose", false, "print the rendered compose.yaml")
	sls := fs.Bool("sls", false, "print the rendered Salt state")
	tomlOut := fs.Bool("toml", false, "print the stack definition as TOML")
	_ = fs.Parse(reorderStackArgs(args))
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "argus stack show: NAME is required")
		os.Exit(2)
	}
	name := fs.Arg(0)
	registry, err := loadStackRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	f, ok := registry.Get(name)
	if !ok || !f.IsStack() {
		fmt.Fprintf(os.Stderr, "argus stack: unknown stack %q\n", name)
		os.Exit(1)
	}
	switch {
	case *compose:
		fmt.Print(f.Files["compose.yaml"])
	case *sls:
		fmt.Print(f.Files["init.sls"])
	case *tomlOut:
		def, err := findStackDefinition(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
			os.Exit(1)
		}
		data, err := stack.Encode(def)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(data)
	default:
		fmt.Printf("stack:        %s\n", f.ID)
		fmt.Printf("target:       %s\n", valueOr(f.TargetMinion, "(not set)"))
		fmt.Printf("install root: %s\n", stackRoot(f))
		fmt.Printf("source:       %s\n", stackSource(name))
		fmt.Printf("doc:          %s\n\n", f.Docs)
		fmt.Printf("%-12s %-28s %-8s %s\n", "SERVICE", "IMAGE", "PORT", "CONFIG PATH")
		for _, svc := range stackServices(f) {
			fmt.Printf("%-12s %-28s %-8d %s\n",
				svc.id, truncatePlain(svc.image, 28), svc.port, svc.config)
		}
	}
}

func runStackInit(args []string) {
	fs := flag.NewFlagSet("argus stack init", flag.ExitOnError)
	target := fs.String("target", "", "Salt minion to run the stack on")
	force := fs.Bool("force", false, "overwrite an existing stack file")
	_ = fs.Parse(reorderStackArgs(args, "target"))
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "argus stack init: NAME is required")
		os.Exit(2)
	}
	name := fs.Arg(0)

	defs, err := stack.Builtins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	var def *stack.Definition
	for i := range defs {
		if defs[i].Name == name {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		fmt.Fprintf(os.Stderr, "argus stack: %q is not a built-in stack\n", name)
		os.Exit(1)
	}
	if *target != "" {
		def.Target = *target
	}
	data, err := stack.Encode(*def)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	dir := stack.DefaultDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	out := filepath.Join(dir, name+".toml")
	if _, err := os.Stat(out); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "argus stack: %s exists; use --force to overwrite\n", out)
		os.Exit(1)
	}
	header := "# argus stack definition — edit paths and target, then:\n" +
		"#   argus stack show " + name + " --compose\n" +
		"#   argus stack apply " + name + " --yes\n\n"
	if err := os.WriteFile(out, []byte(header+data), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\nEdit it, then: argus stack apply %s --yes\n", out, name)
}

func runStackApply(args []string) {
	fs := flag.NewFlagSet("argus stack apply", flag.ExitOnError)
	yes := fs.Bool("yes", false, "apply the stack")
	dryRun := fs.Bool("dry-run", false, "render and plan without changing the host")
	target := fs.String("target", "", "override the Salt minion in the stack file")
	_ = fs.Parse(reorderStackArgs(args, "target"))
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "argus stack apply: NAME is required")
		os.Exit(2)
	}
	if !*yes && !*dryRun {
		fmt.Fprintln(os.Stderr, "argus stack apply: requires --yes (or use --dry-run)")
		os.Exit(2)
	}
	name := fs.Arg(0)

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	registry, defs, err := stackRegistryWithTarget(name, *target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	f, ok := registry.Get(name)
	if !ok || !f.IsStack() {
		fmt.Fprintf(os.Stderr, "argus stack: unknown stack %q\n", name)
		os.Exit(1)
	}
	if f.TargetMinion == "" {
		fmt.Fprintf(os.Stderr, "argus stack: %q has no target minion; edit %s or pass --target\n", name, stack.DefaultDir())
		os.Exit(1)
	}
	def := findDefinition(defs, name)
	if def == nil {
		fallback, err := findStackDefinition(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
			os.Exit(1)
		}
		def = &fallback
	}

	mgr, host, err := newCLIManager(cfg, registry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
		os.Exit(1)
	}
	mgr.DryRun = *dryRun
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	plan, err := mgr.Plan(ctx, []string{name})
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack plan: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Stack %s → minion %s (%d service(s), install root %s)\n",
		name, f.TargetMinion, len(def.Services), def.InstallRoot)
	for _, step := range plan.Steps {
		fmt.Printf("  [%-5s] %s: %s\n", step.Status, step.Title, step.Detail)
	}
	if *dryRun {
		if line, err := stackProvisionDryRun(ctx, mgr, *def); err != nil {
			fmt.Fprintf(os.Stderr, "argus stack: %v\n", err)
			os.Exit(1)
		} else if line != "" {
			fmt.Println("  " + line)
		}
		fmt.Println("\nDry run: no files or containers changed.")
		return
	}

	// A stack with a [provision] block creates its own minion first. This is
	// skipped when the target key is already accepted (idempotent re-apply).
	provisionLog, err := ensureStackTarget(ctx, mgr, cfg, *def)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argus stack apply: %v\n", err)
		os.Exit(1)
	}
	for _, line := range provisionLog {
		fmt.Println("  " + line)
	}

	database, dbErr := db.Open()
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "warning: local database unavailable: %v\n", dbErr)
		database = nil
	}
	if database != nil {
		defer database.Close()
	}
	runID := int64(0)
	if database != nil {
		runID, _ = database.SaveBootstrapRun(db.BootstrapRun{
			Host: host, Mode: "stack", Status: "running", StartedAt: time.Now(),
		})
	}
	run, err := mgr.Apply(ctx, []string{name})
	if err != nil {
		if database != nil && runID != 0 {
			_ = database.UpdateBootstrapRun(runID, "failed", "", err.Error())
		}
		fmt.Fprintf(os.Stderr, "argus stack apply: %v\n", err)
		os.Exit(1)
	}
	for _, line := range run.Log {
		fmt.Println("  " + line)
	}
	if database != nil {
		if runID != 0 {
			all := append(append([]string{}, provisionLog...), run.Log...)
			_ = database.UpdateBootstrapRun(runID, "success", strings.Join(all, "\n"), "")
		}
		for _, applied := range run.AppliedTargets {
			_ = database.SaveFormulaInstallation(db.FormulaInstallation{
				FormulaID: name, Target: applied.Target, State: applied.State,
				Version: f.Version, Status: "applied", AppliedAt: time.Now(),
			})
		}
		registerStackServices(database, def, cfg.Globals.Domain)
	}
	fmt.Printf("Applied stack %s on %s\n", name, f.TargetMinion)
}

// stackRegistryWithTarget loads the registry and optionally overrides the
// target for one stack, so --target does not require editing the file.
func stackRegistryWithTarget(name, target string) (*formula.Registry, []stack.Definition, error) {
	registry, err := formula.BuiltinRegistry()
	if err != nil {
		return nil, nil, err
	}
	defs, err := stack.LoadDir(stack.DefaultDir())
	if err != nil {
		return nil, nil, err
	}
	if target != "" {
		found := false
		for i := range defs {
			if defs[i].Name == name {
				// A defaulted minion_id follows --target; an explicit one wins.
				if p := defs[i].Provision; p != nil && (p.MinionID == "" || p.MinionID == defs[i].Target) {
					p.MinionID = target
				}
				defs[i].Target = target
				found = true
			}
		}
		if !found {
			builtins, err := stack.Builtins()
			if err != nil {
				return nil, nil, err
			}
			for _, def := range builtins {
				if def.Name == name {
					def.Target = target
					defs = append(defs, def)
					found = true
					break
				}
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("unknown stack %q", name)
		}
	}
	merged, err := formula.WithStacks(registry, defs)
	if err != nil {
		return nil, nil, err
	}
	return merged, defs, nil
}

func findDefinition(defs []stack.Definition, name string) *stack.Definition {
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

func findStackDefinition(name string) (stack.Definition, error) {
	if defs, err := stack.LoadDir(stack.DefaultDir()); err == nil {
		for _, def := range defs {
			if def.Name == name {
				return def, nil
			}
		}
	}
	defs, err := stack.Builtins()
	if err != nil {
		return stack.Definition{}, err
	}
	for _, def := range defs {
		if def.Name == name {
			return def, nil
		}
	}
	return stack.Definition{}, fmt.Errorf("unknown stack %q", name)
}

// stackServices and stackRoot derive display metadata from a rendered formula.
// Files are the source of truth so `show` agrees with what would be applied.
func stackServices(f formula.Formula) []struct {
	id, image, config string
	port              int
} {
	def, err := definitionForFormula(f)
	if err != nil {
		return nil
	}
	out := make([]struct {
		id, image, config string
		port              int
	}, 0, len(def.Services))
	for _, svc := range def.Services {
		conf := svc.ConfigPath
		out = append(out, struct {
			id, image, config string
			port              int
		}{svc.ID, svc.Image, conf, svc.Port})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func definitionForFormula(f formula.Formula) (stack.Definition, error) {
	if def, err := findStackDefinition(f.ID); err == nil {
		return def, nil
	}
	return stack.Definition{}, fmt.Errorf("definition not found")
}

func stackRoot(f formula.Formula) string {
	services := stackServices(f)
	if len(services) == 0 {
		return ""
	}
	root := services[0].config
	if idx := strings.LastIndex(root, "/"); idx > 0 {
		// <root>/<service>/config -> <root>
		if parent := filepath.Dir(filepath.Dir(root)); parent != "." {
			return parent
		}
	}
	return root
}

func stackSource(name string) string {
	for _, def := range mustUserDefs() {
		if def.Name == name {
			return def.Source
		}
	}
	return "built-in catalog"
}

func mustUserDefs() []stack.Definition {
	defs, _ := stack.LoadDir(stack.DefaultDir())
	return defs
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// newCLIManager builds the bootstrap manager from config and keychain values.
func newCLIManager(cfg *config.Config, registry *formula.Registry) (*bootstrap.Manager, string, error) {
	host := hostFromURL(cfg.Host)
	if host == "" {
		return nil, "", fmt.Errorf("no Proxmox host configured")
	}
	key := cfg.Bootstrap.SSHKey
	if key == "" {
		key = cfg.Globals.SSHKey
	}
	user := cfg.Bootstrap.SSHUser
	if user == "" {
		user = "root"
	}
	pass, _ := gkr.Load(gkr.KeySSHPassword)
	runner, err := bootstrap.NewSSHRunner(host, user, key, pass)
	if err != nil {
		return nil, "", err
	}
	masterID := cfg.Bootstrap.SaltMasterID
	if masterID == "" {
		masterID = "proxmox"
	}
	ip := resolveIP(host)
	apiURL := cfg.Bootstrap.SaltAPIURL
	if apiURL == "" {
		apiURL = "https://" + ip + ":8000"
	}
	apiUser := cfg.Bootstrap.SaltAPIUser
	if apiUser == "" {
		apiUser = "saltapi"
	}
	saltPass := cfg.Salt.Password
	if saltPass == "" {
		saltPass, _ = gkr.Load(gkr.KeySaltPassword)
	}
	var saltClient *api.SaltClient
	if cfg.Salt.URL != "" {
		saltClient, _ = api.NewSaltClient(cfg.Salt)
	}
	mgr, err := bootstrap.NewManager(runner, api.NewClient(cfg), saltClient, registry,
		masterID, apiURL, apiUser, saltPass, ip)
	if err != nil {
		return nil, "", err
	}
	return mgr, host, nil
}

// registerStackServices makes installed stack endpoints visible to the
// Services, health-check, DNS, and Plex/Servarr screens.
func registerStackServices(database *db.DB, def *stack.Definition, domain string) {
	if database == nil || def == nil {
		return
	}
	existing, _ := database.Services()
	host := def.Target
	if domain != "" {
		host = def.Target + "." + domain
	}
	for _, svc := range def.Services {
		if svc.Port == 0 {
			continue
		}
		duplicate := false
		for _, row := range existing {
			if row.Port == svc.Port && strings.EqualFold(row.Host, host) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		_, _ = database.SaveService(db.Service{
			Name: svc.Name, Node: def.Node, Host: host, Port: svc.Port,
			Scheme: "http", Notes: "stack " + def.Name, Enabled: true,
		})
	}
}
