package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alytaphoenix/argus/api"
)

// ── DNS tab ───────────────────────────────────────────────────────────────────

func (m ServicesModel) loadDNSZones() tea.Cmd {
	client := m.dnsClient
	return func() tea.Msg {
		if client == nil {
			return dnsZonesLoadedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		zones, err := client.Zones(ctx)
		return dnsZonesLoadedMsg{zones: zones, err: err}
	}
}

func (m ServicesModel) loadDNSRecords(zone string) tea.Cmd {
	client := m.dnsClient
	return func() tea.Msg {
		if client == nil {
			return dnsRecordsLoadedMsg{zone: zone, err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recs, err := client.Records(ctx, zone, "")
		return dnsRecordsLoadedMsg{zone: zone, records: recs, err: err}
	}
}

func (m ServicesModel) pingCA() tea.Cmd {
	client := m.caClient
	return func() tea.Msg {
		if client == nil {
			return caHealthMsg{status: "not configured"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx); err != nil {
			return caHealthMsg{status: "error", err: err}
		}
		return caHealthMsg{status: "ok"}
	}
}

func (m ServicesModel) addDNSCmd() tea.Cmd {
	client := m.dnsClient
	zone := m.dnsZone
	recType := strings.ToUpper(m.dnsFields[dnsFieldType].Value())
	name := m.dnsFields[dnsFieldName].Value()
	value := m.dnsFields[dnsFieldValue].Value()
	ttl := 300
	fmt.Sscanf(m.dnsFields[dnsFieldTTL].Value(), "%d", &ttl)
	return func() tea.Msg {
		if client == nil {
			return dnsAddedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		opts := api.DNSAddOptions{
			Zone:   zone,
			Domain: name,
			Type:   recType,
			TTL:    ttl,
		}
		switch recType {
		case "A", "AAAA":
			opts.IPAddress = value
		case "CNAME":
			opts.CName = value
		case "TXT":
			opts.Text = value
		case "MX":
			opts.Exchange = value
			opts.Preference = 10
		}
		err := client.AddRecord(ctx, opts)
		return dnsAddedMsg{err: err}
	}
}

func (m ServicesModel) deleteDNSCmd() tea.Cmd {
	client := m.dnsClient
	if len(m.dnsRecords) == 0 || m.recCursor >= len(m.dnsRecords) {
		return nil
	}
	rec := m.dnsRecords[m.recCursor]
	zone := m.dnsZone
	value := dnsRecordValue(rec)
	return func() tea.Msg {
		if client == nil {
			return dnsDeletedMsg{err: fmt.Errorf("Technitium not configured")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.DeleteRecord(ctx, zone, rec.Name, rec.Type, value)
		return dnsDeletedMsg{err: err}
	}
}

func (m ServicesModel) updateDNS(msg tea.Msg) (ServicesModel, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case dnsZonesLoadedMsg:
		m.dnsLoading = false
		if msg.err != nil {
			m.notice = "DNS: " + msg.err.Error()
			return m, nil, true
		}
		m.zones = msg.zones
		if len(m.zones) > 0 {
			m.dnsZone = m.zones[0].Name
			return m, m.loadDNSRecords(m.dnsZone), true
		}
		return m, nil, true

	case dnsRecordsLoadedMsg:
		m.dnsLoading = false
		if msg.err != nil {
			m.notice = "DNS records: " + msg.err.Error()
			return m, nil, true
		}
		m.dnsRecords = msg.records
		m.recCursor = 0
		m.dnsZone = msg.zone
		return m, nil, true

	case caHealthMsg:
		if msg.err != nil {
			m.caStatus = "error: " + msg.err.Error()
		} else {
			m.caStatus = msg.status
		}
		return m, nil, true

	case dnsAddedMsg:
		m.addingDNS = false
		if msg.err != nil {
			m.notice = "DNS add failed: " + msg.err.Error()
		} else {
			m.notice = "record added"
			m.resetDNSForm()
		}
		return m, m.loadDNSRecords(m.dnsZone), true

	case dnsDeletedMsg:
		if msg.err != nil {
			m.notice = "DNS delete failed: " + msg.err.Error()
		} else {
			m.notice = "record deleted"
		}
		return m, m.loadDNSRecords(m.dnsZone), true
	}
	return m, nil, false
}

func (m ServicesModel) handleDNSTab(key string) (ServicesModel, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.recCursor > 0 {
			m.recCursor--
		}
	case "down", "j":
		if m.recCursor < len(m.dnsRecords)-1 {
			m.recCursor++
		}
	case "[":
		if m.zoneCursor > 0 {
			m.zoneCursor--
			m.dnsZone = m.zones[m.zoneCursor].Name
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "]":
		if m.zoneCursor < len(m.zones)-1 {
			m.zoneCursor++
			m.dnsZone = m.zones[m.zoneCursor].Name
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "r":
		if m.dnsZone != "" {
			m.dnsLoading = true
			return m, m.loadDNSRecords(m.dnsZone)
		}
	case "R":
		m.dnsLoading = true
		return m, m.loadDNSZones()
	case "a":
		m.addingDNS = true
		m.dnsFocus = 0
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "d":
		if len(m.dnsRecords) > 0 {
			return m, m.deleteDNSCmd()
		}
	}
	return m, nil
}

func (m ServicesModel) handleDNSForm(key string, msg tea.KeyMsg) (ServicesModel, tea.Cmd) {
	switch key {
	case "esc":
		m.addingDNS = false
		m.resetDNSForm()
		return m, nil
	case "tab", "down":
		m.dnsFields[m.dnsFocus].Blur()
		m.dnsFocus = (m.dnsFocus + 1) % dnsFieldCount
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "shift+tab", "up":
		m.dnsFields[m.dnsFocus].Blur()
		m.dnsFocus = (m.dnsFocus - 1 + dnsFieldCount) % dnsFieldCount
		m.dnsFields[m.dnsFocus].Focus()
		return m, nil
	case "enter":
		if m.dnsFocus < dnsFieldCount-1 {
			m.dnsFields[m.dnsFocus].Blur()
			m.dnsFocus++
			m.dnsFields[m.dnsFocus].Focus()
			return m, nil
		}
		if m.dnsZone == "" {
			m.notice = "no zone selected"
			m.addingDNS = false
			return m, nil
		}
		recType := strings.TrimSpace(m.dnsFields[dnsFieldType].Value())
		name := strings.TrimSpace(m.dnsFields[dnsFieldName].Value())
		value := strings.TrimSpace(m.dnsFields[dnsFieldValue].Value())
		if recType == "" || name == "" || value == "" {
			m.notice = "type, name, and value are required"
			return m, nil
		}
		return m, m.addDNSCmd()
	}
	var cmd tea.Cmd
	m.dnsFields[m.dnsFocus], cmd = m.dnsFields[m.dnsFocus].Update(msg)
	return m, cmd
}

func (m *ServicesModel) resetDNSForm() {
	for i := range m.dnsFields {
		m.dnsFields[i].SetValue("")
		m.dnsFields[i].Blur()
	}
	m.dnsFields[dnsFieldTTL].SetValue("300")
	m.dnsFocus = 0
}

func (m ServicesModel) viewDNS(height int) string {
	var b strings.Builder
	if m.caClient != nil {
		caLine := "  step-ca: "
		switch m.caStatus {
		case "ok":
			caLine += lipgloss.NewStyle().Foreground(colorGreen).Render("● online")
		case "not configured":
			caLine += styleDim.Render("not configured")
		case "":
			caLine += styleDim.Render("checking…")
		default:
			caLine += lipgloss.NewStyle().Foreground(colorRed).Render(m.caStatus)
		}
		b.WriteString(caLine + "\n")
	}

	if m.addingDNS {
		b.WriteString(m.viewDNSForm())
		return b.String()
	}

	if m.dnsClient == nil {
		b.WriteString(styleDim.Render("  Technitium not configured — set technitium.url in config.toml\n"))
		return b.String()
	}

	if len(m.zones) == 0 {
		if m.dnsLoading {
			b.WriteString(styleDim.Render("  " + m.spinner.View() + " loading zones…\n"))
		} else {
			b.WriteString(styleDim.Render("  No zones found. Press R to refresh.\n"))
		}
		return b.String()
	}

	var zoneBar strings.Builder
	zoneBar.WriteString("  Zones: ")
	for i, z := range m.zones {
		label := z.Name
		if z.Disabled {
			label = styleDim.Render(label)
		}
		if i == m.zoneCursor {
			zoneBar.WriteString(styleSelected.Render("[" + label + "]"))
		} else {
			zoneBar.WriteString(styleDim.Render(" " + label + " "))
		}
		if i < len(m.zones)-1 {
			zoneBar.WriteString(styleDim.Render("│"))
		}
	}
	b.WriteString(zoneBar.String() + styleDim.Render("  [/] switch zone") + "\n")
	b.WriteString(styleDim.Render("  ─────────────────────────────────────\n"))

	if m.dnsLoading {
		b.WriteString(styleDim.Render("  " + m.spinner.View() + " loading records…\n"))
		return b.String()
	}

	if len(m.dnsRecords) == 0 {
		b.WriteString(styleDim.Render("  No records in zone.\n"))
		return b.String()
	}

	hdrFmt := "  %-36s %-8s %-6s  %s\n"
	b.WriteString(styleSubtle.Render(fmt.Sprintf(hdrFmt, "Name", "Type", "TTL", "Value")))

	maxRows := height - 6
	for i, rec := range m.dnsRecords {
		if i >= maxRows {
			break
		}
		value := dnsRecordValue(rec)
		row := fmt.Sprintf(hdrFmt, truncate(rec.Name, 36), rec.Type, fmt.Sprintf("%d", rec.TTL), truncate(value, 40))
		if i == m.recCursor {
			b.WriteString(styleSelected.Render(truncate(row, m.width)))
		} else {
			b.WriteString(truncate(row, m.width))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m ServicesModel) viewDNSForm() string {
	labels := []string{"Type", "Name", "Value", "TTL"}
	zone := m.dnsZone
	var b strings.Builder
	b.WriteString(styleSubtle.Render(fmt.Sprintf("  Add DNS record → zone: %s\n", zone)))
	b.WriteString(styleDim.Render("  ─────────────────────────────────────\n"))
	for i, f := range m.dnsFields {
		label := fmt.Sprintf("  %-8s ", labels[i])
		if i == m.dnsFocus {
			b.WriteString(styleSelected.Render(label) + f.View() + "\n")
		} else {
			b.WriteString(styleDim.Render(label) + f.View() + "\n")
		}
	}
	b.WriteString(styleDim.Render("\n  enter to advance · esc to cancel\n"))
	return b.String()
}

func (m ServicesModel) dnsHelpString() string {
	if m.addingDNS {
		return "  tab/enter advance · esc cancel"
	}
	return "  ↑/↓ records · [/] zone · a add · d delete · r refresh · ←/→ tab"
}

// dnsRecordValue returns the primary value for a DNS record for display / delete.
func dnsRecordValue(rec api.DNSRecord) string {
	switch strings.ToUpper(rec.Type) {
	case "A", "AAAA":
		return rec.RData.IPAddress
	case "CNAME":
		return rec.RData.CName
	case "TXT":
		return rec.RData.Text
	case "MX":
		return fmt.Sprintf("%d %s", rec.RData.Preference, rec.RData.Exchange)
	default:
		return ""
	}
}
