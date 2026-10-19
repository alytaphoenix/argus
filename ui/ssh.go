package ui

import (
	"os"
	"os/exec"
)

// sshCommand builds an interactive SSH command.
//
// Two security properties matter here:
//   - the password is passed via sshpass's SSH ASKPASS environment variable
//     (sshpass -e) instead of argv, so it does not show up in `ps` output;
//   - host-key checking uses accept-new rather than "no", so a changed key for
//     an already-known host is still rejected.
func sshCommand(user, host, password string) *exec.Cmd {
	target := user + "@" + host
	args := []string{"-o", "StrictHostKeyChecking=accept-new", target}
	if password == "" {
		return exec.Command("ssh", args...)
	}
	cmd := exec.Command("sshpass", append([]string{"-e", "ssh"}, args...)...)
	cmd.Env = append(os.Environ(), "SSHPASS="+password)
	return cmd
}
