package formula

import (
	"testing"

	"github.com/alytaphoenix/argus/stack"
)

func TestFromStackTargetsMinion(t *testing.T) {
	def := stack.Definition{
		Name: "demo", Target: "media01", InstallRoot: "/opt/demo",
		Services: []stack.Service{{ID: "app", Image: "example/app:1", Port: 8080, HealthPath: "/ping"}},
	}
	f, err := FromStack(def)
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsStack() {
		t.Fatal("formula is not marked as a stack")
	}
	if f.TargetMinion != "media01" {
		t.Fatalf("target minion = %q", f.TargetMinion)
	}
	if f.StateName() != "argus.demo" {
		t.Fatalf("state = %q", f.StateName())
	}
	if f.Files["compose.yaml"] == "" || f.Files["init.sls"] == "" {
		t.Fatal("rendered files missing")
	}
	if f.Health == nil || f.Health.TCP != "127.0.0.1:8080" || f.Health.Path != "/ping" {
		t.Fatalf("health = %+v", f.Health)
	}
}

func TestWithStacksOverridesBuiltin(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	custom := stack.Definition{
		Name: "servarr", Target: "other", InstallRoot: "/srv/servarr",
		Services: []stack.Service{{ID: "custom", Image: "custom:1", Port: 1234}},
	}
	merged, err := WithStacks(r, []stack.Definition{custom})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := merged.Get("servarr")
	if !ok {
		t.Fatal("servarr missing")
	}
	if f.TargetMinion != "other" || f.Files["compose.yaml"] == "" {
		t.Fatal("custom stack did not override the builtin")
	}
	if len(merged.StackIDs()) != 1 {
		t.Fatalf("stack ids = %v", merged.StackIDs())
	}
}

func TestWithStacksRejectsFormulaConflict(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = WithStacks(r, []stack.Definition{{
		Name: "docker", InstallRoot: "/opt/docker",
		Services: []stack.Service{{ID: "app", Image: "x:1"}},
	}})
	if err == nil {
		t.Fatal("expected a conflict error")
	}
}
