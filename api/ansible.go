package api

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// AnsibleRunner executes ansible-playbook commands.
type AnsibleRunner struct {
	binary    string // path to ansible-playbook binary
	inventory string // path to inventory file or host pattern
}

// NewAnsibleRunner creates a runner. Binary defaults to "ansible-playbook".
func NewAnsibleRunner(binary, inventory string) *AnsibleRunner {
	if binary == "" {
		binary = "ansible-playbook"
	}
	return &AnsibleRunner{binary: binary, inventory: inventory}
}

// AnsibleResult holds the output of a playbook run.
type AnsibleResult struct {
	Output string
	Err    error
}

// RunPlaybook executes a playbook against the configured inventory.
// playbook is a path to a .yml file. extraVars is passed as --extra-vars.
func (r *AnsibleRunner) RunPlaybook(ctx context.Context, playbook string, extraVars map[string]string) AnsibleResult {
	args := []string{playbook}
	if r.inventory != "" {
		args = append(args, "-i", r.inventory)
	}
	if len(extraVars) > 0 {
		var pairs []string
		for k, v := range extraVars {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--extra-vars", strings.Join(pairs, " "))
	}

	cmd := exec.CommandContext(ctx, r.binary, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return AnsibleResult{Output: buf.String(), Err: err}
}

// Ping runs ansible -m ping against a target to check connectivity.
func (r *AnsibleRunner) Ping(ctx context.Context, target string) AnsibleResult {
	args := []string{target, "-m", "ping"}
	if r.inventory != "" {
		args = append(args, "-i", r.inventory)
	}
	cmd := exec.CommandContext(ctx, "ansible", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return AnsibleResult{Output: buf.String(), Err: err}
}

// Available returns true if the ansible-playbook binary can be found.
func (r *AnsibleRunner) Available() bool {
	_, err := exec.LookPath(r.binary)
	return err == nil
}
