package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
)

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// The provisioning screen is one large form; tab must always move focus
// instead of switching top-level sections.
func TestScreenCapturesTabProvision(t *testing.T) {
	a := App{screen: screenProvision}
	if !a.screenCapturesTab() {
		t.Fatal("provision screen must capture tab for field navigation")
	}
}

func TestScreenCapturesTabResizeForms(t *testing.T) {
	a := App{screen: screenResize}
	if a.screenCapturesTab() {
		t.Fatal("resize list view should allow section tab navigation")
	}
	a.resize.editMode = true
	if !a.screenCapturesTab() {
		t.Fatal("resize edit form must capture tab")
	}
	a.resize.editMode = false
	a.resize.diskMode = true
	if !a.screenCapturesTab() {
		t.Fatal("resize disk form must capture tab")
	}
	a.resize.diskMode = false
	a.resize.confirmKind = confirmResizeApply
	if !a.screenCapturesTab() {
		t.Fatal("resize confirmation must capture tab")
	}
}

func TestScreenCapturesTabOrchestrateConfirm(t *testing.T) {
	a := App{screen: screenOrchestrate}
	a.orch.confirm = &orchConfirm{action: "accept", minion: "dns-01"}
	if !a.screenCapturesTab() {
		t.Fatal("orchestrate confirmation must capture tab so it cannot be bypassed")
	}
}

func TestOrchestrateConfirmIgnoresUnrelatedKeys(t *testing.T) {
	m := OrchestrateModel{confirm: &orchConfirm{action: "accept", minion: "dns-01"}}
	for _, k := range []string{"tab", "up", "x"} {
		updated, _ := m.handleKey(keyMsg(k))
		if updated.confirm == nil {
			t.Fatalf("key %q must not dismiss the confirmation dialog", k)
		}
	}
	updated, _ := m.handleKey(keyMsg("esc"))
	if updated.confirm != nil {
		t.Fatal("esc should dismiss the confirmation dialog")
	}
}

func TestApplySaltProfileWithoutDatabase(t *testing.T) {
	failed := applySaltProfile(context.Background(), nil, nil, config.SaltConfig{}, "minion", "profile", 0)
	if len(failed) != 1 || !strings.Contains(failed[0], "database unavailable") {
		t.Fatalf("applySaltProfile() = %v, want database-unavailable error", failed)
	}
}

func TestProvisionQDoesNotQuit(t *testing.T) {
	m := NewProvisionModel(nil, api.Node{}, config.DefaultsConfig{}, nil, nil, "")
	updated, cmd := m.Update(keyMsg("q"))
	if !strings.HasSuffix(updated.inputs[provFieldTemplate].Value(), "q") {
		t.Fatal("q should be typed into the focused field")
	}
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("typing q in the provisioning form must not quit the app")
		}
	}
}

func TestHelpOverlayToggles(t *testing.T) {
	a := App{screen: screenNodes, width: 100, height: 40}
	updated, _ := a.Update(keyMsg("H"))
	a = updated.(App)
	if !a.helpVisible {
		t.Fatal("H should open the help overlay")
	}
	updated, _ = a.Update(keyMsg("esc"))
	a = updated.(App)
	if a.helpVisible {
		t.Fatal("esc should close the help overlay")
	}
}

func TestNodesAuthErrorShowsActionableHint(t *testing.T) {
	m := NodesModel{err: errString("proxmox: 401 authentication failure"), width: 100, height: 40}
	view := m.View()
	if !strings.Contains(view, "argus config") {
		t.Fatalf("auth error view should mention `argus config`, got:\n%s", view)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
