package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Plex Media Server client ──────────────────────────────────────────────────

// PlexClient talks to a Plex Media Server via the HTTP API.
type PlexClient struct {
	base  string // e.g. http://192.168.1.50:32400
	token string // Plex authentication token (X-Plex-Token)
	http  *http.Client
}

func NewPlexClient(base, token string, insecure bool) *PlexClient {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
	return &PlexClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
}

func (c *PlexClient) get(ctx context.Context, path string, out interface{}) error {
	url := c.base + path
	if strings.Contains(path, "?") {
		url += "&X-Plex-Token=" + c.token
	} else {
		url += "?X-Plex-Token=" + c.token
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PlexServerInfo holds basic Plex Media Server metadata.
type PlexServerInfo struct {
	FriendlyName    string `json:"friendlyName"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platformVersion"`
	MachineID       string `json:"machineIdentifier"`
}

// PlexLibrary is a Plex library section.
type PlexLibrary struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Type      string `json:"type"`  // movie, show, music, photo
	Agent     string `json:"agent"`
	Scanner   string `json:"scanner"`
	Language  string `json:"language"`
	Locations []struct {
		Path string `json:"path"`
	} `json:"Location"`
	Count int `json:"count"`
}

// PlexSession is an active playback session.
type PlexSession struct {
	SessionKey string `json:"sessionKey"`
	Title      string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle"` // show name for episodes
	Type       string `json:"type"`
	User       struct {
		Title string `json:"title"`
	} `json:"User"`
	Player struct {
		Device  string `json:"device"`
		State   string `json:"state"`
		Product string `json:"product"`
	} `json:"Player"`
	ViewOffset     int `json:"viewOffset"`
	Duration       int `json:"duration"`
}

// ServerInfo returns Plex Media Server metadata.
func (c *PlexClient) ServerInfo(ctx context.Context) (*PlexServerInfo, error) {
	var resp struct {
		MediaContainer PlexServerInfo `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/", &resp); err != nil {
		return nil, err
	}
	return &resp.MediaContainer, nil
}

// Libraries returns all library sections.
func (c *PlexClient) Libraries(ctx context.Context) ([]PlexLibrary, error) {
	var resp struct {
		MediaContainer struct {
			Directory []PlexLibrary `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/library/sections", &resp); err != nil {
		return nil, err
	}
	return resp.MediaContainer.Directory, nil
}

// Sessions returns active streaming sessions.
func (c *PlexClient) Sessions(ctx context.Context) ([]PlexSession, error) {
	var resp struct {
		MediaContainer struct {
			Metadata []PlexSession `json:"Metadata"`
			Size     int           `json:"size"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/status/sessions", &resp); err != nil {
		return nil, err
	}
	return resp.MediaContainer.Metadata, nil
}

// Ping checks if Plex is reachable and authenticated.
func (c *PlexClient) Ping(ctx context.Context) error {
	_, err := c.ServerInfo(ctx)
	return err
}

// AddLibrary adds a new library section to Plex via the API.
func (c *PlexClient) AddLibrary(ctx context.Context, name, libType, path string) error {
	url := fmt.Sprintf("%s/library/sections?name=%s&type=%s&agent=tv.plex.agents.movie&scanner=Plex%%20Movie&language=en&importFromiTunes=&enableAutoPhotoTags=&location=%s&X-Plex-Token=%s",
		c.base, name, libType, path, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// PlexStatus is the combined status returned by FetchAll.
type PlexStatus struct {
	Info      *PlexServerInfo
	Libraries []PlexLibrary
	Sessions  []PlexSession
}

// FetchAll loads all Plex status in one call.
func (c *PlexClient) FetchAll(ctx context.Context) (*PlexStatus, error) {
	info, err := c.ServerInfo(ctx)
	if err != nil {
		return nil, err
	}
	libs, _ := c.Libraries(ctx)
	sessions, _ := c.Sessions(ctx)
	return &PlexStatus{
		Info:      info,
		Libraries: libs,
		Sessions:  sessions,
	}, nil
}

// ── Servarr clients (Sonarr, Radarr, Lidarr, Readarr, Prowlarr, Bazarr) ─────

// ServarrClient is a generic client for *arr apps (same API shape).
type ServarrClient struct {
	base    string // e.g. http://192.168.1.50:8989
	apiKey  string
	appName string // "sonarr", "radarr", etc.
	http    *http.Client
}

func NewServarrClient(base, apiKey, appName string, insecure bool) *ServarrClient {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
	}
	return &ServarrClient{
		base:    strings.TrimRight(base, "/"),
		apiKey:  apiKey,
		appName: appName,
		http:    &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
}

func (c *ServarrClient) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ServarrHealth is one health check item.
type ServarrHealth struct {
	Source  string `json:"source"`
	Type    string `json:"type"` // "warning", "error"
	Message string `json:"message"`
}

// ServarrSystemStatus is the /api/v3/system/status response.
type ServarrSystemStatus struct {
	AppName  string `json:"appName"`
	Version  string `json:"version"`
	OsName   string `json:"osName"`
	IsLinux  bool   `json:"isLinux"`
	IsDocker bool   `json:"isDocker"`
}

// ServarrQueueItem is one item in the download queue.
type ServarrQueueItem struct {
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	TrackedStatus  string  `json:"trackedDownloadStatus"`
	SizeTotal      float64 `json:"size"`
	Sizeleft       float64 `json:"sizeleft"`
	Timeleft       string  `json:"timeleft"`
	Protocol       string  `json:"protocol"`
}

// SystemStatus returns basic app info and version.
func (c *ServarrClient) SystemStatus(ctx context.Context) (*ServarrSystemStatus, error) {
	var out ServarrSystemStatus
	if err := c.get(ctx, "/api/v3/system/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Health returns active health check issues.
func (c *ServarrClient) Health(ctx context.Context) ([]ServarrHealth, error) {
	var out []ServarrHealth
	if err := c.get(ctx, "/api/v3/health", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Queue returns active download queue items.
func (c *ServarrClient) Queue(ctx context.Context) ([]ServarrQueueItem, error) {
	var resp struct {
		Records []ServarrQueueItem `json:"records"`
	}
	if err := c.get(ctx, "/api/v3/queue?pageSize=50", &resp); err != nil {
		return nil, err
	}
	return resp.Records, nil
}

// Ping checks connectivity to the app.
func (c *ServarrClient) Ping(ctx context.Context) error {
	_, err := c.SystemStatus(ctx)
	return err
}

// ServarrStatus is the combined status for one *arr app.
type ServarrStatus struct {
	AppName string
	URL     string
	Online  bool
	Status  *ServarrSystemStatus
	Health  []ServarrHealth
	Queue   []ServarrQueueItem
}

// FetchStatus loads the full status for one servarr app.
func (c *ServarrClient) FetchStatus(ctx context.Context) ServarrStatus {
	s := ServarrStatus{AppName: c.appName, URL: c.base}
	status, err := c.SystemStatus(ctx)
	if err != nil {
		s.Online = false
		return s
	}
	s.Online = true
	s.Status = status
	s.Health, _ = c.Health(ctx)
	s.Queue, _ = c.Queue(ctx)
	return s
}

// ── Media service definitions ──────────────────────────────────────────────────

// MediaServiceDef describes a third-party media service that can be managed.
type MediaServiceDef struct {
	ID          string // "sonarr", "radarr", etc.
	Name        string // display name
	Description string
	DefaultPort int
	DockerImage string
	Category    string // "arr" | "utility" | "request"
}

// MediaServices is the catalog of supported third-party media services.
var MediaServices = []MediaServiceDef{
	{ID: "sonarr", Name: "Sonarr", Description: "TV show management & download automation", DefaultPort: 8989, DockerImage: "linuxserver/sonarr", Category: "arr"},
	{ID: "radarr", Name: "Radarr", Description: "Movie management & download automation", DefaultPort: 7878, DockerImage: "linuxserver/radarr", Category: "arr"},
	{ID: "lidarr", Name: "Lidarr", Description: "Music management & download automation", DefaultPort: 8686, DockerImage: "linuxserver/lidarr", Category: "arr"},
	{ID: "readarr", Name: "Readarr", Description: "Book/audiobook management", DefaultPort: 8787, DockerImage: "linuxserver/readarr", Category: "arr"},
	{ID: "prowlarr", Name: "Prowlarr", Description: "Indexer manager / proxy for *arr apps", DefaultPort: 9696, DockerImage: "linuxserver/prowlarr", Category: "arr"},
	{ID: "bazarr", Name: "Bazarr", Description: "Subtitle management for Sonarr & Radarr", DefaultPort: 6767, DockerImage: "linuxserver/bazarr", Category: "utility"},
	{ID: "overseerr", Name: "Overseerr", Description: "Media request management (Plex-native)", DefaultPort: 5055, DockerImage: "linuxserver/overseerr", Category: "request"},
	{ID: "jellyseerr", Name: "Jellyseerr", Description: "Media request management (Jellyfin/Plex)", DefaultPort: 5055, DockerImage: "fallenbagel/jellyseerr", Category: "request"},
	{ID: "tautulli", Name: "Tautulli", Description: "Plex analytics & monitoring", DefaultPort: 8181, DockerImage: "linuxserver/tautulli", Category: "utility"},
	{ID: "ombi", Name: "Ombi", Description: "Media request management", DefaultPort: 3579, DockerImage: "linuxserver/ombi", Category: "request"},
	{ID: "recyclarr", Name: "Recyclarr", Description: "Sync TRaSH guides to Sonarr/Radarr", DefaultPort: 0, DockerImage: "recyclarr/recyclarr", Category: "utility"},
	{ID: "flaresolverr", Name: "FlareSolverr", Description: "Cloudflare bypass proxy for indexers", DefaultPort: 8191, DockerImage: "flaresolverr/flaresolverr", Category: "utility"},
}
