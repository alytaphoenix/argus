package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func haServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"API running."}`))
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "2024.1.0", "location_name": "Home"})
	})
	mux.HandleFunc("/api/states", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"entity_id": "light.living_room", "state": "on", "attributes": map[string]any{}},
			{"entity_id": "switch.garage", "state": "off", "attributes": map[string]any{}},
		})
	})
	mux.HandleFunc("/api/states/light.living_room", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"entity_id": "light.living_room",
			"state":     "on",
			"attributes": map[string]any{"brightness": 255},
		})
	})
	mux.HandleFunc("/api/services/light/turn_on", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	})
	return httptest.NewServer(mux)
}

func TestHAClient_Ping(t *testing.T) {
	srv := haServer(t)
	defer srv.Close()
	c := NewHAClient(srv.URL, "test-token", false)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestHAClient_Version(t *testing.T) {
	srv := haServer(t)
	defer srv.Close()
	c := NewHAClient(srv.URL, "test-token", false)
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "2024.1.0" {
		t.Errorf("want 2024.1.0, got %s", v)
	}
}

func TestHAClient_States(t *testing.T) {
	srv := haServer(t)
	defer srv.Close()
	c := NewHAClient(srv.URL, "test-token", false)
	entities, err := c.States(context.Background())
	if err != nil {
		t.Fatalf("States: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("want 2 entities, got %d", len(entities))
	}
	if entities[0].EntityID != "light.living_room" {
		t.Errorf("want light.living_room, got %s", entities[0].EntityID)
	}
}

func TestHAClient_GetState(t *testing.T) {
	srv := haServer(t)
	defer srv.Close()
	c := NewHAClient(srv.URL, "test-token", false)
	e, err := c.GetState(context.Background(), "light.living_room")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if e.State != "on" {
		t.Errorf("want on, got %s", e.State)
	}
}

func TestHAClient_CallService(t *testing.T) {
	srv := haServer(t)
	defer srv.Close()
	c := NewHAClient(srv.URL, "test-token", false)
	if err := c.CallService(context.Background(), "light", "turn_on", "light.living_room", nil); err != nil {
		t.Fatalf("CallService: %v", err)
	}
}
