package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// datasetRows answers pool.dataset.query the way TrueNAS does: children nest
// inside their parent row unless the caller asks for flat rows. Only flat rows
// let callers see /mnt/<pool>/<dataset> — the Plex media-root picker and the
// cluster pillar build a path per row.
func datasetRows(params []any) any {
	flat := false
	if len(params) > 1 {
		if opts, ok := params[1].(map[string]any); ok {
			if extra, ok := opts["extra"].(map[string]any); ok {
				flat, _ = extra["flat"].(bool)
			}
		}
	}
	row := func(path string) map[string]any {
		return map[string]any{
			"id":        path,
			"name":      path,
			"available": map[string]any{"parsed": 4096},
		}
	}
	if flat {
		return []any{row("PhoenixNest"), row("PhoenixNest/Media")}
	}
	root := row("PhoenixNest")
	root["children"] = []any{row("PhoenixNest/Media")}
	return []any{root}
}

// truenasWSServer stands in for a TrueNAS 26+ box: REST v2.0 answers 404, so
// the client must fall back to the JSON-RPC WebSocket API at /api/current.
func truenasWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/current", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params []any  `json:"params"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			var result any
			switch req.Method {
			case "auth.login_with_api_key":
				result = true
			case "pool.dataset.query":
				result = datasetRows(req.Params)
			}
			if err := conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": result,
			}); err != nil {
				return
			}
		}
	})
	return httptest.NewServer(mux)
}

func datasetNames(ds []TrueNASDataset) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func TestTrueNASDatasetsSurfaceChildrenOverWebSocket(t *testing.T) {
	srv := truenasWSServer(t)
	defer srv.Close()

	c := NewTrueNASClient(srv.URL, "key", true)
	if c == nil {
		t.Fatal("NewTrueNASClient returned nil")
	}
	got, err := c.Datasets(context.Background())
	if err != nil {
		t.Fatalf("Datasets: %v", err)
	}
	want := []string{"PhoenixNest", "PhoenixNest/Media"}
	if names := datasetNames(got); !slices.Equal(names, want) {
		t.Errorf("Datasets() = %v, want %v — a nested response means the query does not ask for flat rows",
			datasetNames(got), want)
	}
}

func TestTrueNASDatasetsAskForFlatRowsOverREST(t *testing.T) {
	var extra string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2.0/system/info", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"hostname": "nas", "version": "25.10.8"})
	})
	mux.HandleFunc("/api/v2.0/pool/dataset", func(w http.ResponseWriter, r *http.Request) {
		extra = r.URL.Query().Get("extra")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "PhoenixNest", "name": "PhoenixNest", "available": map[string]any{"parsed": 4096}},
			{"id": "PhoenixNest/Media", "name": "PhoenixNest/Media", "available": map[string]any{"parsed": 4096}},
			{"id": "PhoenixNest/Media/Movies", "name": "PhoenixNest/Media/Movies", "available": map[string]any{"parsed": 4096}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewTrueNASClient(srv.URL, "key", false)
	if c == nil {
		t.Fatal("NewTrueNASClient returned nil")
	}
	got, err := c.Datasets(context.Background())
	if err != nil {
		t.Fatalf("Datasets: %v", err)
	}
	if !strings.Contains(extra, `"flat":true`) {
		t.Errorf(`dataset query extra = %q, want it to request "flat":true`, extra)
	}
	// Depth-2 entries are content folders, not export candidates.
	want := []string{"PhoenixNest", "PhoenixNest/Media"}
	if names := datasetNames(got); !slices.Equal(names, want) {
		t.Errorf("Datasets() = %v, want %v", datasetNames(got), want)
	}
}
