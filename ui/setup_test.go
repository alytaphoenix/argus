package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func setupKey(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "ctrl+t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}
	}
	t := tea.KeyMsg{}
	return t
}

func TestSetupFocusCycling(t *testing.T) {
	m := NewSetupModel("")
	if m.focus != setupFieldHost {
		t.Fatalf("initial focus = %d, want host", m.focus)
	}
	next, _ := m.Update(setupKey("tab"))
	m = next.(SetupModel)
	if m.focus != setupFieldUsername {
		t.Fatalf("focus after tab = %d, want username", m.focus)
	}
	next, _ = m.Update(setupKey("shift+tab"))
	m = next.(SetupModel)
	if m.focus != setupFieldHost {
		t.Fatalf("focus after shift+tab = %d, want host", m.focus)
	}
}

func TestSetupEnterAdvancesFields(t *testing.T) {
	m := NewSetupModel("")
	next, _ := m.Update(setupKey("enter"))
	m = next.(SetupModel)
	if m.focus != setupFieldUsername {
		t.Fatalf("focus = %d, want username after enter on host", m.focus)
	}
}

func TestSetupRequiresAllFields(t *testing.T) {
	m := NewSetupModel("")
	m = m.setFocus(setupFieldPassword)
	next, cmd := m.Update(setupKey("enter"))
	m = next.(SetupModel)
	if cmd != nil {
		t.Error("expected no connect cmd with empty fields")
	}
	if m.errMsg == "" {
		t.Error("expected validation error with empty fields")
	}
	if m.testing {
		t.Error("should not enter testing state with empty fields")
	}
}

func TestSetupConnectFiresWithAllFields(t *testing.T) {
	m := NewSetupModel("")
	m.inputs[setupFieldHost].SetValue("pve.test:8006")
	m.inputs[setupFieldPassword].SetValue("hunter2")
	m = m.setFocus(setupFieldPassword)
	next, cmd := m.Update(setupKey("enter"))
	m = next.(SetupModel)
	if cmd == nil {
		t.Fatal("expected connect cmd")
	}
	if !m.testing {
		t.Error("expected testing state")
	}
}

func TestSetupInsecureToggle(t *testing.T) {
	m := NewSetupModel("")
	if !m.insecure {
		t.Fatal("insecure should default to true for homelab self-signed certs")
	}
	next, _ := m.Update(setupKey("ctrl+t"))
	m = next.(SetupModel)
	if m.insecure {
		t.Error("ctrl+t should toggle insecure off")
	}
}

func TestSetupResultError(t *testing.T) {
	m := NewSetupModel("")
	m.testing = true
	next, _ := m.Update(setupResultMsg{err: errors.New("boom")})
	m = next.(SetupModel)
	if m.testing {
		t.Error("testing should clear on result")
	}
	if m.errMsg == "" {
		t.Error("expected error message")
	}
	if m.Done() {
		t.Error("should not be done on error")
	}
}

func TestSetupResultSuccess(t *testing.T) {
	m := NewSetupModel("")
	m.testing = true
	next, cmd := m.Update(setupResultMsg{})
	m = next.(SetupModel)
	if !m.Done() {
		t.Error("expected done on success")
	}
	if cmd == nil {
		t.Error("expected quit cmd")
	}
}
