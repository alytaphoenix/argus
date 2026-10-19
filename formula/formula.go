// Package formula defines argus's opinionated Salt formula registry.
//
// A formula is a small, reviewable unit of desired state. Built-in formulas
// carry their SLS files so argus can install them on a Salt master and apply
// them idempotently. External formulas represent an existing Salt state tree;
// integration formulas represent a service argus can observe but does not
// install (for example, TrueNAS or Plex).
package formula

import (
	"fmt"
	"sort"
	"strings"
)

// Kind describes how a formula is provided.
type Kind string

const (
	// KindBuiltin ships its SLS files inside argus.
	KindBuiltin Kind = "builtin"
	// KindExternal expects the state files to already exist on the Salt master.
	KindExternal Kind = "external"
	// KindIntegration represents an existing service that argus can wire up and
	// health-check, but should not install.
	KindIntegration Kind = "integration"
)

// Health describes how to verify a formula's service after apply.
type Health struct {
	URL  string `json:"url,omitempty"`
	TCP  string `json:"tcp,omitempty"`
	Path string `json:"path,omitempty"`
}

// Formula is one entry in the registry.
type Formula struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Version     string         `json:"version"`
	Kind        Kind           `json:"kind"`
	Priority    int            `json:"priority"`
	DependsOn   []string       `json:"depends_on,omitempty"`
	States      []string       `json:"states,omitempty"`
	Target      string         `json:"target,omitempty"` // any, master, minion
	Provides    []string       `json:"provides,omitempty"`
	Pillar      map[string]any `json:"pillar,omitempty"`
	Health      *Health        `json:"health,omitempty"`
	Docs        string         `json:"docs,omitempty"`
	Destructive bool           `json:"destructive"`

	// Files maps a path relative to the formula directory to its content. It is
	// only used for built-ins and is intentionally omitted from JSON output.
	Files map[string]string `json:"-"`
}

// Slug is the filesystem/state component for this formula. Salt state names
// and paths cannot contain hyphens, so IDs use underscores on disk.
func (f Formula) Slug() string {
	return strings.ReplaceAll(f.ID, "-", "_")
}

// StateName returns the Salt state name to apply for this formula.
func (f Formula) StateName() string {
	if len(f.States) > 0 {
		return f.States[0]
	}
	if f.Kind == KindBuiltin {
		return "argus." + f.Slug()
	}
	return ""
}

// Installable reports whether argus can apply this formula.
func (f Formula) Installable() bool {
	return f.Kind == KindBuiltin || f.Kind == KindExternal
}

// Registry is an immutable-ish catalog of formulas.
type Registry struct {
	byID map[string]Formula
}

// NewRegistry builds a registry from the given formulas and validates it.
func NewRegistry(formulas ...Formula) (*Registry, error) {
	r := &Registry{byID: make(map[string]Formula, len(formulas))}
	for _, f := range formulas {
		if _, ok := r.byID[f.ID]; ok {
			return nil, fmt.Errorf("formula %q is defined twice", f.ID)
		}
		if err := validate(f); err != nil {
			return nil, err
		}
		r.byID[f.ID] = f
	}
	for _, f := range formulas {
		for _, dep := range f.DependsOn {
			if _, ok := r.byID[dep]; !ok {
				return nil, fmt.Errorf("formula %q depends on unknown formula %q", f.ID, dep)
			}
		}
	}
	return r, nil
}

func validate(f Formula) error {
	switch {
	case strings.TrimSpace(f.ID) == "":
		return fmt.Errorf("formula id is required")
	case strings.ContainsAny(f.ID, " \t\n/\\"):
		return fmt.Errorf("formula %q has an invalid id", f.ID)
	case f.Kind != KindBuiltin && f.Kind != KindExternal && f.Kind != KindIntegration:
		return fmt.Errorf("formula %q has an invalid kind %q", f.ID, f.Kind)
	case f.Kind == KindBuiltin && len(f.Files) == 0:
		return fmt.Errorf("builtin formula %q has no files", f.ID)
	case f.Destructive:
		return fmt.Errorf("formula %q is marked destructive; argus refuses to register destructive formulas", f.ID)
	}
	switch f.Target {
	case "", "any", "master", "minion":
	default:
		return fmt.Errorf("formula %q has an invalid target %q", f.ID, f.Target)
	}
	return nil
}

// Get returns a formula by ID.
func (r *Registry) Get(id string) (Formula, bool) {
	f, ok := r.byID[id]
	return f, ok
}

// List returns every formula sorted by priority then ID.
func (r *Registry) List() []Formula {
	out := make([]Formula, 0, len(r.byID))
	for _, f := range r.byID {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Resolve returns the formulas in dependency order. When ids is empty the
// baseline is used. Unknown IDs and cycles are errors.
func (r *Registry) Resolve(ids []string) ([]Formula, error) {
	if len(ids) == 0 {
		ids = BaselineIDs
	}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var out []Formula

	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("formula dependency cycle at %q", id)
		}
		f, ok := r.byID[id]
		if !ok {
			return fmt.Errorf("unknown formula %q", id)
		}
		visiting[id] = true
		for _, dep := range f.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[id] = false
		seen[id] = true
		out = append(out, f)
		return nil
	}

	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// BaselineIDs is the minimum opinionated platform baseline applied by
// bootstrap. Keep this small and safe; larger stacks are opt-in.
var BaselineIDs = []string{"base", "node-exporter"}

// RecommendedIDs is the next step for a full platform. The TUI shows these as
// available but does not apply them without an explicit choice.
var RecommendedIDs = []string{"docker", "technitium", "uptime-kuma"}
