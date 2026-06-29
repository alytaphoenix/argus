package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Certificates tab ──────────────────────────────────────────────────────────

func (m ServicesModel) handleCertTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.certCursor > 0 {
			m.certCursor--
		}
	case "down", "j":
		if m.certCursor < len(m.certs)-1 {
			m.certCursor++
		}
	case "r":
		return m, m.loadAll()
	}
	return m, nil
}

func (m ServicesModel) viewCerts(height int) string {
	var b strings.Builder
	if len(m.certs) == 0 {
		b.WriteString(styleDim.Render("  No certificates tracked yet.\n"))
		b.WriteString(styleDim.Render("  Add HTTPS services and press r to run health checks — certs will be discovered automatically.\n"))
		return b.String()
	}

	hdrFmt := "  %-3s %-32s %-28s %-12s %s\n"
	b.WriteString(styleSubtle.Render(fmt.Sprintf(hdrFmt, "●", "Common Name / Host", "Issuer", "Expires", "Days")))
	b.WriteString(styleDim.Render(strings.Repeat("─", min(m.width, 100))) + "\n")

	for i, cert := range m.certs {
		if i >= height-3 {
			break
		}
		days := cert.DaysUntilExpiry()
		var dot, daysStr string
		switch {
		case days < 0:
			dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorRed).Render("EXPIRED")
		case days <= 7:
			dot = lipgloss.NewStyle().Foreground(colorRed).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorRed).Render(fmt.Sprintf("%d days", days))
		case days <= 30:
			dot = lipgloss.NewStyle().Foreground(colorYellow).Render("●")
			daysStr = lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("%d days", days))
		default:
			dot = lipgloss.NewStyle().Foreground(colorGreen).Render("●")
			daysStr = styleDim.Render(fmt.Sprintf("%d days", days))
		}

		label := cert.CommonName
		if label == "" {
			label = fmt.Sprintf("%s:%d", cert.Host, cert.Port)
		}
		expiry := cert.NotAfter.Format("2006-01-02")
		row := fmt.Sprintf(hdrFmt, dot, truncate(label, 32), truncate(cert.Issuer, 28), expiry, daysStr)
		if i == m.certCursor {
			b.WriteString(styleSelected.Render(truncate(row, m.width)) + "\n")
		} else {
			b.WriteString(truncate(row, m.width) + "\n")
		}

		if i == m.certCursor && cert.SANNames != "" {
			sans := "     SANs: " + cert.SANNames
			b.WriteString(styleDim.Render(truncate(sans, m.width)) + "\n")
		}
	}
	return b.String()
}

func (m ServicesModel) certsHelpString() string {
	return "  ↑/↓ navigate · r reload · ←/→ tab"
}
