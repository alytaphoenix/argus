package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/config"
)

// The SSH key payload is the one cloud-init value that carries characters a
// URL form encoding mangles: '+' (base64), '/' and '=' (base64 padding) and
// spaces between type, blob and comment. Proxmox requires RFC 3986
// percent-encoding of the whole authorized_keys blob, so a round trip through
// SetCloudInit must reproduce the input byte for byte.
func TestSetCloudInitSSHKeyEncodingRoundTrip(t *testing.T) {
	const keys = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIP2Y1nZ0Zq0m1cVC1kEQ3sQxW7pYbGhKd6TJXK9ZcQ1a key+with/special=chars\n" +
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDTrailingPaddingBase64+/+/+/+/== second key"

	var path, method, contentType string
	var sent map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method, contentType = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Errorf("body is not JSON: %q (%v)", raw, err)
		}
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	client := NewClient(&config.Config{Host: srv.URL, TokenID: "user@pam!tok", Secret: "sec"})
	if err := client.SetCloudInit(context.Background(), "pve1", 101, CloudInitOptions{SSHKeys: keys}); err != nil {
		t.Fatalf("SetCloudInit: %v", err)
	}

	if method != http.MethodPut || path != "/api2/json/nodes/pve1/qemu/101/config" {
		t.Errorf("%s %s, want PUT /api2/json/nodes/pve1/qemu/101/config", method, path)
	}
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("content type = %q, want JSON", contentType)
	}
	if len(sent) != 1 {
		t.Errorf("body = %+v, want only the sshkeys field", sent)
	}
	encoded, ok := sent["sshkeys"]
	if !ok {
		t.Fatal("sshkeys missing from the request body")
	}
	// No raw '+', '/' or whitespace may survive: Proxmox reads '+' as a space.
	if strings.ContainsAny(encoded, "+/\n ") {
		t.Errorf("sshkeys value is not fully percent-encoded: %q", encoded)
	}
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("unescape %q: %v", encoded, err)
	}
	if decoded != keys {
		t.Errorf("round trip mismatch:\ngot  %q\nwant %q", decoded, keys)
	}
}
