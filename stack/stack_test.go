package stack

import (
	"strings"
	"testing"
)

func TestBuiltinsRenderServarr(t *testing.T) {
	defs, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins: %v", err)
	}
	var servarr *Definition
	for i := range defs {
		if defs[i].Name == "servarr" {
			servarr = &defs[i]
		}
	}
	if servarr == nil {
		t.Fatal("servarr builtin missing")
	}
	if len(servarr.Services) != 7 {
		t.Fatalf("servarr has %d services, want 7", len(servarr.Services))
	}
	want := map[string]int{"prowlarr": 9696, "sonarr": 8989, "radarr": 7878, "lidarr": 8686, "whisparr": 6969, "qbittorrent": 8080, "bazarr": 6767}
	for _, svc := range servarr.Services {
		port, ok := want[svc.ID]
		if !ok {
			t.Fatalf("unexpected service %q", svc.ID)
		}
		if svc.Port != port {
			t.Fatalf("%s port = %d, want %d", svc.ID, svc.Port, port)
		}
	}
	compose := servarr.Compose()
	for _, want := range []string{"whisparr:", "ghcr.io/hotio/whisparr:v3", `"/mnt/media:/data"`, `"6969:6969"`,
		"lscr.io/linuxserver/bazarr:latest", `"6767:6767"`, `WEBUI_PORT: "8080"`} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose missing %q:\n%s", want, compose)
		}
	}
	sls := servarr.SLS()
	for _, want := range []string{
		"salt://argus/servarr/compose.yaml",
		`"/opt/argus/servarr/whisparr/config"`,
		"grep -qx 7",
		"argus.docker",
	} {
		if !strings.Contains(sls, want) {
			t.Fatalf("SLS missing %q:\n%s", want, sls)
		}
	}
}

func TestDecodeValidation(t *testing.T) {
	valid := `
name = "demo"
target = "minion1"
install_root = "/opt/demo"

[[service]]
id = "app"
image = "example/app:1"
port = 8080
`
	if _, err := Decode(valid); err != nil {
		t.Fatalf("valid stack rejected: %v", err)
	}

	cases := map[string]string{
		"unknown key":   valid + "\nunexpected = 1\n",
		"relative root": strings.Replace(valid, `install_root = "/opt/demo"`, `install_root = "opt/demo"`, 1),
		"bad port":      strings.Replace(valid, "port = 8080", "port = 70000", 1),
		"no image":      strings.Replace(valid, `image = "example/app:1"`, `image = ""`, 1),
		"traversal":     strings.Replace(valid, `install_root = "/opt/demo"`, `install_root = "/opt/../demo"`, 1),
		"duplicate":     valid + "\n[[service]]\nid = \"other\"\nimage = \"x\"\nport = 8080\n",
	}
	for name, input := range cases {
		if _, err := Decode(input); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestCommandAndConfigOwner(t *testing.T) {
	def, err := Decode(`
name = "agent"
target = "minion1"
install_root = "/opt/agent"

[[service]]
id = "app"
image = "example/app:1"
config_owner = "ubuntu:ubuntu"
user = "1000:1000"
command = ["gateway", "run"]
`)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	compose := def.Compose()
	if !strings.Contains(compose, `command: ["gateway", "run"]`) {
		t.Fatalf("compose missing command:\n%s", compose)
	}
	if !strings.Contains(compose, `user: "1000:1000"`) {
		t.Fatalf("compose missing user:\n%s", compose)
	}
	sls := def.SLS()
	for _, want := range []string{
		`- user: "ubuntu"`,
		`- group: "ubuntu"`,
		"- mode: 0700",
	} {
		if !strings.Contains(sls, want) {
			t.Fatalf("SLS missing %q:\n%s", want, sls)
		}
	}

	bad := strings.Replace(validStackForTest, `port = 8080`, "command = [\"a\\nb\"]", 1)
	if _, err := Decode(bad); err == nil {
		t.Fatal("multi-line command argument accepted")
	}
	bad = strings.Replace(validStackForTest, `port = 8080`, `config_owner = "root:wheel:x"`, 1)
	if _, err := Decode(bad); err == nil {
		t.Fatal("invalid config_owner accepted")
	}
}

const validStackForTest = `
name = "demo"
target = "minion1"
install_root = "/opt/demo"

[[service]]
id = "app"
image = "example/app:1"
port = 8080
`

func TestHyphenatedStackUsesFormulaSlug(t *testing.T) {
	def, err := Decode(`
name = "hermes-agent"
target = "minion1"
install_root = "/opt/agent"

[[service]]
id = "app"
image = "example/app:1"
`)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if def.SiteSlug() != "hermes_agent" {
		t.Fatalf("SiteSlug = %q", def.SiteSlug())
	}
	if !strings.Contains(def.SLS(), "salt://argus/hermes_agent/compose.yaml") {
		t.Fatalf("SLS must use the formula slug:\n%s", def.SLS())
	}
}

func TestComposeIsDeterministic(t *testing.T) {
	def := Definition{
		Name: "demo", InstallRoot: "/opt/demo", PUID: 1000, PGID: 1000, Timezone: "UTC",
		Services: []Service{
			{ID: "z", Image: "z:1", Port: 2, ConfigPath: "/opt/demo/z/config", ConfigTarget: "/config", Env: map[string]string{"B": "2", "A": "1"}},
			{ID: "a", Image: "a:1", Port: 1, ConfigPath: "/opt/demo/a/config", ConfigTarget: "/config"},
		},
	}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	first := def.Compose()
	for range 20 {
		if got := def.Compose(); got != first {
			t.Fatal("Compose output is not deterministic")
		}
	}
	if strings.Index(first, "  a:") > strings.Index(first, "  z:") {
		t.Fatal("services are not sorted by id")
	}
}
