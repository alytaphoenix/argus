package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Tabs ──────────────────────────────────────────────────────────────────────

type pxeTab int

const (
	pxeTabProfiles  pxeTab = iota
	pxeTabGroups
	pxeTabBootLog
	pxeTabTemplates
	pxeTabSpawn
	pxeTabCount
)

var pxeTabLabels = []string{"Profiles", "Groups", "Boot Log", "Templates", "Spawn Node"}

// ── Form field indices ────────────────────────────────────────────────────────

const (
	pxeProfFieldID     = 0
	pxeProfFieldName   = 1
	pxeProfFieldDesc   = 2
	pxeProfFieldKernel = 3
	pxeProfFieldInitrd = 4
	pxeProfFieldArgs   = 5
	pxeProfFieldCount  = 6
)

const (
	pxeGrpFieldID      = 0
	pxeGrpFieldName    = 1
	pxeGrpFieldProfile = 2
	pxeGrpFieldMAC     = 3
	pxeGrpFieldCount   = 4
)

// Spawn wizard field indices
const (
	spawnFieldHostname  = 0
	spawnFieldMAC       = 1
	spawnFieldIPConfig  = 2
	spawnFieldClusterIP = 3 // HA only: existing cluster node IP
	spawnFieldCount     = 4
)

// Spawn wizard steps
const (
	spawnStepType      = 0 // choose: standalone | ha
	spawnStepProfile   = 1 // select PXE profile
	spawnStepHostInfo  = 2 // hostname + MAC form
	spawnStepNetwork   = 3 // IP config form
	spawnStepCluster   = 4 // HA cluster join IP (HA only)
	spawnStepConfirm   = 5 // review + confirm
	spawnStepLaunched  = 6 // waiting for node to boot
)

// ── Model ─────────────────────────────────────────────────────────────────────

// PXEModel manages PXE profiles, groups, and boot history.
type PXEModel struct {
	width, height int
	tab           pxeTab

	matchboxClient *api.MatchboxClient
	proxmoxClient  *api.Client
	database       *db.DB

	// Profiles tab
	profiles   []db.PXEProfile
	profCursor int
	addingProf bool
	profFields [pxeProfFieldCount]textinput.Model
	profFocus  int

	// Groups tab
	groups     []db.PXEGroup
	grpCursor  int
	addingGrp  bool
	grpFields  [pxeGrpFieldCount]textinput.Model
	grpFocus   int

	// Boot log tab
	bootLog    []db.PXEBootEvent
	bootCursor int

	// Templates tab (read-only catalog)
	tmplCursor int

	// Matchbox sync status
	matchboxOnline  bool
	matchboxChecked bool

	// Spawn wizard
	spawnStep        int
	spawnNodeType    string // "standalone" | "ha"
	spawnProfileIdx  int
	spawnFields      [spawnFieldCount]textinput.Model
	spawnFocus       int
	spawnClusterNodes []api.ClusterNode
	spawnChecking    bool
	spawning         bool
	spawnGroupID     string
	spawnErr         string

	spinner spinner.Model
	loading bool
	notice  string
}

func NewPXEModel(matchboxClient *api.MatchboxClient, proxmoxClient *api.Client, database *db.DB) PXEModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// Profile form fields
	var pf [pxeProfFieldCount]textinput.Model
	pfPlaceholders := []string{
		"id (slug, e.g. ubuntu-24-04)",
		"display name",
		"description",
		"kernel URL",
		"initrd URLs (comma-separated, or empty)",
		"kernel args",
	}
	for i := range pf {
		ti := textinput.New()
		ti.Placeholder = pfPlaceholders[i]
		ti.CharLimit = 512
		pf[i] = ti
	}

	// Group form fields
	var gf [pxeGrpFieldCount]textinput.Model
	gfPlaceholders := []string{
		"id (slug)",
		"display name",
		"profile id",
		"MAC address (empty = catch-all)",
	}
	for i := range gf {
		ti := textinput.New()
		ti.Placeholder = gfPlaceholders[i]
		ti.CharLimit = 128
		gf[i] = ti
	}

	// Spawn wizard form fields
	var sf [spawnFieldCount]textinput.Model
	sfPlaceholders := []string{
		"hostname (e.g. pve-node-04)",
		"MAC address (e.g. aa:bb:cc:dd:ee:ff)",
		"IP/CIDR or 'dhcp' (e.g. 192.168.1.50/24)",
		"existing cluster node IP (e.g. 192.168.1.10)",
	}
	for i := range sf {
		ti := textinput.New()
		ti.Placeholder = sfPlaceholders[i]
		ti.CharLimit = 256
		sf[i] = ti
	}

	return PXEModel{
		matchboxClient: matchboxClient,
		proxmoxClient:  proxmoxClient,
		database:       database,
		spinner:        sp,
		profFields:     pf,
		grpFields:      gf,
		spawnFields:    sf,
		spawnNodeType:  "standalone",
	}
}

func (m PXEModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadAll(), m.checkMatchbox())
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m PXEModel) loadAll() tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return pxeLoadedMsg{}
		}
		profiles, _ := database.PXEProfiles()
		groups, _ := database.PXEGroups()
		boots, _ := database.RecentPXEBoots(50)
		return pxeLoadedMsg{profiles: profiles, groups: groups, bootLog: boots}
	}
}

func (m PXEModel) checkMatchbox() tea.Cmd {
	client := m.matchboxClient
	return func() tea.Msg {
		if client == nil {
			return matchboxStatusMsg{online: false}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := client.Ping(ctx)
		return matchboxStatusMsg{online: err == nil}
	}
}

func (m PXEModel) syncToMatchbox(profiles []db.PXEProfile, groups []db.PXEGroup) tea.Cmd {
	client := m.matchboxClient
	return func() tea.Msg {
		if client == nil {
			return matchboxSyncedMsg{err: fmt.Errorf("Matchbox not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		for _, p := range profiles {
			var initrds []string
			json.Unmarshal([]byte(p.InitrdURLs), &initrds) //nolint:errcheck
			args := strings.Fields(p.KernelArgs)
			err := client.CreateProfile(ctx, api.MatchboxProfile{
				ID:   p.ID,
				Name: p.Name,
				Boot: api.MatchboxBoot{
					Kernel: p.KernelURL,
					Initrd: initrds,
					Args:   args,
				},
			})
			if err != nil {
				return matchboxSyncedMsg{err: fmt.Errorf("profile %s: %w", p.ID, err)}
			}
		}
		for _, g := range groups {
			selector := map[string]string{}
			if g.MACAddress != "" {
				selector["mac"] = g.MACAddress
			}
			err := client.CreateGroup(ctx, api.MatchboxGroup{
				ID:       g.ID,
				Name:     g.Name,
				Profile:  g.ProfileID,
				Selector: selector,
			})
			if err != nil {
				return matchboxSyncedMsg{err: fmt.Errorf("group %s: %w", g.ID, err)}
			}
		}
		return matchboxSyncedMsg{}
	}
}

func (m PXEModel) saveProfileCmd() tea.Cmd {
	database := m.database
	p := db.PXEProfile{
		ID:          strings.TrimSpace(m.profFields[pxeProfFieldID].Value()),
		Name:        strings.TrimSpace(m.profFields[pxeProfFieldName].Value()),
		Description: strings.TrimSpace(m.profFields[pxeProfFieldDesc].Value()),
		KernelURL:   strings.TrimSpace(m.profFields[pxeProfFieldKernel].Value()),
		KernelArgs:  strings.TrimSpace(m.profFields[pxeProfFieldArgs].Value()),
	}
	// Parse initrd as JSON array or comma list.
	initrdRaw := strings.TrimSpace(m.profFields[pxeProfFieldInitrd].Value())
	if initrdRaw != "" && !strings.HasPrefix(initrdRaw, "[") {
		parts := strings.Split(initrdRaw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		b, _ := json.Marshal(parts)
		p.InitrdURLs = string(b)
	} else {
		p.InitrdURLs = initrdRaw
	}
	return func() tea.Msg {
		if database == nil {
			return pxeProfileSavedMsg{err: fmt.Errorf("no database")}
		}
		return pxeProfileSavedMsg{err: database.SavePXEProfile(p)}
	}
}

func (m PXEModel) deleteProfileCmd(id string) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return pxeProfileSavedMsg{}
		}
		return pxeProfileSavedMsg{err: database.DeletePXEProfile(id)}
	}
}

func (m PXEModel) saveGroupCmd() tea.Cmd {
	database := m.database
	g := db.PXEGroup{
		ID:         strings.TrimSpace(m.grpFields[pxeGrpFieldID].Value()),
		Name:       strings.TrimSpace(m.grpFields[pxeGrpFieldName].Value()),
		ProfileID:  strings.TrimSpace(m.grpFields[pxeGrpFieldProfile].Value()),
		MACAddress: strings.TrimSpace(m.grpFields[pxeGrpFieldMAC].Value()),
	}
	return func() tea.Msg {
		if database == nil {
			return pxeGroupSavedMsg{err: fmt.Errorf("no database")}
		}
		return pxeGroupSavedMsg{err: database.SavePXEGroup(g)}
	}
}

func (m PXEModel) deleteGroupCmd(id string) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return pxeGroupSavedMsg{err: fmt.Errorf("no database")}
		}
		return pxeGroupSavedMsg{err: database.DeletePXEGroup(id)}
	}
}

// cloneTemplateCmd creates a new DB profile from a built-in template.
func (m PXEModel) cloneTemplateCmd(tmpl api.PXETemplate) tea.Cmd {
	database := m.database
	return func() tea.Msg {
		if database == nil {
			return pxeProfileSavedMsg{err: fmt.Errorf("no database")}
		}
		var initrdJSON string
		if len(tmpl.Initrd) > 0 {
			b, _ := json.Marshal(tmpl.Initrd)
			initrdJSON = string(b)
		}
		p := db.PXEProfile{
			ID:          tmpl.Key,
			Name:        tmpl.Name,
			Description: tmpl.Description,
			KernelURL:   tmpl.Kernel,
			InitrdURLs:  initrdJSON,
			KernelArgs:  tmpl.Args,
			Template:    tmpl.Key,
		}
		return pxeProfileSavedMsg{err: database.SavePXEProfile(p)}
	}
}

// checkClusterStatusCmd fetches Proxmox cluster membership for HA wizard step.
func (m PXEModel) checkClusterStatusCmd() tea.Cmd {
	client := m.proxmoxClient
	return func() tea.Msg {
		if client == nil {
			return clusterStatusMsg{err: fmt.Errorf("Proxmox client not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		nodes, err := client.ClusterStatus(ctx)
		return clusterStatusMsg{nodes: nodes, err: err}
	}
}

// createSpawnGroupCmd persists a PXE group for the new node and syncs to Matchbox.
func (m PXEModel) createSpawnGroupCmd() tea.Cmd {
	database := m.database
	mbClient := m.matchboxClient
	hostname := strings.TrimSpace(m.spawnFields[spawnFieldHostname].Value())
	mac := strings.TrimSpace(m.spawnFields[spawnFieldMAC].Value())

	if len(m.profiles) == 0 || m.spawnProfileIdx >= len(m.profiles) {
		return func() tea.Msg { return pxeSpawnDoneMsg{err: fmt.Errorf("no PXE profile selected")} }
	}
	profile := m.profiles[m.spawnProfileIdx]

	slug := strings.ToLower(strings.ReplaceAll(hostname, " ", "-"))
	groupID := "spawn-" + slug
	g := db.PXEGroup{
		ID:         groupID,
		Name:       hostname + " (spawned)",
		ProfileID:  profile.ID,
		MACAddress: mac,
	}

	return func() tea.Msg {
		if database != nil {
			if err := database.SavePXEGroup(g); err != nil {
				return pxeSpawnDoneMsg{err: fmt.Errorf("save group: %w", err)}
			}
		}
		if mbClient != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			selector := map[string]string{}
			if mac != "" {
				selector["mac"] = mac
			}
			err := mbClient.CreateGroup(ctx, api.MatchboxGroup{
				ID:       g.ID,
				Name:     g.Name,
				Profile:  g.ProfileID,
				Selector: selector,
			})
			if err != nil {
				return pxeSpawnDoneMsg{groupID: groupID, err: fmt.Errorf("matchbox sync: %w", err)}
			}
		}
		return pxeSpawnDoneMsg{groupID: groupID}
	}
}

// resetSpawnWizard clears all spawn wizard state.
func (m *PXEModel) resetSpawnWizard() {
	m.spawnStep = spawnStepType
	m.spawnNodeType = "standalone"
	m.spawnProfileIdx = 0
	m.spawnFocus = 0
	m.spawnGroupID = ""
	m.spawnErr = ""
	m.spawnChecking = false
	m.spawning = false
	m.spawnClusterNodes = nil
	for i := range m.spawnFields {
		m.spawnFields[i].SetValue("")
		m.spawnFields[i].Blur()
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m PXEModel) Update(msg tea.Msg) (PXEModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case pxeLoadedMsg:
		m.loading = false
		m.profiles = msg.profiles
		m.groups = msg.groups
		m.bootLog = msg.bootLog
		return m, nil

	case matchboxStatusMsg:
		m.matchboxOnline = msg.online
		m.matchboxChecked = true
		return m, nil

	case matchboxSyncedMsg:
		m.loading = false
		if msg.err != nil {
			m.notice = "sync error: " + msg.err.Error()
		} else {
			m.notice = "synced to Matchbox"
		}
		return m, nil

	case pxeProfileSavedMsg:
		m.addingProf = false
		if msg.err != nil {
			m.notice = "error: " + msg.err.Error()
		} else {
			m.notice = "profile saved"
			m.resetProfForm()
		}
		return m, m.loadAll()

	case pxeGroupSavedMsg:
		m.addingGrp = false
		if msg.err != nil {
			m.notice = "error: " + msg.err.Error()
		} else {
			m.notice = "group saved"
			m.resetGrpForm()
		}
		return m, m.loadAll()

	case clusterStatusMsg:
		m.spawnChecking = false
		if msg.err != nil {
			m.spawnErr = "cluster status: " + msg.err.Error()
		} else {
			m.spawnClusterNodes = msg.nodes
		}
		return m, nil

	case pxeSpawnDoneMsg:
		m.spawning = false
		if msg.err != nil {
			m.spawnErr = msg.err.Error()
		} else {
			m.spawnGroupID = msg.groupID
			m.spawnStep = spawnStepLaunched
		}
		return m, m.loadAll()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m PXEModel) handleKey(msg tea.KeyMsg) (PXEModel, tea.Cmd) {
	key := msg.String()

	if m.addingProf {
		return m.handleProfForm(key, msg)
	}
	if m.addingGrp {
		return m.handleGrpForm(key, msg)
	}

	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
		}
		return m, nil
	case "right", "l":
		if m.tab < pxeTabCount-1 {
			m.tab++
			m.notice = ""
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	}

	switch m.tab {
	case pxeTabProfiles:
		return m.handleProfilesTab(key)
	case pxeTabGroups:
		return m.handleGroupsTab(key)
	case pxeTabBootLog:
		return m.handleBootLogTab(key)
	case pxeTabTemplates:
		return m.handleTemplatesTab(key)
	case pxeTabSpawn:
		return m.handleSpawnTab(key, msg)
	}
	return m, nil
}

func (m PXEModel) handleSpawnTab(key string, msg tea.KeyMsg) (PXEModel, tea.Cmd) {
	// If a form step has focus, route keys to the active text input.
	formStep := m.spawnStep == spawnStepHostInfo ||
		m.spawnStep == spawnStepNetwork ||
		(m.spawnStep == spawnStepCluster && m.spawnNodeType == "ha")

	if formStep {
		switch key {
		case "esc":
			m.resetSpawnWizard()
			return m, nil
		case "tab", "down":
			maxField := spawnFieldIPConfig // steps 2-3 use fields 0-2
			if m.spawnStep == spawnStepCluster {
				maxField = spawnFieldClusterIP
				// Only field on this step
				m.spawnFields[m.spawnFocus].Blur()
				m.spawnFocus = spawnFieldClusterIP
				m.spawnFields[m.spawnFocus].Focus()
				return m, nil
			}
			m.spawnFields[m.spawnFocus].Blur()
			if m.spawnStep == spawnStepHostInfo {
				next := m.spawnFocus + 1
				if next > spawnFieldMAC {
					next = spawnFieldHostname
				}
				m.spawnFocus = next
			} else { // network
				m.spawnFocus = spawnFieldIPConfig
			}
			m.spawnFields[m.spawnFocus].Focus()
			_ = maxField
			return m, nil
		case "shift+tab", "up":
			m.spawnFields[m.spawnFocus].Blur()
			if m.spawnStep == spawnStepHostInfo {
				prev := m.spawnFocus - 1
				if prev < spawnFieldHostname {
					prev = spawnFieldMAC
				}
				m.spawnFocus = prev
			}
			m.spawnFields[m.spawnFocus].Focus()
			return m, nil
		case "enter":
			// On last field of step, advance wizard
			if m.spawnStep == spawnStepHostInfo && m.spawnFocus == spawnFieldMAC {
				hostname := strings.TrimSpace(m.spawnFields[spawnFieldHostname].Value())
				if hostname == "" {
					m.notice = "hostname is required"
					return m, nil
				}
				m.spawnFields[m.spawnFocus].Blur()
				m.spawnStep = spawnStepNetwork
				m.spawnFocus = spawnFieldIPConfig
				m.spawnFields[spawnFieldIPConfig].Focus()
				return m, nil
			}
			if m.spawnStep == spawnStepHostInfo {
				// advance field within step
				m.spawnFields[m.spawnFocus].Blur()
				m.spawnFocus = spawnFieldMAC
				m.spawnFields[m.spawnFocus].Focus()
				return m, nil
			}
			if m.spawnStep == spawnStepNetwork {
				ipCfg := strings.TrimSpace(m.spawnFields[spawnFieldIPConfig].Value())
				if ipCfg == "" {
					m.notice = "IP config is required (use 'dhcp' for dynamic)"
					return m, nil
				}
				m.spawnFields[m.spawnFocus].Blur()
				if m.spawnNodeType == "ha" {
					m.spawnStep = spawnStepCluster
					m.spawnFocus = spawnFieldClusterIP
					m.spawnFields[spawnFieldClusterIP].Focus()
					m.spawnChecking = true
					return m, m.checkClusterStatusCmd()
				}
				m.spawnStep = spawnStepConfirm
				return m, nil
			}
			if m.spawnStep == spawnStepCluster {
				m.spawnFields[m.spawnFocus].Blur()
				m.spawnStep = spawnStepConfirm
				return m, nil
			}
		default:
			// Route to focused text input
			var cmd tea.Cmd
			m.spawnFields[m.spawnFocus], cmd = m.spawnFields[m.spawnFocus].Update(msg)
			return m, cmd
		}
		// Fallthrough: pass to focused input
		var cmd tea.Cmd
		m.spawnFields[m.spawnFocus], cmd = m.spawnFields[m.spawnFocus].Update(msg)
		return m, cmd
	}

	// Non-form steps
	switch m.spawnStep {
	case spawnStepType:
		switch key {
		case "left", "h", "right", "l", " ":
			if m.spawnNodeType == "standalone" {
				m.spawnNodeType = "ha"
			} else {
				m.spawnNodeType = "standalone"
			}
		case "enter":
			m.spawnStep = spawnStepProfile
		case "esc":
			// nothing — esc on type step goes back to tab bar
		}
	case spawnStepProfile:
		switch key {
		case "up", "k":
			if m.spawnProfileIdx > 0 {
				m.spawnProfileIdx--
			}
		case "down", "j":
			if m.spawnProfileIdx < len(m.profiles)-1 {
				m.spawnProfileIdx++
			}
		case "enter":
			if len(m.profiles) == 0 {
				m.notice = "add a PXE profile first (Profiles tab)"
				return m, nil
			}
			m.spawnStep = spawnStepHostInfo
			m.spawnFocus = spawnFieldHostname
			m.spawnFields[spawnFieldHostname].Focus()
		case "esc":
			m.spawnStep = spawnStepType
		}
	case spawnStepConfirm:
		switch key {
		case "enter", "y":
			m.spawning = true
			m.spawnErr = ""
			return m, m.createSpawnGroupCmd()
		case "esc":
			// go back one step
			if m.spawnNodeType == "ha" {
				m.spawnStep = spawnStepCluster
				m.spawnFocus = spawnFieldClusterIP
				m.spawnFields[spawnFieldClusterIP].Focus()
			} else {
				m.spawnStep = spawnStepNetwork
				m.spawnFocus = spawnFieldIPConfig
				m.spawnFields[spawnFieldIPConfig].Focus()
			}
		}
	case spawnStepLaunched:
		switch key {
		case "R":
			return m, m.loadAll()
		case "esc":
			m.resetSpawnWizard()
		}
	}
	return m, nil
}

func (m PXEModel) handleProfilesTab(key string) (PXEModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.profCursor > 0 {
			m.profCursor--
		}
	case "down", "j":
		if m.profCursor < len(m.profiles)-1 {
			m.profCursor++
		}
	case "a":
		m.addingProf = true
		m.profFocus = 0
		m.profFields[0].Focus()
		return m, nil
	case "d":
		if m.profCursor < len(m.profiles) {
			id := m.profiles[m.profCursor].ID
			return m, m.deleteProfileCmd(id)
		}
	case "S":
		// Sync all profiles + groups to Matchbox
		m.loading = true
		m.notice = "syncing to Matchbox…"
		return m, m.syncToMatchbox(m.profiles, m.groups)
	case "R":
		return m, m.loadAll()
	}
	return m, nil
}

func (m PXEModel) handleGroupsTab(key string) (PXEModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.grpCursor > 0 {
			m.grpCursor--
		}
	case "down", "j":
		if m.grpCursor < len(m.groups)-1 {
			m.grpCursor++
		}
	case "a":
		m.addingGrp = true
		m.grpFocus = 0
		m.grpFields[0].Focus()
		return m, nil
	case "d":
		if m.grpCursor < len(m.groups) {
			id := m.groups[m.grpCursor].ID
			return m, m.deleteGroupCmd(id)
		}
	case "R":
		return m, m.loadAll()
	}
	return m, nil
}

func (m PXEModel) handleBootLogTab(key string) (PXEModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.bootCursor > 0 {
			m.bootCursor--
		}
	case "down", "j":
		if m.bootCursor < len(m.bootLog)-1 {
			m.bootCursor++
		}
	case "R":
		return m, m.loadAll()
	}
	return m, nil
}

func (m PXEModel) handleTemplatesTab(key string) (PXEModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.tmplCursor > 0 {
			m.tmplCursor--
		}
	case "down", "j":
		if m.tmplCursor < len(api.PXETemplates)-1 {
			m.tmplCursor++
		}
	case "enter", "c":
		// Clone template into profiles
		tmpl := api.PXETemplates[m.tmplCursor]
		m.notice = "cloning " + tmpl.Name + "…"
		return m, m.cloneTemplateCmd(tmpl)
	}
	return m, nil
}

func (m PXEModel) handleProfForm(key string, msg tea.KeyMsg) (PXEModel, tea.Cmd) {
	switch key {
	case "esc":
		m.addingProf = false
		m.resetProfForm()
		return m, nil
	case "tab", "down":
		m.profFields[m.profFocus].Blur()
		m.profFocus = (m.profFocus + 1) % pxeProfFieldCount
		m.profFields[m.profFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.profFields[m.profFocus].Blur()
		m.profFocus = (m.profFocus - 1 + pxeProfFieldCount) % pxeProfFieldCount
		m.profFields[m.profFocus].Focus()
		return m, nil
	case "enter":
		if m.profFocus < pxeProfFieldCount-1 {
			m.profFields[m.profFocus].Blur()
			m.profFocus++
			m.profFields[m.profFocus].Focus()
			return m, nil
		}
		id := strings.TrimSpace(m.profFields[pxeProfFieldID].Value())
		kernel := strings.TrimSpace(m.profFields[pxeProfFieldKernel].Value())
		if id == "" || kernel == "" {
			m.notice = "id and kernel URL are required"
			return m, nil
		}
		return m, m.saveProfileCmd()
	}
	var cmd tea.Cmd
	m.profFields[m.profFocus], cmd = m.profFields[m.profFocus].Update(msg)
	return m, cmd
}

func (m PXEModel) handleGrpForm(key string, msg tea.KeyMsg) (PXEModel, tea.Cmd) {
	switch key {
	case "esc":
		m.addingGrp = false
		m.resetGrpForm()
		return m, nil
	case "tab", "down":
		m.grpFields[m.grpFocus].Blur()
		m.grpFocus = (m.grpFocus + 1) % pxeGrpFieldCount
		m.grpFields[m.grpFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.grpFields[m.grpFocus].Blur()
		m.grpFocus = (m.grpFocus - 1 + pxeGrpFieldCount) % pxeGrpFieldCount
		m.grpFields[m.grpFocus].Focus()
		return m, nil
	case "enter":
		if m.grpFocus < pxeGrpFieldCount-1 {
			m.grpFields[m.grpFocus].Blur()
			m.grpFocus++
			m.grpFields[m.grpFocus].Focus()
			return m, nil
		}
		id := strings.TrimSpace(m.grpFields[pxeGrpFieldID].Value())
		profileID := strings.TrimSpace(m.grpFields[pxeGrpFieldProfile].Value())
		if id == "" || profileID == "" {
			m.notice = "id and profile id are required"
			return m, nil
		}
		return m, m.saveGroupCmd()
	}
	var cmd tea.Cmd
	m.grpFields[m.grpFocus], cmd = m.grpFields[m.grpFocus].Update(msg)
	return m, cmd
}

func (m *PXEModel) resetProfForm() {
	for i := range m.profFields {
		m.profFields[i].SetValue("")
		m.profFields[i].Blur()
	}
	m.profFocus = 0
}

func (m *PXEModel) resetGrpForm() {
	for i := range m.grpFields {
		m.grpFields[i].SetValue("")
		m.grpFields[i].Blur()
	}
	m.grpFocus = 0
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m PXEModel) View() string {
	var b strings.Builder

	// Matchbox status dot
	mbStatus := styleDim.Render("● Matchbox not configured")
	if m.matchboxChecked {
		if m.matchboxClient != nil {
			if m.matchboxOnline {
				mbStatus = lipgloss.NewStyle().Foreground(colorGreen).Render("● Matchbox online")
			} else {
				mbStatus = lipgloss.NewStyle().Foreground(colorRed).Render("● Matchbox offline")
			}
		}
	}
	b.WriteString(mbStatus + "\n")

	// Tab bar
	b.WriteString(m.renderTabBar() + "\n\n")

	contentH := m.height - 7
	if contentH < 4 {
		contentH = 4
	}

	switch m.tab {
	case pxeTabProfiles:
		if m.addingProf {
			b.WriteString(m.viewProfForm())
		} else {
			b.WriteString(m.viewProfiles(contentH))
		}
	case pxeTabGroups:
		if m.addingGrp {
			b.WriteString(m.viewGrpForm())
		} else {
			b.WriteString(m.viewGroups(contentH))
		}
	case pxeTabBootLog:
		b.WriteString(m.viewBootLog(contentH))
	case pxeTabTemplates:
		b.WriteString(m.viewTemplates(contentH))
	case pxeTabSpawn:
		b.WriteString(m.viewSpawn(contentH))
	}

	if m.notice != "" {
		b.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m PXEModel) renderTabBar() string {
	var parts []string
	for i, label := range pxeTabLabels {
		l := " " + label + " "
		if pxeTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch")
}

func (m PXEModel) viewProfiles(height int) string {
	var b strings.Builder
	if len(m.profiles) == 0 {
		b.WriteString(styleDim.Render("  No PXE profiles yet. Press a to add one, or go to Templates to clone a preset.\n"))
		return b.String()
	}
	hdr := fmt.Sprintf("  %-24s %-32s %-10s  %s\n", "ID", "Name", "Template", "Kernel URL")
	b.WriteString(styleDim.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 100))) + "\n")
	for i, p := range m.profiles {
		if i >= height-2 {
			break
		}
		row := fmt.Sprintf("  %-24s %-32s %-10s  %s\n",
			truncate(p.ID, 24), truncate(p.Name, 32),
			truncate(p.Template, 10), truncate(p.KernelURL, 40))
		if i == m.profCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m PXEModel) viewProfForm() string {
	labels := []string{"ID", "Name", "Desc", "Kernel URL", "Initrd URLs", "Kernel Args"}
	var b strings.Builder
	b.WriteString(styleSubtle.Render("  Add PXE profile\n"))
	b.WriteString(styleDim.Render("  ──────────────────────────────────────────\n"))
	for i, f := range m.profFields {
		lbl := fmt.Sprintf("  %-12s ", labels[i])
		if i == m.profFocus {
			b.WriteString(styleSelected.Render(lbl) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(lbl) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("\n  enter advance · esc cancel\n"))
	return b.String()
}

func (m PXEModel) viewGroups(height int) string {
	var b strings.Builder
	if len(m.groups) == 0 {
		b.WriteString(styleDim.Render("  No boot groups yet. Press a to add one.\n"))
		return b.String()
	}
	hdr := fmt.Sprintf("  %-20s %-30s %-24s  %s\n", "ID", "Name", "Profile", "MAC")
	b.WriteString(styleDim.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 100))) + "\n")
	for i, g := range m.groups {
		if i >= height-2 {
			break
		}
		mac := g.MACAddress
		if mac == "" {
			mac = styleDim.Render("(any)")
		}
		row := fmt.Sprintf("  %-20s %-30s %-24s  %s\n",
			truncate(g.ID, 20), truncate(g.Name, 30), truncate(g.ProfileID, 24), mac)
		if i == m.grpCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m PXEModel) viewGrpForm() string {
	labels := []string{"ID", "Name", "Profile ID", "MAC Address"}
	var b strings.Builder
	b.WriteString(styleSubtle.Render("  Add boot group\n"))
	b.WriteString(styleDim.Render("  ──────────────────────────────────────────\n"))
	for i, f := range m.grpFields {
		lbl := fmt.Sprintf("  %-12s ", labels[i])
		if i == m.grpFocus {
			b.WriteString(styleSelected.Render(lbl) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(lbl) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("\n  enter advance · esc cancel\n"))
	return b.String()
}

func (m PXEModel) viewBootLog(height int) string {
	var b strings.Builder
	if len(m.bootLog) == 0 {
		b.WriteString(styleDim.Render("  No boot events recorded yet.\n"))
		return b.String()
	}
	hdr := fmt.Sprintf("  %-18s %-20s %-16s %-24s  %s\n", "When", "MAC", "IP", "Profile", "Hostname")
	b.WriteString(styleDim.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 100))) + "\n")
	for i, ev := range m.bootLog {
		if i >= height-2 {
			break
		}
		when := fmtAge(ev.BootedAt)
		row := fmt.Sprintf("  %-18s %-20s %-16s %-24s  %s\n",
			when, truncate(ev.MACAddress, 20), truncate(ev.IPAddress, 16),
			truncate(ev.ProfileID, 24), truncate(ev.Hostname, 20))
		if i == m.bootCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m PXEModel) viewTemplates(height int) string {
	var b strings.Builder
	b.WriteString(styleDim.Render("  Built-in boot image templates — press enter/c to clone into Profiles\n\n"))
	hdr := fmt.Sprintf("  %-22s %-40s  %s\n", "Template", "Name", "Description")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 100))) + "\n")
	for i, t := range api.PXETemplates {
		if i >= height-3 {
			break
		}
		row := fmt.Sprintf("  %-22s %-40s  %s\n",
			truncate(t.Key, 22), truncate(t.Name, 40), truncate(t.Description, 40))
		if i == m.tmplCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}
	return b.String()
}

func (m PXEModel) viewSpawn(height int) string {
	var b strings.Builder

	totalSteps := spawnStepLaunched
	if m.spawnNodeType != "ha" {
		totalSteps-- // no cluster step for standalone
	}

	// Header
	stepLabel := ""
	switch m.spawnStep {
	case spawnStepType:
		stepLabel = "Step 1: Node Type"
	case spawnStepProfile:
		stepLabel = "Step 2: PXE Profile"
	case spawnStepHostInfo:
		stepLabel = "Step 3: Host Info"
	case spawnStepNetwork:
		stepLabel = "Step 4: Network"
	case spawnStepCluster:
		stepLabel = "Step 5: Cluster"
	case spawnStepConfirm:
		stepLabel = "Review & Confirm"
	case spawnStepLaunched:
		stepLabel = "Launched"
	}
	_ = totalSteps
	b.WriteString(styleSubtle.Render(fmt.Sprintf("  Spawn Node Wizard — %s\n", stepLabel)))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 80)) + "\n\n"))

	if m.spawnErr != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorRed).Render("  ✗ "+m.spawnErr) + "\n\n")
	}

	switch m.spawnStep {
	case spawnStepType:
		b.WriteString(styleDim.Render("  Choose what kind of node to bring up:\n\n"))
		standalone := "  [ standalone ]  — independent bare-metal node, separate from Proxmox cluster\n"
		ha := "  [ HA cluster ]  — Proxmox VE node that will join the existing cluster\n"
		if m.spawnNodeType == "standalone" {
			b.WriteString(styleSelected.Render(strings.TrimRight(standalone, "\n")) + "\n")
			b.WriteString(styleDim.Render(ha))
		} else {
			b.WriteString(styleDim.Render(standalone))
			b.WriteString(styleSelected.Render(strings.TrimRight(ha, "\n")) + "\n")
		}
		b.WriteString(styleDim.Render("\n  ← / → / space to toggle · enter to continue\n"))

	case spawnStepProfile:
		if len(m.profiles) == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorYellow).Render(
				"  No PXE profiles found.\n  Go to the Profiles or Templates tab to create one first.\n"))
		} else {
			b.WriteString(styleDim.Render("  Select the boot profile for the new node:\n\n"))
			for i, p := range m.profiles {
				if i >= height-6 {
					break
				}
				row := fmt.Sprintf("  %-22s  %s", truncate(p.ID, 22), truncate(p.Name, 40))
				if i == m.spawnProfileIdx {
					b.WriteString(styleSelected.Render(row) + "\n")
				} else {
					b.WriteString(styleDim.Render(row) + "\n")
				}
			}
			b.WriteString(styleDim.Render("\n  ↑/↓ select · enter to continue · esc back\n"))
		}

	case spawnStepHostInfo:
		b.WriteString(styleDim.Render("  Enter host identification:\n\n"))
		labels := []string{"Hostname", "MAC Address"}
		fields := []int{spawnFieldHostname, spawnFieldMAC}
		for fi, idx := range fields {
			lbl := fmt.Sprintf("  %-14s ", labels[fi])
			if m.spawnFocus == idx {
				b.WriteString(styleSelected.Render(lbl) + m.spawnFields[idx].View() + "\n")
			} else {
				b.WriteString(styleDim.Render(lbl) + m.spawnFields[idx].View() + "\n")
			}
		}
		b.WriteString(styleDim.Render("\n  tab/enter advance · esc cancel\n"))

	case spawnStepNetwork:
		b.WriteString(styleDim.Render("  Network configuration:\n\n"))
		lbl := "  IP / CIDR        "
		if m.spawnFocus == spawnFieldIPConfig {
			b.WriteString(styleSelected.Render(lbl) + m.spawnFields[spawnFieldIPConfig].View() + "\n")
		} else {
			b.WriteString(styleDim.Render(lbl) + m.spawnFields[spawnFieldIPConfig].View() + "\n")
		}
		b.WriteString(styleDim.Render("\n  Examples: dhcp  ·  192.168.1.50/24\n"))
		b.WriteString(styleDim.Render("  enter to continue · esc cancel\n"))

	case spawnStepCluster:
		b.WriteString(styleDim.Render("  HA cluster join configuration:\n\n"))
		if m.spawnChecking {
			b.WriteString("  " + m.spinner.View() + " fetching cluster status…\n\n")
		} else if len(m.spawnClusterNodes) > 0 {
			b.WriteString(styleDim.Render("  Current cluster members:\n"))
			for _, n := range m.spawnClusterNodes {
				dot := lipgloss.NewStyle().Foreground(colorGreen).Render("●")
				if n.Online != 1 {
					dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
				}
				b.WriteString(fmt.Sprintf("    %s  %-16s  %s\n", dot, n.Name, n.IP))
			}
			b.WriteString("\n")
		}
		lbl := "  Existing node IP "
		if m.spawnFocus == spawnFieldClusterIP {
			b.WriteString(styleSelected.Render(lbl) + m.spawnFields[spawnFieldClusterIP].View() + "\n")
		} else {
			b.WriteString(styleDim.Render(lbl) + m.spawnFields[spawnFieldClusterIP].View() + "\n")
		}
		b.WriteString(styleDim.Render("\n  IP of any online cluster member (used in join instructions after boot)\n"))
		b.WriteString(styleDim.Render("  enter to continue · esc back\n"))

	case spawnStepConfirm:
		b.WriteString(styleDim.Render("  Please review your configuration:\n\n"))
		profName := "(none)"
		if len(m.profiles) > 0 && m.spawnProfileIdx < len(m.profiles) {
			profName = m.profiles[m.spawnProfileIdx].ID
		}
		rows := []struct{ label, value string }{
			{"Node type", m.spawnNodeType},
			{"PXE profile", profName},
			{"Hostname", strings.TrimSpace(m.spawnFields[spawnFieldHostname].Value())},
			{"MAC address", strings.TrimSpace(m.spawnFields[spawnFieldMAC].Value())},
			{"IP config", strings.TrimSpace(m.spawnFields[spawnFieldIPConfig].Value())},
		}
		if m.spawnNodeType == "ha" {
			rows = append(rows, struct{ label, value string }{
				"Join cluster IP", strings.TrimSpace(m.spawnFields[spawnFieldClusterIP].Value()),
			})
		}
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  %-16s  %s\n", styleDim.Render(r.label+":"), r.value))
		}
		if m.spawning {
			b.WriteString("\n  " + m.spinner.View() + " creating PXE group and syncing to Matchbox…\n")
		} else {
			b.WriteString(styleDim.Render("\n  enter / y to spawn · esc go back\n"))
		}

	case spawnStepLaunched:
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(
			fmt.Sprintf("  ✓ PXE group %q created and synced to Matchbox.\n\n", m.spawnGroupID)))
		b.WriteString(styleDim.Render("  Power on the bare-metal machine and boot from network.\n"))
		b.WriteString(styleDim.Render("  The Boot Log tab will show an entry when the node first PXE boots.\n\n"))

		if m.spawnNodeType == "ha" {
			clusterIP := strings.TrimSpace(m.spawnFields[spawnFieldClusterIP].Value())
			b.WriteString(styleSubtle.Render("  Post-boot steps to join the Proxmox cluster:\n"))
			b.WriteString(styleDim.Render("    1. Wait for node to boot and install Proxmox VE via PXE\n"))
			b.WriteString(styleDim.Render("    2. SSH into the new node and run:\n"))
			if clusterIP != "" {
				b.WriteString(lipgloss.NewStyle().Foreground(colorPurple).Render(
					fmt.Sprintf("         pvecm add %s\n", clusterIP)))
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(colorPurple).Render(
					"         pvecm add <existing-cluster-node-ip>\n"))
			}
			b.WriteString(styleDim.Render("    3. Verify in Proxmox UI: Datacenter → Cluster\n\n"))
		}

		hostname := strings.TrimSpace(m.spawnFields[spawnFieldHostname].Value())
		ipCfg := strings.TrimSpace(m.spawnFields[spawnFieldIPConfig].Value())
		if hostname != "" {
			b.WriteString(styleDim.Render(fmt.Sprintf("  Hostname: %s   IP: %s\n\n", hostname, ipCfg)))
		}

		// Show last few boot log entries filtered by MAC if available
		mac := strings.TrimSpace(m.spawnFields[spawnFieldMAC].Value())
		var relevant []db.PXEBootEvent
		for _, ev := range m.bootLog {
			if mac == "" || strings.EqualFold(ev.MACAddress, mac) {
				relevant = append(relevant, ev)
			}
		}
		if len(relevant) > 0 {
			b.WriteString(styleSubtle.Render("  Recent boot events for this node:\n"))
			for i, ev := range relevant {
				if i >= 5 {
					break
				}
				b.WriteString(styleDim.Render(fmt.Sprintf("    %s  %s  %s\n",
					fmtAge(ev.BootedAt), ev.IPAddress, ev.ProfileID)))
			}
		} else if mac != "" {
			b.WriteString(styleDim.Render("  No boot events for this MAC yet — waiting for node to PXE boot…\n"))
		}
		b.WriteString(styleDim.Render("\n  R reload boot log · esc reset wizard\n"))
	}

	return b.String()
}

func (m PXEModel) helpStr() string {
	switch m.tab {
	case pxeTabProfiles:
		if m.addingProf {
			return "  tab/enter advance · esc cancel"
		}
		return "  ↑/↓ navigate · a add · d delete · S sync to Matchbox · R reload · ←/→ tab · b back"
	case pxeTabGroups:
		if m.addingGrp {
			return "  tab/enter advance · esc cancel"
		}
		return "  ↑/↓ navigate · a add · d delete · R reload · ←/→ tab · b back"
	case pxeTabBootLog:
		return "  ↑/↓ navigate · R reload · ←/→ tab · b back"
	case pxeTabTemplates:
		return "  ↑/↓ navigate · enter/c clone to profiles · ←/→ tab · b back"
	case pxeTabSpawn:
		switch m.spawnStep {
		case spawnStepType:
			return "  ←/→/space toggle type · enter next · ←/→ (outer) switch tab · b back"
		case spawnStepProfile:
			return "  ↑/↓ select profile · enter next · esc back"
		case spawnStepHostInfo, spawnStepNetwork, spawnStepCluster:
			return "  tab/enter advance field · esc cancel wizard"
		case spawnStepConfirm:
			return "  enter/y spawn · esc go back"
		case spawnStepLaunched:
			return "  R reload boot log · esc reset wizard · b back"
		}
		return "  ←/→ tab · b back"
	}
	return "  ←/→ tab · b back"
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}
