package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	gkr "github.com/alytaphoenix/argus/keyring"
)

const (
	setupFieldHost = iota
	setupFieldUsername
	setupFieldPassword
	setupFieldCount
)

type SetupModel struct {
	inputs      [setupFieldCount]textinput.Model
	focus       int
	insecure    bool
	testing     bool
	cancelTest  context.CancelFunc
	confirmQuit bool // set after a first esc with typed input; second esc quits
	done        bool
	errMsg      string
	reason      string
	width       int
	height      int
}

type setupResultMsg struct{ err error }

func NewSetupModel(reason string) SetupModel {
	m := SetupModel{insecure: true, reason: reason}

	host := textinput.New()
	host.Placeholder = "https://pve.example.com:8006"
	host.CharLimit = 256
	host.Width = 48
	host.Focus()
	m.inputs[setupFieldHost] = host

	user := textinput.New()
	user.Placeholder = "root@pam"
	user.SetValue("root@pam")
	user.CharLimit = 128
	user.Width = 48
	m.inputs[setupFieldUsername] = user

	pass := textinput.New()
	pass.Placeholder = "password"
	pass.EchoMode = textinput.EchoPassword
	pass.CharLimit = 256
	pass.Width = 48
	m.inputs[setupFieldPassword] = pass

	return m
}

// Done reports whether setup completed and wrote a working config.
func (m SetupModel) Done() bool { return m.done }

func (m SetupModel) Init() tea.Cmd { return textinput.Blink }

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case setupResultMsg:
		m.testing = false
		if m.cancelTest != nil {
			m.cancelTest()
			m.cancelTest = nil
		}
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.done = true
		return m, tea.Quit

	case tea.KeyMsg:
		if m.testing {
			switch msg.String() {
			case "ctrl+c":
				if m.cancelTest != nil {
					m.cancelTest()
				}
				return m, tea.Quit
			case "esc":
				if m.cancelTest != nil {
					m.cancelTest()
				}
				m.testing = false
				m.cancelTest = nil
				m.errMsg = "connection test cancelled"
			}
			return m, nil
		}
		if msg.String() != "esc" {
			m.confirmQuit = false
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			host := strings.TrimSpace(m.inputs[setupFieldHost].Value())
			pass := m.inputs[setupFieldPassword].Value()
			if !m.confirmQuit && (host != "" || pass != "") {
				m.confirmQuit = true
				m.errMsg = "press esc again to quit and discard input"
				return m, nil
			}
			return m, tea.Quit
		case "tab", "down":
			return m.setFocus((m.focus + 1) % setupFieldCount), nil
		case "shift+tab", "up":
			return m.setFocus((m.focus + setupFieldCount - 1) % setupFieldCount), nil
		case "ctrl+t":
			m.insecure = !m.insecure
			return m, nil
		case "enter":
			if m.focus < setupFieldPassword {
				return m.setFocus(m.focus + 1), nil
			}
			host := strings.TrimSpace(m.inputs[setupFieldHost].Value())
			username := strings.TrimSpace(m.inputs[setupFieldUsername].Value())
			password := m.inputs[setupFieldPassword].Value()
			if host == "" || username == "" || password == "" {
				m.errMsg = "all fields are required"
				return m, nil
			}
			if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
				host = "https://" + host
			}
			m.errMsg = ""
			m.testing = true
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			m.cancelTest = cancel
			return m, setupTestCmd(ctx, host, username, password, m.insecure)
		}
	}

	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m SetupModel) setFocus(i int) SetupModel {
	m.inputs[m.focus].Blur()
	m.focus = i
	m.inputs[m.focus].Focus()
	return m
}

// setupTestCmd tests the Proxmox connection. ctx is owned by the caller
// (SetupModel.cancelTest), which cancels it either when the user presses esc
// to abort the test or once a result arrives — so no explicit cancel() call
// is needed here.
func setupTestCmd(ctx context.Context, host, username, password string, insecure bool) tea.Cmd {
	return func() tea.Msg {
		cfg := &config.Config{Host: host, Username: username, Password: password, Insecure: insecure}
		client := api.NewClient(cfg)
		if _, err := client.Nodes(ctx); err != nil {
			if ctx.Err() != nil {
				return nil // cancelled by the user — SetupModel already updated its own state
			}
			return setupResultMsg{err: fmt.Errorf("connection failed: %w", err)}
		}
		if err := gkr.Save(gkr.KeyProxmoxPassword, password); err != nil {
			return setupResultMsg{err: fmt.Errorf("keychain: %w", err)}
		}
		if err := config.SaveProxmoxSetup(host, username, insecure); err != nil {
			return setupResultMsg{err: fmt.Errorf("write config: %w", err)}
		}
		return setupResultMsg{}
	}
}

func (m SetupModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("argus setup") + "\n\n")
	b.WriteString(styleDim.Render("Proxmox is not configured. Enter the connection details below;") + "\n")
	b.WriteString(styleDim.Render("everything else is discovered from the cluster at runtime.") + "\n")
	if m.reason != "" {
		b.WriteString(styleDim.Render("("+m.reason+")") + "\n")
	}
	b.WriteString("\n")

	labels := [setupFieldCount]string{"Host    ", "Username", "Password"}
	for i, in := range m.inputs {
		label := labels[i]
		if i == m.focus {
			label = styleCyan.Render(label)
		} else {
			label = styleSubtle.Render(label)
		}
		b.WriteString("  " + label + " " + in.View() + "\n")
	}

	verify := styleRunning.Render("on")
	if m.insecure {
		verify = stylePaused.Render("off (self-signed certs accepted)")
	}
	b.WriteString("\n  " + styleSubtle.Render("TLS verify") + " " + verify + "\n\n")

	switch {
	case m.testing:
		b.WriteString("  " + stylePaused.Render("connecting to "+strings.TrimSpace(m.inputs[setupFieldHost].Value())+" ... (esc to cancel)") + "\n")
	case m.errMsg != "":
		b.WriteString("  " + styleError.Render(m.errMsg) + "\n")
	}

	help := "enter: next/connect · tab: switch field · ctrl+t: toggle TLS verify · esc: quit"
	if m.testing {
		help = "esc: cancel test · ctrl+c: quit"
	}
	b.WriteString("\n" + styleHelp.Render(help))
	return b.String()
}

// RunSetup shows the first-run Proxmox setup screen. It returns true when a
// working config was written, false when the user aborted.
func RunSetup(reason string) (bool, error) {
	p := tea.NewProgram(NewSetupModel(reason), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return false, err
	}
	sm, ok := final.(SetupModel)
	return ok && sm.Done(), nil
}
