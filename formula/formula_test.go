package formula

import "testing"

func TestBuiltinRegistry(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatalf("BuiltinRegistry: %v", err)
	}
	if _, ok := r.Get("base"); !ok {
		t.Fatal("base formula missing")
	}
	for _, f := range r.List() {
		if f.Destructive {
			t.Fatalf("destructive formula %q must not be published", f.ID)
		}
	}
}

func TestResolveOrdersDependencies(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve([]string{"technitium"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"base", "docker", "technitium"}
	if len(got) != len(want) {
		t.Fatalf("resolved %d formulas, want %d: %v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("resolved[%d] = %q, want %q (all: %v)", i, got[i].ID, id, ids(got))
		}
	}
}

func TestResolveUnknownAndCycle(t *testing.T) {
	r, err := NewRegistry(
		Formula{ID: "a", Kind: KindExternal, States: []string{"a"}, DependsOn: []string{"b"}},
		Formula{ID: "b", Kind: KindExternal, States: []string{"b"}, DependsOn: []string{"a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve([]string{"missing"}); err == nil {
		t.Fatal("expected unknown formula error")
	}
	if _, err := r.Resolve([]string{"a"}); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestFormulaSlugAndState(t *testing.T) {
	f := Formula{ID: "node-exporter", Kind: KindBuiltin, Files: map[string]string{"init.sls": "x"}}
	if f.Slug() != "node_exporter" {
		t.Fatalf("Slug = %q", f.Slug())
	}
	if f.StateName() != "argus.node_exporter" {
		t.Fatalf("StateName = %q", f.StateName())
	}
}

func ids(fs []Formula) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID
	}
	return out
}
