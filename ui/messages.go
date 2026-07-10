package ui

import (
	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// Proxmox messages
type backMsg struct{}
type nodeSelectedMsg struct{ node api.Node }
type guestSelectedMsg struct{ guest api.Guest }
type nodesLoadedMsg struct{ nodes []api.Node }
type guestsLoadedMsg struct{ guests []api.Guest }
type guestStatusMsg struct{ guest api.Guest }
type actionDoneMsg struct{ err error }
type errMsg struct{ err error }
type vmStatusSyncedMsg struct{} // DB write complete; no UI update needed

// Salt messages
type openSaltMsg struct{}
type minionsLoadedMsg struct{ minions []api.Minion }
type minionSelectedMsg struct{ minion api.Minion }
type saltActionDoneMsg struct {
	notice  string
	jid     string // Salt job ID for result polling
	eventID int64  // DB row ID for UpdateSaltApplyResult
	err     error
}
type saltJobsMsg struct {
	jobs []api.Job
	err  error
}

// Salt result poll messages
type saltPollTickMsg struct {
	jid     string
	eventID int64
	attempt int
}
type saltPollResultMsg struct {
	jid     string
	eventID int64
	success bool
	errMsg  string
	done    bool // false = not yet complete, keep polling
}

// Nomad messages
type openNomadMsg struct{}
type nomadJobsLoadedMsg struct{ jobs []api.NomadJob }
type nomadJobSelectedMsg struct{ job api.NomadJob }
type nomadAllocsLoadedMsg struct {
	allocs []api.NomadAllocation
	err    error
}
type nomadActionDoneMsg struct {
	notice string
	err    error
}
type nomadSyncDoneMsg struct {
	newEvents    []db.NomadJobEvent
	recentEvents []db.NomadJobEvent
}

// Provision messages
type openProvisionMsg struct{ node api.Node }
type provisionDoneMsg struct {
	taskID string
	vmid   int
	count  int
	err    error
}

// Credentials messages
type openCredentialsMsg struct{}

// Profiles messages
type openProfilesMsg struct{}

// Metrics messages
type openMetricsMsg struct{}
type metricsDBLoadedMsg struct {
	snapshot *db.MetricsSnapshot
	err      error
}
type metricsPromLoadedMsg struct {
	tab     int // metricsTab value; avoid import cycle with a plain int
	results []api.PromSample
	err     error
}

// Resize / right-sizing messages
type openResizeMsg struct {
	node   api.Node
	guests []api.Guest
}
type resizeLoadedMsg struct {
	nodeStats *api.NodeStats
	analyses  []api.AnalysisResult
	err       error
}
type claudeExplainMsg struct {
	vmid        int
	explanation *api.VMResizeExplanation
	err         error
}
type resizeAppliedMsg struct {
	vmid int
	err  error
}
type diskResizeAppliedMsg struct {
	vmid int
	err  error
}

// Services messages
type openServicesMsg struct{}
type trueNASLoadedMsg struct {
	status *api.TrueNASStatus
	err    error
}

// Service host provision / registration messages
type svcHostsLoadedMsg struct {
	hosts map[string]db.ServiceHost
	err   error
}
type svcProvisionCheckMsg struct {
	host               string
	alreadyRegistered  bool
	existingHost       *db.ServiceHost
	alreadyProvisioned bool   // found in provisioned_vms by IP
	provisionedVMID    int
	provisionedNode    string
}
type svcDiscoveryDoneMsg struct {
	host     string
	findings []api.ScanFinding
	err      error
}
type svcRegisteredMsg struct {
	host             string
	registrationType string // "managed" | "unmanaged"
	err              error
}
type svcBannedHostsLoadedMsg struct {
	banned map[string]bool // IPs/hostnames with active CrowdSec bans
}
type svcPortsUpdatedMsg struct{} // discovery updated service_host discovered_ports in DB
type servicesLoadedMsg struct {
	services []db.Service
	checks   map[int64]db.HealthCheck
	certs    []db.Certificate
	uptimes  map[int64]float64
	err      error
}
type healthCheckedMsg struct {
	results []api.HealthResult
	err     error
}
type dnsZonesLoadedMsg struct {
	zones []api.DNSZone
	err   error
}
type dnsRecordsLoadedMsg struct {
	zone    string
	records []api.DNSRecord
	err     error
}
type serviceAddedMsg struct {
	id  int64
	err error
}
type serviceDeletedMsg struct{ err error }
type svcWiringAppliedMsg struct {
	notice string
	err    error
}
type dnsAddedMsg struct{ err error }
type dnsDeletedMsg struct{ err error }
type caHealthMsg struct {
	status string // "ok", "error", or message
	err    error
}

// PXE messages
type pxeLoadedMsg struct {
	profiles []db.PXEProfile
	groups   []db.PXEGroup
	bootLog  []db.PXEBootEvent
}
type matchboxStatusMsg struct{ online bool }
type matchboxSyncedMsg struct{ err error }
type pxeProfileSavedMsg struct{ err error }
type pxeGroupSavedMsg struct{ err error }

// Security messages
type openSecurityMsg struct{}

// Scan messages
type scanGuestsLoadedMsg struct {
	guests []api.Guest
	err    error
}
type scanCompleteMsg struct {
	findings []api.ScanFinding
	err      error
}
type scanRemediatedMsg struct {
	finding api.ScanFinding
	err     error
}
type cveScanDoneMsg struct {
	findings []api.CVEFinding
	err      error
}

type crowdSecLoadedMsg struct {
	decisions []api.CrowdSecDecision
	alerts    []api.CrowdSecAlert
	bouncers  []api.CrowdSecBouncer
	err       error
}
type wazuhLoadedMsg struct {
	agents   []api.WazuhAgent
	alerts   []api.WazuhAlert
	summary  api.WazuhSummary
	err      error
	alertErr error // non-nil if alerts endpoint failed (non-fatal; varies by Wazuh version)
}
type secHistoryLoadedMsg struct {
	events []db.SecurityEvent
	err    error
}
type secActionDoneMsg struct{ err error }
type secScanHistoryMsg struct {
	results []db.ScanResult
	err     error
}
type secServiceHostsLoadedMsg struct {
	hosts map[string]db.ServiceHost
	err   error
}

// Settings messages
type openSettingsMsg struct{}
type settingsLoadedMsg struct {
	settings []db.AppSetting
	err      error
}
type settingsSavedMsg struct {
	key string
	err error
}

// PXE screen opener
type openPXEMsg struct{}

// Spawn node wizard messages
type clusterStatusMsg struct {
	nodes []api.ClusterNode
	err   error
}
type pxeSpawnDoneMsg struct {
	groupID string
	err     error
}

// Plex messages
type openPlexMsg struct{}
type plexStatusMsg struct {
	status *api.PlexStatus
	err    error
}
type plexMediaRootsMsg struct {
	roots []string
}
type plexServicesCheckedMsg struct {
	results []api.ServarrStatus
}
type plexAutoConfigMsg struct {
	added []string
	err   error
}

// mediaStatusMsg carries a lightweight Plex + servarr status summary for the nodes-screen badge.
type mediaStatusMsg struct {
	plexOnline bool
	services   []api.ServarrStatus
	err        error
}

// Maintenance messages
type openMaintenanceMsg struct{}
type maintNodesLoadedMsg struct {
	nodes   []api.Node
	cluster []api.ClusterNode
	err     error
}
type maintRunsLoadedMsg struct {
	runs []db.MaintenanceRun
	err  error
}
type maintSchedulesLoadedMsg struct {
	schedules []db.MaintenanceSchedule
	err       error
}
type maintStepsLoadedMsg struct {
	runID int64
	steps []db.MaintenanceRunStep
	err   error
}
type maintSavedMsg struct {
	schedID int64
	runID   int64
	runNow  bool
	err     error
}
type maintBeginExecMsg struct {
	runID     int64
	nodeOrder []string
	taskFlags db.MaintenanceTaskFlags
	haAware   bool
}
type maintRunDoneMsg struct {
	runID int64
	err   error
}

// schedulerTickMsg drives the app-level scheduler that fires due
// maintenance schedules regardless of which screen is open.
type schedulerTickMsg struct{}

// Bulk VM action messages
type bulkActionDoneMsg struct {
	action string
	count  int
	err    error
}

// Claude overlay messages
type claudeOverlayMsg struct{}
type claudeQueryMsg  struct{ question string }
type claudeResponseMsg struct {
	answer string
	err    error
}

// Help overlay toggle
type helpOverlayMsg struct{}

// Config validation
type configCheckResult struct {
	name string
	ok   bool
	msg  string
}
type configValidatedMsg struct {
	results []configCheckResult
}

// Runbook messages
type openRunbooksMsg   struct{}
type runbookRunDoneMsg struct {
	runID int64
	err   error
}

// Alert route messages
type openAlertRoutesMsg struct{}
type alertFiredMsg struct {
	routeID int64
	err     error
}

// Lifecycle messages
type openLifecycleMsg    struct{}
type lifecycleRunDoneMsg struct {
	applied int
	err     error
}

// Node metrics snapshot (for sparklines)
type nodeMetricsSnapshotMsg struct {
	node string
	err  error
}

// SSH messages
type guestIPMsg struct {
	guest api.Guest
	ip    string
	err   error
}
type sshDoneMsg struct{ err error }

// Snapshot messages
type snapshotsLoadedMsg struct {
	snaps []api.Snapshot
	err   error
}
type snapshotCreatedMsg struct {
	upid string
	err  error
}
type snapshotDeletedMsg struct {
	upid string
	err  error
}

// Backup messages
type backupsLoadedMsg struct {
	entries []api.BackupEntry
	err     error
}

// Orchestrate messages
type openOrchestrateMsg struct{}
type orchKeysLoadedMsg struct {
	keys *api.SaltKeys
	err  error
}
type orchPendingLoadedMsg struct {
	vms []db.ProvisionedVM
	err error
}
type orchPillarLoadedMsg struct {
	entries []db.ClusterPillarEntry
	err     error
}
type orchKeyTickMsg struct{}
type orchAcceptDoneMsg struct {
	minion       string
	action       string // "accept" or "reject"
	failedStates []string
	err          error
}
type orchPillarPushedMsg struct{ err error }
type orchTrueNASSyncedMsg struct{ err error }

// Bootstrap execution messages
type orchBootstrapCheckMsg struct{}
type orchSvcCheckDoneMsg struct {
	id     string
	status string // "ok", "partial", "missing", "notconfigured", "error"
	detail string
	err    error
}
type orchSvcIngestDoneMsg struct {
	id     string
	notice string
	err    error
}
type orchSvcProvisionStartedMsg struct {
	id  string
	jid string
}
type orchSvcProvisionDoneMsg struct {
	id           string
	jid          string
	failedStates []string
	err          error
}
