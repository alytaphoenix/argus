package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alytaphoenix/argus/config"
)

func TestTicketAuth(t *testing.T) {
	logins := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/access/ticket":
			logins++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("username") != "root@pam" || r.Form.Get("password") != "hunter2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"data":{"ticket":"PVE:root@pam:TICKET","CSRFPreventionToken":"CSRF123"}}`))
		case "/api2/json/nodes":
			cookie, err := r.Cookie("PVEAuthCookie")
			if err != nil || cookie.Value != "PVE:root@pam:TICKET" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method != http.MethodGet && r.Header.Get("CSRFPreventionToken") != "CSRF123" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(&config.Config{Host: srv.URL, Username: "root@pam", Password: "hunter2"})
	ctx := context.Background()

	var out []struct{}
	if err := c.get(ctx, "/nodes", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := c.post(ctx, "/nodes"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if logins != 1 {
		t.Fatalf("expected 1 login, got %d", logins)
	}
}

func TestTicketAuthBadPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(&config.Config{Host: srv.URL, Username: "root@pam", Password: "wrong"})
	var out []struct{}
	if err := c.get(context.Background(), "/nodes", &out); err == nil {
		t.Fatal("expected login error")
	}
}

func TestTokenAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=user@pam!tok=sec" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := NewClient(&config.Config{Host: srv.URL, TokenID: "user@pam!tok", Secret: "sec"})
	var out []struct{}
	if err := c.get(context.Background(), "/nodes", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
}
