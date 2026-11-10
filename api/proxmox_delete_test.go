package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alytaphoenix/argus/config"
)

func TestDeleteGuestPurge(t *testing.T) {
	var gotMethod, gotPath, gotPurge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotPurge = r.Method, r.URL.Path, r.URL.Query().Get("purge")
		if r.Header.Get("Authorization") != "PVEAPIToken=argus@pve!argus=secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":"UPID:proxmox:0001:0002:0003:qmdestroy:101:root@pam:"}`))
	}))
	defer srv.Close()

	c := NewClient(&config.Config{Host: srv.URL, TokenID: "argus@pve!argus", Secret: "secret"})
	upid, err := c.DeleteGuest(context.Background(), "proxmox", "qemu", 101, true)
	if err != nil {
		t.Fatalf("DeleteGuest: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", gotMethod)
	}
	if gotPath != "/api2/json/nodes/proxmox/qemu/101" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotPurge != "1" {
		t.Fatalf("purge = %q, want 1", gotPurge)
	}
	if upid == "" {
		t.Fatal("expected a task UPID")
	}
}

func TestDeleteGuestWithoutPurge(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"data":"UPID:proxmox:1:2:3:lxdestroy:200:root@pam:"}`))
	}))
	defer srv.Close()

	c := NewClient(&config.Config{Host: srv.URL, TokenID: "argus@pve!argus", Secret: "secret"})
	if _, err := c.DeleteGuest(context.Background(), "proxmox", "lxc", 200, false); err != nil {
		t.Fatalf("DeleteGuest: %v", err)
	}
	if query != "" {
		t.Fatalf("query = %q, want empty", query)
	}
}
