// Package bootstrap installs and reconciles the Salt-based platform layer on
// a Proxmox host. It is deliberately separate from the TUI so the same plan
// can be run from tests, CI, or `argus bootstrap`.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a shell script on the target host.
type Runner interface {
	Run(ctx context.Context, script string) (Result, error)
	Probe(ctx context.Context) error
}

// Result captures remote command output.
type Result struct {
	Stdout string
	Stderr string
}

// SSHRunner runs scripts through the system OpenSSH client. Using the system
// client keeps key/agent support on macOS/Linux without adding a second SSH
// implementation to argus.
type SSHRunner struct {
	Host     string
	User     string
	Port     int
	KeyPath  string
	Password string
	Timeout  time.Duration
}

// NewSSHRunner validates the target and returns a runner.
func NewSSHRunner(host, user, keyPath, password string) (*SSHRunner, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("ssh host is required")
	}
	if strings.ContainsAny(host, " \t\n;|&$`") {
		return nil, fmt.Errorf("invalid ssh host %q", host)
	}
	if user == "" {
		user = "root"
	}
	return &SSHRunner{
		Host: host, User: user, Port: 22, KeyPath: keyPath, Password: password,
		Timeout: 30 * time.Minute,
	}, nil
}

func (r *SSHRunner) args() []string {
	args := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-p", fmt.Sprintf("%d", r.Port),
	}
	if r.KeyPath != "" {
		args = append(args, "-i", r.KeyPath)
	}
	args = append(args, r.User+"@"+r.Host, "bash -s")
	return args
}

// Probe verifies that the host is reachable and root/sudo access works.
func (r *SSHRunner) Probe(ctx context.Context) error {
	_, err := r.Run(ctx, "id -u >/dev/null && hostname")
	return err
}

// Run executes script on the remote host. Scripts are sent on stdin, never
// interpolated into a shell argument.
func (r *SSHRunner) Run(ctx context.Context, script string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	var cmd *exec.Cmd
	if r.Password != "" {
		cmd = exec.CommandContext(ctx, "sshpass", append([]string{"-e", "ssh"}, r.args()...)...)
		cmd.Env = append(os.Environ(), "SSHPASS="+r.Password)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", r.args()...)
	}
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = err.Error()
		}
		return res, fmt.Errorf("ssh %s: %s", r.Host, msg)
	}
	return res, nil
}
