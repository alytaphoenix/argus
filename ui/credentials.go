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
	credSectionVMKey
)

type CredentialsModel struct {
	cursor    int
	section   credSection
	editing   bool
	input     textinput.Model
	notice    string
	err       error
	width     int
	height    int
	database  *db.DB
	cfg       config.Config
	nodeCreds []db.NodeSSHCred
	// per-node edit state
	nodeEditMode string // "node" | "user" | "pass" | ""
	nodeEditNode string
	nodeEditUser string

	// delete confirmation state
	confirmDelete       bool
	confirmDeleteGlobal bool // true = credRow delete, false = node SSH cred delete
	confirmDeleteRow    credRow
	confirmDeleteNode   db.NodeSSHCred

	// VM cloud-init key state (handled in credentials_vm_key.go)
	vmKeySaving  bool
	confirmVMKey string // "clear" | "rotate" | ""
}

func NewCredentialsModel(database *db.DB, cfg config.Config) CredentialsModel {
	m := CredentialsModel{database: database, cfg: cfg}
	if database != nil {
		m.nodeCreds, _ = database.NodeSSHCreds()
	}
	return m
}

func (m CredentialsModel) Init() tea.Cmd { return nil }

// armDeleteConfirm opens the shared confirmation dialog for a keychain or DB
// delete. confirmVMKey is always reset: one dialog answers every delete, so a
// rotate/clear that was armed earlier and cancelled would otherwise run in
// place of the row the operator just selected.
func (m *CredentialsModel) armDeleteConfirm(global bool, row credRow, node db.NodeSSHCred) {
	m.confirmVMKey = ""
	m.confirmDeleteGlobal = global
	m.confirmDeleteRow = row
	m.confirmDeleteNode = node
	m.confirmDelete = true
}

func (m CredentialsModel) Update(msg tea.Msg) (CredentialsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y", "enter":
				m.confirmDelete = false
				if m.confirmVMKey != "" {
					action := m.confirmVMKey
					m.confirmVMKey = ""
					return m.runVMKeyConfirm(action)
				}
				if m.confirmDeleteGlobal {
					row := m.confirmDeleteRow
					if err := gkr.Delete(row.key); err != nil {
						m.err = err
					} else {
						m.err = nil
						m.notice = "deleted " + row.displayName
					}
				} else {
					nc := m.confirmDeleteNode
					var delErr error
					if err := gkr.Delete(nc.KeyringKey); err != nil {
						delErr = err
					}
					if m.database != nil {
						if err := m.database.DeleteNodeSSHCred(nc.Node); err != nil && delErr == nil {
							delErr = err
						}
						m.nodeCreds, _ = m.database.NodeSSHCreds()
					}
					if delErr != nil {
						m.err = delErr
					} else {
						m.err = nil
						m.notice = "deleted SSH credentials for " + nc.Node
					}
					if m.cursor >= len(m.nodeCreds) && m.cursor > 0 {
						m.cursor--
					}
				}
			default:
				m.confirmDelete = false
				m.confirmVMKey = ""
			}
			return m, nil
		}

		if m.editing {
			switch msg.String() {
			case "enter":
				val := m.input.Value()
				if m.section == credSectionVMKey {
					return m.finishVMKeyImport(val)
				}
				if m.section == credSectionGlobal {
					row := credRows[m.cursor]
					if val == "" {
						// Blank submits as a delete request — route through
						// the same confirm dialog as the "d" key so an
						// accidental blank submit can't erase a secret.
						if gkr.IsSet(row.key) {
							m.armDeleteConfirm(true, row, db.NodeSSHCred{})
						} else {
							m.notice = "nothing to delete"
						}
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
							// Blank submits as a delete request — same confirm
							// gate as the "d" key.
							if gkr.IsSet(keyringKey) {
								m.armDeleteConfirm(false, credRow{},
									db.NodeSSHCred{Node: node, SSHUser: user, KeyringKey: keyringKey})
							} else {
								m.notice = "nothing to delete"
							}
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

		// The VM cloud-init section owns its keys; the shared confirmation and
		// paste-input states above still apply to it.
		if m.section == credSectionVMKey {
			return m.updateVMKey(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "b":
			return m, func() tea.Msg { return backMsg{} }
		case "left", "right", "h", "l":
			switch m.section {
			case credSectionGlobal:
				m.section = credSectionNodes
			case credSectionNodes:
				m.section = credSectionVMKey
			default:
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
			m.notice, m.err = "", nil
			if m.section == credSectionGlobal {
				row := credRows[m.cursor]
				if !gkr.IsSet(row.key) {
					m.notice = "not in keychain"
				} else {
					m.armDeleteConfirm(true, row, db.NodeSSHCred{})
				}
			} else if m.cursor < len(m.nodeCreds) {
				m.armDeleteConfirm(false, credRow{}, m.nodeCreds[m.cursor])
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

	sb.WriteString(styleDim.Render(`  Keychain-backed secrets are not written to the config file.`) + "\n")
	sb.WriteString(styleDim.Render(`  Cloud-init passwords and Servarr API keys live in the local DB/config (0600).`) + "\n")
	sb.WriteString(styleDim.Render(`  Protect backups: the SQLite DB can contain cloud-init secrets.`) + "\n")
	sb.WriteString(styleDim.Render("  Global SSH user: ") +
		styleBold.Render(m.cfg.Globals.SSHUser))
	if m.cfg.Globals.Domain != "" {
		sb.WriteString(styleDim.Render("   Domain: ") + styleBold.Render(m.cfg.Globals.Domain))
	}
	sb.WriteString("\n\n")

	// Section tabs
	tabs := []string{" Global credentials ", " Per-node SSH ", " VM cloud-init "}
	for i, label := range tabs {
		if i > 0 {
			sb.WriteString(styleDim.Render("│"))
		}
		if credSection(i) == m.section {
			sb.WriteString(styleTabActive.Render("▸" + label))
		} else {
			sb.WriteString(styleTabInactive.Render(" " + label))
		}
	}
	sb.WriteString(styleDim.Render("  ←/→ switch") + "\n\n")

	switch m.section {
	case credSectionGlobal:
		m.viewGlobal(&sb)
	case credSectionNodes:
		m.viewNodes(&sb)
	default:
		m.viewVMKey(&sb)
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
		if m.section == credSectionVMKey {
			prompt = "  SSH public key: " + m.input.View() + "\n\n"
		}
		sb.WriteString(prompt)
		sb.WriteString(styleHelp.Render("  enter save  esc cancel") + "\n")
	} else if m.confirmDelete {
		var label string
		switch {
		case m.confirmVMKey == vmKeyConfirmClear:
			label = "Clear the default cloud-init SSH public key? New VMs get no key until one is set. [Y/n]"
		case m.confirmVMKey == vmKeyConfirmRotate:
			label = "Generate a NEW keypair at the managed path, replacing the existing one? " +
				"Only future VMs change; existing VMs keep the old key. [Y/n]"
		case m.confirmDeleteGlobal:
			label = fmt.Sprintf("Delete %q from Keychain? [Y/n]", m.confirmDeleteRow.displayName)
		default:
			label = fmt.Sprintf("Delete SSH credentials for %q? [Y/n]", m.confirmDeleteNode.Node)
		}
		sb.WriteString(styleError.Render("  "+label) + "\n\n")
		sb.WriteString(styleHelp.Render("  Y confirm  n cancel") + "\n")
	} else {
		if m.err != nil {
			sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
		} else if m.notice != "" {
			sb.WriteString(styleRunning.Render("  ✓ "+m.notice) + "\n\n")
		} else {
			sb.WriteString("\n\n")
		}
		switch m.section {
		case credSectionGlobal:
			sb.WriteString(styleHelp.Render("  enter edit  d delete  ←/→ switch section  b back") + "\n")
		case credSectionNodes:
			sb.WriteString(styleHelp.Render("  enter add/edit  d delete  ←/→ switch section  b back") + "\n")
		default:
			sb.WriteString(styleHelp.Render("  enter/i paste key  g generate  d clear  ←/→ switch section  b back") + "\n")
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
	sb.WriteString(styleDim.Render("  Per-node SSH overrides loaded from keychain before global SSH password.") + "\n\n")

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
