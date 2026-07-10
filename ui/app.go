package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/config"
	"github.com/alytaphoenix/ratataskr/db"
)

type screenID int

const (
	screenNodes screenID = iota
	screenGuests
	screenDetail
	screenSalt
	screenSaltDetail
	screenOrchestrate
	screenNomad
	screenNomadDetail
	screenProvision
	screenCredentials
	screenProfiles
	screenMetrics
	screenServices
	screenResize
	screenPXE
	screenSecurity
	screenSettings
	screenMaintenance
	screenPlex
	screenRunbooks
	screenAlertRoutes
	screenLifecycle
)

type App struct {
	screen         screenID
	width          int
	height         int
	client         *api.Client
	saltClient     *api.SaltClient
	nomadClient    *api.NomadClient
	promClient     *api.PrometheusClient
	lokiClient     *api.LokiClient
	dnsClient      *api.TechnitiumClient
	caClient       *api.StepCAClient
	claudeClient   *api.ClaudeClient
	matchboxClient *api.MatchboxClient
	csClient       *api.CrowdSecClient
	wazuhClient    *api.WazuhClient
	trueNASClient  *api.TrueNASClient
	plexClient     *api.PlexClient
	haClient       *api.HAClient
	hermesClient   *api.HermesClient
	database       *db.DB
	sshUser        string
	sshPassword    string
	defaults       config.DefaultsConfig
	cfg            config.Config
	nodes          NodesModel
	guests         GuestsModel
	detail         DetailModel
	salt           SaltModel
	saltDetail     SaltDetailModel
	orch           OrchestrateModel
	nomad          NomadModel
	nomadDetail    NomadDetailModel
	provision      ProvisionModel
	credentials    CredentialsModel
	profiles       ProfilesModel
	metrics        MetricsModel
	services       ServicesModel
	resize         ResizeModel
	pxe            PXEModel
	security       SecurityModel
	settings       SettingsModel
	maintenance    MaintenanceModel
	plex           PlexModel
	runbooks       RunbooksModel
	alertRoutes    AlertRoutesModel
	lifecycle      LifecycleModel
	claudeOverlay  ClaudeOverlayModel
	appSettings    db.AppSettings
	// lightweight badge state for nodes screen
	mediaBadgePlex     *bool   // nil=unchecked, true=online, false=offline
	mediaBadgeServices []api.ServarrStatus
	// alert route delivery: track last-seen counts to detect new events
	lastCSBanCount     int // -1 = not yet loaded
	lastWazuhHighCount int // -1 = not yet loaded
}

func NewApp(client *api.Client, saltClient *api.SaltClient, nomadClient *api.NomadClient,
	promClient *api.PrometheusClient, lokiClient *api.LokiClient,
	dnsClient *api.TechnitiumClient, caClient *api.StepCAClient,
	claudeClient *api.ClaudeClient, matchboxClient *api.MatchboxClient,
	csClient *api.CrowdSecClient, wazuhClient *api.WazuhClient,
	trueNASClient *api.TrueNASClient, plexClient *api.PlexClient, haClient *api.HAClient,
	hermesClient *api.HermesClient,
	sshUser, sshPassword string, defaults config.DefaultsConfig, cfg config.Config, database *db.DB) App {
	a := App{
		client:         client,
		saltClient:     saltClient,
		nomadClient:    nomadClient,
		promClient:     promClient,
		lokiClient:     lokiClient,
		dnsClient:      dnsClient,
		caClient:       caClient,
		claudeClient:   claudeClient,
		matchboxClient: matchboxClient,
		csClient:       csClient,
		wazuhClient:    wazuhClient,
		trueNASClient:  trueNASClient,
		plexClient:     plexClient,
		haClient:       haClient,
		hermesClient:   hermesClient,
		database:           database,
		sshUser:            sshUser,
		sshPassword:        sshPassword,
		defaults:           defaults,
		cfg:                cfg,
		lastCSBanCount:     -1,
		lastWazuhHighCount: -1,
		nodes:              NewNodesModel(client, saltClient != nil, nomadClient != nil, database),
		// Constructed eagerly so FireMatchingRoutes has the stored routes even
		// when the Alerts screen has never been opened.
		alertRoutes:   NewAlertRoutesModel(database),
		claudeOverlay: NewClaudeOverlay(claudeClient),
	}
	a.loadAppSettings()
	return a
}

// loadAppSettings loads current settings from DB into the App. Called at init and after settings saved.
func (a *App) loadAppSettings() {
	if a.database != nil {
		a.appSettings = a.database.LoadAppSettings()
	}
}

func (a App) Init() tea.Cmd {
	return tea.Batch(a.nodes.Init(), a.checkMediaStatusCmd(), a.schedulerTickCmd())
}

// schedulerTickCmd re-arms the once-a-minute scheduler tick.
func (a App) schedulerTickCmd() tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg { return schedulerTickMsg{} })
}

// runDueSchedulesCmd fires any maintenance schedules whose run_at has passed.
// Each schedule is advanced (or disabled, for one-shots) before its run starts
// so an overlapping tick cannot fire it twice.
func (a App) runDueSchedulesCmd() tea.Cmd {
	client := a.client
	salt := a.saltClient
	database := a.database
	return func() tea.Msg {
		if database == nil {
			return nil
		}
		due, err := database.DueMaintenanceSchedules(time.Now())
		if err != nil || len(due) == 0 {
			return nil
		}
		var last tea.Msg
		for _, s := range due {
			if err := database.AdvanceMaintenanceSchedule(s, time.Now()); err != nil {
				continue
			}
			var nodeOrder []string
			_ = json.Unmarshal([]byte(s.NodeOrder), &nodeOrder)
			if len(nodeOrder) == 0 {
				continue
			}
			var tf db.MaintenanceTaskFlags
			_ = json.Unmarshal([]byte(s.TaskFlags), &tf)
			runID, err := database.CreateMaintenanceRun(db.MaintenanceRun{
				ScheduleID: s.ID,
				Name:       s.Name + " (scheduled)",
				Status:     "pending",
				NodeOrder:  s.NodeOrder,
				TaskFlags:  s.TaskFlags,
				HAAware:    s.HAAware,
			})
			if err != nil {
				continue
			}
			last = runMaintenance(client, salt, database, runID, nodeOrder, tf, s.HAAware)
		}
		return last
	}
}

// checkMediaStatusCmd does a lightweight Plex + servarr ping for the nodes-screen badge.
func (a App) checkMediaStatusCmd() tea.Cmd {
	plexClient := a.plexClient
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var plexOnline bool
		if plexClient != nil {
			plexOnline = plexClient.Ping(ctx) == nil
		}

		cfgMap := map[string]config.ServarrAppConfig{
			"sonarr":    cfg.Servarr.Sonarr,
			"radarr":    cfg.Servarr.Radarr,
			"lidarr":    cfg.Servarr.Lidarr,
			"readarr":   cfg.Servarr.Readarr,
			"prowlarr":  cfg.Servarr.Prowlarr,
			"bazarr":    cfg.Servarr.Bazarr,
			"overseerr": cfg.Servarr.Overseerr,
			"tautulli":  cfg.Servarr.Tautulli,
		}
		var services []api.ServarrStatus
		for id, appCfg := range cfgMap {
			if appCfg.URL == "" {
				continue
			}
			client := api.NewServarrClient(appCfg.URL, appCfg.APIKey, id, appCfg.Insecure)
			s := client.FetchStatus(ctx)
			s.AppName = id
			services = append(services, s)
		}
		return mediaStatusMsg{plexOnline: plexOnline, services: services}
	}
}

func (a App) sectionRoots() []screenID {
	roots := []screenID{screenNodes}
	if a.saltClient != nil {
		roots = append(roots, screenSalt)
		roots = append(roots, screenOrchestrate)
	}
	if a.nomadClient != nil {
		roots = append(roots, screenNomad)
	}
	roots = append(roots, screenProfiles)
	roots = append(roots, screenMetrics)
	roots = append(roots, screenServices)
	roots = append(roots, screenPXE)
	roots = append(roots, screenSecurity)
	roots = append(roots, screenMaintenance)
	roots = append(roots, screenPlex)
	roots = append(roots, screenRunbooks)
	roots = append(roots, screenAlertRoutes)
	roots = append(roots, screenLifecycle)
	roots = append(roots, screenSettings)
	roots = append(roots, screenCredentials)
	return roots
}

func sectionOf(s screenID) screenID {
	switch s {
	case screenGuests, screenDetail, screenProvision, screenResize:
		return screenNodes
	case screenSaltDetail:
		return screenSalt
	case screenNomadDetail:
		return screenNomad
	default:
		return s
	}
}


// screenCapturesTab reports whether the active screen has a form or text
// input open that uses tab for field navigation, in which case tab must not
// switch sections.
func (a App) screenCapturesTab() bool {
	switch a.screen {
	case screenOrchestrate:
		return a.orch.form != nil
	case screenProfiles:
		return a.profiles.editing != editNone
	case screenCredentials:
		return a.credentials.editing
	case screenMetrics:
		return a.metrics.customActive || a.metrics.lokiActive
	case screenServices:
		return a.services.addingSvc || a.services.addingDNS || a.services.pickingTemplate
	case screenPXE:
		return a.pxe.capturesTab()
	case screenSecurity:
		return a.security.scanEditing || a.security.cveEditing
	case screenSettings:
		return a.settings.editing
	case screenMaintenance:
		return a.maintenance.tab == maintTabConfigure && a.maintenance.wizardStep == maintWizardSchedule
	case screenRunbooks:
		return a.runbooks.editing
	case screenAlertRoutes:
		return a.alertRoutes.editing
	case screenLifecycle:
		return a.lifecycle.editing
	}
	return false
}

func (a App) switchSection(next screenID) (App, tea.Cmd) {
	switch next {
	case screenSalt:
		if a.saltClient != nil {
			a.screen = screenSalt
			a.salt = NewSaltModel(a.saltClient)
			a.salt.width, a.salt.height = a.width, a.height
			return a, a.salt.Init()
		}
	case screenOrchestrate:
		if a.saltClient != nil {
			a.screen = screenOrchestrate
			a.orch = NewOrchestrateModel(a.saltClient, a.database, a.cfg.Salt, a.trueNASClient, a.hermesClient, a.cfg,
				a.dnsClient, a.caClient, a.plexClient)
			a.orch.width, a.orch.height = a.width, a.height
			return a, a.orch.Init()
		}
	case screenNomad:
		if a.nomadClient != nil {
			a.screen = screenNomad
			a.nomad = NewNomadModel(a.nomadClient, a.database)
			a.nomad.width, a.nomad.height = a.width, a.height
			return a, a.nomad.Init()
		}
	case screenCredentials:
		a.screen = screenCredentials
		a.credentials = NewCredentialsModel(a.database, a.cfg.Globals)
		a.credentials.width, a.credentials.height = a.width, a.height
		return a, a.credentials.Init()
	case screenProfiles:
		a.screen = screenProfiles
		a.profiles = NewProfilesModel(a.database)
		a.profiles.width, a.profiles.height = a.width, a.height
		return a, a.profiles.Init()
	case screenMetrics:
		a.screen = screenMetrics
		a.metrics = NewMetricsModel(a.database, a.promClient, a.lokiClient, a.cfg)
		a.metrics.width, a.metrics.height = a.width, a.height
		return a, a.metrics.Init()
	case screenServices:
		a.screen = screenServices
		a.services = NewServicesModel(a.dnsClient, a.caClient, a.trueNASClient, a.haClient, a.database, a.cfg, a.appSettings, a.saltClient)
		a.services.width, a.services.height = a.width, a.height
		return a, a.services.Init()
	case screenPXE:
		a.screen = screenPXE
		a.pxe = NewPXEModel(a.matchboxClient, a.client, a.database)
		a.pxe.width, a.pxe.height = a.width, a.height
		return a, a.pxe.Init()
	case screenSecurity:
		a.screen = screenSecurity
		a.security = NewSecurityModel(a.csClient, a.wazuhClient, a.client, a.database, a.appSettings)
		a.security.width, a.security.height = a.width, a.height
		return a, a.security.Init()
	case screenSettings:
		a.screen = screenSettings
		a.settings = NewSettingsModel(a.database)
		a.settings.width, a.settings.height = a.width, a.height
		return a, a.settings.Init()
	case screenMaintenance:
		a.screen = screenMaintenance
		a.maintenance = NewMaintenanceModel(a.client, a.saltClient, a.database)
		a.maintenance.width, a.maintenance.height = a.width, a.height
		return a, a.maintenance.Init()
	case screenPlex:
		a.screen = screenPlex
		a.plex = NewPlexModel(a.plexClient, a.client, a.trueNASClient, a.database, a.cfg)
		a.plex.width, a.plex.height = a.width, a.height
		return a, a.plex.Init()
	case screenRunbooks:
		a.screen = screenRunbooks
		a.runbooks = NewRunbooksModel(a.database, a.saltClient)
		a.runbooks.width, a.runbooks.height = a.width, a.height
		return a, a.runbooks.Init()
	case screenAlertRoutes:
		a.screen = screenAlertRoutes
		a.alertRoutes = NewAlertRoutesModel(a.database)
		a.alertRoutes.width, a.alertRoutes.height = a.width, a.height
		return a, a.alertRoutes.Init()
	case screenLifecycle:
		a.screen = screenLifecycle
		a.lifecycle = NewLifecycleModel(a.database, a.client)
		a.lifecycle.width, a.lifecycle.height = a.width, a.height
		return a, a.lifecycle.Init()
	case screenNodes:
		a.screen = screenNodes
		a.nodes = NewNodesModel(a.client, a.saltClient != nil, a.nomadClient != nil, a.database)
		a.nodes.width, a.nodes.height = a.width, a.height
		return a, a.nodes.Init()
	}
	return a, nil
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.nodes.width, a.nodes.height = msg.Width, msg.Height
		a.guests.width, a.guests.height = msg.Width, msg.Height
		a.detail.width, a.detail.height = msg.Width, msg.Height
		a.salt.width, a.salt.height = msg.Width, msg.Height
		a.saltDetail.width, a.saltDetail.height = msg.Width, msg.Height
		a.orch.width, a.orch.height = msg.Width, msg.Height
		a.nomad.width, a.nomad.height = msg.Width, msg.Height
		a.nomadDetail.width, a.nomadDetail.height = msg.Width, msg.Height
		a.provision.width, a.provision.height = msg.Width, msg.Height
		a.credentials.width, a.credentials.height = msg.Width, msg.Height
		a.profiles.width, a.profiles.height = msg.Width, msg.Height
		a.metrics.width, a.metrics.height = msg.Width, msg.Height
		a.services.width, a.services.height = msg.Width, msg.Height
		a.resize.width, a.resize.height = msg.Width, msg.Height
		a.pxe.width, a.pxe.height = msg.Width, msg.Height
		a.security.width, a.security.height = msg.Width, msg.Height
		a.settings.width, a.settings.height = msg.Width, msg.Height
		a.maintenance.width, a.maintenance.height = msg.Width, msg.Height
		a.plex.width, a.plex.height = msg.Width, msg.Height
		a.runbooks.width, a.runbooks.height = msg.Width, msg.Height
		a.alertRoutes.width, a.alertRoutes.height = msg.Width, msg.Height
		a.lifecycle.width, a.lifecycle.height = msg.Width, msg.Height
		a.claudeOverlay.width, a.claudeOverlay.height = msg.Width, msg.Height
		return a, nil

	case nodeSelectedMsg:
		a.screen = screenGuests
		a.guests = NewGuestsModel(a.client, msg.node, a.sshUser, a.sshPassword, a.database)
		a.guests.width, a.guests.height = a.width, a.height
		return a, a.guests.Init()

	case guestSelectedMsg:
		a.screen = screenDetail
		a.detail = NewDetailModel(a.client, a.saltClient, msg.guest, a.sshUser, a.sshPassword, a.defaults.BackupStorage)
		a.detail.width, a.detail.height = a.width, a.height
		return a, a.detail.Init()

	case openSaltMsg:
		return a.switchSection(screenSalt)

	case openOrchestrateMsg:
		return a.switchSection(screenOrchestrate)

	case minionSelectedMsg:
		a.screen = screenSaltDetail
		a.saltDetail = NewSaltDetailModel(a.saltClient, msg.minion, a.database)
		a.saltDetail.width, a.saltDetail.height = a.width, a.height
		return a, a.saltDetail.Init()

	case openNomadMsg:
		return a.switchSection(screenNomad)

	case nomadJobSelectedMsg:
		a.screen = screenNomadDetail
		a.nomadDetail = NewNomadDetailModel(a.nomadClient, msg.job, a.database)
		a.nomadDetail.width, a.nomadDetail.height = a.width, a.height
		return a, a.nomadDetail.Init()

	case openProvisionMsg:
		a.screen = screenProvision
		a.provision = NewProvisionModel(a.client, msg.node, a.defaults, a.database)
		a.provision.width, a.provision.height = a.width, a.height
		return a, a.provision.Init()

	case openCredentialsMsg:
		return a.switchSection(screenCredentials)

	case openProfilesMsg:
		return a.switchSection(screenProfiles)

	case openMetricsMsg:
		return a.switchSection(screenMetrics)

	case openServicesMsg:
		return a.switchSection(screenServices)

	case openPXEMsg:
		return a.switchSection(screenPXE)

	case openSecurityMsg:
		return a.switchSection(screenSecurity)

	case openSettingsMsg:
		return a.switchSection(screenSettings)

	case openMaintenanceMsg:
		return a.switchSection(screenMaintenance)

	case openPlexMsg:
		return a.switchSection(screenPlex)

	case openRunbooksMsg:
		return a.switchSection(screenRunbooks)

	case openAlertRoutesMsg:
		return a.switchSection(screenAlertRoutes)

	case openLifecycleMsg:
		return a.switchSection(screenLifecycle)

	case crowdSecLoadedMsg:
		var secCmd tea.Cmd
		a.security, secCmd = a.security.Update(msg)
		if msg.err == nil {
			banCount := 0
			for _, d := range msg.decisions {
				if d.Type == "ban" {
					banCount++
				}
			}
			if a.lastCSBanCount >= 0 && banCount > a.lastCSBanCount {
				fireCmd := a.alertRoutes.FireMatchingRoutes("crowdsec", "high")
				a.lastCSBanCount = banCount
				return a, tea.Batch(secCmd, fireCmd)
			}
			a.lastCSBanCount = banCount
		}
		return a, secCmd

	case wazuhLoadedMsg:
		var secCmd tea.Cmd
		a.security, secCmd = a.security.Update(msg)
		if msg.err == nil {
			highCount := 0
			sev := "high"
			for _, al := range msg.alerts {
				if al.Rule.Level >= a.appSettings.WazuhHighLevel {
					highCount++
				}
				if al.Rule.Level >= a.appSettings.WazuhCriticalLevel {
					sev = "critical"
				}
			}
			if a.lastWazuhHighCount >= 0 && highCount > a.lastWazuhHighCount {
				fireCmd := a.alertRoutes.FireMatchingRoutes("wazuh", sev)
				a.lastWazuhHighCount = highCount
				return a, tea.Batch(secCmd, fireCmd)
			}
			a.lastWazuhHighCount = highCount
		}
		return a, secCmd

	case claudeOverlayMsg:
		a.claudeOverlay = a.claudeOverlay.Open(a.currentContext())
		return a, a.claudeOverlay.Init()

	case claudeResponseMsg:
		var cmd tea.Cmd
		a.claudeOverlay, cmd = a.claudeOverlay.Update(msg)
		return a, cmd

	case mediaStatusMsg:
		online := msg.plexOnline
		a.mediaBadgePlex = &online
		a.mediaBadgeServices = msg.services
		return a, nil

	case schedulerTickMsg:
		return a, tea.Batch(a.runDueSchedulesCmd(), a.schedulerTickCmd())

	case settingsSavedMsg:
		// Reload app settings and push to any active sub-models that hold a copy.
		a.loadAppSettings()
		a.security.appSettings = a.appSettings
		a.services.appSettings = a.appSettings

	case openResizeMsg:
		a.screen = screenResize
		a.resize = NewResizeModel(a.client, a.claudeClient, msg.node, msg.guests, a.database)
		a.resize.width, a.resize.height = a.width, a.height
		return a, a.resize.Init()

	case backMsg:
		switch a.screen {
		case screenGuests, screenSalt, screenOrchestrate, screenNomad, screenCredentials, screenProfiles,
			screenMetrics, screenServices, screenPXE, screenSecurity, screenSettings,
			screenMaintenance, screenPlex, screenRunbooks, screenAlertRoutes, screenLifecycle:
			a.screen = screenNodes
		case screenResize:
			a.screen = screenGuests
		case screenDetail:
			a.screen = screenGuests
		case screenSaltDetail:
			a.screen = screenSalt
		case screenNomadDetail:
			a.screen = screenNomad
		case screenProvision:
			a.screen = screenGuests
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		// Global Claude overlay toggle
		if msg.String() == "ctrl+a" && a.claudeClient != nil {
			if a.claudeOverlay.visible {
				a.claudeOverlay = a.claudeOverlay.Close()
			} else {
				a.claudeOverlay = a.claudeOverlay.Open(a.currentContext())
				return a, a.claudeOverlay.Init()
			}
			return a, nil
		}
		// If overlay visible, send keys there
		if a.claudeOverlay.visible {
			var oCmd tea.Cmd
			a.claudeOverlay, oCmd = a.claudeOverlay.Update(msg)
			return a, oCmd
		}
		cur := sectionOf(a.screen)
		if msg.String() == "tab" || msg.String() == "shift+tab" {
			if !a.screenCapturesTab() {
				roots := a.sectionRoots()
				idx := 0
				for i, r := range roots {
					if r == cur {
						idx = i
						break
					}
				}
				var next int
				if msg.String() == "tab" {
					next = (idx + 1) % len(roots)
				} else {
					next = (idx - 1 + len(roots)) % len(roots)
				}
				if roots[next] != cur {
					return a.switchSection(roots[next])
				}
				return a, nil
			}
		}
	}

	var cmd tea.Cmd
	switch a.screen {
	case screenNodes:
		a.nodes, cmd = a.nodes.Update(msg)
	case screenGuests:
		a.guests, cmd = a.guests.Update(msg)
	case screenDetail:
		a.detail, cmd = a.detail.Update(msg)
	case screenSalt:
		a.salt, cmd = a.salt.Update(msg)
	case screenSaltDetail:
		a.saltDetail, cmd = a.saltDetail.Update(msg)
	case screenOrchestrate:
		a.orch, cmd = a.orch.Update(msg)
	case screenNomad:
		a.nomad, cmd = a.nomad.Update(msg)
	case screenNomadDetail:
		a.nomadDetail, cmd = a.nomadDetail.Update(msg)
	case screenProvision:
		a.provision, cmd = a.provision.Update(msg)
	case screenCredentials:
		a.credentials, cmd = a.credentials.Update(msg)
	case screenProfiles:
		a.profiles, cmd = a.profiles.Update(msg)
	case screenMetrics:
		a.metrics, cmd = a.metrics.Update(msg)
	case screenServices:
		a.services, cmd = a.services.Update(msg)
	case screenResize:
		a.resize, cmd = a.resize.Update(msg)
	case screenPXE:
		a.pxe, cmd = a.pxe.Update(msg)
	case screenSecurity:
		a.security, cmd = a.security.Update(msg)
	case screenSettings:
		a.settings, cmd = a.settings.Update(msg)
	case screenMaintenance:
		a.maintenance, cmd = a.maintenance.Update(msg)
	case screenPlex:
		a.plex, cmd = a.plex.Update(msg)
	case screenRunbooks:
		a.runbooks, cmd = a.runbooks.Update(msg)
	case screenAlertRoutes:
		a.alertRoutes, cmd = a.alertRoutes.Update(msg)
	case screenLifecycle:
		a.lifecycle, cmd = a.lifecycle.Update(msg)
	}

	// Claude overlay receives updates regardless of screen if visible
	if a.claudeOverlay.visible {
		var oCmd tea.Cmd
		a.claudeOverlay, oCmd = a.claudeOverlay.Update(msg)
		if oCmd != nil {
			cmd = tea.Batch(cmd, oCmd)
		}
	}

	return a, cmd
}

func (a App) tabBar() string {
	type tabEntry struct {
		label  string
		screen screenID
	}
	entries := []tabEntry{{"Proxmox", screenNodes}}
	if a.saltClient != nil {
		entries = append(entries, tabEntry{"Salt", screenSalt})
		entries = append(entries, tabEntry{"Orch", screenOrchestrate})
	}
	if a.nomadClient != nil {
		entries = append(entries, tabEntry{"Nomad", screenNomad})
	}
	entries = append(entries, tabEntry{"Profiles", screenProfiles})
	entries = append(entries, tabEntry{"Metrics", screenMetrics})
	entries = append(entries, tabEntry{"Services", screenServices})
	entries = append(entries, tabEntry{"PXE", screenPXE})
	entries = append(entries, tabEntry{"Security", screenSecurity})
	entries = append(entries, tabEntry{"Maintenance", screenMaintenance})
	entries = append(entries, tabEntry{"Plex", screenPlex})
	entries = append(entries, tabEntry{"Runbooks", screenRunbooks})
	entries = append(entries, tabEntry{"Alerts", screenAlertRoutes})
	entries = append(entries, tabEntry{"Lifecycle", screenLifecycle})
	entries = append(entries, tabEntry{"Settings", screenSettings})
	entries = append(entries, tabEntry{"Keys", screenCredentials})

	cur := sectionOf(a.screen)
	var parts []string
	for _, t := range entries {
		label := " " + t.label + " "
		if t.screen == cur {
			parts = append(parts, styleTabActive.Render("▸"+label))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+label))
		}
	}

	maxw := max(a.width, 40)
	sep := styleDim.Render("│")
	var lines []string
	line, lineW := "", 0
	for _, p := range parts {
		w := lipgloss.Width(p)
		if lineW > 0 && lineW+1+w > maxw {
			lines = append(lines, line)
			line, lineW = "", 0
		}
		if lineW > 0 {
			line += sep
			lineW++
		}
		line += p
		lineW += w
	}
	hint := styleDim.Render(" tab/shift+tab")
	if lineW+2+lipgloss.Width(hint) <= maxw {
		line += "  " + hint
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n") + "\n" + styleDim.Render(strings.Repeat("─", maxw)) + "\n"
}

func (a App) View() string {
	content := ""
	switch a.screen {
	case screenNodes:
		content = a.nodes.View() + a.mediaBadgeBanner()
	case screenGuests:
		content = a.guests.View()
	case screenDetail:
		content = a.detail.View()
	case screenSalt:
		content = a.salt.View()
	case screenSaltDetail:
		content = a.saltDetail.View()
	case screenOrchestrate:
		content = a.orch.View()
	case screenNomad:
		content = a.nomad.View()
	case screenNomadDetail:
		content = a.nomadDetail.View()
	case screenProvision:
		content = a.provision.View()
	case screenCredentials:
		content = a.credentials.View()
	case screenProfiles:
		content = a.profiles.View()
	case screenMetrics:
		content = a.metrics.View()
	case screenServices:
		content = a.services.View()
	case screenResize:
		content = a.resize.View()
	case screenPXE:
		content = a.pxe.View()
	case screenSecurity:
		content = a.security.View()
	case screenSettings:
		content = a.settings.View()
	case screenMaintenance:
		content = a.maintenance.View()
	case screenPlex:
		content = a.plex.View()
	case screenRunbooks:
		content = a.runbooks.View()
	case screenAlertRoutes:
		content = a.alertRoutes.View()
	case screenLifecycle:
		content = a.lifecycle.View()
	}

	view := a.tabBar() + content
	if a.width > 0 {
		view = lipgloss.NewStyle().MaxWidth(a.width).Render(view)
	}
	if a.claudeOverlay.visible {
		view = a.claudeOverlay.renderOver(view, a.width, a.height)
	}
	return view
}

// mediaBadgeBanner returns a one-line service status banner for the nodes screen.
// Shows badges for Plex and any configured servarr services.
func (a App) mediaBadgeBanner() string {
	// If no media services configured at all, show nothing
	hasAny := a.plexClient != nil || len(a.mediaBadgeServices) > 0
	if !hasAny && a.mediaBadgePlex == nil {
		return ""
	}

	var parts []string

	// Plex badge
	if a.plexClient != nil {
		if a.mediaBadgePlex == nil {
			parts = append(parts, styleDim.Render("● Plex …"))
		} else if *a.mediaBadgePlex {
			parts = append(parts, lipgloss.NewStyle().Foreground(colorGreen).Render("● Plex"))
		} else {
			parts = append(parts, lipgloss.NewStyle().Foreground(colorRed).Render("● Plex"))
		}
	}

	// Servarr badges
	for _, svc := range a.mediaBadgeServices {
		name := svc.AppName
		if svc.Online {
			badge := lipgloss.NewStyle().Foreground(colorGreen).Render("● " + name)
			if len(svc.Health) > 0 {
				badge += lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("⚠%d", len(svc.Health)))
			}
			parts = append(parts, badge)
		} else {
			parts = append(parts, lipgloss.NewStyle().Foreground(colorRed).Render("● " + name))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return "\n  " + styleDim.Render("Media: ") + strings.Join(parts, styleDim.Render("  ")) + "\n"
}

// currentContext returns a short summary of the current screen state for the Claude overlay.
func (a App) currentContext() string {
	switch a.screen {
	case screenNodes:
		return fmt.Sprintf("Viewing %d Proxmox nodes", len(a.nodes.nodes))
	case screenGuests:
		return fmt.Sprintf("Viewing guests on node %s (%d total)", a.guests.node.Name, len(a.guests.guests))
	case screenDetail:
		return fmt.Sprintf("Guest detail: %s (VMID %d, status: %s)", a.detail.guest.Name, a.detail.guest.VMID, a.detail.guest.Status)
	case screenSalt:
		return fmt.Sprintf("Salt minions: %d total", len(a.salt.minions))
	case screenNomad:
		return fmt.Sprintf("Nomad jobs: %d total", len(a.nomad.jobs))
	case screenMetrics:
		return "Viewing cluster metrics and Prometheus data"
	case screenServices:
		return fmt.Sprintf("Services: %d tracked", len(a.services.services))
	case screenSecurity:
		return "Viewing security — CrowdSec, Wazuh, port scan, CVE"
	case screenMaintenance:
		return "Viewing maintenance schedules and runs"
	case screenRunbooks:
		return fmt.Sprintf("Runbooks: %d defined", len(a.runbooks.runbooks))
	default:
		return "ratataskr homelab dashboard"
	}
}

