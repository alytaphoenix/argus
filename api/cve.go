package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CVEFinding is one vulnerability found by a scanner.
type CVEFinding struct {
	Target   string
	ID       string // CVE-YYYY-NNNNN
	Severity string // CRITICAL, HIGH, MEDIUM, LOW, UNKNOWN
	PkgName  string
	Version  string
	FixedIn  string
	Title    string
}

// CVEScanner wraps Trivy (preferred) or Grype for local CVE scanning.
type CVEScanner struct {
	binary string // "trivy" or "grype"
}

// NewCVEScanner creates a scanner. It prefers trivy, falls back to grype.
func NewCVEScanner() *CVEScanner {
	for _, b := range []string{"trivy", "grype"} {
		if _, err := exec.LookPath(b); err == nil {
			return &CVEScanner{binary: b}
		}
	}
	return &CVEScanner{binary: "trivy"} // default; will fail gracefully
}

// Available returns true if the scanner binary exists.
func (s *CVEScanner) Available() bool {
	_, err := exec.LookPath(s.binary)
	return err == nil
}

// Binary returns the scanner binary name being used.
func (s *CVEScanner) Binary() string { return s.binary }

// ScanImage scans a container image and returns CVE findings.
func (s *CVEScanner) ScanImage(ctx context.Context, image string) ([]CVEFinding, error) {
	switch s.binary {
	case "trivy":
		return s.trivyScan(ctx, "image", image)
	case "grype":
		return s.grypeScan(ctx, image)
	}
	return nil, fmt.Errorf("no CVE scanner available (install trivy or grype)")
}

// ScanFS scans a filesystem path and returns CVE findings.
func (s *CVEScanner) ScanFS(ctx context.Context, path string) ([]CVEFinding, error) {
	switch s.binary {
	case "trivy":
		return s.trivyScan(ctx, "fs", path)
	case "grype":
		return s.grypeScan(ctx, "dir:"+path)
	}
	return nil, fmt.Errorf("no CVE scanner available (install trivy or grype)")
}

func (s *CVEScanner) trivyScan(ctx context.Context, scanType, target string) ([]CVEFinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "trivy", scanType, "--format", "json", "--quiet", target)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("trivy: %w", err)
	}

	var report struct {
		Results []struct {
			Target          string `json:"Target"`
			Vulnerabilities []struct {
				VulnerabilityID  string `json:"VulnerabilityID"`
				Severity         string `json:"Severity"`
				PkgName          string `json:"PkgName"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
				Title            string `json:"Title"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		return nil, fmt.Errorf("trivy parse: %w", err)
	}

	var out []CVEFinding
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			out = append(out, CVEFinding{
				Target:   r.Target,
				ID:       v.VulnerabilityID,
				Severity: v.Severity,
				PkgName:  v.PkgName,
				Version:  v.InstalledVersion,
				FixedIn:  v.FixedVersion,
				Title:    v.Title,
			})
		}
	}
	return out, nil
}

func (s *CVEScanner) grypeScan(ctx context.Context, target string) ([]CVEFinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "grype", target, "-o", "json", "--quiet")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("grype: %w", err)
	}

	var report struct {
		Matches []struct {
			Vulnerability struct {
				ID          string `json:"id"`
				Severity    string `json:"severity"`
				Description string `json:"description"`
				Fix         struct {
					Versions []string `json:"versions"`
				} `json:"fix"`
			} `json:"vulnerability"`
			Artifact struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"artifact"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		return nil, fmt.Errorf("grype parse: %w", err)
	}

	var out []CVEFinding
	for _, m := range report.Matches {
		fix := ""
		if len(m.Vulnerability.Fix.Versions) > 0 {
			fix = strings.Join(m.Vulnerability.Fix.Versions, ", ")
		}
		out = append(out, CVEFinding{
			ID:       m.Vulnerability.ID,
			Severity: strings.ToUpper(m.Vulnerability.Severity),
			PkgName:  m.Artifact.Name,
			Version:  m.Artifact.Version,
			FixedIn:  fix,
			Title:    m.Vulnerability.Description,
		})
	}
	return out, nil
}
