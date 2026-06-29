package ui

import (
	"fmt"
	"testing"
)

// TestDetailModel_SnapDeleteFlagIndependent verifies that snapshotDeletedMsg
// only clears snapDeleting and does not touch the acting flag, so concurrent
// guest power actions and snapshot deletes don't interfere.
func TestDetailModel_SnapDeleteFlagIndependent(t *testing.T) {
	// snapshotDeletedMsg (error path) must clear snapDeleting only.
	m := DetailModel{acting: true, snapDeleting: true}
	m2, _ := m.Update(snapshotDeletedMsg{err: fmt.Errorf("simulated error")})
	if m2.snapDeleting {
		t.Error("snapDeleting should be false after snapshotDeletedMsg")
	}
	if !m2.acting {
		t.Error("acting must remain true: snapshotDeletedMsg should not clear it")
	}
}

// TestDetailModel_ActionDoneFlagIndependent verifies that actionDoneMsg
// only clears acting and does not touch the snapDeleting flag.
func TestDetailModel_ActionDoneFlagIndependent(t *testing.T) {
	m := DetailModel{acting: true, snapDeleting: true}
	m2, _ := m.Update(actionDoneMsg{err: fmt.Errorf("simulated error")})
	if m2.acting {
		t.Error("acting should be false after actionDoneMsg")
	}
	if !m2.snapDeleting {
		t.Error("snapDeleting must remain true: actionDoneMsg should not clear it")
	}
}

// TestDetailModel_BothFlagsCanClearIndependently verifies the full concurrent lifecycle:
// both flags start true, each clears only when its own message arrives.
func TestDetailModel_BothFlagsCanClearIndependently(t *testing.T) {
	m := DetailModel{acting: true, snapDeleting: true}

	// Snapshot finishes first (error path avoids needing client for fetchSnapshotsCmd).
	m, _ = m.Update(snapshotDeletedMsg{err: fmt.Errorf("err")})
	if m.snapDeleting {
		t.Error("snapDeleting should be false")
	}
	if !m.acting {
		t.Error("acting should still be true")
	}

	// Guest action finishes.
	m, _ = m.Update(actionDoneMsg{err: fmt.Errorf("err")})
	if m.acting {
		t.Error("acting should be false")
	}
	if m.snapDeleting {
		t.Error("snapDeleting should still be false")
	}
}
