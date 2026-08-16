package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	gkr "github.com/alytaphoenix/argus/keyring"
)

type credRow struct {
	key         string
	displayName string
	password    bool
}

// Global service credentials stored in the macOS Keychain.
var credRows = []credRow{
	{gkr.KeyProxmoxSecret, "Proxmox API secret", true},
	{gkr.KeyProxmoxPassword, "Proxmox password (ticket auth)", true},
	{gkr.KeySaltPassword, "Salt password", true},
	{gkr.KeyNomadToken, "Nomad ACL token", true},
	{gkr.KeySSHPassword, "SSH password (global)", true},
	{gkr.KeyWazuhPassword, "Wazuh password", true},
	{gkr.KeyCrowdSecAPIKey, "CrowdSec API key", true},
	{gkr.KeyMatchboxToken, "Matchbox token", true},
	{gkr.KeyTechnitiumToken, "Technitium token", true},
	{gkr.KeyPrometheusToken, "Prometheus token", true},
	{gkr.KeyAnthropicAPIKey, "Anthropic API key", true},
	{gkr.KeyTrueNASAPIKey, "TrueNAS API key", true},
	{gkr.KeyPlexToken, "Plex token (X-Plex-Token)", true},
}

type credSection int

const (
	credSectionGlobal credSection = iota
	credSectionNodes
)

type CredentialsModel struct {
	cursor   int
	section  credSection
	editing  bool
	input    textinput.Model
	notice   string
	err      error
	width    int
	height   int
	database  *db.DB
	globals   config.GlobalsConfig
	nodeCreds []db.NodeSSHCred
	// per-node edit state
	nodeEditMode string // "node" | "user" | "pass" | ""
	nodeEditNode string
	nodeEditUser string
}

func NewCredentialsModel(database *db.DB, globals config.GlobalsConfig) CredentialsModel {
	m := CredentialsModel{database: database, globals: globals}
	if database != nil {
		m.nodeCreds, _ = database.NodeSSHCreds()
	}
	return m
}

func (m CredentialsModel) Init() tea.Cmd { return nil }

func (m CredentialsModel) Update(msg tea.Msg) (CredentialsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.editing {
			switch msg.String() {
			case "enter":
				val := m.input.Value()
				if m.section == credSectionGlobal {
					row := credRows[m.cursor]
					if val == "" {
						gkr.Delete(row.key) //nolint:errcheck
						m.notice = "deleted " + row.displayName
					} else {
						if err := gkr.Save(row.key, val); err != nil {
							m.err = err
						} else {
							m.notice = "saved " + row.displayName + " to keychain"
						}
					}
				} else {
					// Per-node SSH credential edit
					if m.nodeEditMode == "user" {
						m.nodeEditUser = val
						// Now ask for password
						ti := textinput.New()
						ti.Placeholder = "enter SSH password (blank = delete)"
						ti.EchoMode = textinput.EchoPassword
						ti.CharLimit = 256
						ti.Focus()
						m.input = ti
						m.nodeEditMode = "pass"
						return m, textinput.Blink
					} else if m.nodeEditMode == "pass" {
						pass := val
						node := m.nodeEditNode
						user := m.nodeEditUser
						if user == "" {
							user = "root"
						}
						keyringKey := gkr.SSHNodeKey(node)
						if pass == "" {
							// Delete
							gkr.Delete(keyringKey) //nolint:errcheck
							if m.database != nil {
								m.database.DeleteNodeSSHCred(node) //nolint:errcheck
							}
							m.notice = "deleted SSH credentials for " + node
						} else {
							if err := gkr.Save(keyringKey, pass); err != nil {
								m.err = err
							} else {
								if m.database != nil {
									m.database.SaveNodeSSHCred(db.NodeSSHCred{ //nolint:errcheck
										Node:       node,
										SSHUser:    user,
										KeyringKey: keyringKey,
									})
								}
								m.notice = "saved SSH credentials for " + node + " (" + user + ")"
							}
						}
						// Refresh list
						if m.database != nil {
							m.nodeCreds, _ = m.database.NodeSSHCreds()
						}
						m.nodeEditMode = ""
					}
					m.editing = false
				}
				if m.nodeEditMode == "" || m.section == credSectionGlobal {
					m.editing = false
				}
				return m, nil
			case "esc":
				m.editing = false
				m.nodeEditMode = ""
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			return m, func() tea.Msg { return backMsg{} }
		case "left", "right", "h", "l":
			if m.section == credSectionGlobal {
				m.section = credSectionNodes
			} else {
				m.section = credSectionGlobal
			}
			m.cursor = 0
			m.notice, m.err = "", nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.notice, m.err = "", nil
		case "down", "j":
			max := len(credRows) - 1
			if m.section == credSectionNodes {
				max = len(m.nodeCreds) // +1 for "add" row
			}
			if m.cursor < max {
				m.cursor++
			}
			m.notice, m.err = "", nil
		case "enter":
			if m.section == credSectionGlobal {
				row := credRows[m.cursor]
				ti := textinput.New()
				ti.Placeholder = "enter value (blank = delete)"
				ti.CharLimit = 512
				if row.password {
					ti.EchoMode = textinput.EchoPassword
				}
				ti.Focus()
				m.input = ti
				m.editing = true
				m.err = nil
				m.notice = ""
				return m, textinput.Blink
			} else {
				// Per-node SSH: edit or add
				var node string
				if m.cursor < len(m.nodeCreds) {
					node = m.nodeCreds[m.cursor].Node
				} else {
					node = "" // add new
				}
				// First ask for node name if new
				ti := textinput.New()
				if node == "" {
					ti.Placeholder = "node name (e.g. pve1)"
				} else {
					ti.Placeholder = fmt.Sprintf("SSH user for %s (blank=root)", node)
					m.nodeEditNode = node
					m.nodeEditMode = "user"
				}
				ti.CharLimit = 64
				ti.Focus()
				m.input = ti
				m.editing = true
				m.err = nil
				m.notice = ""
				if node == "" {
					m.nodeEditMode = "node"
				}
				return m, textinput.Blink
			}
		case "d":
			if m.section == credSectionGlobal {
				row := credRows[m.cursor]
				if err := gkr.Delete(row.key); err == nil {
					m.notice = "deleted " + row.displayName
				} else {
					m.notice = "not in keychain"
				}
			} else if m.cursor < len(m.nodeCreds) {
				nc := m.nodeCreds[m.cursor]
				gkr.Delete(nc.KeyringKey) //nolint:errcheck
				if m.database != nil {
					m.database.DeleteNodeSSHCred(nc.Node) //nolint:errcheck
					m.nodeCreds, _ = m.database.NodeSSHCreds()
				}
				m.notice = "deleted SSH credentials for " + nc.Node
				if m.cursor >= len(m.nodeCreds) && m.cursor > 0 {
					m.cursor--
				}
			}
		}
	}

	// Handle the "node name" input step (special case for adding new node)
	if m.editing && m.nodeEditMode == "node" {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				node := strings.TrimSpace(m.input.Value())
				if node == "" {
					m.editing = false
					m.nodeEditMode = ""
					return m, nil
				}
				m.nodeEditNode = node
				ti := textinput.New()
				ti.Placeholder = fmt.Sprintf("SSH user for %s (blank=root)", node)
				ti.CharLimit = 64
				ti.Focus()
				m.input = ti
				m.nodeEditMode = "user"
				return m, textinput.Blink
			case "esc":
				m.editing = false
				m.nodeEditMode = ""
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m CredentialsModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render("credentials & keys") + "\n\n")

	sb.WriteString(styleDim.Render(`  All secrets are stored in the macOS Keychain — never written to disk.`) + "\n")
	sb.WriteString(styleDim.Render(`  Config file values act as fallback only; keychain entries take priority.`) + "\n")
	sb.WriteString(styleDim.Render("  Global SSH user: ") +
		styleBold.Render(m.globals.SSHUser))
	if m.globals.Domain != "" {
		sb.WriteString(styleDim.Render("   Domain: ") + styleBold.Render(m.globals.Domain))
	}
	sb.WriteString("\n\n")

	// Section tabs
	gs := " Global credentials "
	ns := " Per-node SSH "
	if m.section == credSectionGlobal {
		sb.WriteString(styleTabActive.Render("▸"+gs) + styleDim.Render("│") + styleTabInactive.Render(" "+ns))
	} else {
		sb.WriteString(styleTabInactive.Render(" "+gs) + styleDim.Render("│") + styleTabActive.Render("▸"+ns))
	}
	sb.WriteString(styleDim.Render("  ←/→ switch") + "\n\n")

	if m.section == credSectionGlobal {
		m.viewGlobal(&sb)
	} else {
		m.viewNodes(&sb)
	}

	sb.WriteString("\n")

	if m.editing {
		prompt := "  " + m.input.View() + "\n\n"
		switch m.nodeEditMode {
		case "node":
			prompt = "  Node name: " + m.input.View() + "\n\n"
		case "user":
			prompt = "  SSH user:   " + m.input.View() + "\n\n"
		case "pass":
			prompt = "  SSH pass:   " + m.input.View() + "\n\n"
		}
		sb.WriteString(prompt)
		sb.WriteString(styleHelp.Render("  enter save  esc cancel") + "\n")
	} else {
		if m.err != nil {
			sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		} else if m.notice != "" {
			sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
		} else {
			sb.WriteString("\n\n")
		}
		if m.section == credSectionGlobal {
			sb.WriteString(styleHelp.Render("  enter edit  d delete  ←/→ switch section  b back") + "\n")
		} else {
			sb.WriteString(styleHelp.Render("  enter add/edit  d delete  ←/→ switch section  b back") + "\n")
		}
	}

	return sb.String()
}

func (m CredentialsModel) viewGlobal(sb *strings.Builder) {
	header := fmt.Sprintf("  %-28s  %s", "CREDENTIAL", "KEYCHAIN")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 44))) + "\n")

	for i, row := range credRows {
		stored := styleDim.Render("not set")
		if gkr.IsSet(row.key) {
			stored = styleRunning.Render("● set")
		}
		line := fmt.Sprintf("  %-28s  %s", row.displayName, stored)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}
}

func (m CredentialsModel) viewNodes(sb *strings.Builder) {
	sb.WriteString(styleDim.Render("  Per-node SSH overrides loaded from keychain before global SSH password.\n\n"))

	header := fmt.Sprintf("  %-20s  %-12s  %s", "NODE", "SSH USER", "KEYCHAIN")
	sb.WriteString(styleDim.Render(header) + "\n")
	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 44))) + "\n")

	for i, nc := range m.nodeCreds {
		stored := styleDim.Render("not set")
		if gkr.IsSet(nc.KeyringKey) {
			stored = styleRunning.Render("● set")
		}
		line := fmt.Sprintf("  %-20s  %-12s  %s", nc.Node, nc.SSHUser, stored)
		if m.section == credSectionNodes && i == m.cursor {
			sb.WriteString(styleSelected.Render(line) + "\n")
		} else {
			sb.WriteString(line + "\n")
		}
	}

	// "Add" row
	addLine := fmt.Sprintf("  %-20s", "[+ add node]")
	if m.section == credSectionNodes && m.cursor == len(m.nodeCreds) {
		sb.WriteString(styleSelected.Render(addLine) + "\n")
	} else {
		sb.WriteString(styleDim.Render(addLine) + "\n")
	}
}
