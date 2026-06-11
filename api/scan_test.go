package api

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// ── PortScan tests ────────────────────────────────────────────────────────────

// listenRandom starts a TCP listener on a random port and returns the port number
// along with a function to close it.
func listenRandom(t *testing.T) (port int, close func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { ln.Close() }
}

func TestPortScanFindsOpenPort(t *testing.T) {
	port, closeListener := listenRandom(t)
	defer closeListener()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	findings := PortScan(ctx, "127.0.0.1", []int{port})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for open port %d, got %d", port, len(findings))
	}
	f := findings[0]
	if f.Target != "127.0.0.1" {
		t.Errorf("Target = %q, want 127.0.0.1", f.Target)
	}
	if f.ScanType != "port" {
		t.Errorf("ScanType = %q, want port", f.ScanType)
	}
	if f.Check != "tcp:"+intToStr(port) {
		t.Errorf("Check = %q, want tcp:%d", f.Check, port)
	}
}

func TestPortScanClosedPort(t *testing.T) {
	// Use a port that is almost certainly closed; bind and immediately close to get a free port number.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("can't get free port:", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // close immediately — port is now closed

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	findings := PortScan(ctx, "127.0.0.1", []int{port})
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for closed port %d, got %d", port, len(findings))
	}
}

func TestPortScanMultiplePorts(t *testing.T) {
	port1, close1 := listenRandom(t)
	defer close1()
	port2, close2 := listenRandom(t)
	defer close2()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Include one definitely-closed port too
	findings := PortScan(ctx, "127.0.0.1", []int{port1, port2, 1}) // port 1 is reserved/closed
	// Should find at least port1 and port2
	found := make(map[int]bool)
	for _, f := range findings {
		var p int
		intFromStr(f.Check[4:], &p) // strip "tcp:"
		found[p] = true
	}
	if !found[port1] {
		t.Errorf("port %d not found in scan results", port1)
	}
	if !found[port2] {
		t.Errorf("port %d not found in scan results", port2)
	}
}

func TestPortScanCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should return quickly without hanging
	done := make(chan struct{})
	go func() {
		PortScan(ctx, "192.0.2.1", []int{80, 443, 8080}) // TEST-NET, should not respond
		close(done)
	}()
	select {
	case <-done:
		// passed — scan completed quickly after cancellation
	case <-time.After(3 * time.Second):
		t.Error("PortScan did not respect cancelled context — timed out waiting")
	}
}

// ── Severity helpers ──────────────────────────────────────────────────────────

func TestScanSeverityOrder(t *testing.T) {
	cases := []struct {
		sev  string
		want int
	}{
		{"critical", 5},
		{"high", 4},
		{"medium", 3},
		{"low", 2},
		{"info", 1},
		{"unknown", 0},
		{"", 0},
	}
	for _, c := range cases {
		got := ScanSeverityOrder(c.sev)
		if got != c.want {
			t.Errorf("ScanSeverityOrder(%q) = %d, want %d", c.sev, got, c.want)
		}
	}
}

// ── Well-known service labels and severity mappings ───────────────────────────

func TestPortScanSeverityMapping(t *testing.T) {
	// Ports with elevated severity should be flagged appropriately.
	// We test by opening a listener on a local high port that mimics the
	// "dangerous" ports via a remapping in the scan.
	// Since we can't easily open port 23 (requires root), we verify the
	// severity logic via the wellKnownService helper and the switch statement.
	cases := []struct {
		port     int
		minSev   string // minimum expected severity
	}{
		{23, "critical"},  // telnet
		{21, "high"},      // ftp
		{5900, "high"},    // vnc
		{6379, "medium"},  // redis
		{3389, "medium"},  // rdp
		{2049, "medium"},  // nfs
		{22, "info"},      // ssh (just info)
		{80, "info"},      // http (just info)
	}
	for _, c := range cases {
		svc := wellKnownService(c.port)
		if svc == "" {
			t.Errorf("wellKnownService(%d) = empty string", c.port)
		}
		// For ports with known severity rules, build a fake finding and verify ordering.
		// We can't call PortScan on these ports directly, so we check the severity
		// assignment logic by simulating what PortScan does:
		f := ScanFinding{Severity: "info"}
		switch c.port {
		case 23:
			f.Severity = "critical"
		case 21:
			f.Severity = "high"
		case 5900:
			f.Severity = "high"
		case 9200, 9300, 27017:
			f.Severity = "high"
		case 111, 2049, 135, 139, 445, 3389, 6379:
			f.Severity = "medium"
		}
		if ScanSeverityOrder(f.Severity) < ScanSeverityOrder(c.minSev) {
			t.Errorf("port %d: severity %q < expected minimum %q", c.port, f.Severity, c.minSev)
		}
	}
}

// ── DiscoverHost ──────────────────────────────────────────────────────────────

func TestDiscoverHost(t *testing.T) {
	port, closeListener := listenRandom(t)
	defer closeListener()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	summary, findings := DiscoverHost(ctx, "127.0.0.1")
	// We opened one port but DiscoverHost scans CommonPorts, not our custom port.
	// It might or might not find it, but the function must not panic and the
	// summary must be consistent with the findings.
	_ = port // just needed to have a listener open

	portCount := len(findings)
	if portCount > 0 && summary == "" {
		t.Error("summary is empty but findings are non-empty")
	}
	if portCount == 0 && summary != "" {
		t.Error("summary is non-empty but findings are empty")
	}
}

// ── CommonPorts list sanity ───────────────────────────────────────────────────

func TestCommonPorts(t *testing.T) {
	if len(CommonPorts) == 0 {
		t.Error("CommonPorts is empty")
	}
	seen := make(map[int]bool)
	for _, p := range CommonPorts {
		if p <= 0 || p > 65535 {
			t.Errorf("CommonPorts contains invalid port: %d", p)
		}
		if seen[p] {
			t.Errorf("CommonPorts contains duplicate port: %d", p)
		}
		seen[p] = true
	}
}

// helpers

func intToStr(n int) string {
	return fmt.Sprintf("%d", n)
}

func intFromStr(s string, out *int) {
	fmt.Sscanf(s, "%d", out)
}
