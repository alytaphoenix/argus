// Package stack defines argus's declarative service-stack format.
//
// A stack is a TOML file describing one or more containers that belong
// together, the Salt minion they run on, and the host paths that hold config
// and media. argus renders the definition into a Compose file and a Salt SLS
// state, so the same definition can be reviewed before it changes a host.
package stack

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Volume is an extra bind mount for one service.
type Volume struct {
	Host      string `toml:"host"`
	Container string `toml:"container"`
	ReadOnly  bool   `toml:"read_only"`
}

// Service is one container in a stack.
type Service struct {
	ID           string            `toml:"id"`
	Name         string            `toml:"name"`
	Description  string            `toml:"description"`
	Image        string            `toml:"image"`
	Port         int               `toml:"port"`          // host and container port; 0 = no published port
	ConfigPath   string            `toml:"config_path"`   // host path mounted at ConfigTarget
	ConfigTarget string            `toml:"config_target"` // container path; default /config
	ConfigOwner  string            `toml:"config_owner"`  // optional user[:group] for ConfigPath
	User         string            `toml:"user"`          // optional container user[:group]
	Command      []string          `toml:"command"`       // optional container command
	Env          map[string]string `toml:"env"`
	Volumes      []Volume          `toml:"volume"`
	HealthPath   string            `toml:"health_path"`
}

// Definition is a complete service stack.
type Definition struct {
	Name        string    `toml:"name"`
	Description string    `toml:"description"`
	Target      string    `toml:"target"` // Salt minion ID that runs the stack
	Node        string    `toml:"node"`   // Proxmox node, informational
	VMID        int       `toml:"vmid"`   // Proxmox VMID, informational
	InstallRoot string    `toml:"install_root"`
	MediaRoot   string    `toml:"media_root"` // host path mounted at /data; optional
	PUID        int       `toml:"puid"`
	PGID        int       `toml:"pgid"`
	Timezone    string    `toml:"timezone"`
	Services    []Service `toml:"service"`

	// Provision, when present, lets `argus stack apply` create the target VM
	// and install a Salt minion on it before applying the stack.
	Provision *Provision `toml:"provision"`

	// Source is the file the definition was loaded from (empty for built-ins).
	Source string `toml:"-"`
}

// DefaultDir returns the per-user stack directory. ARGUS_STACKS_DIR overrides
// it, matching the ARGUS_* convention used by the rest of argus.
func DefaultDir() string {
	if dir := strings.TrimSpace(os.Getenv("ARGUS_STACKS_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "argus", "stacks")
	}
	return filepath.Join(home, ".config", "argus", "stacks")
}

// LoadFile reads and validates one stack definition.
func LoadFile(file string) (Definition, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Definition{}, err
	}
	def, err := Decode(string(data))
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", file, err)
	}
	def.Source = file
	return def, nil
}

// Decode parses and validates a stack definition.
func Decode(data string) (Definition, error) {
	var def Definition
	md, err := toml.Decode(data, &def)
	if err != nil {
		return Definition{}, err
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		keys := make([]string, len(unknown))
		for i, k := range unknown {
			keys[i] = k.String()
		}
		return Definition{}, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	def.normalize()
	if err := def.Validate(); err != nil {
		return Definition{}, err
	}
	return def, nil
}

// LoadDir loads every *.toml file in dir. A missing directory is not an error.
func LoadDir(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Definition
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toml") {
			continue
		}
		def, err := LoadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Normalized returns a copy with defaults applied. It is used by callers that
// build a Definition in code rather than decoding a TOML file.
func (d Definition) Normalized() Definition {
	d.normalize()
	return d
}

func (d *Definition) normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Target = strings.TrimSpace(d.Target)
	d.Node = strings.TrimSpace(d.Node)
	if d.Description == "" {
		d.Description = "Container stack " + d.Name
	}
	if d.InstallRoot == "" {
		d.InstallRoot = "/opt/argus/" + d.Name
	}
	if d.PUID == 0 {
		d.PUID = 1000
	}
	if d.PGID == 0 {
		d.PGID = 1000
	}
	if d.Timezone == "" {
		d.Timezone = "Etc/UTC"
	}
	if d.Provision != nil {
		p := d.Provision
		p.Node = strings.TrimSpace(p.Node)
		p.Name = strings.TrimSpace(p.Name)
		p.CloudImage = strings.TrimSpace(p.CloudImage)
		p.Storage = strings.TrimSpace(p.Storage)
		p.Bridge = strings.TrimSpace(p.Bridge)
		p.CIUser = strings.TrimSpace(p.CIUser)
		p.IPConfig = strings.TrimSpace(p.IPConfig)
		p.Nameserver = strings.TrimSpace(p.Nameserver)
		p.SearchDomain = strings.TrimSpace(p.SearchDomain)
		p.SSHPublicKey = strings.TrimSpace(p.SSHPublicKey)
		p.SSHPrivateKey = strings.TrimSpace(p.SSHPrivateKey)
		p.MinionID = strings.TrimSpace(p.MinionID)
		if p.MinionID == "" {
			p.MinionID = d.Target
		}
	}
	for i := range d.Services {
		svc := &d.Services[i]
		svc.ID = strings.TrimSpace(svc.ID)
		svc.Image = strings.TrimSpace(svc.Image)
		svc.ConfigOwner = strings.TrimSpace(svc.ConfigOwner)
		svc.User = strings.TrimSpace(svc.User)
		if svc.Name == "" {
			svc.Name = svc.ID
		}
		if svc.ConfigTarget == "" {
			svc.ConfigTarget = "/config"
		}
		if svc.ConfigPath == "" {
			svc.ConfigPath = path.Join(d.InstallRoot, svc.ID, "config")
		} else if !path.IsAbs(svc.ConfigPath) {
			svc.ConfigPath = path.Join(d.InstallRoot, svc.ConfigPath)
		}
	}
}

// Validate checks the definition before anything is rendered or applied.
func (d Definition) Validate() error {
	if err := validName(d.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if strings.ContainsAny(d.Target, " \t\r\n") {
		return fmt.Errorf("target: Salt minion IDs cannot contain whitespace")
	}
	if err := validAbsPath("install_root", d.InstallRoot); err != nil {
		return err
	}
	if d.MediaRoot != "" {
		if err := validAbsPath("media_root", d.MediaRoot); err != nil {
			return err
		}
	}
	if d.VMID < 0 || d.VMID > 999999999 {
		return fmt.Errorf("vmid: %d is out of range", d.VMID)
	}
	if d.Provision != nil {
		if err := d.Provision.Validate(d.Target); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
	}
	if d.PUID < 0 || d.PGID < 0 {
		return fmt.Errorf("puid/pgid must be non-negative")
	}
	if strings.ContainsAny(d.Timezone, " \t\r\n") {
		return fmt.Errorf("timezone: must not contain whitespace")
	}
	if len(d.Services) == 0 {
		return fmt.Errorf("at least one [[service]] is required")
	}
	seenID := map[string]bool{}
	seenPort := map[int]string{}
	for i, svc := range d.Services {
		where := fmt.Sprintf("service[%d]", i)
		if err := validName(svc.ID); err != nil {
			return fmt.Errorf("%s id: %w", where, err)
		}
		if seenID[svc.ID] {
			return fmt.Errorf("%s: duplicate service id %q", where, svc.ID)
		}
		seenID[svc.ID] = true
		if svc.Image == "" || strings.ContainsAny(svc.Image, " \t\r\n") {
			return fmt.Errorf("service %q: image is required and must not contain whitespace", svc.ID)
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return fmt.Errorf("service %q: port %d is out of range", svc.ID, svc.Port)
		}
		if svc.Port != 0 {
			if other, ok := seenPort[svc.Port]; ok {
				return fmt.Errorf("service %q: port %d is already used by %q", svc.ID, svc.Port, other)
			}
			seenPort[svc.Port] = svc.ID
		}
		if err := validAbsPath("config_path", svc.ConfigPath); err != nil {
			return fmt.Errorf("service %q: %w", svc.ID, err)
		}
		if !path.IsAbs(svc.ConfigTarget) {
			return fmt.Errorf("service %q: config_target must be absolute", svc.ID)
		}
		if svc.HealthPath != "" && !strings.HasPrefix(svc.HealthPath, "/") {
			return fmt.Errorf("service %q: health_path must start with /", svc.ID)
		}
		for i, arg := range svc.Command {
			if arg == "" || strings.ContainsAny(arg, "\r\n") {
				return fmt.Errorf("service %q: command[%d] must be non-empty and single-line", svc.ID, i)
			}
		}
		if svc.ConfigOwner != "" && !validOwner(svc.ConfigOwner) {
			return fmt.Errorf("service %q: config_owner must be user[:group]", svc.ID)
		}
		if svc.User != "" && !validOwner(svc.User) {
			return fmt.Errorf("service %q: user must be user[:group]", svc.ID)
		}
		for key, value := range svc.Env {
			if !validEnvKey(key) {
				return fmt.Errorf("service %q: invalid env key %q", svc.ID, key)
			}
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("service %q: env %s must be a single line", svc.ID, key)
			}
		}
		seenMount := map[string]bool{svc.ConfigTarget: true}
		if d.MediaRoot != "" {
			seenMount["/data"] = true
		}
		for _, vol := range svc.Volumes {
			if err := validAbsPath("volume host", vol.Host); err != nil {
				return fmt.Errorf("service %q: %w", svc.ID, err)
			}
			if !path.IsAbs(vol.Container) {
				return fmt.Errorf("service %q: volume container path must be absolute", svc.ID)
			}
			if seenMount[vol.Container] {
				return fmt.Errorf("service %q: duplicate container mount %q", svc.ID, vol.Container)
			}
			seenMount[vol.Container] = true
		}
	}
	return nil
}

// Slug returns the filesystem-safe stack name used in Salt paths.
func (d Definition) Slug() string {
	return slug(d.Name)
}

// SiteSlug is the filesystem name Salt materialises the stack under. It
// matches formula.Formula.Slug, which replaces dashes with underscores.
func (d Definition) SiteSlug() string {
	return strings.ReplaceAll(d.Slug(), "-", "_")
}

// StateName is the Salt state that installs the stack files and starts it.
func (d Definition) StateName() string {
	return "argus." + strings.ReplaceAll(d.Slug(), "-", "_")
}

// ServicePaths returns every host config directory the stack needs.
func (d Definition) ServicePaths() []string {
	out := make([]string, 0, len(d.Services))
	for _, svc := range d.Services {
		out = append(out, svc.ConfigPath)
	}
	return out
}

// Compose renders a deterministic Compose file for the stack.
func (d Definition) Compose() string {
	var b strings.Builder
	b.WriteString("# Managed by argus — stack " + d.Name + ".\n")
	b.WriteString("# Edit the stack definition, not this file.\n")
	b.WriteString("services:\n")
	services := append([]Service(nil), d.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	for _, svc := range services {
		b.WriteString("  " + svc.ID + ":\n")
		b.WriteString("    image: " + yamlString(svc.Image) + "\n")
		b.WriteString("    container_name: " + yamlString(svc.ID) + "\n")
		b.WriteString("    restart: unless-stopped\n")
		if svc.User != "" {
			b.WriteString("    user: " + yamlString(svc.User) + "\n")
		}
		if len(svc.Command) > 0 {
			args := make([]string, len(svc.Command))
			for i, arg := range svc.Command {
				args[i] = yamlString(arg)
			}
			b.WriteString("    command: [" + strings.Join(args, ", ") + "]\n")
		}

		env := map[string]string{
			"PUID": strconv.Itoa(d.PUID),
			"PGID": strconv.Itoa(d.PGID),
			"TZ":   d.Timezone,
		}
		for key, value := range svc.Env {
			env[key] = value
		}
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("    environment:\n")
		for _, key := range keys {
			b.WriteString("      " + key + ": " + yamlString(env[key]) + "\n")
		}

		if svc.Port != 0 {
			b.WriteString("    ports:\n")
			b.WriteString("      - " + yamlString(fmt.Sprintf("%d:%d", svc.Port, svc.Port)) + "\n")
		}

		b.WriteString("    volumes:\n")
		b.WriteString("      - " + yamlString(svc.ConfigPath+":"+svc.ConfigTarget) + "\n")
		if d.MediaRoot != "" {
			b.WriteString("      - " + yamlString(d.MediaRoot+":/data") + "\n")
		}
		for _, vol := range svc.Volumes {
			mount := vol.Host + ":" + vol.Container
			if vol.ReadOnly {
				mount += ":ro"
			}
			b.WriteString("      - " + yamlString(mount) + "\n")
		}
	}
	return b.String()
}

// SLS renders the Salt state that materialises and starts the stack.
func (d Definition) SLS() string {
	s := d.SiteSlug()
	state := "argus-stack-" + s
	var b strings.Builder
	b.WriteString("# Managed by argus — stack " + d.Name + ".\n")
	b.WriteString("include:\n  - argus.docker\n\n")
	b.WriteString(yamlString(d.InstallRoot) + ":\n  file.directory:\n    - makedirs: True\n\n")
	for i, svc := range d.Services {
		fmt.Fprintf(&b, "%s-dir-%d:\n  file.directory:\n    - name: %s\n    - makedirs: True\n", state, i, yamlString(svc.ConfigPath))
		if svc.ConfigOwner != "" {
			user, group, _ := strings.Cut(svc.ConfigOwner, ":")
			if group == "" {
				group = user
			}
			b.WriteString("    - user: " + yamlString(user) + "\n")
			b.WriteString("    - group: " + yamlString(group) + "\n")
			b.WriteString("    - mode: 0700\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `%s-compose:
  file.managed:
    - name: %s
    - source: salt://argus/%s/compose.yaml
    - makedirs: True
    - mode: 0644

%s-up:
  cmd.run:
    - name: "docker-compose down --remove-orphans || true; docker-compose up -d --remove-orphans"
    - cwd: %s
    - unless: "docker-compose ps --services --filter status=running | wc -l | grep -qx %d"
    - require:
      - file: %s-compose
      - pkg: argus-docker-packages
      - service: argus-docker-service

%s-reload:
  cmd.wait:
    - name: "docker-compose down --remove-orphans || true; docker-compose up -d --remove-orphans"
    - cwd: %s
    - watch:
      - file: %s-compose
`, state, yamlString(path.Join(d.InstallRoot, "compose.yaml")), s,
		state, yamlString(d.InstallRoot), len(d.Services),
		state, state, yamlString(d.InstallRoot), state)
	return b.String()
}

func validName(s string) error {
	if s == "" {
		return fmt.Errorf("is required")
	}
	if len(s) > 63 {
		return fmt.Errorf("must be at most 63 characters")
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok || i == 0 && !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return fmt.Errorf("%q must match [a-z0-9][a-z0-9._-]*", s)
		}
	}
	return nil
}

func validAbsPath(label, p string) error {
	if p == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !path.IsAbs(p) {
		return fmt.Errorf("%s must be an absolute Linux path: %q", label, p)
	}
	if strings.ContainsAny(p, "\r\n\t") {
		return fmt.Errorf("%s must not contain control characters", label)
	}
	clean := path.Clean(p)
	if clean != p {
		return fmt.Errorf("%s must be clean (got %q, want %q)", label, p, clean)
	}
	if strings.Contains(clean, "..") {
		return fmt.Errorf("%s must not contain '..'", label)
	}
	return nil
}

func validOwner(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		numeric := true
		for _, r := range part {
			if r < '0' || r > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			continue
		}
		for i, r := range part {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || i > 0 && r == '.'
			if !ok || i == 0 && !(r >= 'a' && r <= 'z' || r == '_') {
				return false
			}
		}
	}
	return true
}

func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}
	return true
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func yamlString(s string) string {
	return strconv.Quote(s)
}
