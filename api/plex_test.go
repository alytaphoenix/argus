package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── PlexClient tests ──────────────────────────────────────────────────────────

func newPlexTestServer(t *testing.T, token string, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify token is passed
		if got := r.URL.Query().Get("X-Plex-Token"); got != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	return srv
}

func TestPlexClientServerInfo(t *testing.T) {
	const token = "testtoken123"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"friendlyName":      "My Plex Server",
				"version":           "1.40.0",
				"platform":          "Linux",
				"platformVersion":   "Ubuntu 22.04",
				"machineIdentifier": "abc123",
			},
		})
	})

	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.ServerInfo(ctx)
	if err != nil {
		t.Fatalf("ServerInfo error: %v", err)
	}
	if info.FriendlyName != "My Plex Server" {
		t.Errorf("FriendlyName = %q, want %q", info.FriendlyName, "My Plex Server")
	}
	if info.Version != "1.40.0" {
		t.Errorf("Version = %q, want %q", info.Version, "1.40.0")
	}
	if info.MachineID != "abc123" {
		t.Errorf("MachineID = %q, want %q", info.MachineID, "abc123")
	}
}

func TestPlexClientLibraries(t *testing.T) {
	const token = "testtoken123"
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"Directory": []map[string]interface{}{
					{
						"key":      "1",
						"title":    "Movies",
						"type":     "movie",
						"agent":    "tv.plex.agents.movie",
						"scanner":  "Plex Movie",
						"language": "en",
						"count":    42,
						"Location": []map[string]interface{}{
							{"path": "/mnt/tank/movies"},
						},
					},
					{
						"key":      "2",
						"title":    "TV Shows",
						"type":     "show",
						"agent":    "tv.plex.agents.series",
						"scanner":  "Plex Series Scanner",
						"language": "en",
						"count":    15,
						"Location": []map[string]interface{}{
							{"path": "/mnt/tank/tv"},
						},
					},
				},
			},
		})
	})

	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	libs, err := client.Libraries(ctx)
	if err != nil {
		t.Fatalf("Libraries error: %v", err)
	}
	if len(libs) != 2 {
		t.Fatalf("len(libs) = %d, want 2", len(libs))
	}
	if libs[0].Title != "Movies" {
		t.Errorf("libs[0].Title = %q, want %q", libs[0].Title, "Movies")
	}
	if libs[0].Count != 42 {
		t.Errorf("libs[0].Count = %d, want 42", libs[0].Count)
	}
	if len(libs[0].Locations) == 0 || libs[0].Locations[0].Path != "/mnt/tank/movies" {
		t.Errorf("libs[0].Locations[0].Path = %q, want /mnt/tank/movies", libs[0].Locations[0].Path)
	}
	if libs[1].Type != "show" {
		t.Errorf("libs[1].Type = %q, want %q", libs[1].Type, "show")
	}
}

func TestPlexClientSessions(t *testing.T) {
	const token = "tok"
	mux := http.NewServeMux()
	mux.HandleFunc("/status/sessions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"size": 1,
				"Metadata": []map[string]interface{}{
					{
						"sessionKey":       "1",
						"title":            "Inception",
						"grandparentTitle": "",
						"type":             "movie",
						"viewOffset":       30000,
						"duration":         9000000,
						"User":             map[string]interface{}{"title": "alyta"},
						"Player":           map[string]interface{}{"device": "TV", "state": "playing", "product": "Plex for TV"},
					},
				},
			},
		})
	})

	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessions, err := client.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Title != "Inception" {
		t.Errorf("Title = %q, want Inception", s.Title)
	}
	if s.User.Title != "alyta" {
		t.Errorf("User.Title = %q, want alyta", s.User.Title)
	}
	if s.Player.State != "playing" {
		t.Errorf("Player.State = %q, want playing", s.Player.State)
	}
}

func TestPlexClientPingSuccess(t *testing.T) {
	const token = "tok"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"friendlyName": "PingTest",
			},
		})
	})

	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping error: %v", err)
	}
}

func TestPlexClientPingFail(t *testing.T) {
	// Point to a non-existent server
	client := NewPlexClient("http://127.0.0.1:1", "tok", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err == nil {
		t.Error("expected error pinging unreachable server, got nil")
	}
}

func TestPlexClientUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Always 401
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, "wrongtoken", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err == nil {
		t.Error("expected error with bad token, got nil")
	}
}

func TestPlexClientFetchAll(t *testing.T) {
	const token = "tok"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{"friendlyName": "Test"},
		})
	})
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"Directory": []map[string]interface{}{},
			},
		})
	})
	mux.HandleFunc("/status/sessions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{
				"Metadata": []interface{}{},
			},
		})
	})

	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	client := NewPlexClient(srv.URL, token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.FetchAll(ctx)
	if err != nil {
		t.Fatalf("FetchAll error: %v", err)
	}
	if status.Info == nil {
		t.Error("FetchAll: Info is nil")
	}
	if status.Libraries == nil {
		t.Error("FetchAll: Libraries is nil")
	}
}

// ── ServarrClient tests ───────────────────────────────────────────────────────

func newServarrTestServer(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/system/status":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"appName": "Sonarr",
				"version": "4.0.0",
				"osName":  "ubuntu",
				"isLinux": true,
			})
		case "/api/v3/health":
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"source": "IndexerStatusCheck", "type": "warning", "message": "Indexer unavailable"},
			})
		case "/api/v3/queue":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"records": []map[string]interface{}{
					{
						"title":                "Some Show S01E01",
						"status":               "downloading",
						"trackedDownloadStatus": "ok",
						"size":                 1073741824.0,
						"sizeleft":             536870912.0,
						"timeleft":             "00:15:00",
						"protocol":             "torrent",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestServarrClientSystemStatus(t *testing.T) {
	const apiKey = "myapikey"
	srv := newServarrTestServer(t, apiKey)
	defer srv.Close()

	client := NewServarrClient(srv.URL, apiKey, "sonarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.SystemStatus(ctx)
	if err != nil {
		t.Fatalf("SystemStatus error: %v", err)
	}
	if status.AppName != "Sonarr" {
		t.Errorf("AppName = %q, want Sonarr", status.AppName)
	}
	if status.Version != "4.0.0" {
		t.Errorf("Version = %q, want 4.0.0", status.Version)
	}
	if !status.IsLinux {
		t.Error("IsLinux = false, want true")
	}
}

func TestServarrClientHealth(t *testing.T) {
	const apiKey = "myapikey"
	srv := newServarrTestServer(t, apiKey)
	defer srv.Close()

	client := NewServarrClient(srv.URL, apiKey, "sonarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if len(health) != 1 {
		t.Fatalf("len(health) = %d, want 1", len(health))
	}
	if health[0].Type != "warning" {
		t.Errorf("Type = %q, want warning", health[0].Type)
	}
}

func TestServarrClientQueue(t *testing.T) {
	const apiKey = "myapikey"
	srv := newServarrTestServer(t, apiKey)
	defer srv.Close()

	client := NewServarrClient(srv.URL, apiKey, "sonarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	queue, err := client.Queue(ctx)
	if err != nil {
		t.Fatalf("Queue error: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("len(queue) = %d, want 1", len(queue))
	}
	if queue[0].Protocol != "torrent" {
		t.Errorf("Protocol = %q, want torrent", queue[0].Protocol)
	}
}

func TestServarrClientFetchStatus(t *testing.T) {
	const apiKey = "myapikey"
	srv := newServarrTestServer(t, apiKey)
	defer srv.Close()

	client := NewServarrClient(srv.URL, apiKey, "sonarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status := client.FetchStatus(ctx)
	if !status.Online {
		t.Error("Online = false, want true")
	}
	if status.Status == nil {
		t.Error("Status is nil")
	}
	if len(status.Health) != 1 {
		t.Errorf("len(Health) = %d, want 1", len(status.Health))
	}
	if len(status.Queue) != 1 {
		t.Errorf("len(Queue) = %d, want 1", len(status.Queue))
	}
}

func TestServarrClientFetchStatusOffline(t *testing.T) {
	client := NewServarrClient("http://127.0.0.1:1", "key", "radarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status := client.FetchStatus(ctx)
	if status.Online {
		t.Error("Online = true for unreachable server, want false")
	}
}

func TestServarrClientUnauthorized(t *testing.T) {
	srv := newServarrTestServer(t, "correct-key")
	defer srv.Close()

	client := NewServarrClient(srv.URL, "wrong-key", "sonarr", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status := client.FetchStatus(ctx)
	if status.Online {
		t.Error("Online = true with wrong API key, want false")
	}
}

// ── NewPlexClient URL trimming test ──────────────────────────────────────────

func TestNewPlexClientTrimSlash(t *testing.T) {
	const token = "tok"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"MediaContainer": map[string]interface{}{"friendlyName": "OK"},
		})
	})
	srv := newPlexTestServer(t, token, mux)
	defer srv.Close()

	// Create with trailing slash — should still work
	client := NewPlexClient(srv.URL+"/", token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping with trailing slash: %v", err)
	}
}

// ── MediaServices catalog test ────────────────────────────────────────────────

func TestMediaServicesCatalog(t *testing.T) {
	expected := []string{"sonarr", "radarr", "lidarr", "readarr", "prowlarr", "bazarr", "overseerr", "jellyseerr", "tautulli", "ombi", "recyclarr", "flaresolverr"}
	if len(MediaServices) != len(expected) {
		t.Errorf("len(MediaServices) = %d, want %d", len(MediaServices), len(expected))
	}
	ids := make(map[string]bool)
	for _, svc := range MediaServices {
		ids[svc.ID] = true
		if svc.Name == "" {
			t.Errorf("service %q has empty Name", svc.ID)
		}
		if svc.Description == "" {
			t.Errorf("service %q has empty Description", svc.ID)
		}
		if svc.Category == "" {
			t.Errorf("service %q has empty Category", svc.ID)
		}
	}
	for _, id := range expected {
		if !ids[id] {
			t.Errorf("MediaServices missing entry for %q", id)
		}
	}
}
