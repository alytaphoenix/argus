package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/formula"
)

// Status is the outcome of one plan step.
type Status string

const (
	StatusOK      Status = "ok"
	StatusTodo    Status = "todo"
	StatusWarning Status = "warning"
	StatusBlocked Status = "blocked"
)

// Step is one human-readable readiness check.
type Step struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Phase    string   `json:"phase"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail"`
	Commands []string `json:"commands,omitempty"`
}

// FormulaStatus is the registry view for one formula.
type FormulaStatus struct {
	Formula   formula.Formula `json:"formula"`
	Installed bool            `json:"installed"`
	Detail    string          `json:"detail"`
}

// Plan is the read-only result of inspecting a Proxmox host.
type Plan struct {
	Host        string          `json:"host"`
	GeneratedAt time.Time       `json:"generated_at"`
	Steps       []Step          `json:"steps"`
	Formulas    []FormulaStatus `json:"formulas"`
	Ready       bool            `json:"ready"`
}

// Run is the result of applying a plan.
type Run struct {
	Plan            Plan     `json:"plan"`
	Applied         []string `json:"applied"`
	SaltAPIPassword string   `json:"-"` // never serialize
	Log             []string `json:"log"`
}

// Manager owns the bootstrap dependencies.
type Manager struct {
	SSH          Runner
	Proxmox      *api.Client
	Salt         *api.SaltClient
	Registry     *formula.Registry
	MasterID     string
	SaltAPIURL   string
	SaltAPIUser  string
	SaltPassword string
	HostIP       string
	DryRun       bool
	ApplyTimeout time.Duration
}

// NewManager constructs a manager with safe defaults.
func NewManager(ssh Runner, proxmox *api.Client, salt *api.SaltClient, registry *formula.Registry, masterID, saltAPIURL, saltAPIUser, saltPassword, hostIP string) (*Manager, error) {
	if registry == nil {
		var err error
		registry, err = formula.BuiltinRegistry()
		if err != nil {
			return nil, err
		}
	}
	if masterID == "" {
		if proxmox != nil {
			masterID = "proxmox"
		}
	}
	if saltAPIURL == "" && hostIP != "" {
		saltAPIURL = "https://" + hostIP + ":8000"
	}
	if saltAPIUser == "" {
		saltAPIUser = "saltapi"
	}
	return &Manager{
		SSH: ssh, Proxmox: proxmox, Salt: salt, Registry: registry,
		MasterID: masterID, SaltAPIURL: saltAPIURL, SaltAPIUser: saltAPIUser,
		SaltPassword: saltPassword, HostIP: hostIP,
		ApplyTimeout: 10 * time.Minute,
	}, nil
}

// Plan inspects the target without changing it.
func (m *Manager) Plan(ctx context.Context, selected []string) (Plan, error) {
	p := Plan{GeneratedAt: time.Now()}
	if m.HostIP != "" {
		p.Host = m.HostIP
	}
	add := func(s Step) { p.Steps = append(p.Steps, s) }

	// Proxmox inventory.
	if m.Proxmox == nil {
		add(Step{ID: "proxmox", Title: "Proxmox inventory", Phase: "platform", Status: StatusBlocked, Detail: "Proxmox client is not configured"})
	} else {
		nodes, err := m.Proxmox.Nodes(ctx)
		if err != nil {
			add(Step{ID: "proxmox", Title: "Proxmox inventory", Phase: "platform", Status: StatusBlocked, Detail: "Proxmox unreachable: " + err.Error()})
		} else {
			guests := 0
			for _, n := range nodes {
				if gs, gErr := m.Proxmox.Guests(ctx, n.Name); gErr == nil {
					guests += len(gs)
				}
			}
			add(Step{ID: "proxmox", Title: "Proxmox inventory", Phase: "platform", Status: StatusOK,
				Detail: fmt.Sprintf("%d node(s), %d guest(s) discovered", len(nodes), guests)})
		}
	}

	// SSH reachability.
	sshOK := false
	if m.SSH == nil {
		add(Step{ID: "ssh", Title: "Host access", Phase: "platform", Status: StatusBlocked,
			Detail: "no SSH runner configured (set [globals] ssh_user/key or --ssh-key)"})
	} else if err := m.SSH.Probe(ctx); err != nil {
		add(Step{ID: "ssh", Title: "Host access", Phase: "platform", Status: StatusBlocked, Detail: err.Error()})
	} else {
		sshOK = true
		add(Step{ID: "ssh", Title: "Host access", Phase: "platform", Status: StatusOK, Detail: "SSH reachable as root"})
	}

	// Salt packages.
	saltInstalled := false
	if sshOK {
		probe := `if [ -x /opt/argus-salt/venv/bin/salt-master ] && [ -x /opt/argus-salt/venv/bin/salt-api ]; then echo installed; else echo missing; fi`
		res, err := m.SSH.Run(ctx, probe)
		if err != nil {
			add(Step{ID: "salt-packages", Title: "Salt packages", Phase: "salt", Status: StatusBlocked, Detail: err.Error()})
		} else if strings.Contains(res.Stdout, "installed") {
			saltInstalled = true
			detail := "Salt 3008.x runtime installed in /opt/argus-salt"
			if out, _ := m.SSH.Run(ctx, "systemctl is-active --quiet argus-salt-master argus-salt-api && echo active || echo inactive"); strings.Contains(out.Stdout, "inactive") {
				detail += " (services inactive)"
			}
			add(Step{ID: "salt-packages", Title: "Salt runtime", Phase: "salt", Status: StatusOK, Detail: detail})
		} else {
			add(Step{ID: "salt-packages", Title: "Salt runtime", Phase: "salt", Status: StatusTodo, Detail: "install Salt 3008.x in an isolated venv"})
		}
	}

	// Salt API / keys.
	accepted := 0
	if m.Salt != nil {
		keys, err := m.Salt.ListKeys(ctx)
		if err != nil {
			add(Step{ID: "salt-api", Title: "Salt API", Phase: "salt", Status: StatusWarning, Detail: "configured but unreachable: " + err.Error()})
		} else {
			accepted = len(keys.Accepted)
			st := StatusOK
			if accepted == 0 {
				st = StatusWarning
			}
			add(Step{ID: "salt-api", Title: "Salt API", Phase: "salt", Status: st,
				Detail: fmt.Sprintf("reachable, %d accepted minion(s)", accepted)})
		}
	} else {
		add(Step{ID: "salt-api", Title: "Salt API", Phase: "salt", Status: StatusTodo, Detail: "will be configured by apply"})
	}

	// State tree.
	if sshOK {
		res, _ := m.SSH.Run(ctx, "test -d /srv/salt/argus && echo present || echo missing")
		if strings.Contains(res.Stdout, "present") {
			add(Step{ID: "state-tree", Title: "Formula state tree", Phase: "salt", Status: StatusOK, Detail: "/srv/salt/argus present"})
		} else {
			add(Step{ID: "state-tree", Title: "Formula state tree", Phase: "salt", Status: StatusTodo, Detail: "formula files not installed yet"})
		}
	}

	// Formula registry.
	resolved, err := m.Registry.Resolve(selected)
	if err != nil {
		return p, err
	}
	for _, f := range resolved {
		fs := FormulaStatus{Formula: f, Detail: "available"}
		if f.Kind == formula.KindIntegration {
			fs.Detail = "external service — configure its URL/token in argus"
		} else if m.Salt != nil {
			// We cannot reliably infer state application from the API without a
			// job history query; report availability and let apply record status.
			fs.Detail = "ready to apply on " + m.MasterID
		}
		p.Formulas = append(p.Formulas, fs)
	}

	p.Ready = sshOK && (saltInstalled || m.Salt != nil)
	return p, nil
}

// Apply installs Salt when needed, materialises the selected formulas, and
// applies them on the master minion. It is idempotent and never deletes VMs,
// storage, or guests.
func (m *Manager) Apply(ctx context.Context, selected []string) (Run, error) {
	run := Run{}
	plan, err := m.Plan(ctx, selected)
	if err != nil {
		return run, err
	}
	run.Plan = plan
	if m.DryRun {
		return run, nil
	}
	if m.SSH == nil {
		return run, fmt.Errorf("SSH access is required for the first bootstrap")
	}
	if m.MasterID == "" {
		return run, fmt.Errorf("salt master id is required")
	}

	// Install Salt if it is not already present. The script is idempotent.
	if err := m.ensureSalt(ctx); err != nil {
		return run, err
	}
	// ensureSalt restarts salt-master/salt-api when it applies units or config;
	// any cached API token from the pre-apply plan is now invalid.
	m.Salt = nil
	run.Log = append(run.Log, "salt master/api configured")

	// Write built-in formula files.
	resolved, err := m.Registry.Resolve(selected)
	if err != nil {
		return run, err
	}
	files, err := FormulaFiles(resolved)
	if err != nil {
		return run, err
	}
	if len(files) > 0 {
		if _, err := m.SSH.Run(ctx, WriteFormulaScript(files)); err != nil {
			return run, fmt.Errorf("write formula files: %w", err)
		}
		run.Log = append(run.Log, fmt.Sprintf("wrote %d formula file(s) to /srv/salt/argus", len(files)))
	}

	// Recreate the Salt client with the authoritative password from the host.
	// This also forces a fresh API login after the service restart above.
	password, passErr := m.readSaltAPIPassword(ctx)
	if passErr != nil {
		password = m.SaltPassword
	}
	if password == "" {
		return run, fmt.Errorf("salt API password unavailable: %v", passErr)
	}
	m.SaltPassword = password
	client, err := api.NewSaltClient(saltConfig(m.SaltAPIURL, m.SaltAPIUser, password))
	if err != nil {
		return run, err
	}
	m.Salt = client
	run.SaltAPIPassword = password

	// Apply each state in dependency order and wait for completion.
	for _, state := range States(resolved) {
		jid, err := m.Salt.ApplyState(ctx, m.MasterID, state)
		if err != nil {
			return run, fmt.Errorf("apply %s: %w", state, err)
		}
		run.Log = append(run.Log, "apply "+state+" jid="+jid)
		if err := m.waitJob(ctx, jid); err != nil {
			return run, fmt.Errorf("apply %s: %w", state, err)
		}
		run.Applied = append(run.Applied, state)
	}
	return run, nil
}

func (m *Manager) ensureSalt(ctx context.Context) error {
	probe := `if [ -x /opt/argus-salt/venv/bin/salt-master ] && [ -x /opt/argus-salt/venv/bin/salt-api ] && systemctl is-active --quiet argus-salt-master argus-salt-api; then echo ready; else echo missing; fi`
	if res, err := m.SSH.Run(ctx, probe); err == nil && strings.Contains(res.Stdout, "ready") {
		return nil
	}
	script, err := SaltInstallScript(SaltInstallOptions{
		MasterID: m.MasterID, APIUser: m.SaltAPIUser, APIPort: 8000,
		HostIP: m.HostIP, Interface: "0.0.0.0", InstallMinion: true, UsePVECA: true,
	})
	if err != nil {
		return err
	}
	res, err := m.SSH.Run(ctx, script)
	if err != nil {
		return fmt.Errorf("salt install: %w", err)
	}
	if !strings.Contains(res.Stdout, "ARGUS_SALT_MASTER=ok") || !strings.Contains(res.Stdout, "ARGUS_SALT_API=ok") {
		return fmt.Errorf("salt install did not report success: %s", strings.TrimSpace(res.Stdout))
	}
	return nil
}

func (m *Manager) readSaltAPIPassword(ctx context.Context) (string, error) {
	res, err := m.SSH.Run(ctx, SaltAPIPasswordCommand())
	if err != nil {
		return "", fmt.Errorf("read salt api password: %w", err)
	}
	pass := strings.TrimSpace(res.Stdout)
	if pass == "" {
		return "", fmt.Errorf("salt api password is empty")
	}
	return pass, nil
}

func (m *Manager) waitJob(ctx context.Context, jid string) error {
	timeout := m.ApplyTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		done, ok, failed, err := m.Salt.JobResult(ctx, jid)
		if err != nil {
			return err
		}
		if done {
			if !ok {
				return fmt.Errorf("failed states: %s", strings.Join(failed, ", "))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for job %s", jid)
}

func saltConfig(apiURL, user, password string) config.SaltConfig {
	return config.SaltConfig{
		URL:        apiURL,
		Username:   user,
		Password:   password,
		Eauth:      "pam",
		PillarBase: "/srv/pillar",
		StateBase:  "/srv/salt",
	}
}
