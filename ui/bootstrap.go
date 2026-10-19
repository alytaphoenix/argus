package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/bootstrap"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	"github.com/alytaphoenix/argus/formula"
	gkr "github.com/alytaphoenix/argus/keyring"
)

type bootstrapPlanMsg struct {
	plan     bootstrap.Plan
	installs map[string]db.FormulaInstallation
	err      error
}

type bootstrapApplyDoneMsg struct {
	run   bootstrap.Run
	runID int64
	err   error
}

// BootstrapModel is the top-level platform readiness and formula registry
// screen. It is intentionally read-mostly: plan/refresh are safe, apply asks
// for confirmation, and the same engine is available from `argus bootstrap`.
type BootstrapModel struct {
	client        *api.Client
	salt          *api.SaltClient
	database      *db.DB
	cfg           config.Config
	sshPassword   string
	registry      *formula.Registry
	installations map[string]db.FormulaInstallation

	plan       *bootstrap.Plan
	loading    bool
	applying   bool
	err        error
	notice     string
	cursor     int
	showAll    bool
	selected   map[string]bool
	confirm    bool
	confirmIDs []string
	width      int
	height     int
	spinner    spinner.Model
}

// NewBootstrapModel constructs the platform bootstrap/registry screen.
func NewBootstrapModel(client *api.Client, salt *api.SaltClient, database *db.DB, cfg config.Config, sshPassword string) BootstrapModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	registry, _ := formula.BuiltinRegistry()
	return BootstrapModel{
		client: client, salt: salt, database: database, cfg: cfg,
		sshPassword: sshPassword, registry: registry,
		installations: map[string]db.FormulaInstallation{},
		selected:      map[string]bool{}, showAll: true, spinner: sp,
		loading: true,
	}
}

func (m BootstrapModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCmd())
}

func (m BootstrapModel) manager() (*bootstrap.Manager, error) {
	host := hostFromURL(m.cfg.Host)
	if host == "" {
		return nil, fmt.Errorf("Proxmox host is not configured")
	}
	user := m.cfg.Bootstrap.SSHUser
	if user == "" {
		user = "root"
	}
	key := m.cfg.Bootstrap.SSHKey
	if key == "" {
		key = m.cfg.Globals.SSHKey
	}
	runner, err := bootstrap.NewSSHRunner(host, user, key, m.sshPassword)
	if err != nil {
		return nil, err
	}
	masterID := m.cfg.Bootstrap.SaltMasterID
	if masterID == "" {
		masterID = "proxmox"
	}
	ip := resolveHostIP(host)
	apiURL := m.cfg.Bootstrap.SaltAPIURL
	if apiURL == "" {
		apiURL = "https://" + ip + ":8000"
	}
	apiUser := m.cfg.Bootstrap.SaltAPIUser
	if apiUser == "" {
		apiUser = "saltapi"
	}
	return bootstrap.NewManager(runner, m.client, m.salt, m.registry, masterID, apiURL, apiUser, m.cfg.Salt.Password, ip)
}

func (m BootstrapModel) loadCmd() tea.Cmd {
	client, salt, database, cfg, registry, sshPass := m.client, m.salt, m.database, m.cfg, m.registry, m.sshPassword
	return func() tea.Msg {
		model := BootstrapModel{client: client, salt: salt, database: database, cfg: cfg, registry: registry, sshPassword: sshPass}
		mgr, err := model.manager()
		if err != nil {
			return bootstrapPlanMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		plan, err := mgr.Plan(ctx, nil)
		if err != nil {
			return bootstrapPlanMsg{err: err}
		}
		installs := map[string]db.FormulaInstallation{}
		if database != nil {
			if rows, dbErr := database.FormulaInstallations(); dbErr == nil {
				for _, row := range rows {
					installs[row.FormulaID] = row
				}
			}
		}
		return bootstrapPlanMsg{plan: plan, installs: installs}
	}
}

func (m BootstrapModel) applyCmd(ids []string) tea.Cmd {
	client, salt, database, cfg, registry, sshPass := m.client, m.salt, m.database, m.cfg, m.registry, m.sshPassword
	host := hostFromURL(cfg.Host)
	if m.plan != nil && m.plan.Host != "" {
		host = m.plan.Host
	}
	var runID int64
	if database != nil {
		runID, _ = database.SaveBootstrapRun(db.BootstrapRun{
			Host: host, Mode: "apply", Status: "running", StartedAt: time.Now(),
		})
	}
	return func() tea.Msg {
		model := BootstrapModel{client: client, salt: salt, database: database, cfg: cfg, registry: registry, sshPassword: sshPass}
		mgr, err := model.manager()
		if err != nil {
			if database != nil && runID != 0 {
				_ = database.UpdateBootstrapRun(runID, "failed", "", err.Error())
			}
			return bootstrapApplyDoneMsg{runID: runID, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		run, err := mgr.Apply(ctx, ids)
		return bootstrapApplyDoneMsg{run: run, runID: runID, err: err}
	}
}

func (m BootstrapModel) Update(msg tea.Msg) (BootstrapModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case bootstrapPlanMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.plan = &msg.plan
			m.installations = msg.installs
			m.err = nil
			m.clampCursor()
		}
		return m, nil
	case bootstrapApplyDoneMsg:
		m.applying = false
		m.confirm = false
		if msg.err != nil {
			if m.database != nil && msg.runID != 0 {
				_ = m.database.UpdateBootstrapRun(msg.runID, "failed", "", msg.err.Error())
			}
			m.err = msg.err
			return m, nil
		}
		if msg.run.SaltAPIPassword != "" {
			_ = gkr.Save(gkr.KeySaltPassword, msg.run.SaltAPIPassword)
		}
		m.recordApplicaton(msg)
		m.err = nil
		m.notice = fmt.Sprintf("applied %d state(s)", len(msg.run.Applied))
		return m, m.loadCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m BootstrapModel) handleKey(msg tea.KeyMsg) (BootstrapModel, tea.Cmd) {
	if m.confirm {
		switch msg.String() {
		case "y", "Y", "enter":
			ids := append([]string(nil), m.confirmIDs...)
			m.confirm = false
			m.confirmIDs = nil
			if len(ids) == 0 {
				return m, nil
			}
			m.applying = true
			m.err = nil
			m.notice = "applying..."
			return m, m.applyCmd(ids)
		case "n", "N", "esc":
			m.confirm = false
			m.confirmIDs = nil
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "b":
		return m, func() tea.Msg { return backMsg{} }
	case "x", "r", "R":
		if !m.applying {
			m.loading = true
			m.err = nil
			return m, m.loadCmd()
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visibleFormulas())-1 {
			m.cursor++
		}
	case "f":
		m.showAll = !m.showAll
		m.cursor = 0
	case " ":
		if f, ok := m.currentFormula(); ok && f.Installable() {
			m.selected[f.ID] = !m.selected[f.ID]
		}
	case "a":
		if !m.applying {
			var ids []string
			for _, f := range m.visibleFormulas() {
				if m.selected[f.ID] && f.Installable() {
					ids = append(ids, f.ID)
				}
			}
			if len(ids) == 0 {
				m.err = fmt.Errorf("select one or more installable formulas with space")
				return m, nil
			}
			m.confirm = true
			m.confirmIDs = ids
		}
	case "A":
		if !m.applying {
			m.confirm = true
			m.confirmIDs = append([]string(nil), formula.BaselineIDs...)
		}
	}
	return m, nil
}

func (m BootstrapModel) recordApplicaton(msg bootstrapApplyDoneMsg) {
	if m.database == nil {
		return
	}
	planJSON, _ := json.Marshal(msg.run.Plan)
	if msg.runID != 0 {
		_ = m.database.UpdateBootstrapRun(msg.runID, "success", strings.Join(msg.run.Log, "\n"), "")
	}
	masterID := m.cfg.Bootstrap.SaltMasterID
	if masterID == "" {
		masterID = "proxmox"
	}
	apiURL := m.cfg.Bootstrap.SaltAPIURL
	if apiURL == "" && m.plan != nil {
		apiURL = "https://" + m.plan.Host + ":8000"
	}
	_ = m.database.SavePlatformProfile(db.PlatformProfile{
		Host: msg.run.Plan.Host, SaltMasterID: masterID, SaltAPIURL: apiURL,
		SaltAPIUser: m.cfg.Bootstrap.SaltAPIUser, SaltVersion: "3008.x",
		Status: "ready", DetailsJSON: string(planJSON), LastChecked: time.Now(),
	})
	for _, state := range msg.run.Applied {
		for _, f := range m.registry.List() {
			if f.StateName() != state {
				continue
			}
			_ = m.database.SaveFormulaInstallation(db.FormulaInstallation{
				FormulaID: f.ID, Target: masterID, State: state, Version: f.Version,
				Status: "applied", AppliedAt: time.Now(),
			})
		}
	}
}

func (m BootstrapModel) visibleFormulas() []formula.Formula {
	all := m.registry.List()
	if m.showAll {
		return all
	}
	var out []formula.Formula
	for _, f := range all {
		if f.Installable() {
			out = append(out, f)
		}
	}
	return out
}

func (m *BootstrapModel) clampCursor() {
	n := len(m.visibleFormulas())
	if m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

func (m BootstrapModel) currentFormula() (formula.Formula, bool) {
	fs := m.visibleFormulas()
	if m.cursor < 0 || m.cursor >= len(fs) {
		return formula.Formula{}, false
	}
	return fs[m.cursor], true
}

func (m BootstrapModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("⚡ argus") + styleDim.Render(" › ") + styleBold.Render("bootstrap") + "\n\n")

	if m.loading {
		sb.WriteString("  " + m.spinner.View() + " inspecting platform...\n")
		return sb.String()
	}
	if m.err != nil {
		sb.WriteString(styleError.Render("  ✗ "+sanitizeText(m.err.Error())) + "\n\n")
	}
	if m.notice != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+sanitizeText(m.notice)) + "\n\n")
	}

	if m.plan != nil {
		sb.WriteString(styleBold.Render("  Platform readiness") + styleDim.Render("  "+m.plan.Host) + "\n")
		for _, step := range m.plan.Steps {
			sb.WriteString(fmt.Sprintf("  %s  %-24s %s\n",
				bootstrapStatusStyle(step.Status), step.Title, sanitizeText(step.Detail)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(styleBold.Render("  Formula registry") + styleDim.Render(
		fmt.Sprintf("  %d total  space select  a apply  A baseline  f filter", len(m.registry.List()))) + "\n")
	if !m.showAll {
		sb.WriteString(styleDim.Render("  showing installable formulas only") + "\n")
	}
	fs := m.visibleFormulas()
	if len(fs) == 0 {
		sb.WriteString(styleDim.Render("  no formulas match this filter") + "\n")
	} else {
		hdr := fmt.Sprintf("  %-3s %-18s %-12s %-10s  %s", "", "ID", "CATEGORY", "STATUS", "DETAIL")
		sb.WriteString(styleDim.Render(hdr) + "\n")
		for i, f := range fs {
			mark := " "
			if m.selected[f.ID] {
				mark = "●"
			}
			status := "available"
			detail := f.Description
			if f.Kind == formula.KindIntegration {
				status = "external"
			} else if inst, ok := m.installations[f.ID]; ok {
				status = inst.Status
				detail = "applied " + inst.AppliedAt.Format("2006-01-02 15:04")
			}
			if f.Kind == formula.KindBuiltin {
				detail = f.Description
			}
			row := fmt.Sprintf("  %s   %-18s %-12s %-10s  %s",
				mark, truncate(f.ID, 18), truncate(f.Category, 12), truncate(status, 10), truncate(detail, 60))
			if i == m.cursor {
				sb.WriteString(styleSelected.Render(row) + "\n")
			} else {
				sb.WriteString(row + "\n")
			}
		}
	}

	sb.WriteString("\n")
	if m.confirm {
		sb.WriteString(styleBold.Render("  Apply "+fmt.Sprintf("%d formula(s)", len(m.confirmIDs))+"?") +
			styleDim.Render("  "+strings.Join(m.confirmIDs, ", ")) + "\n")
		sb.WriteString(styleHelp.Render("  y/enter confirm  n/esc cancel") + "\n")
	} else if m.applying {
		sb.WriteString("  " + m.spinner.View() + " applying states...\n")
	} else {
		sb.WriteString(styleHelp.Render("  x/r plan+refresh  space select  a apply selected  A apply baseline  f filter  b back  q quit") + "\n")
	}
	return sb.String()
}

func bootstrapStatusStyle(s bootstrap.Status) string {
	switch s {
	case bootstrap.StatusOK:
		return styleRunning.Render("[ OK ]")
	case bootstrap.StatusTodo:
		return stylePaused.Render("[ TODO ]")
	case bootstrap.StatusWarning:
		return stylePaused.Render("[ WARN ]")
	case bootstrap.StatusBlocked:
		return styleError.Render("[BLOCK]")
	default:
		return styleDim.Render("[ ?? ]")
	}
}

func resolveHostIP(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	if ips, err := net.LookupHost(host); err == nil && len(ips) > 0 {
		return ips[0]
	}
	return host
}
