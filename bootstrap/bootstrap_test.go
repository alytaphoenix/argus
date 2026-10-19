package bootstrap

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/alytaphoenix/argus/formula"
)

func TestSaltInstallScriptIsValidShell(t *testing.T) {
	script, err := SaltInstallScript(SaltInstallOptions{
		MasterID: "proxmox", APIUser: "saltapi", APIPort: 8000,
		HostIP: "10.0.0.42", Interface: "0.0.0.0",
		InstallMinion: true, UsePVECA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ARGUS_SALT_MASTER=ok",
		"ARGUS_SALT_API=ok",
		"external_auth:",
		"DNS:salt-api",
		"/etc/argus/salt-api-password",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(script, "API_PASSWORD="+"hunter2") {
		t.Fatal("script contains a static password")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestSaltInstallOptionsValidate(t *testing.T) {
	bad := []SaltInstallOptions{
		{MasterID: "bad id"},
		{MasterID: "ok", APIUser: "bad user"},
		{MasterID: "ok", APIPort: 70000},
		{MasterID: "ok", HostIP: "not-an-ip"},
		{MasterID: "ok", Interface: "eth0"},
	}
	for _, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", o)
		}
	}
}

func TestFormulaFiles(t *testing.T) {
	r, err := formula.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := r.Resolve([]string{"base", "docker", "technitium"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := FormulaFiles(fs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["argus/base/init.sls"]; !ok {
		t.Fatalf("missing base init.sls: %v", keys(files))
	}
	if _, ok := files["argus/technitium/compose.yaml"]; !ok {
		t.Fatalf("missing technitium compose: %v", keys(files))
	}
	script := WriteFormulaScript(files)
	if !strings.Contains(script, "base64 -d") || !strings.Contains(script, "/srv/salt/argus/base/init.sls") {
		t.Fatalf("unexpected write script:\n%s", script)
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestSSHArgsDoNotContainPassword(t *testing.T) {
	r, err := NewSSHRunner("10.0.0.5", "root", "/tmp/key", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.args(), " ")
	if strings.Contains(joined, "hunter2") {
		t.Fatalf("password leaked into ssh args: %s", joined)
	}
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("missing host-key policy: %s", joined)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
