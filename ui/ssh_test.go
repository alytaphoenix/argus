package ui

import (
	"strings"
	"testing"
)

func TestSSHCommandKeepsPasswordOutOfArgv(t *testing.T) {
	cmd := sshCommand("root", "10.0.0.5", "hunter2")
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "hunter2") {
		t.Fatalf("password leaked into argv: %q", joined)
	}
	if !strings.Contains(joined, "sshpass") || !strings.Contains(joined, "-e") {
		t.Fatalf("expected sshpass -e, got %q", joined)
	}
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("expected accept-new host key policy, got %q", joined)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "SSHPASS=hunter2" {
			found = true
		}
	}
	if !found {
		t.Fatal("password was not passed via SSHPASS")
	}
}

func TestSSHCommandWithoutPassword(t *testing.T) {
	cmd := sshCommand("root", "10.0.0.5", "")
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "sshpass") {
		t.Fatalf("unexpected sshpass with key/agent auth: %q", joined)
	}
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("expected accept-new host key policy, got %q", joined)
	}
}
