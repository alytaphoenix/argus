package stack

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// Builtins returns the stack definitions shipped with argus.
func Builtins() ([]Definition, error) {
	entries, err := fs.Glob(builtinFS, "builtin/*.toml")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	out := make([]Definition, 0, len(entries))
	for _, name := range entries {
		data, err := builtinFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		def, err := Decode(string(data))
		if err != nil {
			return nil, fmt.Errorf("builtin stack %s: %w", filepath.Base(name), err)
		}
		out = append(out, def)
	}
	return out, nil
}

// Encode renders a definition back to TOML for `argus stack init`.
func Encode(def Definition) (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(def); err != nil {
		return "", err
	}
	return buf.String(), nil
}
