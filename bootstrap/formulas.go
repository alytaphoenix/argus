package bootstrap

import (
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/alytaphoenix/argus/formula"
)

// FormulaFiles materialises built-in formula files below /srv/salt. External
// and integration formulas have no files.
func FormulaFiles(formulas []formula.Formula) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range formulas {
		if f.Kind != formula.KindBuiltin {
			continue
		}
		if len(f.Files) == 0 {
			return nil, fmt.Errorf("formula %q has no files", f.ID)
		}
		dir := "argus/" + f.Slug()
		for name, content := range f.Files {
			clean := path.Clean(name)
			if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
				return nil, fmt.Errorf("formula %q has unsafe file path %q", f.ID, name)
			}
			out[path.Join(dir, clean)] = content
		}
	}
	return out, nil
}

// WriteFormulaScript returns a shell script that writes the formula files to
// the Salt master. Contents are base64-encoded so arbitrary YAML/Go-template
// text cannot break the shell.
func WriteFormulaScript(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("install -d -m 0755 /srv/salt/argus\n")
	for _, name := range names {
		full := path.Join("/srv/salt", name)
		dir := path.Dir(full)
		encoded := base64.StdEncoding.EncodeToString([]byte(files[name]))
		fmt.Fprintf(&b, "install -d -m 0755 %q\n", dir)
		fmt.Fprintf(&b, "printf '%%s' %q | base64 -d > %q\n", encoded, full)
		fmt.Fprintf(&b, "chmod 0644 %q\n", full)
	}
	return b.String()
}

// States returns the distinct SLS state names to apply for the given formulas.
func States(formulas []formula.Formula) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range formulas {
		if !f.Installable() {
			continue
		}
		state := f.StateName()
		if state == "" || seen[state] {
			continue
		}
		seen[state] = true
		out = append(out, state)
	}
	return out
}
