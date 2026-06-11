package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/ratataskr/api"
)

// ansibleRunDoneMsg is sent when an ansible-playbook run completes.
type ansibleRunDoneMsg struct {
	output string
	err    error
}

// AnsibleModel is the Ansible playbook runner screen.
type AnsibleModel struct {
	runner *api.AnsibleRunner

	playbookInput textinput.Model
	extraVars     textinput.Model
	targetInput   textinput.Model
	focused       int

	running  bool
	output   string
	err      error
	spinner  spinner.Model
	width    int
	height   int
}

func NewAnsibleModel(runner *api.AnsibleRunner) AnsibleModel {
	mk := func(ph string, limit int) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.CharLimit = limit
		return ti
	}
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := AnsibleModel{
		runner:        runner,
		playbookInput: mk("/path/to/playbook.yml", 256),
		extraVars:     mk("key=value key2=value2 (optional)", 256),
		targetInput:   mk("host or group (optional, overrides inventory)", 128),
		spinner:       sp,
	}
	m.playbookInput.Focus()
	return m
}

func (m AnsibleModel) Init() tea.Cmd { return m.spinner.Tick }

func (m AnsibleModel) Update(msg tea.Msg) (AnsibleModel, tea.Cmd) {
	switch msg := msg.(type) {
	case ansibleRunDoneMsg:
		m.running = false
		m.output = msg.output
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
		}
		return m, nil

	case spinner.TickMsg:
		if m.running {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m AnsibleModel) handleKey(msg tea.KeyMsg) (AnsibleModel, tea.Cmd) {
	inputs := []*textinput.Model{&m.playbookInput, &m.extraVars, &m.targetInput}
	key := msg.String()

	switch key {
	case "b", "esc":
		if !m.running {
			return m, func() tea.Msg { return backMsg{} }
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		inputs[m.focused].Blur()
		m.focused = (m.focused + 1) % len(inputs)
		inputs[m.focused].Focus()
		return m, nil
	case "shift+tab":
		inputs[m.focused].Blur()
		m.focused = (m.focused - 1 + len(inputs)) % len(inputs)
		inputs[m.focused].Focus()
		return m, nil
	case "enter":
		if !m.running {
			playbook := strings.TrimSpace(m.playbookInput.Value())
			if playbook == "" {
				m.err = fmt.Errorf("playbook path required")
				return m, nil
			}
			m.running = true
			m.output = ""
			m.err = nil
			return m, m.runCmd(playbook)
		}
	default:
		var cmd tea.Cmd
		switch m.focused {
		case 0:
			m.playbookInput, cmd = m.playbookInput.Update(msg)
		case 1:
			m.extraVars, cmd = m.extraVars.Update(msg)
		case 2:
			m.targetInput, cmd = m.targetInput.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m AnsibleModel) runCmd(playbook string) tea.Cmd {
	runner := m.runner
	extraVarsStr := strings.TrimSpace(m.extraVars.Value())

	var extraVarsMap map[string]string
	if extraVarsStr != "" {
		extraVarsMap = make(map[string]string)
		for _, pair := range strings.Fields(extraVarsStr) {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				extraVarsMap[parts[0]] = parts[1]
			}
		}
	}

	return func() tea.Msg {
		if runner == nil || !runner.Available() {
			return ansibleRunDoneMsg{err: fmt.Errorf("ansible-playbook not found in PATH")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res := runner.RunPlaybook(ctx, playbook, extraVarsMap)
		return ansibleRunDoneMsg{output: res.Output, err: res.Err}
	}
}

func (m AnsibleModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleTitle.Render("  Ansible") + "\n")

	available := m.runner != nil && m.runner.Available()
	if !available {
		sb.WriteString(styleDim.Render("\n  ansible-playbook not found in PATH.\n"))
		sb.WriteString(styleDim.Render("  Install Ansible: pip install ansible\n\n"))
	} else {
		sb.WriteString(styleDim.Render("  Run ansible-playbook against your inventory\n"))
	}
	sb.WriteString("\n")

	fields := []struct {
		label string
		ti    textinput.Model
	}{
		{"  Playbook   ", m.playbookInput},
		{"  Extra vars ", m.extraVars},
		{"  Target     ", m.targetInput},
	}
	for i, f := range fields {
		lbl := f.label
		if i == m.focused {
			sb.WriteString(styleCyan.Render(lbl) + f.ti.View() + "\n")
		} else {
			sb.WriteString(styleDim.Render(lbl) + f.ti.View() + "\n")
		}
	}

	sb.WriteString("\n")
	switch {
	case m.running:
		sb.WriteString("  " + m.spinner.View() + styleDim.Render(" running ansible-playbook…") + "\n")
	case m.err != nil:
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n")
		if m.output != "" {
			sb.WriteString("\n" + styleDim.Render("  Output:\n"))
			for _, l := range strings.Split(m.output, "\n") {
				sb.WriteString("  " + truncate(l, m.width-4) + "\n")
			}
		}
	case m.output != "":
		sb.WriteString(styleRunning.Render("  ✓ Playbook completed") + "\n\n")
		sb.WriteString(styleDim.Render("  Output:\n"))
		lines := strings.Split(m.output, "\n")
		visible := m.height - 16
		if visible < 5 {
			visible = 5
		}
		if len(lines) > visible {
			lines = lines[len(lines)-visible:]
		}
		for _, l := range lines {
			sb.WriteString("  " + truncate(l, m.width-4) + "\n")
		}
	}

	sb.WriteString("\n" + styleDim.Render("  tab advance · enter run · b back · q quit"))
	return sb.String()
}
