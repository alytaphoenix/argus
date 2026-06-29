package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func lokiServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/loki/api/v1/query_range", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{
					{
						"stream": map[string]string{"app": "nginx", "host": "web1"},
						"values": [][]string{
							{"1700000002000000000", "request complete"},
							{"1700000001000000000", "request started"},
						},
					},
				},
			},
		})
	})

	mux.HandleFunc("/loki/api/v1/labels", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   []string{"app", "host", "job"},
		})
	})

	return httptest.NewServer(mux)
}

func TestLokiClient_QueryRange(t *testing.T) {
	srv := lokiServer(t)
	defer srv.Close()

	c := NewLokiClient(srv.URL, "test-token", false)
	end := time.Now()
	entries, err := c.QueryRange(context.Background(), `{app="nginx"}`, end.Add(-time.Hour), end, 100)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Verify sorted newest-first.
	if !entries[0].Timestamp.After(entries[1].Timestamp) {
		t.Error("entries should be sorted newest-first")
	}
	if entries[0].Line != "request complete" {
		t.Errorf("want 'request complete', got %q", entries[0].Line)
	}
	if entries[0].Labels["app"] != "nginx" {
		t.Errorf("want label app=nginx, got %q", entries[0].Labels["app"])
	}
}

func TestLokiClient_QueryRange_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "error",
			"error":  "parse error at line 1: unexpected token",
		})
	}))
	defer srv.Close()

	c := NewLokiClient(srv.URL, "", false)
	_, err := c.QueryRange(context.Background(), `bad{`, time.Now().Add(-time.Hour), time.Now(), 10)
	if err == nil {
		t.Fatal("want error for status:error body, got nil")
	}
}

func TestLokiClient_QueryRange_HTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	c := NewLokiClient(srv.URL, "", false)
	_, err := c.QueryRange(context.Background(), `{app="x"}`, time.Now().Add(-time.Hour), time.Now(), 10)
	if err == nil {
		t.Fatal("want error for HTTP 400, got nil")
	}
}

func TestLokiClient_Labels(t *testing.T) {
	srv := lokiServer(t)
	defer srv.Close()

	c := NewLokiClient(srv.URL, "test-token", false)
	labels, err := c.Labels(context.Background())
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if len(labels) != 3 {
		t.Fatalf("want 3 labels, got %d", len(labels))
	}
	found := false
	for _, l := range labels {
		if l == "app" {
			found = true
			break
		}
	}
	if !found {
		t.Error("want label 'app' in result")
	}
}

func TestLokiClient_Labels_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "error",
			"error":  "internal error",
		})
	}))
	defer srv.Close()

	c := NewLokiClient(srv.URL, "", false)
	_, err := c.Labels(context.Background())
	if err == nil {
		t.Fatal("want error for status:error body, got nil")
	}
}

func TestLokiClient_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{}})
	}))
	defer srv.Close()

	c := NewLokiClient(srv.URL, "my-secret-token", false)
	c.Labels(context.Background())
	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("want 'Bearer my-secret-token', got %q", gotAuth)
	}
}

func TestLokiClient_NoAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{}})
	}))
	defer srv.Close()

	c := NewLokiClient(srv.URL, "", false)
	c.Labels(context.Background())
	if gotAuth != "" {
		t.Errorf("want no Authorization header when token is empty, got %q", gotAuth)
	}
}
