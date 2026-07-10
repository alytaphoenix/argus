package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alytaphoenix/ratataskr/config"
)

func saltKeyServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"return": []map[string]any{{"token": "test-token", "expire": 9999999999.0}},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cmd := body[0]
		fun, _ := cmd["fun"].(string)

		switch fun {
		case "key.list_all":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"return": []map[string]any{{
					"minions":          []string{"web01", "db01"},
					"minions_pre":      []string{"new-node"},
					"minions_rejected": []string{"bad-node"},
					"minions_denied":   []string{},
				}},
			})
		case "key.accept":
			match, _ := cmd["match"].(string)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"return": []map[string]any{{"minions": []string{match}}},
			})
		case "key.reject":
			match, _ := cmd["match"].(string)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"return": []map[string]any{{"minions_rejected": []string{match}}},
			})
		case "key.delete":
			match, _ := cmd["match"].(string)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"return": []map[string]any{{"minions": []string{match}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return httptest.NewServer(mux), &calls
}

func newTestSaltClient(t *testing.T, url string) *SaltClient {
	t.Helper()
	c, err := NewSaltClient(config.SaltConfig{URL: url, Username: "admin", Password: "pass"})
	if err != nil {
		t.Fatalf("NewSaltClient: %v", err)
	}
	return c
}

func TestListKeys(t *testing.T) {
	srv, _ := saltKeyServer(t)
	defer srv.Close()
	c := newTestSaltClient(t, srv.URL)

	keys, err := c.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys.Accepted) != 2 {
		t.Errorf("want 2 accepted, got %d", len(keys.Accepted))
	}
	if len(keys.Pending) != 1 || keys.Pending[0] != "new-node" {
		t.Errorf("want pending=[new-node], got %v", keys.Pending)
	}
	if len(keys.Rejected) != 1 || keys.Rejected[0] != "bad-node" {
		t.Errorf("want rejected=[bad-node], got %v", keys.Rejected)
	}
}

func TestListKeys_NestedFormat(t *testing.T) {
	// Test the nested {"data": {"return": {...}}} wheel response format.
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"return": []map[string]any{{"token": "tok", "expire": 9999999999.0}},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"return": []map[string]any{{
				"data": map[string]any{
					"return": map[string]any{
						"minions":          []string{"a"},
						"minions_pre":      []string{"b"},
						"minions_rejected": []string{},
						"minions_denied":   []string{},
					},
				},
			}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestSaltClient(t, srv.URL)
	keys, err := c.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("ListKeys nested: %v", err)
	}
	if len(keys.Accepted) != 1 || keys.Accepted[0] != "a" {
		t.Errorf("want accepted=[a], got %v", keys.Accepted)
	}
	if len(keys.Pending) != 1 || keys.Pending[0] != "b" {
		t.Errorf("want pending=[b], got %v", keys.Pending)
	}
}

func TestAcceptKey(t *testing.T) {
	srv, _ := saltKeyServer(t)
	defer srv.Close()
	c := newTestSaltClient(t, srv.URL)

	if err := c.AcceptKey(context.Background(), "new-node"); err != nil {
		t.Fatalf("AcceptKey: %v", err)
	}
}

func TestRejectKey(t *testing.T) {
	srv, _ := saltKeyServer(t)
	defer srv.Close()
	c := newTestSaltClient(t, srv.URL)

	if err := c.RejectKey(context.Background(), "new-node"); err != nil {
		t.Fatalf("RejectKey: %v", err)
	}
}

func TestDeleteKey(t *testing.T) {
	srv, _ := saltKeyServer(t)
	defer srv.Close()
	c := newTestSaltClient(t, srv.URL)

	if err := c.DeleteKey(context.Background(), "web01"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
}
