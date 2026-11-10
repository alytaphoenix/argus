package formula

import (
	"fmt"
	"sort"

	"github.com/alytaphoenix/argus/stack"
)

// FromStack converts a declarative stack into an installable formula. The
// rendered Compose file and SLS state are carried as formula files, so the
// normal bootstrap materialisation path installs them under /srv/salt.
func FromStack(def stack.Definition) (Formula, error) {
	def = def.Normalized()
	if err := def.Validate(); err != nil {
		return Formula{}, err
	}
	f := Formula{
		ID:           def.Name,
		Name:         def.Name,
		Description:  def.Description,
		Category:     "Stack",
		Version:      "1.0.0",
		Kind:         KindBuiltin,
		Priority:     70,
		Target:       "minion",
		TargetMinion: def.Target,
		States:       []string{def.StateName()},
		Provides:     []string{"stack:" + def.Name},
		Docs: fmt.Sprintf("Declarative stack from %s. Installs containers under %s on minion %s.",
			stackSource(def), def.InstallRoot, def.Target),
		Files: map[string]string{
			"init.sls":     def.SLS(),
			"compose.yaml": def.Compose(),
		},
	}
	for _, svc := range def.Services {
		if svc.Port == 0 {
			continue
		}
		path := svc.HealthPath
		if path == "" {
			path = "/"
		}
		f.Health = &Health{TCP: fmt.Sprintf("127.0.0.1:%d", svc.Port), Path: path}
		break
	}
	if def.Target == "" {
		f.Docs += " Set target to an accepted Salt minion before applying."
	}
	return f, nil
}

// WithStacks merges user stack definitions into a registry. A user stack
// overrides a built-in stack of the same name; it may not shadow a non-stack
// formula.
func WithStacks(r *Registry, defs []stack.Definition) (*Registry, error) {
	if r == nil {
		return nil, fmt.Errorf("registry is required")
	}
	all := r.List()
	for _, def := range defs {
		f, err := FromStack(def)
		if err != nil {
			return nil, fmt.Errorf("stack %q: %w", def.Name, err)
		}
		replaced := false
		for i := range all {
			if all[i].ID != f.ID {
				continue
			}
			if !all[i].IsStack() {
				return nil, fmt.Errorf("stack %q conflicts with formula %q", def.Name, f.ID)
			}
			all[i] = f
			replaced = true
			break
		}
		if !replaced {
			all = append(all, f)
		}
	}
	return NewRegistry(all...)
}

// WithStackDir loads stack files from dir and merges them into the registry.
// A missing directory is not an error.
func WithStackDir(r *Registry, dir string) (*Registry, error) {
	defs, err := stack.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	return WithStacks(r, defs)
}

func stackSource(def stack.Definition) string {
	if def.Source == "" {
		return "the built-in stack catalog"
	}
	return def.Source
}

// StackIDs returns the IDs of every stack formula in the registry.
func (r *Registry) StackIDs() []string {
	var out []string
	for _, f := range r.List() {
		if f.IsStack() {
			out = append(out, f.ID)
		}
	}
	sort.Strings(out)
	return out
}
