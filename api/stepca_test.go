package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/config"
)

func TestStepCARootCertPinsFingerprint(t *testing.T) {
	var certPEM []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/root") {
			_, _ = w.Write(certPEM)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cert := srv.Certificate()
	sum := sha256.Sum256(cert.Raw)
	fingerprint := hex.EncodeToString(sum[:])
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	client := NewStepCAClient(config.StepCAConfig{URL: srv.URL, Fingerprint: fingerprint})
	info, err := client.RootCert(context.Background())
	if err != nil {
		t.Fatalf("RootCert with correct pin: %v", err)
	}
	if info.Fingerprint != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", info.Fingerprint, fingerprint)
	}

	bad := NewStepCAClient(config.StepCAConfig{URL: srv.URL, Fingerprint: strings.Repeat("0", 64)})
	if _, err := bad.RootCert(context.Background()); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("RootCert with wrong pin = %v, want fingerprint mismatch", err)
	}
}
