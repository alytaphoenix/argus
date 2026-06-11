package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/ratataskr/api"
	"github.com/alytaphoenix/ratataskr/config"
	"github.com/alytaphoenix/ratataskr/db"
)

// ── Tabs ──────────────────────────────────────────────────────────────────────

type plexTab int

const (
	plexTabStatus    plexTab = iota // Plex server + sessions
	plexTabLibraries                // Library sections
	plexTabServices                 // Servarr / companion services checklist
	plexTabProvision                // Provision Plex VM if needed
	plexTabCount
)

var plexTabLabels = []string{"Status", "Libraries", "Services", "Provision"}

// ── Model ─────────────────────────────────────────────────────────────────────

// PlexModel manages the Plex Media Server and companion services panel.
type PlexModel struct {
	width, height int
	tab           plexTab

	plexClient  *api.PlexClient
	proxmox     *api.Client
	trueNAS     *api.TrueNASClient
	database    *db.DB
	cfg         config.Config

	// Status tab
	plexStatus  *api.PlexStatus
	plexLoading bool
	plexChecked bool
	plexOnline  bool

	// Libraries tab
	libCursor   int
	mediaRoots  []string // media directories discovered from TrueNAS

	// Services tab
	services    []plexServiceState
	svcCursor   int

	// Provision tab
	provNotice  string

	spinner spinner.Model
	notice  string
}

// plexServiceState tracks one companion service.
type plexServiceState struct {
	Def      api.MediaServiceDef
	Online   bool
	Checked  bool
	Loading  bool
	Status   *api.ServarrStatus
	Enabled  bool   // configured in ratataskr config
	VMID     int    // Proxmox VMID if known
	VMNode   string
}

func NewPlexModel(plexClient *api.PlexClient, proxmox *api.Client,
	trueNAS *api.TrueNASClient, database *db.DB, cfg config.Config) PlexModel {

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// Build service list from config
	services := buildPlexServices(cfg)

	return PlexModel{
		plexClient: plexClient,
		proxmox:    proxmox,
		trueNAS:    trueNAS,
		database:   database,
		cfg:        cfg,
		spinner:    sp,
		services:   services,
	}
}

// buildPlexServices creates the companion service list based on what's configured.
func buildPlexServices(cfg config.Config) []plexServiceState {
	// Map config to service states
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

	var states []plexServiceState
	for _, def := range api.MediaServices {
		appCfg := cfgMap[def.ID]
		states = append(states, plexServiceState{
			Def:     def,
			Enabled: appCfg.URL != "",
			VMID:    appCfg.VMID,
			VMNode:  appCfg.VMNode,
		})
	}
	return states
}

func (m PlexModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, m.loadPlexCmd()}
	if m.trueNAS != nil {
		cmds = append(cmds, m.loadMediaRootsCmd())
	}
	// Check configured servarr services
	cmds = append(cmds, m.checkAllServicesCmd())
	return tea.Batch(cmds...)
}

// ── Commands ──────────────────────────────────────────────────────────────────

func (m PlexModel) loadPlexCmd() tea.Cmd {
	client := m.plexClient
	return func() tea.Msg {
		if client == nil {
			return plexStatusMsg{err: fmt.Errorf("not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, err := client.FetchAll(ctx)
		return plexStatusMsg{status: status, err: err}
	}
}

func (m PlexModel) loadMediaRootsCmd() tea.Cmd {
	client := m.trueNAS
	return func() tea.Msg {
		if client == nil {
			return plexMediaRootsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, err := client.FetchAll(ctx)
		if err != nil {
			return plexMediaRootsMsg{}
		}
		// Look for datasets that look like media directories.
		// TrueNAS datasets are named like "tank/media"; they mount at /mnt/<name>.
		var roots []string
		for _, ds := range status.Datasets {
			lower := strings.ToLower(ds.Name)
			if strings.Contains(lower, "media") || strings.Contains(lower, "movies") ||
				strings.Contains(lower, "tv") || strings.Contains(lower, "music") ||
				strings.Contains(lower, "photos") || strings.Contains(lower, "plex") {
				roots = append(roots, "/mnt/"+ds.Name)
			}
		}
		return plexMediaRootsMsg{roots: roots}
	}
}

func (m PlexModel) checkAllServicesCmd() tea.Cmd {
	cfgMap := map[string]config.ServarrAppConfig{
		"sonarr":    m.cfg.Servarr.Sonarr,
		"radarr":    m.cfg.Servarr.Radarr,
		"lidarr":    m.cfg.Servarr.Lidarr,
		"readarr":   m.cfg.Servarr.Readarr,
		"prowlarr":  m.cfg.Servarr.Prowlarr,
		"bazarr":    m.cfg.Servarr.Bazarr,
		"overseerr": m.cfg.Servarr.Overseerr,
		"tautulli":  m.cfg.Servarr.Tautulli,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var results []api.ServarrStatus
		for id, appCfg := range cfgMap {
			if appCfg.URL == "" {
				continue
			}
			client := api.NewServarrClient(appCfg.URL, appCfg.APIKey, id, appCfg.Insecure)
			s := client.FetchStatus(ctx)
			s.AppName = id
			results = append(results, s)
		}
		return plexServicesCheckedMsg{results: results}
	}
}

func (m PlexModel) autoConfigureLibrariesCmd() tea.Cmd {
	plexClient := m.plexClient
	mediaRoots := m.mediaRoots
	return func() tea.Msg {
		if plexClient == nil {
			return plexAutoConfigMsg{err: fmt.Errorf("Plex not configured")}
		}
		if len(mediaRoots) == 0 {
			return plexAutoConfigMsg{err: fmt.Errorf("no media directories discovered from TrueNAS")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var added []string
		for _, path := range mediaRoots {
			lower := strings.ToLower(path)
			libType := "movie"
			libName := "Movies"
			switch {
			case strings.Contains(lower, "tv") || strings.Contains(lower, "show"):
				libType = "show"
				libName = "TV Shows"
			case strings.Contains(lower, "music"):
				libType = "artist"
				libName = "Music"
			case strings.Contains(lower, "photo"):
				libType = "photo"
				libName = "Photos"
			}
			if err := plexClient.AddLibrary(ctx, libName, libType, path); err == nil {
				added = append(added, fmt.Sprintf("%s → %s", path, libName))
			}
		}
		return plexAutoConfigMsg{added: added}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m PlexModel) Update(msg tea.Msg) (PlexModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case plexStatusMsg:
		m.plexLoading = false
		m.plexChecked = true
		if msg.err != nil {
			m.plexOnline = false
			if msg.err.Error() != "not configured" {
				m.notice = "Plex: " + msg.err.Error()
			}
		} else {
			m.plexOnline = true
			m.plexStatus = msg.status
		}
		return m, nil

	case plexMediaRootsMsg:
		m.mediaRoots = msg.roots
		return m, nil

	case plexServicesCheckedMsg:
		// Update service states with results
		for _, result := range msg.results {
			for i := range m.services {
				if m.services[i].Def.ID == result.AppName {
					m.services[i].Online = result.Online
					m.services[i].Checked = true
					r := result
					m.services[i].Status = &r
					break
				}
			}
		}
		return m, nil

	case plexAutoConfigMsg:
		if msg.err != nil {
			m.notice = "auto-config error: " + msg.err.Error()
		} else if len(msg.added) > 0 {
			m.notice = fmt.Sprintf("✓ added %d library/libraries to Plex", len(msg.added))
		} else {
			m.notice = "no new libraries to add"
		}
		return m, m.loadPlexCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m PlexModel) handleKey(msg tea.KeyMsg) (PlexModel, tea.Cmd) {
	key := msg.String()

	switch key {
	case "left", "h":
		if m.tab > 0 {
			m.tab--
			m.notice = ""
		}
		return m, nil
	case "right", "l":
		if m.tab < plexTabCount-1 {
			m.tab++
			m.notice = ""
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return backMsg{} }
	case "R":
		m.plexLoading = true
		m.notice = ""
		return m, tea.Batch(m.loadPlexCmd(), m.checkAllServicesCmd())
	}

	switch m.tab {
	case plexTabStatus:
		switch key {
		case "r":
			m.plexLoading = true
			return m, m.loadPlexCmd()
		}
	case plexTabLibraries:
		switch key {
		case "up", "k":
			if m.libCursor > 0 {
				m.libCursor--
			}
		case "down", "j":
			if m.plexStatus != nil && m.libCursor < len(m.plexStatus.Libraries)-1 {
				m.libCursor++
			}
		case "A":
			// Auto-configure libraries from TrueNAS media directories
			if m.trueNAS != nil {
				m.notice = "auto-configuring libraries from TrueNAS…"
				return m, m.autoConfigureLibrariesCmd()
			} else {
				m.notice = "TrueNAS not configured — cannot auto-detect media directories"
			}
		}
	case plexTabServices:
		switch key {
		case "up", "k":
			if m.svcCursor > 0 {
				m.svcCursor--
			}
		case "down", "j":
			if m.svcCursor < len(m.services)-1 {
				m.svcCursor++
			}
		case "r":
			return m, m.checkAllServicesCmd()
		case "p":
			// Open provision dialog for selected service
			if m.svcCursor < len(m.services) {
				svc := m.services[m.svcCursor]
				if !svc.Enabled {
					m.notice = fmt.Sprintf("Add %s config to [servarr.%s] in config.toml to enable, then provision a VM", svc.Def.Name, svc.Def.ID)
				} else {
					m.notice = fmt.Sprintf("%s is configured at port %d — use PXE or provision screens to deploy a VM", svc.Def.Name, svc.Def.DefaultPort)
				}
			}
		}
	case plexTabProvision:
		switch key {
		case "p":
			// Navigate to PXE spawn for Plex VM
			return m, func() tea.Msg { return openPXEMsg{} }
		}
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m PlexModel) View() string {
	var b strings.Builder

	// Header with Plex online status
	plexDot := styleDim.Render("● Plex not configured")
	if m.plexChecked {
		if m.plexClient == nil {
			plexDot = styleDim.Render("● Plex not configured")
		} else if m.plexOnline {
			plexDot = lipgloss.NewStyle().Foreground(colorGreen).Render("● Plex online")
		} else {
			plexDot = lipgloss.NewStyle().Foreground(colorRed).Render("● Plex offline")
		}
	}
	b.WriteString(plexDot + "\n")
	b.WriteString(m.renderTabBar() + "\n\n")

	contentH := m.height - 7
	if contentH < 4 {
		contentH = 4
	}

	switch m.tab {
	case plexTabStatus:
		b.WriteString(m.viewStatus(contentH))
	case plexTabLibraries:
		b.WriteString(m.viewLibraries(contentH))
	case plexTabServices:
		b.WriteString(m.viewServices(contentH))
	case plexTabProvision:
		b.WriteString(m.viewProvision(contentH))
	}

	if m.notice != "" {
		b.WriteString("\n" + styleDim.Render("  "+m.notice))
	}
	b.WriteString("\n" + styleDim.Render(m.helpStr()))
	return b.String()
}

func (m PlexModel) renderTabBar() string {
	var parts []string
	for i, label := range plexTabLabels {
		l := " " + label + " "
		if plexTab(i) == m.tab {
			parts = append(parts, styleTabActive.Render("▸"+l))
		} else {
			parts = append(parts, styleTabInactive.Render(" "+l))
		}
	}
	return strings.Join(parts, styleDim.Render("│")) + styleDim.Render("  ←/→ switch")
}

func (m PlexModel) viewStatus(height int) string {
	var b strings.Builder

	if m.plexClient == nil {
		b.WriteString(styleDim.Render("  Plex not configured.\n"))
		b.WriteString(styleDim.Render("  Set [plex] url and token in config.toml.\n\n"))
		b.WriteString(styleDim.Render("  To get your Plex token:\n"))
		b.WriteString(styleDim.Render("    1. Open Plex Web → click account icon → 'XML' export\n"))
		b.WriteString(styleDim.Render("    2. Or visit: https://support.plex.tv/articles/204059436\n\n"))
		b.WriteString(styleDim.Render("  Use the Provision tab to deploy Plex in a new VM.\n"))
		return b.String()
	}

	if m.plexLoading || !m.plexChecked {
		b.WriteString("  " + m.spinner.View() + " connecting to Plex…\n")
		return b.String()
	}

	if !m.plexOnline {
		b.WriteString(lipgloss.NewStyle().Foreground(colorRed).Render("  ✗ Plex is offline or unreachable\n"))
		b.WriteString(styleDim.Render("  Check URL and token in config.toml · r to retry\n"))
		return b.String()
	}

	if m.plexStatus == nil {
		return b.String()
	}

	info := m.plexStatus.Info
	if info != nil {
		b.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("Server:"), info.FriendlyName))
		b.WriteString(fmt.Sprintf("  %-16s %s\n", styleDim.Render("Version:"), info.Version))
		b.WriteString(fmt.Sprintf("  %-16s %s %s\n", styleDim.Render("Platform:"), info.Platform, info.PlatformVersion))
		b.WriteString("\n")
	}

	// Active sessions
	sessions := m.plexStatus.Sessions
	if len(sessions) == 0 {
		b.WriteString(styleDim.Render("  No active streams.\n"))
	} else {
		b.WriteString(styleSubtle.Render(fmt.Sprintf("  %-3s  %-30s %-16s %-10s  %s\n",
			"", "Title", "User", "Device", "State")))
		b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 85))) + "\n")
		for i, s := range sessions {
			if i >= height-6 {
				break
			}
			title := s.Title
			if s.GrandparentTitle != "" {
				title = s.GrandparentTitle + " — " + s.Title
			}
			progress := ""
			if s.Duration > 0 {
				pct := 100 * s.ViewOffset / s.Duration
				progress = fmt.Sprintf(" %d%%", pct)
			}
			row := fmt.Sprintf("  ▶   %-30s %-16s %-10s  %s%s\n",
				truncate(title, 30), truncate(s.User.Title, 16),
				truncate(s.Player.Product, 10), s.Player.State, progress)
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(strings.TrimRight(row, "\n")) + "\n")
		}
	}

	// Library summary
	if len(m.plexStatus.Libraries) > 0 {
		b.WriteString("\n")
		b.WriteString(styleDim.Render(fmt.Sprintf("  Libraries: %d sections — see Libraries tab\n", len(m.plexStatus.Libraries))))
	}
	return b.String()
}

func (m PlexModel) viewLibraries(height int) string {
	var b strings.Builder

	if m.plexStatus == nil || !m.plexOnline {
		b.WriteString(styleDim.Render("  Connect Plex to view library sections.\n"))
		return b.String()
	}

	libs := m.plexStatus.Libraries
	if len(libs) == 0 {
		b.WriteString(styleDim.Render("  No library sections found.\n"))
		if len(m.mediaRoots) > 0 {
			b.WriteString(styleDim.Render("  Detected TrueNAS media directories:\n"))
			for _, root := range m.mediaRoots {
				b.WriteString(styleDim.Render("    " + root + "\n"))
			}
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(
				"\n  Press A to auto-add these as Plex libraries.\n"))
		}
		return b.String()
	}

	hdr := fmt.Sprintf("  %-30s %-10s %-8s  %s\n", "Library", "Type", "Count", "Location")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 80))) + "\n")

	for i, lib := range libs {
		if i >= height-2 {
			break
		}
		loc := ""
		if len(lib.Locations) > 0 {
			loc = lib.Locations[0].Path
		}
		row := fmt.Sprintf("  %-30s %-10s %-8d  %s\n",
			truncate(lib.Title, 30), lib.Type, lib.Count, truncate(loc, 35))
		if i == m.libCursor {
			b.WriteString(styleSelected.Render(strings.TrimRight(row, "\n")) + "\n")
		} else {
			b.WriteString(row)
		}
	}

	if len(m.mediaRoots) > 0 {
		b.WriteString(styleDim.Render("\n  TrueNAS media directories detected: " + strings.Join(m.mediaRoots, ", ") + "\n"))
		b.WriteString(styleDim.Render("  Press A to auto-add any missing libraries.\n"))
	}
	return b.String()
}

func (m PlexModel) viewServices(height int) string {
	var b strings.Builder

	b.WriteString(styleDim.Render("  Third-party Plex companion services — configure in [servarr.*] sections\n\n"))

	hdr := fmt.Sprintf("  %-3s  %-12s %-32s %-8s  %s\n", "●", "Name", "Description", "Port", "Status")
	b.WriteString(styleSubtle.Render(hdr))
	b.WriteString(styleDim.Render("  " + strings.Repeat("─", min(m.width-4, 95))) + "\n")

	for i, svc := range m.services {
		if i >= height-4 {
			b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more\n", len(m.services)-i)))
			break
		}

		dot := styleDim.Render("○")
		statusStr := styleDim.Render("not configured")

		if svc.Enabled {
			if !svc.Checked {
				dot = styleDim.Render(m.spinner.View()[:2])
				statusStr = styleDim.Render("checking…")
			} else if svc.Online {
				dot = lipgloss.NewStyle().Foreground(colorGreen).Render("●")
				statusStr = lipgloss.NewStyle().Foreground(colorGreen).Render("online")
				if svc.Status != nil && svc.Status.Status != nil {
					statusStr += styleDim.Render(" v" + svc.Status.Status.Version)
				}
				if svc.Status != nil && len(svc.Status.Health) > 0 {
					statusStr += lipgloss.NewStyle().Foreground(colorYellow).Render(
						fmt.Sprintf(" ⚠%d issues", len(svc.Status.Health)))
				}
				if svc.Status != nil && len(svc.Status.Queue) > 0 {
					statusStr += styleDim.Render(fmt.Sprintf(" %d queued", len(svc.Status.Queue)))
				}
			} else {
				dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
				statusStr = lipgloss.NewStyle().Foreground(colorRed).Render("offline")
			}
		}

		portStr := "—"
		if svc.Def.DefaultPort > 0 {
			portStr = fmt.Sprintf("%d", svc.Def.DefaultPort)
		}

		catStyle := styleDim
		if svc.Def.Category == "arr" {
			catStyle = lipgloss.NewStyle().Foreground(colorPurple)
		}

		row := fmt.Sprintf("  %-3s  %-12s %-32s %-8s  %s",
			dot,
			catStyle.Render(truncate(svc.Def.Name, 12)),
			styleDim.Render(truncate(svc.Def.Description, 32)),
			portStr,
			statusStr)

		if i == m.svcCursor {
			b.WriteString(styleSelected.Render(row) + "\n")
		} else {
			b.WriteString(row + "\n")
		}
	}

	return b.String()
}

func (m PlexModel) viewProvision(height int) string {
	var b strings.Builder
	_ = height

	b.WriteString(styleSubtle.Render("  Deploy Plex Media Server in a Proxmox VM\n\n"))

	if m.plexClient != nil && m.plexOnline {
		b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(
			"  ✓ Plex is already running and online.\n"))
		b.WriteString(styleDim.Render("  If you need to provision a Plex VM anyway, press p to go to PXE spawn.\n\n"))
	} else {
		b.WriteString(styleDim.Render("  Plex Media Server is not currently configured or reachable.\n\n"))
		b.WriteString(styleSubtle.Render("  Recommended setup:\n"))
		b.WriteString(styleDim.Render("    1. Go to PXE → Spawn Node (press p below)\n"))
		b.WriteString(styleDim.Render("    2. Choose Ubuntu 24.04 LTS profile\n"))
		b.WriteString(styleDim.Render("    3. After boot, install Plex:\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorPurple).Render(
			"         curl https://downloads.plex.tv/plex-keys/PlexSign.key | sudo apt-key add -\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorPurple).Render(
			"         echo 'deb https://downloads.plex.tv/repo/deb public main' | sudo tee /etc/apt/sources.list.d/plexmediaserver.list\n"))
		b.WriteString(lipgloss.NewStyle().Foreground(colorPurple).Render(
			"         sudo apt update && sudo apt install -y plexmediaserver\n"))
		b.WriteString(styleDim.Render("    4. Open http://<plex-ip>:32400/web and claim the server\n"))
		b.WriteString(styleDim.Render("    5. Add your Plex token and URL to config.toml under [plex]\n\n"))
	}

	b.WriteString(styleSubtle.Render("  Companion services to install alongside Plex:\n"))
	for _, svc := range m.services {
		if !svc.Enabled {
			b.WriteString(styleDim.Render(fmt.Sprintf("    ○ %-12s — %s\n", svc.Def.Name, svc.Def.Description)))
		}
	}

	b.WriteString(styleDim.Render("\n  p = go to PXE Spawn wizard · b back\n"))
	return b.String()
}

func (m PlexModel) helpStr() string {
	switch m.tab {
	case plexTabStatus:
		return "  r refresh · R reload all · ←/→ tab · b back"
	case plexTabLibraries:
		return "  ↑/↓ navigate · A auto-add from TrueNAS · ←/→ tab · b back"
	case plexTabServices:
		return "  ↑/↓ navigate · r re-check · p provision info · ←/→ tab · b back"
	case plexTabProvision:
		return "  p go to PXE spawn · ←/→ tab · b back"
	}
	return "  ←/→ tab · b back"
}
