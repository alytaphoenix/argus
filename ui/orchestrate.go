package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
)

// bsvcStatus represents the live check status of one bootstrap service.
type bsvcStatus int

const (
	bsvcUnknown       bsvcStatus = iota
	bsvcChecking                 // check in progress
	bsvcOK                       // reachable + configured
	bsvcPartial                  // reachable but not fully set up
	bsvcMissing                  // not reachable / not deployed
	bsvcNotConfigured            // not in config.toml
	bsvcSkipped                  // user skipped
)

// bootstrapService describes one homelab service in the bootstrap order.
type bootstrapService struct {
	id           string
	name         string
	description  string
	status       bsvcStatus
	detail       string // human-readable status detail
	errMsg       string
	provisioning bool // Salt apply in progress
	ingesting    bool // ingest in progress
}

// orchBootstrapActionDialog is the action menu shown when user presses enter on a service.
type orchBootstrapActionDialog struct {
	service string
	status  bsvcStatus
	opts    []string
	cursor  int
	detail  string // optional advisory note shown below service name
}

// orchProvisionConfirm is shown after the provision form is submitted, before running state.apply.
type orchProvisionConfirm struct {
	svcID         string
	svcName       string
	target        string
	state         string
	derivedURL    string // non-empty when a bootstrapSpec can derive the URL from target+domain
	targetWarning string // non-empty when target doesn't match any accepted Salt minion
}

// discoveredGuest is one Proxmox guest found during cluster discovery, tagged
// with whether argus itself provisioned it. Salt-minion matching is done
// lazily at point of use (against the current m.keys.Accepted) rather than
// stored here, since accepted-key data may not be loaded yet when discovery
// runs — they're independent async commands batched together at startup.
type discoveredGuest struct {
	Guest        api.Guest
	ArgusManaged bool // has a row in provisioned_vms
}

// bootstrapSvcSpec holds URL-derivation conventions for a bootstrap service.
// After a successful Salt provision the derived URL is written to config.toml
// and App is told to rebuild the live client.
type bootstrapSvcSpec struct {
	protocol    string
	port        int
	deps        []string                     // IDs of services that must be OK first (advisory)
	patchConfig func(*config.Config, string) // writes derived URL into cfg
}

func (s bootstrapSvcSpec) deriveURL(target, domain string) string {
	if s.port == 0 || s.protocol == "" {
		return ""
	}
	host := target
	if domain != "" {
		host = target + "." + domain
	}
	return fmt.Sprintf("%s://%s:%d", s.protocol, host, s.port)
}

// bootstrapSpecs maps service IDs to their URL conventions.
var bootstrapSpecs = map[string]bootstrapSvcSpec{
	"salt_master": {
		protocol:    "https",
		port:        8000,
		patchConfig: func(cfg *config.Config, url string) { cfg.Salt.URL = url },
	},
	"dns": {
		protocol:    "http",
		port:        5380,
		deps:        []string{"salt_master"},
		patchConfig: func(cfg *config.Config, url string) { cfg.Technitium.URL = url },
	},
	"certs": {
		protocol:    "https",
		port:        8443,
		deps:        []string{"dns"},
		patchConfig: func(cfg *config.Config, url string) { cfg.StepCA.URL = url },
	},
	"truenas": {
		protocol:    "https",
		port:        443,
		deps:        []string{"salt_master"},
		patchConfig: func(cfg *config.Config, url string) { cfg.TrueNAS.URL = url },
	},
	"plex": {
		protocol:    "http",
		port:        32400,
		deps:        []string{"truenas"},
		patchConfig: func(cfg *config.Config, url string) { cfg.Plex.URL = url },
	},
	"hermes": {
		protocol:    "http",
		port:        7583,
		deps:        []string{"salt_master"},
		patchConfig: func(cfg *config.Config, url string) { cfg.Hermes.URL = url },
	},
}

type orchTab int

const (
	orchTabKeys      orchTab = iota // pending/rejected minion keys
	orchTabPending                  // VMs awaiting salt apply
	orchTabPillar                   // cluster pillar entries
	orchTabBootstrap                // ordered bootstrap steps
)

var orchTabLabels = []string{"Keys", "Pending", "Pillar", "Bootstrap"}

// orchConfirm holds state for the confirmation dialog shown before key
// acceptance/rejection or a cluster pillar entry delete.
type orchConfirm struct {
	action    string // "accept", "reject", or "delete_pillar"
	minion    string
	profile   string // matched salt profile name, or ""
	vmid      int    // matched provisioned_vm VMID, or 0
	pillarKey string // pillar key being deleted, for action == "delete_pillar"
}

// orchPillarForm is the inline form for adding a pillar entry or a bootstrap
// provision target+state pair.
type orchPillarForm struct {
	inputs     []textinput.Model
	focused    int
	knownHints []string // accepted minion names, shown as hints under target field
}

// OrchestrateModel manages the Salt orchestration screen: key acceptance,
// pending VM salt applies, cluster pillar management, and bootstrap execution.
type OrchestrateModel struct {
	saltClient    *api.SaltClient
	database      *db.DB
	saltCfg       config.SaltConfig
	truenas       *api.TrueNASClient
	hermesClient  *api.HermesClient
	dnsClient     *api.TechnitiumClient
	caClient      *api.StepCAClient
	plexClient    *api.PlexClient
	proxmoxClient *api.Client
	cfg           config.Config

	tab     orchTab
	cursor  int
	keys    *api.SaltKeys
	pending []db.ProvisionedVM
	pillar  []db.ClusterPillarEntry

	// Cluster discovery (Bootstrap tab) — live Proxmox inventory, refreshed
	// alongside every service check so Bootstrap knows what already exists
	// before suggesting a Salt provision action.
	clusterGuests           []discoveredGuest
	clusterDiscovered       bool
	clusterDiscoverErr      error
	clusterUnreachableNodes int   // nodes whose Guests() call failed on the last successful scan
	clusterDBErr            error // ProvisionedVMs() failure on the last scan — managed/unmanaged split may be inaccurate

	acting  bool
	notice  string
	err     error
	confirm *orchConfirm
	form    *orchPillarForm // pillar tab form

	// Bootstrap tab state
	bootstrapServices   []bootstrapService
	bsvcActionDialog    *orchBootstrapActionDialog
	provisionForm       *orchPillarForm       // bootstrap provision (target, state)
	provisionConfirm    *orchProvisionConfirm // confirm before running state.apply
	provisioningFor     string                // service id being provisioned
	provisionQueue      []string              // remaining service IDs to provision in order
	activeJID           string                // JID of currently-running provision job
	provisionDerivedURL string                // URL derived from target+domain, carried through provision→done

	width, height int
	spinner       spinner.Model
}

func NewOrchestrateModel(
	saltClient *api.SaltClient, database *db.DB, saltCfg config.SaltConfig,
	truenas *api.TrueNASClient, hermesClient *api.HermesClient, cfg config.Config,
	dnsClient *api.TechnitiumClient, caClient *api.StepCAClient, plexClient *api.PlexClient,
	proxmoxClient *api.Client,
) OrchestrateModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return OrchestrateModel{
		saltClient:        saltClient,
		database:          database,
		saltCfg:           saltCfg,
		truenas:           truenas,
		hermesClient:      hermesClient,
		dnsClient:         dnsClient,
		caClient:          caClient,
		plexClient:        plexClient,
		proxmoxClient:     proxmoxClient,
		cfg:               cfg,
		spinner:           s,
		bootstrapServices: defaultBootstrapServices(),
	}
}

func defaultBootstrapServices() []bootstrapService {
	return []bootstrapService{
		{id: "salt_master", name: "Salt master", description: "Salt automation backbone — required for all provisioning"},
		{id: "dns", name: "DNS (Technitium)", description: "Authoritative DNS for the homelab domain"},
		{id: "certs", name: "Certificates (step-ca)", description: "Internal CA for TLS certificates"},
		{id: "truenas", name: "TrueNAS NFS", description: "Network storage — pools, datasets, NFS shares"},
		{id: "plex", name: "Plex Media Server", description: "Media server with library configuration"},
		{id: "servarr", name: "Servarr suite", description: "Sonarr/Radarr/Prowlarr and companion apps"},
		{id: "hermes", name: "Hermes agent", description: "AI automation agent for cluster management"},
		{id: "signal", name: "Signal CLI", description: "Signal messenger gateway via Hermes"},
	}
}

func (m OrchestrateModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadKeysCmd(), m.loadPendingCmd(), m.loadPillarCmd(), m.keyPollTickCmd())
}

func (m OrchestrateModel) keyPollTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return orchKeyTickMsg{} })
}

func (m OrchestrateModel) loadKeysCmd() tea.Cmd {
	client := m.saltClient
	return func() tea.Msg {
		if client == nil {
			return orchKeysLoadedMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		keys, err := client.ListKeys(ctx)
		return orchKeysLoadedMsg{keys: keys, err: err}
	}
}

func (m OrchestrateModel) loadPendingCmd() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return orchPendingLoadedMsg{}
		}
		vms, err := database.PendingSaltVMs()
		return orchPendingLoadedMsg{vms: vms, err: err}
	}
}

func (m OrchestrateModel) loadPillarCmd() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return orchPillarLoadedMsg{}
		}
		entries, err := database.ClusterPillar()
		return orchPillarLoadedMsg{entries: entries, err: err}
	}
}

func (m OrchestrateModel) applyKeyActionCmd(action, minion, profileName string, vmid int) tea.Cmd {
	saltClient := m.saltClient
	database := m.database
	saltCfg := m.saltCfg
	dnsClient := m.dnsClient
	domain := m.cfg.Globals.Domain
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if action == "accept" {
			if err := saltClient.AcceptKey(ctx, minion); err != nil {
				return orchAcceptDoneMsg{minion: minion, action: action, err: err}
			}
		} else {
			if err := saltClient.RejectKey(ctx, minion); err != nil {
				return orchAcceptDoneMsg{minion: minion, action: action, err: err}
			}
			return orchAcceptDoneMsg{minion: minion, action: action}
		}

		// Key accepted — best-effort DNS A-record from provisioned_vms IP lookup.
		if dnsClient != nil && domain != "" && database != nil {
			if vms, err := database.ProvisionedVMs(); err == nil {
				for _, vm := range vms {
					if strings.EqualFold(vm.Name, minion) && vm.VMIP != "" {
						aCtx, aCancel := context.WithTimeout(context.Background(), 10*time.Second)
						_ = dnsClient.AddRecord(aCtx, api.DNSAddOptions{
							Zone:      domain,
							Domain:    minion + "." + domain,
							Type:      "A",
							IPAddress: vm.VMIP,
							Overwrite: true,
						})
						aCancel()
						break
					}
				}
			}
		}

		// After accepting: apply profile if matched.
		if profileName == "" || database == nil {
			return orchAcceptDoneMsg{minion: minion, action: action}
		}
		failed := applySaltProfile(ctx, saltClient, database, saltCfg, minion, profileName, vmid)
		return orchAcceptDoneMsg{minion: minion, action: action, failedStates: failed}
	}
}

func (m OrchestrateModel) applyPendingVMCmd(vm db.ProvisionedVM) tea.Cmd {
	saltClient := m.saltClient
	database := m.database
	saltCfg := m.saltCfg
	return func() tea.Msg {
		if saltClient == nil {
			return orchAcceptDoneMsg{minion: vm.Name, action: "apply", err: fmt.Errorf("Salt not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		failed := applySaltProfile(ctx, saltClient, database, saltCfg, vm.Name, vm.SaltProfile, vm.VMID)
		return orchAcceptDoneMsg{minion: vm.Name, action: "apply", failedStates: failed}
	}
}

// applySaltProfile writes the per-profile pillar (if any) to the master, then
// applies each state in the profile to the minion. Updates the DB on success.
func applySaltProfile(ctx context.Context, saltClient *api.SaltClient, database *db.DB, cfg config.SaltConfig, minion, profileName string, vmid int) (failedStates []string) {
	if database == nil {
		// Profiles and pillar live in the local DB. Without it we cannot know
		// which states to apply, and every later DB write would panic.
		return []string{"local database unavailable — cannot load salt profile"}
	}
	if cfg.PillarBase == "" {
		cfg.PillarBase = "/srv/pillar"
	}

	profile, err := database.GetSaltProfile(profileName)
	if err != nil || profile == nil {
		return []string{"profile not found: " + profileName}
	}

	// Write per-profile pillar to master if configured.
	if profile.PillarYAML != "" && cfg.MasterMinion != "" {
		header := fmt.Sprintf("# managed by argus — profile: %s\n", profile.Name)
		if wErr := saltClient.WritePillar(ctx, cfg.MasterMinion, cfg.PillarBase,
			"argus_profiles/"+profile.Name, header+profile.PillarYAML); wErr != nil {
			failedStates = append(failedStates, "pillar write: "+wErr.Error())
		}
	}

	// Apply each comma-separated state sequentially.
	allStates := strings.Join(strings.Fields(strings.ReplaceAll(profile.States, ",", " ")), ",")
	for _, state := range strings.Split(allStates, ",") {
		state = strings.TrimSpace(state)
		if state == "" {
			continue
		}
		jid, applyErr := saltClient.ApplyState(ctx, minion, state)
		if applyErr != nil {
			failedStates = append(failedStates, state+": "+applyErr.Error())
			continue
		}
		done := false
		for range 24 {
			time.Sleep(10 * time.Second)
			d, success, failed, pollErr := saltClient.JobResult(ctx, jid)
			if pollErr != nil {
				break
			}
			if d {
				done = true
				if !success {
					failedStates = append(failedStates, failed...)
				}
				break
			}
		}
		if !done {
			failedStates = append(failedStates, state+": timed out")
		}
	}

	success := len(failedStates) == 0
	errStr := strings.Join(failedStates, "; ")
	_, _ = database.RecordSaltApply(db.SaltApplyEvent{
		Minion:      minion,
		States:      allStates,
		SaltProfile: profileName,
		JID:         "",
		Success:     success,
		ErrorMsg:    errStr,
	})

	if success && vmid > 0 {
		_ = database.UpdateVMSaltApplied(vmid, profileName)
		_ = database.SaveServiceHost(db.ServiceHost{
			Host:        minion,
			VMID:        vmid,
			Managed:     true,
			SaltApplied: true,
		})
	}
	return failedStates
}

func (m OrchestrateModel) pushClusterPillarCmd() tea.Cmd {
	saltClient := m.saltClient
	database := m.database
	saltCfg := m.saltCfg
	return func() tea.Msg {
		if saltClient == nil {
			return orchPillarPushedMsg{err: fmt.Errorf("Salt not configured")}
		}
		if database == nil {
			return orchPillarPushedMsg{err: fmt.Errorf("database unavailable")}
		}
		if saltCfg.MasterMinion == "" {
			return orchPillarPushedMsg{err: fmt.Errorf("master_minion not set in [salt] config")}
		}
		if saltCfg.PillarBase == "" {
			saltCfg.PillarBase = "/srv/pillar"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		yaml, err := database.GenerateClusterPillarYAML()
		if err != nil {
			return orchPillarPushedMsg{err: err}
		}
		err = saltClient.WritePillar(ctx, saltCfg.MasterMinion, saltCfg.PillarBase, "argus_cluster", yaml)
		return orchPillarPushedMsg{err: err}
	}
}

func (m OrchestrateModel) syncTrueNASCmd() tea.Cmd {
	truenas := m.truenas
	database := m.database
	return func() tea.Msg {
		if truenas == nil {
			return orchTrueNASSyncedMsg{err: fmt.Errorf("TrueNAS not configured")}
		}
		if database == nil {
			return orchTrueNASSyncedMsg{err: fmt.Errorf("database unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		status, err := truenas.FetchAll(ctx)
		if err != nil {
			return orchTrueNASSyncedMsg{err: err}
		}
		host := truenas.Host()
		var sb strings.Builder
		for _, ds := range status.Datasets {
			slug := strings.NewReplacer("/", "_", "-", "_").Replace(ds.Name)
			mountPath := "/mnt/" + ds.Name
			sb.WriteString(slug + ":\n")
			sb.WriteString("  nfs_host: " + host + "\n")
			sb.WriteString("  nfs_export: " + mountPath + "\n")
			sb.WriteString("  mount_path: " + mountPath + "\n")
		}
		if sb.Len() == 0 {
			return orchTrueNASSyncedMsg{err: fmt.Errorf("no datasets found on TrueNAS")}
		}
		err = database.SetClusterPillar("storage", sb.String(), "truenas")
		return orchTrueNASSyncedMsg{err: err}
	}
}

// checkAllServicesCmd runs all bootstrap service checks in parallel, plus a
// live Proxmox cluster discovery so Bootstrap knows what already exists
// before suggesting a Salt provision action.
func (m OrchestrateModel) checkAllServicesCmd() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.bootstrapServices)+1)
	cmds = append(cmds, m.discoverClusterCmd())
	for _, svc := range m.bootstrapServices {
		cmds = append(cmds, m.checkSvcCmd(svc.id))
	}
	return tea.Batch(cmds...)
}

// discoverClusterCmd inventories every Proxmox guest across all nodes and
// cross-references it against provisioned_vms, tagging each as argus-managed
// or not. It never touches Salt minion data directly (see discoveredGuest).
func (m OrchestrateModel) discoverClusterCmd() tea.Cmd {
	client := m.proxmoxClient
	database := m.database
	return func() tea.Msg {
		if client == nil {
			return orchClusterDiscoverDoneMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		nodes, err := client.Nodes(ctx)
		if err != nil {
			return orchClusterDiscoverDoneMsg{err: err}
		}
		managedVMIDs := map[int]bool{}
		var dbErr error
		if database != nil {
			if pvs, pvErr := database.ProvisionedVMs(); pvErr == nil {
				for _, pv := range pvs {
					managedVMIDs[pv.VMID] = true
				}
			} else {
				dbErr = pvErr
			}
		}
		var all []discoveredGuest
		unreachable := 0
		for _, n := range nodes {
			// Each node gets its own bounded sub-timeout (still capped by the
			// outer ctx) so one hung/offline node can't consume the whole
			// budget and starve every node after it in the loop.
			nodeCtx, nodeCancel := context.WithTimeout(ctx, 5*time.Second)
			guests, gErr := client.Guests(nodeCtx, n.Name)
			nodeCancel()
			if gErr != nil {
				unreachable++
				continue // best-effort per node — one unreachable node shouldn't blank the whole inventory
			}
			for _, g := range guests {
				all = append(all, discoveredGuest{Guest: g, ArgusManaged: managedVMIDs[g.VMID]})
			}
		}
		return orchClusterDiscoverDoneMsg{guests: all, unreachableNodes: unreachable, dbErr: dbErr}
	}
}

// bootstrapServiceKeywordOrder maps hostname substrings (checked against a
// canonicalized, alphanumeric-only guest name) to the bootstrap service they
// likely belong to. Keywords are long enough to avoid false positives (e.g.
// "certs"/"stepca", not a bare "ca" that would match "cache-01"). Checked in
// slice order (a plain map would iterate in randomized order in Go, making a
// hostname that matches two services' keywords classify nondeterministically
// across calls) — order follows defaultBootstrapServices()'s catalog order.
var bootstrapServiceKeywordOrder = []struct {
	svcID    string
	keywords []string
}{
	{"dns", []string{"dns", "technitium"}},
	{"certs", []string{"stepca", "certs"}},
	{"truenas", []string{"truenas"}},
	{"plex", []string{"plex"}},
	{"servarr", []string{"sonarr", "radarr", "lidarr", "readarr", "prowlarr", "bazarr", "overseerr", "tautulli", "servarr"}},
	{"hermes", []string{"hermes"}},
	{"signal", []string{"signal"}},
}

// canonicalizeHostname lowercases and strips everything but letters/digits,
// so "step-ca-01" and "stepCA01" both compare as "stepca01".
func canonicalizeHostname(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchBootstrapService returns the bootstrap service ID a guest name likely
// belongs to, or "" if none of the known keywords match.
func matchBootstrapService(guestName string) string {
	canon := canonicalizeHostname(guestName)
	if canon == "" {
		return ""
	}
	for _, entry := range bootstrapServiceKeywordOrder {
		for _, kw := range entry.keywords {
			if strings.Contains(canon, kw) {
				return entry.svcID
			}
		}
	}
	return ""
}

// matchAcceptedMinion fold-matches name against the currently accepted Salt
// minions and returns the exact accepted minion ID (correct case) if found,
// or "" otherwise. Salt's default glob targeting is case-sensitive, so
// callers that need to actually target a minion must use the returned ID,
// not the input name.
func (m OrchestrateModel) matchAcceptedMinion(name string) string {
	for _, accepted := range m.knownMinions() {
		if strings.EqualFold(accepted, name) {
			return accepted
		}
	}
	return ""
}

// discoveredGuestForService looks for a discovered, argus-unmanaged guest
// whose name suggests it already provides the given bootstrap service. It
// prefers a guest that's already an accepted Salt minion (returning its
// exact-case minion ID) over one that isn't, falling back to the first
// name-only match if no minion candidate exists.
func (m OrchestrateModel) discoveredGuestForService(svcID string) (dg discoveredGuest, minionID string, found bool) {
	var fallback discoveredGuest
	haveFallback := false
	for _, g := range m.clusterGuests {
		if g.ArgusManaged || matchBootstrapService(g.Guest.Name) != svcID {
			continue
		}
		if id := m.matchAcceptedMinion(g.Guest.Name); id != "" {
			return g, id, true
		}
		if !haveFallback {
			fallback, haveFallback = g, true
		}
	}
	if haveFallback {
		return fallback, "", true
	}
	return discoveredGuest{}, "", false
}

// checkSvcCmd runs a health check for one bootstrap service.
func (m OrchestrateModel) checkSvcCmd(id string) tea.Cmd {
	dns := m.dnsClient
	ca := m.caClient
	truenas := m.truenas
	plex := m.plexClient
	hermes := m.hermesClient
	salt := m.saltClient
	cfg := m.cfg

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		switch id {
		case "salt_master":
			if cfg.Salt.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [salt] url + password via argus config"}
			}
			if salt == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised — set [salt] credentials"}
			}
			keys, err := salt.ListKeys(ctx)
			if err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "salt-api unreachable: " + err.Error()}
			}
			accepted := len(keys.Accepted)
			if accepted == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: "reachable, 0 accepted minions"}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: fmt.Sprintf("%d minion(s) accepted", accepted)}

		case "dns":
			if cfg.Technitium.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [technitium] url to config.toml"}
			}
			if dns == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised"}
			}
			zones, err := dns.Zones(ctx)
			if err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "unreachable: " + err.Error()}
			}
			if len(zones) == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: "reachable but 0 zones configured"}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: fmt.Sprintf("%d zone(s)", len(zones))}

		case "certs":
			if cfg.StepCA.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [stepca] url to config.toml"}
			}
			if ca == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised"}
			}
			h, err := ca.Health(ctx)
			if err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "unreachable: " + err.Error()}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: h.Status}

		case "truenas":
			if cfg.TrueNAS.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [truenas] url + api_key to config.toml"}
			}
			if truenas == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised"}
			}
			status, err := truenas.FetchAll(ctx)
			if err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "unreachable: " + err.Error()}
			}
			detail := fmt.Sprintf("%d pool(s), %d dataset(s)", len(status.Pools), len(status.Datasets))
			if len(status.Datasets) == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: detail}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: detail}

		case "plex":
			if cfg.Plex.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [plex] url to config.toml"}
			}
			if plex == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised"}
			}
			if err := plex.Ping(ctx); err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "unreachable: " + err.Error()}
			}
			libs, err := plex.Libraries(ctx)
			if err != nil || len(libs) == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: "reachable, 0 libraries configured"}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: fmt.Sprintf("%d librar(ies)", len(libs))}

		case "servarr":
			apps := servarrAppsFromCfg(cfg)
			if len(apps) == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [servarr.*] sections to config.toml"}
			}
			healthy, total := 0, len(apps)
			for _, a := range apps {
				c := api.NewServarrClient(a.url, a.apiKey, a.name, a.insecure)
				if c.Ping(ctx) == nil {
					healthy++
				}
			}
			if healthy == 0 {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: fmt.Sprintf("0/%d services reachable", total)}
			}
			if healthy < total {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: fmt.Sprintf("%d/%d services healthy", healthy, total)}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: fmt.Sprintf("%d/%d healthy", healthy, total)}

		case "hermes":
			if cfg.Hermes.URL == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add [hermes] url to config.toml"}
			}
			if hermes == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "client not initialised"}
			}
			status, err := hermes.Status(ctx)
			if err != nil {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "unreachable: " + err.Error()}
			}
			detail := "online"
			if status.Version != "" {
				detail = "v" + status.Version
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: detail}

		case "signal":
			// Two serial Salt cmd.run calls — needs more than the standard 10s.
			sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer scancel()
			if cfg.Hermes.SaltTarget == "" {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "add salt_target to [hermes] config"}
			}
			if salt == nil {
				return orchSvcCheckDoneMsg{id: id, status: "notconfigured", detail: "Salt not configured"}
			}
			out, err := salt.RunCmd(sctx, cfg.Hermes.SaltTarget, "signal-cli --version 2>&1 || echo NOTFOUND")
			if err != nil || strings.Contains(out, "NOTFOUND") {
				return orchSvcCheckDoneMsg{id: id, status: "missing", detail: "signal-cli not found on " + cfg.Hermes.SaltTarget}
			}
			if cfg.Hermes.SignalPhone == "" {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: strings.TrimSpace(out) + " — no signal_phone in [hermes] config"}
			}
			out2, _ := salt.RunCmd(sctx, cfg.Hermes.SaltTarget,
				"signal-cli -a '"+cfg.Hermes.SignalPhone+"' listAccounts 2>&1 || echo UNREGISTERED")
			if strings.Contains(out2, "UNREGISTERED") || strings.Contains(out2, "Error") {
				return orchSvcCheckDoneMsg{id: id, status: "partial", detail: strings.TrimSpace(out) + " installed, not registered"}
			}
			return orchSvcCheckDoneMsg{id: id, status: "ok", detail: strings.TrimSpace(out) + " registered"}
		}
		return orchSvcCheckDoneMsg{id: id, status: "unknown", detail: "unknown service"}
	}
}

// servarrApp is an ephemeral descriptor for checking a servarr service.
type servarrApp struct {
	name, url, apiKey string
	insecure          bool
}

// servarrAppsFromCfg returns all configured *arr services with their URLs.
func servarrAppsFromCfg(cfg config.Config) []servarrApp {
	var apps []servarrApp
	add := func(name, url, key string, insecure bool) {
		if url != "" {
			apps = append(apps, servarrApp{name, url, key, insecure})
		}
	}
	s := cfg.Servarr
	add("sonarr", s.Sonarr.URL, s.Sonarr.APIKey, s.Sonarr.Insecure)
	add("radarr", s.Radarr.URL, s.Radarr.APIKey, s.Radarr.Insecure)
	add("lidarr", s.Lidarr.URL, s.Lidarr.APIKey, s.Lidarr.Insecure)
	add("readarr", s.Readarr.URL, s.Readarr.APIKey, s.Readarr.Insecure)
	add("prowlarr", s.Prowlarr.URL, s.Prowlarr.APIKey, s.Prowlarr.Insecure)
	add("bazarr", s.Bazarr.URL, s.Bazarr.APIKey, s.Bazarr.Insecure)
	add("overseerr", s.Overseerr.URL, s.Overseerr.APIKey, s.Overseerr.Insecure)
	add("tautulli", s.Tautulli.URL, s.Tautulli.APIKey, s.Tautulli.Insecure)
	return apps
}

// pushPillarIfConfigured writes the cluster pillar to the Salt master.
// Called inside ingestSvcCmd goroutines after cluster_pillar is updated.
func pushPillarIfConfigured(database *db.DB, salt *api.SaltClient, saltCfg config.SaltConfig) {
	if salt == nil || saltCfg.MasterMinion == "" {
		return
	}
	if saltCfg.PillarBase == "" {
		saltCfg.PillarBase = "/srv/pillar"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if yaml, err := database.GenerateClusterPillarYAML(); err == nil {
		_ = salt.WritePillar(ctx, saltCfg.MasterMinion, saltCfg.PillarBase, "argus_cluster", yaml)
	}
}

// ingestSvcCmd wires up an already-running service: pulls its config into the
// DB, marks it managed in service_hosts, and pushes the updated cluster pillar.
func (m OrchestrateModel) ingestSvcCmd(id string) tea.Cmd {
	dns := m.dnsClient
	truenas := m.truenas
	plex := m.plexClient
	database := m.database
	cfg := m.cfg
	salt := m.saltClient
	saltCfg := m.saltCfg

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		switch id {
		case "dns":
			if dns == nil || database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("dns client or database unavailable")}
			}
			zones, err := dns.Zones(ctx)
			if err != nil {
				return orchSvcIngestDoneMsg{id: id, err: err}
			}
			_ = database.SaveServiceHost(db.ServiceHost{Host: hostFromURL(cfg.Technitium.URL), Managed: true})
			return orchSvcIngestDoneMsg{id: id, notice: fmt.Sprintf("DNS wired — %d zones ingested", len(zones))}

		case "certs":
			if database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("database unavailable")}
			}
			_ = database.SaveServiceHost(db.ServiceHost{Host: hostFromURL(cfg.StepCA.URL), Managed: true})
			return orchSvcIngestDoneMsg{id: id, notice: "step-ca wired as managed service"}

		case "truenas":
			if truenas == nil || database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("truenas client or database unavailable")}
			}
			status, err := truenas.FetchAll(ctx)
			if err != nil {
				return orchSvcIngestDoneMsg{id: id, err: err}
			}
			host := truenas.Host()
			var sb strings.Builder
			for _, ds := range status.Datasets {
				slug := strings.NewReplacer("/", "_", "-", "_").Replace(ds.Name)
				mp := "/mnt/" + ds.Name
				sb.WriteString(slug + ":\n  nfs_host: " + host + "\n  nfs_export: " + mp + "\n  mount_path: " + mp + "\n")
			}
			if sb.Len() > 0 && database != nil {
				_ = database.SetClusterPillar("storage", sb.String(), "truenas")
				pushPillarIfConfigured(database, salt, saltCfg)
			}
			_ = database.SaveServiceHost(db.ServiceHost{Host: host, Managed: true})
			return orchSvcIngestDoneMsg{id: id, notice: fmt.Sprintf("TrueNAS wired — %d dataset(s) ingested and pillar pushed", len(status.Datasets))}

		case "plex":
			if plex == nil || database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("plex client or database unavailable")}
			}
			libs, err := plex.Libraries(ctx)
			if err != nil {
				return orchSvcIngestDoneMsg{id: id, err: err}
			}
			var paths []string
			for _, l := range libs {
				for _, loc := range l.Locations {
					paths = append(paths, loc.Path)
				}
			}
			if len(paths) > 0 {
				_ = database.SetClusterPillar("plex.library_paths", strings.Join(paths, "\n"), "plex")
				pushPillarIfConfigured(database, salt, saltCfg)
			}
			_ = database.SaveServiceHost(db.ServiceHost{Host: hostFromURL(cfg.Plex.URL), Managed: true})
			return orchSvcIngestDoneMsg{id: id, notice: fmt.Sprintf("Plex wired — %d librar(ies), %d path(s) ingested and pillar pushed", len(libs), len(paths))}

		case "servarr":
			if database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("database unavailable")}
			}
			apps := servarrAppsFromCfg(cfg)
			wired := 0
			for _, a := range apps {
				_ = database.SaveServiceHost(db.ServiceHost{Host: hostFromURL(a.url), Managed: true})
				wired++
			}
			return orchSvcIngestDoneMsg{id: id, notice: fmt.Sprintf("%d servarr service(s) wired", wired)}

		case "hermes":
			if database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("database unavailable")}
			}
			_ = database.SaveServiceHost(db.ServiceHost{Host: hostFromURL(cfg.Hermes.URL), Managed: true})
			return orchSvcIngestDoneMsg{id: id, notice: "Hermes agent wired as managed service"}

		case "signal":
			if salt == nil || database == nil {
				return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("salt or database unavailable")}
			}
			gatewayYAML := "gateway_url: http://127.0.0.1:7583\n"
			if cfg.Hermes.SignalPhone != "" {
				gatewayYAML += "signal_phone: " + cfg.Hermes.SignalPhone + "\n"
			}
			_ = database.SetClusterPillar("hermes.signal_gateway", gatewayYAML, "signal")
			pushPillarIfConfigured(database, salt, saltCfg)
			return orchSvcIngestDoneMsg{id: id, notice: "Signal CLI gateway config wired into cluster pillar and pushed"}
		}
		return orchSvcIngestDoneMsg{id: id, err: fmt.Errorf("unknown service: %s", id)}
	}
}

// provisionSvcCmd fires salt state.apply and returns immediately with the JID.
// The caller then runs pollProvisionCmd to wait for completion.
func (m OrchestrateModel) provisionSvcCmd(id, target, state string) tea.Cmd {
	salt := m.saltClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		jid, err := salt.ApplyState(ctx, target, state)
		if err != nil {
			return orchSvcProvisionDoneMsg{id: id, err: err}
		}
		return orchSvcProvisionStartedMsg{id: id, jid: jid}
	}
}

// pollProvisionCmd polls for a Salt job result, returning orchSvcProvisionDoneMsg when done.
func (m OrchestrateModel) pollProvisionCmd(id, jid string) tea.Cmd {
	salt := m.saltClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for range 24 {
			time.Sleep(10 * time.Second)
			done, success, failed, pollErr := salt.JobResult(ctx, jid)
			if pollErr != nil {
				return orchSvcProvisionDoneMsg{id: id, jid: jid, err: pollErr}
			}
			if done {
				if !success {
					return orchSvcProvisionDoneMsg{id: id, jid: jid, failedStates: failed}
				}
				return orchSvcProvisionDoneMsg{id: id, jid: jid}
			}
		}
		return orchSvcProvisionDoneMsg{id: id, jid: jid, err: fmt.Errorf("timed out")}
	}
}

// hostFromURL extracts the host portion of a URL for use as a service_host key.
func hostFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	s := rawURL
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

// joinAdvisory combines two optional advisory strings with a separator,
// skipping either side if empty.
func joinAdvisory(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

// knownMinions returns the list of accepted Salt minion names for provision form hints.
func (m OrchestrateModel) knownMinions() []string {
	if m.keys == nil {
		return nil
	}
	return m.keys.Accepted
}

func (m OrchestrateModel) Update(msg tea.Msg) (OrchestrateModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case orchKeyTickMsg:
		return m, tea.Batch(m.loadKeysCmd(), m.keyPollTickCmd())

	case orchKeysLoadedMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.keys = msg.keys
		}
		return m, nil

	case orchPendingLoadedMsg:
		if msg.err == nil {
			m.pending = msg.vms
		}
		return m, nil

	case orchPillarLoadedMsg:
		if msg.err == nil {
			m.pillar = msg.entries
		}
		return m, nil

	case orchAcceptDoneMsg:
		m.acting = false
		m.confirm = nil
		if msg.err != nil {
			m.err = msg.err
		} else if len(msg.failedStates) > 0 {
			m.err = fmt.Errorf("%s: failed states: %s", msg.minion, strings.Join(msg.failedStates, ", "))
		} else {
			m.notice = fmt.Sprintf("%s: %s done", msg.action, msg.minion)
		}
		return m, tea.Batch(m.loadKeysCmd(), m.loadPendingCmd())

	case orchPillarPushedMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "cluster pillar pushed to master"
		}
		return m, nil

	case orchTrueNASSyncedMsg:
		m.acting = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = "TrueNAS datasets synced to cluster pillar"
		}
		return m, m.loadPillarCmd()

	case orchBootstrapCheckMsg:
		for i := range m.bootstrapServices {
			m.bootstrapServices[i].status = bsvcChecking
			m.bootstrapServices[i].detail = ""
			m.bootstrapServices[i].errMsg = ""
		}
		m.notice = "checking all services..."
		return m, m.checkAllServicesCmd()

	case orchSvcCheckDoneMsg:
		for i, svc := range m.bootstrapServices {
			if svc.id != msg.id {
				continue
			}
			switch msg.status {
			case "ok":
				m.bootstrapServices[i].status = bsvcOK
			case "partial":
				m.bootstrapServices[i].status = bsvcPartial
			case "missing":
				m.bootstrapServices[i].status = bsvcMissing
			case "notconfigured":
				m.bootstrapServices[i].status = bsvcNotConfigured
			default:
				m.bootstrapServices[i].status = bsvcUnknown
			}
			m.bootstrapServices[i].detail = msg.detail
			if msg.err != nil {
				m.bootstrapServices[i].errMsg = msg.err.Error()
			}
			break
		}
		// Only clear the "checking all" notice when no service is still checking.
		stillChecking := false
		for _, svc := range m.bootstrapServices {
			if svc.status == bsvcChecking {
				stillChecking = true
				break
			}
		}
		if !stillChecking {
			m.notice = ""
		}
		return m, nil

	case orchSvcIngestDoneMsg:
		for i, svc := range m.bootstrapServices {
			if svc.id == msg.id {
				m.bootstrapServices[i].ingesting = false
				break
			}
		}
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.notice = msg.notice
		}
		return m, m.checkSvcCmd(msg.id)

	case orchSvcProvisionStartedMsg:
		m.activeJID = msg.jid
		m.notice = fmt.Sprintf("provisioning %s — JID: %s (polling every 10s)", msg.id, msg.jid)
		return m, m.pollProvisionCmd(msg.id, msg.jid)

	case orchSvcProvisionDoneMsg:
		m.activeJID = ""
		for i, svc := range m.bootstrapServices {
			if svc.id == msg.id {
				m.bootstrapServices[i].provisioning = false
				break
			}
		}
		m.provisioningFor = ""
		var extraCmds []tea.Cmd
		if msg.err != nil {
			m.err = msg.err
		} else if len(msg.failedStates) > 0 {
			m.err = fmt.Errorf("provision %s: failed: %s", msg.id, strings.Join(msg.failedStates, ", "))
		} else {
			jidSuffix := ""
			if msg.jid != "" {
				jidSuffix = " (JID: " + msg.jid + ")"
			}
			m.notice = msg.id + " provisioned" + jidSuffix + " — re-checking..."
			// Write derived URL to config.toml and signal App to rebuild the live client.
			if m.provisionDerivedURL != "" {
				if spec, ok := bootstrapSpecs[msg.id]; ok && spec.patchConfig != nil {
					spec.patchConfig(&m.cfg, m.provisionDerivedURL)
					if saveErr := config.SaveConfig(&m.cfg); saveErr != nil {
						m.notice += " (config.toml save failed: " + saveErr.Error() + ")"
					} else {
						updatedCfg := m.cfg
						reloadID := msg.id
						extraCmds = append(extraCmds, func() tea.Msg {
							return orchReloadClientMsg{id: reloadID, updatedCfg: updatedCfg}
						})
					}
				}
			}
			m.provisionDerivedURL = ""
		}
		cmds := append([]tea.Cmd{m.checkSvcCmd(msg.id)}, extraCmds...)
		// Advance provision queue — open form for next service.
		if len(m.provisionQueue) > 0 {
			nextID := m.provisionQueue[0]
			m.provisionQueue = m.provisionQueue[1:]
			nextSvc := m.bootstrapServiceByID(nextID)
			m.provisioningFor = nextID
			m.provisionForm = m.openProvisionFormFor(nextID)
			m.notice = fmt.Sprintf("queue: next is %s — fill in target minion", nextSvc.name)
		}
		return m, tea.Batch(cmds...)

	case orchClusterDiscoverDoneMsg:
		m.clusterDiscovered = true
		m.clusterDiscoverErr = msg.err
		if msg.err == nil {
			m.clusterGuests = msg.guests
			m.clusterUnreachableNodes = msg.unreachableNodes
			m.clusterDBErr = msg.dbErr
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m OrchestrateModel) handleKey(msg tea.KeyMsg) (OrchestrateModel, tea.Cmd) {
	// Provision confirmation dialog (before running state.apply).
	if m.provisionConfirm != nil {
		return m.handleProvisionConfirm(msg)
	}

	// Bootstrap provision form (target + state entry).
	if m.provisionForm != nil {
		return m.handleProvisionForm(msg)
	}

	// Bootstrap action dialog.
	if m.bsvcActionDialog != nil {
		return m.handleBootstrapActionDialog(msg)
	}

	// Pillar form input mode.
	if m.form != nil {
		return m.handlePillarForm(msg)
	}

	// Confirmation dialog (key accept/reject/delete pillar).
	if m.confirm != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			c := m.confirm
			if c.action == "delete_pillar" {
				m.confirm = nil
				if m.database == nil {
					m.err = fmt.Errorf("database unavailable")
					return m, nil
				}
				if err := m.database.DeleteClusterPillarKey(c.pillarKey); err != nil {
					m.err = err
				} else {
					m.notice = "deleted " + c.pillarKey
					if m.cursor > 0 {
						m.cursor--
					}
				}
				return m, m.loadPillarCmd()
			}
			if m.acting {
				return m, nil
			}
			m.acting = true
			return m, m.applyKeyActionCmd(c.action, c.minion, c.profile, c.vmid)
		case "n", "N", "esc":
			m.confirm = nil
		}
		// Ignore every other key while a confirmation is open. Previously a
		// stray arrow/tab press silently cancelled it.
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "b":
		return m, func() tea.Msg { return backMsg{} }
	case "right", "l":
		m.tab = (m.tab + 1) % orchTab(len(orchTabLabels))
		m.cursor = 0
		m.notice, m.err = "", nil
	case "left", "h":
		m.tab = (m.tab + orchTab(len(orchTabLabels)) - 1) % orchTab(len(orchTabLabels))
		m.cursor = 0
		m.notice, m.err = "", nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < m.currentListLen()-1 {
			m.cursor++
		}
	case "R":
		m.notice = "refreshing..."
		return m, tea.Batch(m.loadKeysCmd(), m.loadPendingCmd(), m.loadPillarCmd())

	// Keys tab actions
	case "a":
		if m.tab == orchTabKeys && m.keys != nil && m.cursor < len(m.keys.Pending) {
			minion := m.keys.Pending[m.cursor]
			profile, vmid := m.matchMinion(minion)
			m.confirm = &orchConfirm{action: "accept", minion: minion, profile: profile, vmid: vmid}
		}
	case "r":
		if m.tab == orchTabKeys && m.keys != nil && m.cursor < len(m.keys.Pending) {
			minion := m.keys.Pending[m.cursor]
			m.confirm = &orchConfirm{action: "reject", minion: minion}
		}

	// Bootstrap tab actions
	case "x":
		if m.tab == orchTabBootstrap {
			return m, func() tea.Msg { return orchBootstrapCheckMsg{} }
		}
	case "P":
		if m.tab == orchTabBootstrap && m.activeJID == "" && m.provisionForm == nil {
			var queue []string
			for _, svc := range m.bootstrapServices {
				if svc.status == bsvcMissing {
					queue = append(queue, svc.id)
				}
			}
			if len(queue) == 0 {
				m.notice = "no missing services — run x to check first"
				return m, nil
			}
			firstID := queue[0]
			m.provisionQueue = queue[1:]
			m.provisioningFor = firstID
			m.provisionForm = m.openProvisionFormFor(firstID)
			m.notice = fmt.Sprintf("provision queue: %d service(s), starting with %s", len(queue), m.bootstrapServiceByID(firstID).name)
		}
	case "enter":
		if m.tab == orchTabBootstrap && m.cursor < len(m.bootstrapServices) {
			svc := m.bootstrapServices[m.cursor]
			blocked := m.blockedBy()
			depHint := ""
			if dep, isBlocked := blocked[svc.id]; isBlocked {
				depHint = "note: " + dep + " not yet OK — proceed anyway at your discretion"
			}
			switch svc.status {
			case bsvcNotConfigured:
				if svc.id == "salt_master" {
					m.notice = "Run: argus config — set [salt] url + password in the Salt section, then press x to re-check"
				} else {
					m.notice = "Add configuration for " + svc.name + " to config.toml first (run: argus config)"
				}
			case bsvcMissing:
				opts := []string{"Provision via Salt", "Skip"}
				detail := depHint
				if svc.id == "salt_master" {
					opts = []string{"Configure in argus config", "Skip"}
				} else if dg, minionID, found := m.discoveredGuestForService(svc.id); found {
					if minionID != "" {
						detail = joinAdvisory(detail, fmt.Sprintf("found existing minion '%s' (VMID %d, node %s) — pre-filled as target", minionID, dg.Guest.VMID, dg.Guest.Node))
					} else {
						detail = joinAdvisory(detail, fmt.Sprintf("found unmanaged VM '%s' (VMID %d, node %s) — accept its Salt key on the Keys tab first, then target it here", dg.Guest.Name, dg.Guest.VMID, dg.Guest.Node))
					}
				}
				m.bsvcActionDialog = &orchBootstrapActionDialog{
					service: svc.id,
					status:  svc.status,
					opts:    opts,
					detail:  detail,
				}
			case bsvcPartial:
				detail := depHint
				if _, minionID, found := m.discoveredGuestForService(svc.id); found && minionID != "" {
					detail = joinAdvisory(detail, fmt.Sprintf("found existing minion '%s' — will be pre-filled as target if you re-apply", minionID))
				}
				m.bsvcActionDialog = &orchBootstrapActionDialog{
					service: svc.id,
					status:  svc.status,
					opts:    []string{"Wire up what exists", "Re-apply Salt state", "Skip"},
					detail:  detail,
				}
			case bsvcOK:
				m.bsvcActionDialog = &orchBootstrapActionDialog{
					service: svc.id,
					status:  svc.status,
					opts:    []string{"Ingest / wire up", "Skip"},
				}
			case bsvcUnknown, bsvcChecking:
				return m, m.checkSvcCmd(svc.id)
			case bsvcSkipped:
				m.bootstrapServices[m.cursor].status = bsvcUnknown
				return m, m.checkSvcCmd(svc.id)
			}
		}
	case "s":
		if m.tab == orchTabBootstrap && m.cursor < len(m.bootstrapServices) {
			m.bootstrapServices[m.cursor].status = bsvcSkipped
			m.bootstrapServices[m.cursor].detail = "skipped by operator"
		} else if m.tab == orchTabPending && m.cursor < len(m.pending) && !m.acting {
			vm := m.pending[m.cursor]
			m.acting = true
			m.notice = fmt.Sprintf("applying %s to %s...", vm.SaltProfile, vm.Name)
			return m, m.applyPendingVMCmd(vm)
		}

	// Pillar tab actions
	case "t":
		if m.tab == orchTabPillar && !m.acting {
			m.acting = true
			m.notice = "syncing TrueNAS datasets..."
			return m, m.syncTrueNASCmd()
		}
	case "p":
		if m.tab == orchTabPillar && !m.acting {
			m.acting = true
			m.notice = "pushing cluster pillar to master..."
			return m, m.pushClusterPillarCmd()
		}
	case "n":
		if m.tab == orchTabPillar {
			m.form = newPillarForm("", "", "manual")
		}
	case "d":
		if m.tab == orchTabPillar && m.cursor < len(m.pillar) {
			m.confirm = &orchConfirm{action: "delete_pillar", pillarKey: m.pillar[m.cursor].Key}
		}
	}
	return m, nil
}

func (m OrchestrateModel) handleProvisionConfirm(msg tea.KeyMsg) (OrchestrateModel, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		c := m.provisionConfirm
		m.provisionConfirm = nil
		m.provisionDerivedURL = c.derivedURL
		for i, svc := range m.bootstrapServices {
			if svc.id == c.svcID {
				m.bootstrapServices[i].provisioning = true
				break
			}
		}
		m.notice = fmt.Sprintf("provisioning %s on %s...", c.svcID, c.target)
		return m, m.provisionSvcCmd(c.svcID, c.target, c.state)
	case "n", "esc":
		m.provisionConfirm = nil
	}
	return m, nil
}

func (m OrchestrateModel) handlePillarForm(msg tea.KeyMsg) (OrchestrateModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s", "enter":
		if m.form.focused == len(m.form.inputs)-1 || msg.String() == "ctrl+s" {
			key := strings.TrimSpace(m.form.inputs[0].Value())
			val := strings.TrimSpace(m.form.inputs[1].Value())
			src := strings.TrimSpace(m.form.inputs[2].Value())
			if src == "" {
				src = "manual"
			}
			if key != "" {
				if err := m.database.SetClusterPillar(key, val, src); err != nil {
					m.err = err
				} else {
					m.notice = "saved pillar key " + key
				}
			}
			m.form = nil
			return m, m.loadPillarCmd()
		}
		m.form.inputs[m.form.focused].Blur()
		m.form.focused = (m.form.focused + 1) % len(m.form.inputs)
		m.form.inputs[m.form.focused].Focus()
		return m, textinput.Blink
	case "esc":
		m.form = nil
		return m, nil
	case "tab":
		m.form.inputs[m.form.focused].Blur()
		m.form.focused = (m.form.focused + 1) % len(m.form.inputs)
		m.form.inputs[m.form.focused].Focus()
		return m, textinput.Blink
	case "shift+tab":
		m.form.inputs[m.form.focused].Blur()
		m.form.focused = (m.form.focused - 1 + len(m.form.inputs)) % len(m.form.inputs)
		m.form.inputs[m.form.focused].Focus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.form.inputs[m.form.focused], cmd = m.form.inputs[m.form.focused].Update(msg)
	return m, cmd
}

func (m OrchestrateModel) handleBootstrapActionDialog(msg tea.KeyMsg) (OrchestrateModel, tea.Cmd) {
	d := m.bsvcActionDialog
	switch msg.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j":
		if d.cursor < len(d.opts)-1 {
			d.cursor++
		}
	case "enter":
		opt := d.opts[d.cursor]
		svcID := d.service
		m.bsvcActionDialog = nil
		return m.executeBootstrapAction(svcID, opt)
	case "esc", "q":
		m.bsvcActionDialog = nil
	}
	return m, nil
}

func (m OrchestrateModel) executeBootstrapAction(svcID, opt string) (OrchestrateModel, tea.Cmd) {
	switch {
	case opt == "Skip":
		for i, svc := range m.bootstrapServices {
			if svc.id == svcID {
				m.bootstrapServices[i].status = bsvcSkipped
				m.bootstrapServices[i].detail = "skipped by operator"
				break
			}
		}
	case opt == "Configure in argus config":
		m.notice = "Run: argus config — fill the Salt section, save, then press x to re-check"
	case opt == "Provision via Salt" || opt == "Re-apply Salt state":
		m.provisionForm = m.openProvisionFormFor(svcID)
		m.provisioningFor = svcID
	case opt == "Wire up what exists" || opt == "Ingest / wire up":
		for i, svc := range m.bootstrapServices {
			if svc.id == svcID {
				m.bootstrapServices[i].ingesting = true
				break
			}
		}
		m.notice = "ingesting " + svcID + "..."
		return m, m.ingestSvcCmd(svcID)
	}
	return m, nil
}

func (m OrchestrateModel) handleProvisionForm(msg tea.KeyMsg) (OrchestrateModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		return m.submitProvisionForm()
	case "enter":
		if m.provisionForm.focused == len(m.provisionForm.inputs)-1 {
			return m.submitProvisionForm()
		}
		m.provisionForm.inputs[m.provisionForm.focused].Blur()
		m.provisionForm.focused = (m.provisionForm.focused + 1) % len(m.provisionForm.inputs)
		m.provisionForm.inputs[m.provisionForm.focused].Focus()
		return m, textinput.Blink
	case "esc":
		m.provisionForm = nil
		m.provisioningFor = ""
		m.provisionQueue = nil
		return m, nil
	case "tab":
		m.provisionForm.inputs[m.provisionForm.focused].Blur()
		m.provisionForm.focused = (m.provisionForm.focused + 1) % len(m.provisionForm.inputs)
		m.provisionForm.inputs[m.provisionForm.focused].Focus()
		return m, textinput.Blink
	case "shift+tab":
		m.provisionForm.inputs[m.provisionForm.focused].Blur()
		m.provisionForm.focused = (m.provisionForm.focused - 1 + len(m.provisionForm.inputs)) % len(m.provisionForm.inputs)
		m.provisionForm.inputs[m.provisionForm.focused].Focus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.provisionForm.inputs[m.provisionForm.focused], cmd = m.provisionForm.inputs[m.provisionForm.focused].Update(msg)
	return m, cmd
}

func (m OrchestrateModel) submitProvisionForm() (OrchestrateModel, tea.Cmd) {
	target := strings.TrimSpace(m.provisionForm.inputs[0].Value())
	state := strings.TrimSpace(m.provisionForm.inputs[1].Value())
	svcID := m.provisioningFor
	svc := m.bootstrapServiceByID(svcID)
	m.provisionForm = nil
	m.provisioningFor = ""
	if target == "" || state == "" {
		m.err = fmt.Errorf("target and state are required")
		return m, nil
	}
	if m.saltClient == nil {
		m.err = fmt.Errorf("Salt not configured — cannot provision")
		return m, nil
	}
	var derivedURL string
	if spec, ok := bootstrapSpecs[svcID]; ok && spec.patchConfig != nil {
		derivedURL = spec.deriveURL(target, m.cfg.Globals.Domain)
	}
	var targetWarning string
	if m.keys != nil {
		// Salt's default glob targeting is case-sensitive, so an exact-case
		// match is required — matchAcceptedMinion fold-matches to find a
		// candidate but returns its real casing, letting us tell "no such
		// minion" apart from "right minion, wrong case".
		if exact := m.matchAcceptedMinion(target); exact != target {
			if exact == "" {
				targetWarning = fmt.Sprintf("'%s' does not match any accepted Salt minion — state.apply may fail, or if this is a glob it could target unintended systems", target)
			} else {
				targetWarning = fmt.Sprintf("'%s' doesn't exactly match accepted minion '%s' (case differs) — Salt targeting is case-sensitive; did you mean '%s'?", target, exact, exact)
			}
		}
	}
	m.provisionConfirm = &orchProvisionConfirm{
		svcID:         svcID,
		svcName:       svc.name,
		target:        target,
		state:         state,
		derivedURL:    derivedURL,
		targetWarning: targetWarning,
	}
	return m, nil
}

// openProvisionFormFor builds the bootstrap provision form for svcID,
// prefilling the target when cluster discovery found an already-accepted
// minion candidate. Used by all three entry points into provisioning (the
// per-service action dialog, the "P" bulk-queue shortcut, and the
// queue-advance step after a provision completes) so they behave
// consistently instead of only the dialog path getting discovery's benefit.
func (m OrchestrateModel) openProvisionFormFor(svcID string) *orchPillarForm {
	target := ""
	if _, minionID, found := m.discoveredGuestForService(svcID); found && minionID != "" {
		target = minionID
	}
	return newProvisionForm(target, "services."+svcID, m.knownMinions())
}

func newProvisionForm(target, defaultState string, knownMinions []string) *orchPillarForm {
	specs := []struct{ placeholder, value string }{
		{"minion target (e.g. dns-server)", target},
		{"Salt state (e.g. services.dns)", defaultState},
	}
	inputs := make([]textinput.Model, len(specs))
	for i, s := range specs {
		ti := textinput.New()
		ti.Placeholder = s.placeholder
		ti.SetValue(s.value)
		ti.CharLimit = 256
		inputs[i] = ti
	}
	// Pre-fill target if exactly one accepted minion is known.
	if target == "" && len(knownMinions) == 1 {
		inputs[0].SetValue(knownMinions[0])
	}
	inputs[0].Focus()
	return &orchPillarForm{inputs: inputs, knownHints: knownMinions}
}

func newPillarForm(key, value, source string) *orchPillarForm {
	specs := []struct{ placeholder, value string }{
		{"key (e.g. storage)", key},
		{"value (YAML block)", value},
		{"source", source},
	}
	inputs := make([]textinput.Model, len(specs))
	for i, s := range specs {
		ti := textinput.New()
		ti.Placeholder = s.placeholder
		ti.SetValue(s.value)
		ti.CharLimit = 2048
		inputs[i] = ti
	}
	inputs[0].Focus()
	return &orchPillarForm{inputs: inputs}
}

// matchMinion attempts to match a minion ID to a provisioned VM by name.
func (m OrchestrateModel) matchMinion(minionID string) (profile string, vmid int) {
	for _, vm := range m.pending {
		if strings.EqualFold(vm.Name, minionID) || strings.EqualFold(vm.VMIP, minionID) {
			return vm.SaltProfile, vm.VMID
		}
	}
	if m.database != nil {
		if vms, err := m.database.ProvisionedVMs(); err == nil {
			for _, vm := range vms {
				if strings.EqualFold(vm.Name, minionID) {
					return vm.SaltProfile, vm.VMID
				}
			}
		}
	}
	return "", 0
}

func (m OrchestrateModel) currentListLen() int {
	switch m.tab {
	case orchTabKeys:
		if m.keys == nil {
			return 0
		}
		return len(m.keys.Pending) + len(m.keys.Rejected)
	case orchTabPending:
		return len(m.pending)
	case orchTabPillar:
		return len(m.pillar)
	case orchTabBootstrap:
		return len(m.bootstrapServices)
	}
	return 0
}

func (m OrchestrateModel) renderTabBar() string {
	var parts []string
	for i, label := range orchTabLabels {
		l := " " + label + " "
		if orchTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch")
}

func (m OrchestrateModel) View() string {
	title := styleTitle.Render("⚡ argus") + " › " + styleBold.Render("orchestrate")
	tabs := m.renderTabBar()

	var status string
	if m.err != nil {
		status = styleError.Render("✗ " + sanitizeText(m.err.Error()))
	} else if m.notice != "" {
		status = styleRunning.Render("✓ " + sanitizeText(m.notice))
	}
	if m.acting {
		status = m.spinner.View() + " " + styleDim.Render(m.notice)
	}
	// Show spinner if any bootstrap service is actively checking/provisioning/ingesting.
	if m.tab == orchTabBootstrap && status == "" {
		for _, svc := range m.bootstrapServices {
			if svc.status == bsvcChecking || svc.provisioning || svc.ingesting {
				status = m.spinner.View() + " " + styleDim.Render(m.notice)
				break
			}
		}
	}
	// Show JID in spinner line when polling a provision.
	if m.activeJID != "" {
		status = m.spinner.View() + " " + styleDim.Render(m.notice)
	}

	var body string
	switch m.tab {
	case orchTabKeys:
		body = m.viewKeys()
	case orchTabPending:
		body = m.viewPending()
	case orchTabPillar:
		body = m.viewPillar()
	case orchTabBootstrap:
		body = m.viewBootstrap()
	}

	// Overlays in priority order (highest priority last = topmost in view).
	if m.confirm != nil {
		body = m.viewConfirm()
	}
	if m.bsvcActionDialog != nil {
		body = m.viewBootstrapActionDialog()
	}
	if m.provisionForm != nil {
		body = m.viewProvisionForm()
	}
	if m.provisionConfirm != nil {
		body = m.viewProvisionConfirm()
	}

	var help string
	switch {
	case m.provisionConfirm != nil:
		help = styleBold.Render("y") + "/" + styleBold.Render("enter") + " confirm  " + styleDim.Render("n/esc") + " cancel"
	case m.bsvcActionDialog != nil:
		help = styleDim.Render("↑/↓") + " navigate  " + styleDim.Render("enter") + " confirm  " + styleDim.Render("esc") + " cancel"
	case m.provisionForm != nil:
		queueStr := ""
		if len(m.provisionQueue) > 0 {
			queueStr = fmt.Sprintf("  (%d more in queue)", len(m.provisionQueue))
		}
		help = styleDim.Render("tab") + " next  " + styleDim.Render("ctrl+s") + " confirm  " + styleDim.Render("esc") + " cancel" + styleDim.Render(queueStr)
	case m.confirm != nil:
		help = styleBold.Render("y/enter") + " confirm  " + styleDim.Render("n/esc") + " cancel"
	case m.tab == orchTabKeys:
		help = styleDim.Render("a") + " accept  " + styleDim.Render("r") + " reject  " + styleDim.Render("R") + " refresh  " + styleDim.Render("b") + " back"
	case m.tab == orchTabPending:
		help = styleDim.Render("s") + " apply salt  " + styleDim.Render("R") + " refresh  " + styleDim.Render("b") + " back"
	case m.tab == orchTabPillar:
		help = styleDim.Render("t") + " sync TrueNAS  " + styleDim.Render("p") + " push to master  " + styleDim.Render("n") + " new  " + styleDim.Render("d") + " delete  " + styleDim.Render("b") + " back"
	case m.tab == orchTabBootstrap:
		help = styleDim.Render("x") + " check all  " + styleDim.Render("P") + " provision all missing  " + styleDim.Render("enter") + " action  " + styleDim.Render("s") + " skip  " + styleDim.Render("b") + " back"
	}

	parts := []string{title + "\n" + tabs}
	if status != "" {
		parts = append(parts, status)
	}
	parts = append(parts, body, help)
	return strings.Join(parts, "\n")
}

func (m OrchestrateModel) viewKeys() string {
	if m.saltClient == nil {
		return styleDim.Render("Salt not configured — add [salt] to config.toml to manage minion keys.")
	}
	if m.keys == nil {
		return m.spinner.View() + " loading keys..."
	}

	var sb strings.Builder
	pending := m.keys.Pending
	rejected := m.keys.Rejected
	accepted := m.keys.Accepted

	sb.WriteString(styleBold.Render(fmt.Sprintf("Accepted: %d  Pending: %d  Rejected: %d", len(accepted), len(pending), len(rejected))))
	sb.WriteString("\n\n")

	if len(pending) == 0 && len(rejected) == 0 {
		sb.WriteString(styleDim.Render("No pending or rejected keys."))
		return sb.String()
	}

	hdr := fmt.Sprintf("%-4s  %-30s  %-12s  %s", "#", "MINION", "STATUS", "PROFILE")
	sb.WriteString(styleDim.Render(hdr) + "\n")

	idx := 0
	for _, minion := range pending {
		profile, _ := m.matchMinion(minion)
		matchStr := styleDim.Render("-")
		if profile != "" {
			matchStr = styleRunning.Render(profile)
		}
		row := fmt.Sprintf("%-4d  %-30s  %-12s  %s", idx+1, truncate(minion, 30), "[PENDING]", matchStr)
		if idx == m.cursor {
			sb.WriteString(styleSelected.Render(row) + "\n")
		} else {
			sb.WriteString(row + "\n")
		}
		idx++
	}
	for _, minion := range rejected {
		row := fmt.Sprintf("%-4d  %-30s  %-12s  %s", idx+1, truncate(minion, 30), styleError.Render("[REJECTED]"), styleDim.Render("-"))
		if idx == m.cursor {
			sb.WriteString(styleSelected.Render(row) + "\n")
		} else {
			sb.WriteString(row + "\n")
		}
		idx++
	}

	return sb.String()
}

func (m OrchestrateModel) viewPending() string {
	if m.saltClient == nil {
		return styleDim.Render("Salt not configured — add [salt] to config.toml to manage pending VMs.")
	}
	if len(m.pending) == 0 {
		return styleDim.Render("No VMs awaiting salt apply. Provision a VM with a salt profile assigned and it will appear here.")
	}
	var sb strings.Builder
	hdr := fmt.Sprintf("%-6s  %-20s  %-15s  %-20s  %s", "VMID", "NAME", "IP", "PROFILE", "PROVISIONED")
	sb.WriteString(styleDim.Render(hdr) + "\n")
	for i, vm := range m.pending {
		ts := vm.ProvisionedAt.Format("2006-01-02 15:04")
		row := fmt.Sprintf("%-6d  %-20s  %-15s  %-20s  %s",
			vm.VMID, truncate(vm.Name, 20), truncate(vm.VMIP, 15), truncate(vm.SaltProfile, 20), ts)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(row) + "\n")
		} else {
			sb.WriteString(row + "\n")
		}
	}
	return sb.String()
}

func (m OrchestrateModel) viewPillar() string {
	if m.form != nil {
		return m.renderPillarForm()
	}

	var sb strings.Builder

	masterMinion := m.saltCfg.MasterMinion
	if masterMinion == "" {
		masterMinion = "<master_minion>"
	}
	pillarBase := m.saltCfg.PillarBase
	if pillarBase == "" {
		pillarBase = "/srv/pillar"
	}
	hint := fmt.Sprintf("Add to %s/top.sls:\n  base:\n    '*':\n      - argus_cluster\n    '%s':\n      - argus_profiles/<profile>", pillarBase, masterMinion)
	sb.WriteString(styleDim.Render(hint) + "\n\n")

	if len(m.pillar) == 0 {
		sb.WriteString(styleDim.Render("No cluster pillar entries. Press t to sync TrueNAS datasets, or n to add manually."))
		return sb.String()
	}

	hdr := fmt.Sprintf("%-25s  %-10s  %s", "KEY", "SOURCE", "UPDATED")
	sb.WriteString(styleDim.Render(hdr) + "\n")
	for i, e := range m.pillar {
		row := fmt.Sprintf("%-25s  %-10s  %s", truncate(e.Key, 25), e.Source, e.UpdatedAt.Format("2006-01-02 15:04"))
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(row) + "\n")
		} else {
			sb.WriteString(row + "\n")
		}
	}
	return sb.String()
}

func (m OrchestrateModel) renderPillarForm() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("New cluster pillar entry") + "\n\n")
	labels := []string{"Key", "Value", "Source"}
	for i, inp := range m.form.inputs {
		label := styleDim.Render(labels[i] + ": ")
		if i == m.form.focused {
			label = styleBold.Render(labels[i] + ": ")
		}
		sb.WriteString(label + inp.View() + "\n")
	}
	sb.WriteString("\n" + styleDim.Render("tab/shift+tab") + " navigate  " + styleDim.Render("ctrl+s") + " save  " + styleDim.Render("esc") + " cancel")
	return sb.String()
}

func bsvcStatusLabel(s bsvcStatus) string {
	switch s {
	case bsvcChecking:
		return styleDim.Render("[CHECK]")
	case bsvcOK:
		return styleRunning.Render("[  OK  ]")
	case bsvcPartial:
		return styleBold.Render("[PARTL ]")
	case bsvcMissing:
		return styleError.Render("[ MISS ]")
	case bsvcNotConfigured:
		return styleDim.Render("[CONFIG]")
	case bsvcSkipped:
		return styleDim.Render("[SKIP  ]")
	default:
		return styleDim.Render("[  ?   ]")
	}
}

func (m OrchestrateModel) viewBootstrap() string {
	var sb strings.Builder
	sb.WriteString(styleBold.Render("Bootstrap — check and wire up homelab services") + "\n\n")

	switch {
	case m.clusterDiscoverErr != nil:
		msg := "cluster inventory: unavailable (" + m.clusterDiscoverErr.Error() + ")"
		if len(m.clusterGuests) > 0 {
			msg += " — showing last successful scan, may be stale"
		}
		sb.WriteString(styleDim.Render(msg) + "\n\n")
	case m.clusterDiscovered:
		managed, unmanaged := 0, 0
		for _, g := range m.clusterGuests {
			if g.ArgusManaged {
				managed++
			} else {
				unmanaged++
			}
		}
		line := fmt.Sprintf("cluster inventory: %d VM/LXC found — %d argus-managed, %d unmanaged", len(m.clusterGuests), managed, unmanaged)
		if m.clusterUnreachableNodes > 0 {
			line += fmt.Sprintf(" (%d node(s) unreachable — incomplete)", m.clusterUnreachableNodes)
		}
		if m.clusterDBErr != nil {
			line += " (managed/unmanaged split may be inaccurate — db lookup failed)"
		}
		sb.WriteString(styleDim.Render(line) + "\n\n")
	}

	hdr := fmt.Sprintf("%-3s  %-9s  %-22s  %s", "#", "STATUS", "SERVICE", "DETAIL")
	sb.WriteString(styleDim.Render(hdr) + "\n")

	blocked := m.blockedBy()
	for i, svc := range m.bootstrapServices {
		statusLabel := bsvcStatusLabel(svc.status)
		if svc.provisioning || svc.ingesting {
			statusLabel = m.spinner.View()
		}
		detail := sanitizeText(svc.detail)
		if svc.errMsg != "" {
			detail = styleError.Render(sanitizeText(svc.errMsg))
		}
		if dep, isBlocked := blocked[svc.id]; isBlocked && svc.status == bsvcUnknown {
			detail = styleDim.Render("needs " + dep + " first")
		}
		row := fmt.Sprintf("%-3d  %s  %-22s  %s", i+1, statusLabel, truncate(svc.name, 22), detail)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(row) + "\n")
		} else if _, isBlocked := blocked[svc.id]; isBlocked && svc.status == bsvcUnknown {
			sb.WriteString(styleDim.Render(row) + "\n")
		} else {
			sb.WriteString(row + "\n")
		}
	}

	sb.WriteString("\n" + styleDim.Render("x check all  P provision missing in order  enter act on selected"))
	return sb.String()
}

func (m OrchestrateModel) viewBootstrapActionDialog() string {
	d := m.bsvcActionDialog
	svc := m.bootstrapServiceByID(d.service)
	var sb strings.Builder
	sb.WriteString(styleBold.Render("Action: "+svc.name) + " — " + styleDim.Render(svc.detail) + "\n")
	if d.detail != "" {
		sb.WriteString(styleDim.Render("⚠  "+sanitizeText(d.detail)) + "\n")
	}
	sb.WriteString("\n")
	for i, opt := range d.opts {
		if i == d.cursor {
			sb.WriteString(styleSelected.Render("► "+opt) + "\n")
		} else {
			sb.WriteString("  " + opt + "\n")
		}
	}
	sb.WriteString("\n" + styleDim.Render("↑/↓ navigate  enter confirm  esc cancel"))
	return sb.String()
}

func (m OrchestrateModel) viewProvisionForm() string {
	svcID := m.provisioningFor
	svc := m.bootstrapServiceByID(svcID)
	var sb strings.Builder
	sb.WriteString(styleBold.Render("Provision "+svc.name+" via Salt") + "\n\n")
	labels := []string{"Target minion", "Salt state   "}
	for i, inp := range m.provisionForm.inputs {
		label := styleDim.Render(labels[i] + ": ")
		if i == m.provisionForm.focused {
			label = styleBold.Render(labels[i] + ": ")
		}
		sb.WriteString(label + inp.View() + "\n")
		if i == 0 {
			// Show accepted minions as hints below the target field.
			if len(m.provisionForm.knownHints) > 1 {
				sb.WriteString(styleDim.Render("  known: "+strings.Join(m.provisionForm.knownHints, ", ")) + "\n")
			}
			// Show derived URL preview when a spec is available.
			if target := strings.TrimSpace(inp.Value()); target != "" {
				if spec, ok := bootstrapSpecs[svcID]; ok && spec.patchConfig != nil {
					if u := spec.deriveURL(target, m.cfg.Globals.Domain); u != "" {
						sb.WriteString(styleDim.Render("  → will configure: "+u) + "\n")
					}
				}
			}
		}
	}
	sb.WriteString("\n" + styleDim.Render("tab") + " next  " + styleDim.Render("ctrl+s") + " continue  " + styleDim.Render("esc") + " cancel")
	return sb.String()
}

func (m OrchestrateModel) viewProvisionConfirm() string {
	c := m.provisionConfirm
	var sb strings.Builder
	sb.WriteString(styleBold.Render("Confirm: provision "+c.svcName) + "\n\n")
	sb.WriteString(fmt.Sprintf("Will run:  %s\n", styleBold.Render("salt '"+c.target+"' state.apply "+c.state)))
	if c.targetWarning != "" {
		sb.WriteString(styleError.Render("⚠  "+c.targetWarning) + "\n")
	}
	if c.derivedURL != "" {
		sb.WriteString(styleDim.Render("On success: writes config.toml — "+c.svcID+" url = "+c.derivedURL) + "\n")
		sb.WriteString(styleDim.Render("            (reformats config.toml; comments preserved as generated header)") + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(styleBold.Render("y") + "/" + styleBold.Render("enter") + " confirm  " + styleDim.Render("n/esc") + " cancel")
	return sb.String()
}

func (m OrchestrateModel) bootstrapServiceByID(id string) bootstrapService {
	for _, svc := range m.bootstrapServices {
		if svc.id == id {
			return svc
		}
	}
	return bootstrapService{id: id, name: id}
}

// blockedBy returns a map of svcID → blocking dep ID for services whose deps
// aren't yet OK or skipped. Deps are advisory — blocking is visual only.
func (m OrchestrateModel) blockedBy() map[string]string {
	statusByID := make(map[string]bsvcStatus, len(m.bootstrapServices))
	for _, svc := range m.bootstrapServices {
		statusByID[svc.id] = svc.status
	}
	blocked := map[string]string{}
	for id, spec := range bootstrapSpecs {
		for _, dep := range spec.deps {
			if s := statusByID[dep]; s != bsvcOK && s != bsvcSkipped {
				blocked[id] = dep
				break
			}
		}
	}
	return blocked
}

// ReloadConfig updates the orch model's clients and cfg after a provision has
// written a new service URL to config.toml. All existing bootstrap state is
// preserved; only the specified client pointers are replaced when non-nil.
func (m OrchestrateModel) ReloadConfig(cfg config.Config,
	salt *api.SaltClient, dns *api.TechnitiumClient, ca *api.StepCAClient,
	truenas *api.TrueNASClient, hermes *api.HermesClient,
) OrchestrateModel {
	m.cfg = cfg
	if salt != nil {
		m.saltClient = salt
		m.saltCfg = cfg.Salt
	}
	if dns != nil {
		m.dnsClient = dns
	}
	if ca != nil {
		m.caClient = ca
	}
	if truenas != nil {
		m.truenas = truenas
	}
	if hermes != nil {
		m.hermesClient = hermes
	}
	return m
}

func (m OrchestrateModel) viewConfirm() string {
	c := m.confirm
	var sb strings.Builder
	if c.action == "delete_pillar" {
		sb.WriteString(styleBold.Render("Confirm delete pillar key") + "\n\n")
		sb.WriteString(fmt.Sprintf("Delete pillar key %q? this will need to be re-pushed to re-add it\n", c.pillarKey))
		sb.WriteString("\n")
		sb.WriteString(styleBold.Render("y") + "/" + styleBold.Render("enter") + " confirm  " + styleDim.Render("n/esc") + " cancel")
		return sb.String()
	}
	sb.WriteString(styleBold.Render(fmt.Sprintf("Confirm %s key", c.action)) + "\n\n")
	sb.WriteString(fmt.Sprintf("Minion:  %s\n", styleBold.Render(c.minion)))
	if c.profile != "" {
		sb.WriteString(fmt.Sprintf("Profile: %s  (pillar + states will be applied)\n", styleRunning.Render(c.profile)))
	} else {
		sb.WriteString(styleDim.Render("No matching provisioned VM found — key only, no auto-apply.") + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(styleBold.Render("y") + "/" + styleBold.Render("enter") + " confirm  " + styleDim.Render("n/esc") + " cancel")
	return sb.String()
}
