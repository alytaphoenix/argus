package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// ServiceScheme is the protocol used to check a service.
type ServiceScheme string

const (
	SchemeTCP   ServiceScheme = "tcp"
	SchemeHTTP  ServiceScheme = "http"
	SchemeHTTPS ServiceScheme = "https"
)

// ServiceCheck describes a single service to health-check.
type ServiceCheck struct {
	ID     int64
	Name   string
	Host   string
	Port   int
	Scheme ServiceScheme
}

// HealthResult is the outcome of a single health check.
type HealthResult struct {
	ServiceID  int64
	Up         bool
	LatencyMs  int64
	StatusCode int      // HTTP/HTTPS only; 0 for TCP
	CertInfo   *CertInfo // HTTPS only; nil otherwise
	Error      string
	CheckedAt  time.Time
}

// CheckAll runs health checks on all given services concurrently,
// returning one result per service. The context deadline applies to each
// individual check (not the whole batch).
func CheckAll(ctx context.Context, checks []ServiceCheck) []HealthResult {
	results := make([]HealthResult, len(checks))
	var wg sync.WaitGroup
	for i, sc := range checks {
		wg.Add(1)
		go func(idx int, svc ServiceCheck) {
			defer wg.Done()
			results[idx] = CheckService(ctx, svc)
		}(i, sc)
	}
	wg.Wait()
	return results
}

// CheckService runs a single health check.
func CheckService(ctx context.Context, sc ServiceCheck) HealthResult {
	res := HealthResult{ServiceID: sc.ID, CheckedAt: time.Now()}
	addr := fmt.Sprintf("%s:%d", sc.Host, sc.Port)

	switch sc.Scheme {
	case SchemeTCP, "":
		res = checkTCP(ctx, sc, addr)
	case SchemeHTTP:
		res = checkHTTP(ctx, sc, addr, false)
	case SchemeHTTPS:
		res = checkHTTP(ctx, sc, addr, true)
	default:
		res = checkTCP(ctx, sc, addr)
	}
	return res
}

func checkTCP(ctx context.Context, sc ServiceCheck, addr string) HealthResult {
	res := HealthResult{ServiceID: sc.ID, CheckedAt: time.Now()}
	t0 := time.Now()

	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	res.LatencyMs = time.Since(t0).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	conn.Close()
	res.Up = true
	return res
}

func checkHTTP(ctx context.Context, sc ServiceCheck, addr string, useTLS bool) HealthResult {
	res := HealthResult{ServiceID: sc.ID, CheckedAt: time.Now()}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s", scheme, addr)

	// Use a transport that captures TLS info and doesn't follow redirects blindly.
	var certInfo *CertInfo
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	if useTLS {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // we capture the cert ourselves
		}
		transport.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				certInfo = certToInfo(cs.PeerCertificates[0])
			}
			return nil // never block connection on cert errors
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "ratataskr-healthcheck/1.0")

	t0 := time.Now()
	resp, err := client.Do(req)
	res.LatencyMs = time.Since(t0).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		// Even on error the TLS handshake may have succeeded first
		res.CertInfo = certInfo
		return res
	}
	resp.Body.Close()
	res.Up = true
	res.StatusCode = resp.StatusCode
	res.CertInfo = certInfo
	return res
}
