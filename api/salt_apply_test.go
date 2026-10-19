package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplyStateWithPillar(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"return": []map[string]any{{"token": "test-token", "expire": 9999999999.0}},
			})
			return
		}
		var body []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got = body[0]
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"return": []map[string]any{{"jid": "20260101000000000000"}},
		})
	}))
	defer srv.Close()

	c := newTestSaltClient(t, srv.URL)
	jid, err := c.ApplyStateWithPillar(context.Background(), "proxmox", "services.wiring",
		map[string]any{"services": map[string]any{"grafana": map[string]any{"host": "10.0.0.5"}}})
	if err != nil {
		t.Fatalf("ApplyStateWithPillar: %v", err)
	}
	if jid != "20260101000000000000" {
		t.Fatalf("jid = %q", jid)
	}
	if got["fun"] != "state.apply" {
		t.Errorf("fun = %v, want state.apply", got["fun"])
	}
	kwarg, ok := got["kwarg"].(map[string]any)
	if !ok {
		t.Fatalf("kwarg missing: %#v", got)
	}
	pillar, ok := kwarg["pillar"].(map[string]any)
	if !ok || pillar["services"] == nil {
		t.Fatalf("inline pillar missing: %#v", kwarg)
	}

	// ApplyState without a pillar must not send kwarg.
	got = nil
	if _, err := c.ApplyState(context.Background(), "proxmox", "argus.base"); err != nil {
		t.Fatalf("ApplyState: %v", err)
	}
	if _, ok := got["kwarg"]; ok {
		t.Errorf("ApplyState sent an empty kwarg: %#v", got)
	}
}
