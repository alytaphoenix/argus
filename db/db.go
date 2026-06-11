// Package db provides a local SQLite store for tracking VM profiles and
// provisioning history. The database lives at ~/.config/ratataskr/ratataskr.db.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with all ratataskr-specific queries.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the ratataskr database.
func Open() (*DB, error) {
	path, err := defaultPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	d := &DB{sql: conn}
	if err := d.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.sql.Close() }

func defaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ratataskr", "ratataskr.db"), nil
}

func (d *DB) migrate() error {
	// v1 — original tables
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS cloud_init_profiles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    ci_user      TEXT,
    ci_password  TEXT,
    ci_ssh_key   TEXT,
    ci_ipconfig  TEXT,
    nameserver   TEXT,
    searchdomain TEXT,
    notes        TEXT,
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS salt_profiles (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    target     TEXT NOT NULL,
    states     TEXT NOT NULL,
    notes      TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS provisioned_vms (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    vmid                  INTEGER NOT NULL,
    name                  TEXT NOT NULL,
    node                  TEXT NOT NULL,
    template_id           INTEGER,
    cloud_init_profile    TEXT,
    salt_profile          TEXT,
    salt_applied_at       DATETIME,
    cloud_init_applied_at DATETIME,
    notes                 TEXT,
    provisioned_at        DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vms_vmid ON provisioned_vms(vmid);
CREATE INDEX IF NOT EXISTS idx_vms_node ON provisioned_vms(node);
`); err != nil {
		return err
	}

	// v2 — salt apply event log
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS salt_apply_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    minion       TEXT NOT NULL,
    states       TEXT NOT NULL,
    salt_profile TEXT,
    jid          TEXT,
    success      INTEGER NOT NULL DEFAULT 1,
    error_msg    TEXT,
    triggered_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_salt_events_minion ON salt_apply_events(minion);
CREATE INDEX IF NOT EXISTS idx_salt_events_at    ON salt_apply_events(triggered_at);
`); err != nil {
		return err
	}

	// v2 — new columns on provisioned_vms (ignore "duplicate column" errors)
	for _, stmt := range []string{
		`ALTER TABLE provisioned_vms ADD COLUMN vm_ip      TEXT`,
		`ALTER TABLE provisioned_vms ADD COLUMN vm_type    TEXT`,
		`ALTER TABLE provisioned_vms ADD COLUMN last_status TEXT`,
		`ALTER TABLE provisioned_vms ADD COLUMN last_seen_at DATETIME`,
	} {
		d.sql.Exec(stmt) //nolint:errcheck — harmless if column already exists
	}

	// v3 — nomad job tracking
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS nomad_job_snapshots (
    job_id       TEXT PRIMARY KEY,
    job_type     TEXT NOT NULL,
    status       TEXT NOT NULL,
    running      INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    lost         INTEGER NOT NULL DEFAULT 0,
    last_seen_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS nomad_job_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL,
    job_type    TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    prev_status TEXT,
    new_status  TEXT NOT NULL,
    running     INTEGER NOT NULL DEFAULT 0,
    failed      INTEGER NOT NULL DEFAULT 0,
    lost        INTEGER NOT NULL DEFAULT 0,
    recorded_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nomad_events_job ON nomad_job_events(job_id);
CREATE INDEX IF NOT EXISTS idx_nomad_events_at  ON nomad_job_events(recorded_at);
`); err != nil {
		return err
	}

	// v4 — services, health checks, DNS cache, certificates
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS services (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    node       TEXT NOT NULL DEFAULT '',
    host       TEXT NOT NULL,
    port       INTEGER NOT NULL,
    scheme     TEXT NOT NULL DEFAULT 'tcp',
    notes      TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_services_node ON services(node);

CREATE TABLE IF NOT EXISTS service_health_checks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id  INTEGER NOT NULL,
    up          INTEGER NOT NULL,
    latency_ms  INTEGER,
    status_code INTEGER,
    tls_expiry  DATETIME,
    error_msg   TEXT,
    checked_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_health_service ON service_health_checks(service_id);
CREATE INDEX IF NOT EXISTS idx_health_at      ON service_health_checks(checked_at);

CREATE TABLE IF NOT EXISTS certificates (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    host         TEXT NOT NULL,
    port         INTEGER NOT NULL DEFAULT 443,
    common_name  TEXT NOT NULL DEFAULT '',
    san_names    TEXT NOT NULL DEFAULT '',
    issuer       TEXT NOT NULL DEFAULT '',
    serial       TEXT NOT NULL DEFAULT '',
    not_before   DATETIME NOT NULL,
    not_after    DATETIME NOT NULL,
    fingerprint  TEXT NOT NULL DEFAULT '',
    last_seen_at DATETIME NOT NULL,
    UNIQUE(host, port, fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_certs_expiry ON certificates(not_after);

CREATE TABLE IF NOT EXISTS dns_records_cache (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    zone      TEXT NOT NULL,
    name      TEXT NOT NULL,
    type      TEXT NOT NULL,
    value     TEXT NOT NULL,
    ttl       INTEGER NOT NULL DEFAULT 300,
    synced_at DATETIME NOT NULL,
    UNIQUE(zone, name, type, value)
);
CREATE INDEX IF NOT EXISTS idx_dns_zone ON dns_records_cache(zone);
`); err != nil {
		return err
	}

	// v5 — VM resize/right-sizing history
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS vm_resize_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    vmid        INTEGER NOT NULL,
    node        TEXT NOT NULL,
    vm_type     TEXT NOT NULL,
    field       TEXT NOT NULL,   -- "cpu", "memory", "disk"
    old_value   TEXT NOT NULL,
    new_value   TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT 'manual', -- "manual" or "recommended"
    applied_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resize_vmid ON vm_resize_events(vmid);
CREATE INDEX IF NOT EXISTS idx_resize_at   ON vm_resize_events(applied_at);
`); err != nil {
		return err
	}

	// v6 — PXE provisioning + security monitoring
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS pxe_profiles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    kernel_url  TEXT NOT NULL,
    initrd_urls TEXT NOT NULL DEFAULT '',
    kernel_args TEXT NOT NULL DEFAULT '',
    template    TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS pxe_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    profile_id  TEXT NOT NULL,
    mac_address TEXT NOT NULL DEFAULT '',
    selector    TEXT NOT NULL DEFAULT '',
    metadata    TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS pxe_boot_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    mac_address TEXT NOT NULL DEFAULT '',
    ip_address  TEXT NOT NULL DEFAULT '',
    profile_id  TEXT NOT NULL,
    group_id    TEXT NOT NULL DEFAULT '',
    hostname    TEXT NOT NULL DEFAULT '',
    booted_at   DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pxe_boot_mac ON pxe_boot_events(mac_address);
CREATE INDEX IF NOT EXISTS idx_pxe_boot_at  ON pxe_boot_events(booted_at);

CREATE TABLE IF NOT EXISTS security_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    severity    TEXT NOT NULL DEFAULT 'low',
    description TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    raw_data    TEXT NOT NULL DEFAULT '',
    recorded_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sec_events_at  ON security_events(recorded_at);
CREATE INDEX IF NOT EXISTS idx_sec_events_src ON security_events(source);
`); err != nil {
		return err
	}

	// v8 — service host registration (provision / managed / unmanaged)
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS service_hosts (
    host              TEXT PRIMARY KEY,
    vmid              INTEGER NOT NULL DEFAULT 0,   -- Proxmox VMID if known
    node              TEXT NOT NULL DEFAULT '',     -- Proxmox node name if known
    managed           INTEGER NOT NULL DEFAULT 0,   -- 1 = managed, 0 = unmanaged
    registered        INTEGER NOT NULL DEFAULT 0,   -- 1 = registered
    provision_pending INTEGER NOT NULL DEFAULT 0,   -- 1 = provision requested, not complete
    registration_type TEXT NOT NULL DEFAULT '',     -- "managed" | "unmanaged" | ""
    salt_applied      INTEGER NOT NULL DEFAULT 0,
    wazuh_enrolled    INTEGER NOT NULL DEFAULT 0,
    discovered_ports  TEXT NOT NULL DEFAULT '',     -- JSON: [{port,service}, ...]
    notes             TEXT NOT NULL DEFAULT '',
    registered_at     DATETIME,
    last_discovered   DATETIME
);
CREATE INDEX IF NOT EXISTS idx_svc_hosts_vmid ON service_hosts(vmid);
`); err != nil {
		return err
	}

	// v7 — per-node SSH credentials + security scan results
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS node_ssh_credentials (
    node        TEXT PRIMARY KEY,
    ssh_user    TEXT NOT NULL DEFAULT 'root',
    keyring_key TEXT NOT NULL   -- actual password stored in macOS Keychain under this key
);

CREATE TABLE IF NOT EXISTS scan_results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    target      TEXT NOT NULL,
    node        TEXT NOT NULL DEFAULT '',
    vmid        INTEGER NOT NULL DEFAULT 0,
    scan_type   TEXT NOT NULL,   -- "port", "vm_firewall"
    check_name  TEXT NOT NULL,
    status      TEXT NOT NULL,   -- "pass", "warn", "fail", "info"
    severity    TEXT NOT NULL DEFAULT 'info',
    detail      TEXT NOT NULL DEFAULT '',
    remediation TEXT NOT NULL DEFAULT '',
    rem_action  TEXT NOT NULL DEFAULT '',  -- programmatic action type
    remediated  INTEGER NOT NULL DEFAULT 0,
    scanned_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_scan_target ON scan_results(target);
CREATE INDEX IF NOT EXISTS idx_scan_at     ON scan_results(scanned_at);
`); err != nil {
		return err
	}

	// v9 — app-wide configurable settings
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS app_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    updated_at  DATETIME NOT NULL
);
`); err != nil {
		return err
	}

	// Seed default settings (only inserts if key doesn't exist).
	if err := d.seedDefaultSettings(); err != nil {
		return err
	}

	// v11 — node metrics history for sparklines + trend charts
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS node_metrics_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    node        TEXT NOT NULL,
    cpu_ratio   REAL NOT NULL DEFAULT 0,
    mem_ratio   REAL NOT NULL DEFAULT 0,
    mem_used_mb INTEGER NOT NULL DEFAULT 0,
    mem_max_mb  INTEGER NOT NULL DEFAULT 0,
    recorded_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_node_metrics_node ON node_metrics_history(node);
CREATE INDEX IF NOT EXISTS idx_node_metrics_at   ON node_metrics_history(recorded_at);
`); err != nil {
		return err
	}

	// v12 — service uptime tracking
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS service_uptime_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id INTEGER NOT NULL,
    up         INTEGER NOT NULL,
    checked_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_uptime_svc ON service_uptime_events(service_id);
CREATE INDEX IF NOT EXISTS idx_uptime_at  ON service_uptime_events(checked_at);
`); err != nil {
		return err
	}

	// v13 — runbook executor
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS runbooks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    steps_json  TEXT NOT NULL DEFAULT '[]',
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS runbook_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    runbook_id  INTEGER NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    output      TEXT NOT NULL DEFAULT '',
    started_at  DATETIME,
    done_at     DATETIME,
    created_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runbook_runs_book ON runbook_runs(runbook_id);
`); err != nil {
		return err
	}

	// v14 — alert routing rules
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS alert_routes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT '',
    webhook_url  TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    last_fired   DATETIME,
    created_at   DATETIME NOT NULL
);
`); err != nil {
		return err
	}

	// v15 — VM lifecycle policies
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS vm_lifecycle_policies (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    tag         TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL DEFAULT 'stop',
    idle_hours  INTEGER NOT NULL DEFAULT 168,
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_run    DATETIME,
    created_at  DATETIME NOT NULL
);
`); err != nil {
		return err
	}

	// v10 — cluster maintenance schedules and run history
	if _, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS maintenance_schedules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL DEFAULT 'Cluster Maintenance',
    run_at       DATETIME,            -- NULL = manual / run-now trigger
    repeat       TEXT NOT NULL DEFAULT '',  -- 'weekly','monthly','' = once
    node_order   TEXT NOT NULL DEFAULT '', -- JSON array of node names in order
    task_flags   TEXT NOT NULL DEFAULT '', -- JSON: {os_updates,pkg_updates,proxmox_upgrade,reboot}
    ha_aware     INTEGER NOT NULL DEFAULT 1,  -- 1 = evacuate VMs before rebooting HA nodes
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS maintenance_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id     INTEGER,           -- FK to maintenance_schedules (0 = ad-hoc)
    name            TEXT NOT NULL DEFAULT 'Maintenance Run',
    status          TEXT NOT NULL DEFAULT 'pending', -- pending|running|done|failed|cancelled
    node_order      TEXT NOT NULL DEFAULT '',        -- snapshot at run time
    task_flags      TEXT NOT NULL DEFAULT '',
    ha_aware        INTEGER NOT NULL DEFAULT 1,
    started_at      DATETIME,
    completed_at    DATETIME,
    created_at      DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_maint_run_sched ON maintenance_runs(schedule_id);
CREATE INDEX IF NOT EXISTS idx_maint_run_at    ON maintenance_runs(started_at);

CREATE TABLE IF NOT EXISTS maintenance_run_steps (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     INTEGER NOT NULL,
    node       TEXT NOT NULL,
    task       TEXT NOT NULL,   -- 'evacuate','os_update','pkg_update','proxmox_upgrade','reboot','unevacuate'
    status     TEXT NOT NULL DEFAULT 'pending',  -- pending|running|done|skipped|failed
    output     TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    done_at    DATETIME
);
CREATE INDEX IF NOT EXISTS idx_maint_steps_run ON maintenance_run_steps(run_id);
`); err != nil {
		return err
	}

	return nil
}

// ── App settings ─────────────────────────────────────────────────────────────

// AppSetting is a single key-value configuration entry.
type AppSetting struct {
	Key         string
	Value       string
	Description string
	UpdatedAt   time.Time
}

// AppSettings is a parsed view of the most-used settings, ready for use in commands.
type AppSettings struct {
	WazuhCriticalLevel  int
	WazuhHighLevel      int
	WazuhMediumLevel    int
	ScanTimeoutMs       int
	ScanExtraPorts      string // comma-separated extra ports, empty = none
	BanLookbackHours    int
	HealthCheckTimeoutS int
}

var defaultSettings = []struct {
	key, value, desc string
}{
	{"wazuh.critical_level", "12", "Wazuh rule level threshold for critical severity"},
	{"wazuh.high_level", "10", "Wazuh rule level threshold for high severity"},
	{"wazuh.medium_level", "7", "Wazuh rule level threshold for medium severity"},
	{"scan.timeout_ms", "600", "Port scan TCP connect timeout (milliseconds)"},
	{"scan.extra_ports", "", "Additional ports to scan (comma-separated, e.g. 8080,8443)"},
	{"services.ban_lookback_hours", "48", "Hours to look back when checking CrowdSec bans"},
	{"services.healthcheck_timeout_s", "15", "HTTP/TCP health check timeout (seconds)"},
}

func (d *DB) seedDefaultSettings() error {
	for _, s := range defaultSettings {
		_, err := d.sql.Exec(`
INSERT INTO app_settings (key, value, description, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(key) DO NOTHING`,
			s.key, s.value, s.desc, time.Now())
		if err != nil {
			return err
		}
	}
	return nil
}

// GetSetting returns the stored value for key, or defaultVal if not found.
func (d *DB) GetSetting(key, defaultVal string) string {
	var val string
	err := d.sql.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

// SetSetting persists a setting value.
func (d *DB) SetSetting(key, value string) error {
	_, err := d.sql.Exec(`
UPDATE app_settings SET value=?, updated_at=? WHERE key=?`,
		value, time.Now(), key)
	return err
}

// AllSettings returns all settings ordered by key.
func (d *DB) AllSettings() ([]AppSetting, error) {
	rows, err := d.sql.Query(`SELECT key, value, description, updated_at FROM app_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppSetting
	for rows.Next() {
		var s AppSetting
		if err := rows.Scan(&s.Key, &s.Value, &s.Description, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LoadAppSettings reads all settings from DB and returns a parsed AppSettings struct.
func (d *DB) LoadAppSettings() AppSettings {
	parseIntOr := func(key string, def int) int {
		v := d.GetSetting(key, "")
		if v == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return def
		}
		return n
	}
	return AppSettings{
		WazuhCriticalLevel:  parseIntOr("wazuh.critical_level", 12),
		WazuhHighLevel:      parseIntOr("wazuh.high_level", 10),
		WazuhMediumLevel:    parseIntOr("wazuh.medium_level", 7),
		ScanTimeoutMs:       parseIntOr("scan.timeout_ms", 600),
		ScanExtraPorts:      d.GetSetting("scan.extra_ports", ""),
		BanLookbackHours:    parseIntOr("services.ban_lookback_hours", 48),
		HealthCheckTimeoutS: parseIntOr("services.healthcheck_timeout_s", 15),
	}
}

// ── Cloud-init profiles ──────────────────────────────────────────────────────

type CloudInitProfile struct {
	ID           int64
	Name         string
	CIUser       string
	CIPassword   string
	CISSHKey     string
	CIIPConfig   string
	Nameserver   string
	SearchDomain string
	Notes        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (d *DB) SaveCloudInitProfile(p CloudInitProfile) error {
	now := time.Now()
	if p.ID == 0 {
		_, err := d.sql.Exec(`
INSERT INTO cloud_init_profiles
    (name, ci_user, ci_password, ci_ssh_key, ci_ipconfig, nameserver, searchdomain, notes, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.CIUser, p.CIPassword, p.CISSHKey, p.CIIPConfig,
			p.Nameserver, p.SearchDomain, p.Notes, now, now)
		return err
	}
	_, err := d.sql.Exec(`
UPDATE cloud_init_profiles SET
    name=?, ci_user=?, ci_password=?, ci_ssh_key=?, ci_ipconfig=?,
    nameserver=?, searchdomain=?, notes=?, updated_at=?
WHERE id=?`,
		p.Name, p.CIUser, p.CIPassword, p.CISSHKey, p.CIIPConfig,
		p.Nameserver, p.SearchDomain, p.Notes, now, p.ID)
	return err
}

func (d *DB) DeleteCloudInitProfile(name string) error {
	_, err := d.sql.Exec(`DELETE FROM cloud_init_profiles WHERE name=?`, name)
	return err
}

func (d *DB) CloudInitProfiles() ([]CloudInitProfile, error) {
	rows, err := d.sql.Query(`SELECT id,name,ci_user,ci_password,ci_ssh_key,ci_ipconfig,nameserver,searchdomain,notes,created_at,updated_at FROM cloud_init_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudInitProfile
	for rows.Next() {
		var p CloudInitProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.CIUser, &p.CIPassword, &p.CISSHKey, &p.CIIPConfig,
			&p.Nameserver, &p.SearchDomain, &p.Notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) GetCloudInitProfile(name string) (*CloudInitProfile, error) {
	var p CloudInitProfile
	err := d.sql.QueryRow(`SELECT id,name,ci_user,ci_password,ci_ssh_key,ci_ipconfig,nameserver,searchdomain,notes,created_at,updated_at FROM cloud_init_profiles WHERE name=?`, name).
		Scan(&p.ID, &p.Name, &p.CIUser, &p.CIPassword, &p.CISSHKey, &p.CIIPConfig,
			&p.Nameserver, &p.SearchDomain, &p.Notes, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// ── Salt profiles ────────────────────────────────────────────────────────────

type SaltProfile struct {
	ID        int64
	Name      string
	Target    string
	States    string // comma-separated
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (d *DB) SaveSaltProfile(p SaltProfile) error {
	now := time.Now()
	if p.ID == 0 {
		_, err := d.sql.Exec(`
INSERT INTO salt_profiles (name, target, states, notes, created_at, updated_at)
VALUES (?,?,?,?,?,?)`,
			p.Name, p.Target, p.States, p.Notes, now, now)
		return err
	}
	_, err := d.sql.Exec(`
UPDATE salt_profiles SET name=?, target=?, states=?, notes=?, updated_at=? WHERE id=?`,
		p.Name, p.Target, p.States, p.Notes, now, p.ID)
	return err
}

func (d *DB) DeleteSaltProfile(name string) error {
	_, err := d.sql.Exec(`DELETE FROM salt_profiles WHERE name=?`, name)
	return err
}

func (d *DB) SaltProfiles() ([]SaltProfile, error) {
	rows, err := d.sql.Query(`SELECT id,name,target,states,notes,created_at,updated_at FROM salt_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SaltProfile
	for rows.Next() {
		var p SaltProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.Target, &p.States, &p.Notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Provisioned VM tracking ──────────────────────────────────────────────────

type ProvisionedVM struct {
	ID                 int64
	VMID               int
	Name               string
	Node               string
	TemplateID         int
	CloudInitProfile   string
	SaltProfile        string
	SaltAppliedAt      *time.Time
	CloudInitAppliedAt *time.Time
	Notes              string
	ProvisionedAt      time.Time
	// v2 fields
	VMIP       string
	VMType     string // "qemu" or "lxc"
	LastStatus string // "running", "stopped", etc.
	LastSeenAt *time.Time
}

func (d *DB) RecordProvisionedVM(vm ProvisionedVM) error {
	_, err := d.sql.Exec(`
INSERT INTO provisioned_vms
    (vmid, name, node, template_id, cloud_init_profile, salt_profile,
     salt_applied_at, cloud_init_applied_at, notes, provisioned_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		vm.VMID, vm.Name, vm.Node, vm.TemplateID,
		vm.CloudInitProfile, vm.SaltProfile,
		vm.SaltAppliedAt, vm.CloudInitAppliedAt,
		vm.Notes, time.Now())
	return err
}

func (d *DB) UpdateVMSaltApplied(vmid int, profileName string) error {
	now := time.Now()
	_, err := d.sql.Exec(`UPDATE provisioned_vms SET salt_profile=?, salt_applied_at=? WHERE vmid=?`,
		profileName, now, vmid)
	return err
}

func (d *DB) UpdateVMCloudInitApplied(vmid int, profileName string) error {
	now := time.Now()
	_, err := d.sql.Exec(`UPDATE provisioned_vms SET cloud_init_profile=?, cloud_init_applied_at=? WHERE vmid=?`,
		profileName, now, vmid)
	return err
}

func (d *DB) ProvisionedVMs() ([]ProvisionedVM, error) {
	rows, err := d.sql.Query(`
SELECT id,vmid,name,node,template_id,cloud_init_profile,salt_profile,
       salt_applied_at,cloud_init_applied_at,notes,provisioned_at,
       coalesce(vm_ip,''),coalesce(vm_type,''),coalesce(last_status,''),last_seen_at
FROM provisioned_vms ORDER BY provisioned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvisionedVM
	for rows.Next() {
		var vm ProvisionedVM
		if err := rows.Scan(
			&vm.ID, &vm.VMID, &vm.Name, &vm.Node, &vm.TemplateID,
			&vm.CloudInitProfile, &vm.SaltProfile,
			&vm.SaltAppliedAt, &vm.CloudInitAppliedAt,
			&vm.Notes, &vm.ProvisionedAt,
			&vm.VMIP, &vm.VMType, &vm.LastStatus, &vm.LastSeenAt,
		); err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, rows.Err()
}

// UpdateVMStatus updates the cached status/IP for a provisioned VM.
func (d *DB) UpdateVMStatus(vmid int, status, ip, vmType string) error {
	now := time.Now()
	_, err := d.sql.Exec(`
UPDATE provisioned_vms SET last_status=?, vm_ip=?, vm_type=?, last_seen_at=? WHERE vmid=?`,
		status, ip, vmType, now, vmid)
	return err
}

// GetSaltProfile returns a single salt profile by name, or nil if not found.
func (d *DB) GetSaltProfile(name string) (*SaltProfile, error) {
	var p SaltProfile
	err := d.sql.QueryRow(
		`SELECT id,name,target,states,notes,created_at,updated_at FROM salt_profiles WHERE name=?`, name,
	).Scan(&p.ID, &p.Name, &p.Target, &p.States, &p.Notes, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// ── Salt apply events ────────────────────────────────────────────────────────

// SaltApplyEvent records a single salt.state.apply call.
type SaltApplyEvent struct {
	ID          int64
	Minion      string
	States      string // "highstate" or comma-separated state list
	SaltProfile string // profile name if loaded from one, empty otherwise
	JID         string // Salt job ID returned by local_async
	Success     bool
	ErrorMsg    string
	TriggeredAt time.Time
}

// RecordSaltApply inserts a new salt apply event and returns its row ID.
func (d *DB) RecordSaltApply(ev SaltApplyEvent) (int64, error) {
	successInt := 1
	if !ev.Success {
		successInt = 0
	}
	res, err := d.sql.Exec(`
INSERT INTO salt_apply_events (minion, states, salt_profile, jid, success, error_msg, triggered_at)
VALUES (?,?,?,?,?,?,?)`,
		ev.Minion, ev.States, ev.SaltProfile, ev.JID, successInt, ev.ErrorMsg, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSaltApplyResult writes the polled result back into an existing event row.
func (d *DB) UpdateSaltApplyResult(id int64, success bool, errorMsg string) error {
	successInt := 1
	if !success {
		successInt = 0
	}
	_, err := d.sql.Exec(
		`UPDATE salt_apply_events SET success=?, error_msg=? WHERE id=?`,
		successInt, errorMsg, id)
	return err
}

// SaltApplyEventsForMinion returns the most recent events for a specific minion.
func (d *DB) SaltApplyEventsForMinion(minion string, limit int) ([]SaltApplyEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,minion,states,salt_profile,jid,success,error_msg,triggered_at
FROM salt_apply_events WHERE minion=?
ORDER BY triggered_at DESC LIMIT ?`, minion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSaltEvents(rows)
}

// RecentSaltApplyEvents returns the most recent events across all minions.
func (d *DB) RecentSaltApplyEvents(limit int) ([]SaltApplyEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,minion,states,salt_profile,jid,success,error_msg,triggered_at
FROM salt_apply_events ORDER BY triggered_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSaltEvents(rows)
}

func scanSaltEvents(rows *sql.Rows) ([]SaltApplyEvent, error) {
	var out []SaltApplyEvent
	for rows.Next() {
		var ev SaltApplyEvent
		var successInt int
		if err := rows.Scan(&ev.ID, &ev.Minion, &ev.States, &ev.SaltProfile,
			&ev.JID, &successInt, &ev.ErrorMsg, &ev.TriggeredAt); err != nil {
			return nil, err
		}
		ev.Success = successInt == 1
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── Profile usage counts ─────────────────────────────────────────────────────

// ProfileUsageCounts returns maps of profile-name → number of provisioned VMs
// that reference it (ci) and number of salt apply events that reference it (salt).
func (d *DB) ProfileUsageCounts() (ci map[string]int, salt map[string]int, err error) {
	ci = make(map[string]int)
	salt = make(map[string]int)

	rows, err := d.sql.Query(`
SELECT cloud_init_profile, count(*) FROM provisioned_vms
WHERE cloud_init_profile != '' GROUP BY cloud_init_profile`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, nil, err
		}
		ci[name] = n
	}
	rows.Close()

	rows2, err := d.sql.Query(`
SELECT salt_profile, count(*) FROM salt_apply_events
WHERE salt_profile != '' GROUP BY salt_profile`)
	if err != nil {
		return nil, nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var name string
		var n int
		if err := rows2.Scan(&name, &n); err != nil {
			return nil, nil, err
		}
		salt[name] = n
	}
	return ci, salt, rows2.Err()
}

// ── Nomad job tracking ───────────────────────────────────────────────────────

// NomadSyncEntry is the minimal payload the UI passes to SyncNomadState.
type NomadSyncEntry struct {
	JobID   string
	JobType string
	Status  string
	Running int
	Failed  int
	Lost    int
}

// NomadJobEvent is one recorded state-transition for a Nomad job.
type NomadJobEvent struct {
	ID         int64
	JobID      string
	JobType    string
	EventType  string // "first_seen", "started", "stopped", "degraded", "status_change"
	PrevStatus string
	NewStatus  string
	Running    int
	Failed     int
	Lost       int
	RecordedAt time.Time
}

// SyncNomadState compares the live jobs against the stored snapshots, records
// transition events, and updates the snapshots — all in one transaction.
// Returns the newly inserted events (empty slice means nothing changed).
func (d *DB) SyncNomadState(entries []NomadSyncEntry) ([]NomadJobEvent, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Load existing snapshots into a map.
	type snap struct {
		status  string
		running int
		failed  int
		lost    int
	}
	snapshots := map[string]snap{}
	rows, err := tx.Query(`SELECT job_id, status, running, failed, lost FROM nomad_job_snapshots`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s snap
		if err := rows.Scan(&id, &s.status, &s.running, &s.failed, &s.lost); err != nil {
			rows.Close()
			return nil, err
		}
		snapshots[id] = s
	}
	rows.Close()

	now := time.Now()
	var newEvents []NomadJobEvent

	for _, e := range entries {
		prev, known := snapshots[e.JobID]
		eventType := ""
		prevStatus := ""

		switch {
		case !known:
			eventType = "first_seen"
		case prev.status != e.Status:
			prevStatus = prev.status
			switch e.Status {
			case "running":
				eventType = "started"
			case "dead":
				eventType = "stopped"
			default:
				eventType = "status_change"
			}
		case e.Status == "running" && (e.Failed > prev.failed || e.Lost > prev.lost):
			eventType = "degraded"
		}

		if eventType != "" {
			res, err := tx.Exec(`
INSERT INTO nomad_job_events (job_id, job_type, event_type, prev_status, new_status, running, failed, lost, recorded_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
				e.JobID, e.JobType, eventType, prevStatus, e.Status,
				e.Running, e.Failed, e.Lost, now)
			if err != nil {
				return nil, err
			}
			id, _ := res.LastInsertId()
			newEvents = append(newEvents, NomadJobEvent{
				ID: id, JobID: e.JobID, JobType: e.JobType,
				EventType: eventType, PrevStatus: prevStatus, NewStatus: e.Status,
				Running: e.Running, Failed: e.Failed, Lost: e.Lost,
				RecordedAt: now,
			})
		}

		// Upsert snapshot.
		if _, err := tx.Exec(`
INSERT INTO nomad_job_snapshots (job_id, job_type, status, running, failed, lost, last_seen_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(job_id) DO UPDATE SET
    job_type=excluded.job_type, status=excluded.status,
    running=excluded.running, failed=excluded.failed, lost=excluded.lost,
    last_seen_at=excluded.last_seen_at`,
			e.JobID, e.JobType, e.Status, e.Running, e.Failed, e.Lost, now); err != nil {
			return nil, err
		}
	}

	return newEvents, tx.Commit()
}

// RecentNomadEvents returns the most recent Nomad job events across all jobs.
func (d *DB) RecentNomadEvents(limit int) ([]NomadJobEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,job_id,job_type,event_type,coalesce(prev_status,''),new_status,running,failed,lost,recorded_at
FROM nomad_job_events ORDER BY recorded_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNomadEvents(rows)
}

// NomadEventsForJob returns recent events for a single job.
func (d *DB) NomadEventsForJob(jobID string, limit int) ([]NomadJobEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,job_id,job_type,event_type,coalesce(prev_status,''),new_status,running,failed,lost,recorded_at
FROM nomad_job_events WHERE job_id=? ORDER BY recorded_at DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNomadEvents(rows)
}

func scanNomadEvents(rows *sql.Rows) ([]NomadJobEvent, error) {
	var out []NomadJobEvent
	for rows.Next() {
		var ev NomadJobEvent
		if err := rows.Scan(&ev.ID, &ev.JobID, &ev.JobType, &ev.EventType,
			&ev.PrevStatus, &ev.NewStatus,
			&ev.Running, &ev.Failed, &ev.Lost, &ev.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── Metrics ──────────────────────────────────────────────────────────────────

// ProfileCount pairs a profile name with how many times it has been used.
type ProfileCount struct {
	Name  string
	Count int
}

// MetricsSnapshot is a point-in-time view of all ratataskr DB stats.
type MetricsSnapshot struct {
	// Provisioned VMs
	ProvisionedTotal int
	VMsByStatus      map[string]int // last_status → count

	// Salt (last 24 h)
	SaltApplies24h     int
	SaltSuccesses24h   int
	SaltMinionActivity map[string]int // minion → apply count

	// Nomad (from snapshots table)
	NomadJobsRunning int
	NomadJobsDead    int
	NomadJobsPending int
	NomadEventsToday int

	// Top profiles by usage
	TopCIProfiles   []ProfileCount
	TopSaltProfiles []ProfileCount

	ComputedAt time.Time
}

// ComputeMetrics derives a MetricsSnapshot from the local SQLite DB.
func (d *DB) ComputeMetrics() (*MetricsSnapshot, error) {
	s := &MetricsSnapshot{
		VMsByStatus:        make(map[string]int),
		SaltMinionActivity: make(map[string]int),
		ComputedAt:         time.Now(),
	}

	// Total provisioned VMs + status breakdown
	rows, err := d.sql.Query(`
SELECT count(*), coalesce(last_status,'unknown') FROM provisioned_vms GROUP BY last_status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int
		var status string
		if err := rows.Scan(&n, &status); err != nil {
			rows.Close()
			return nil, err
		}
		s.ProvisionedTotal += n
		s.VMsByStatus[status] = n
	}
	rows.Close()

	// Salt applies in the last 24 h
	cutoff := time.Now().Add(-24 * time.Hour)
	rows, err = d.sql.Query(`
SELECT minion, success FROM salt_apply_events WHERE triggered_at >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var minion string
		var success int
		if err := rows.Scan(&minion, &success); err != nil {
			rows.Close()
			return nil, err
		}
		s.SaltApplies24h++
		if success == 1 {
			s.SaltSuccesses24h++
		}
		s.SaltMinionActivity[minion]++
	}
	rows.Close()

	// Nomad job status from snapshots
	rows, err = d.sql.Query(`SELECT status, count(*) FROM nomad_job_snapshots GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		switch status {
		case "running":
			s.NomadJobsRunning = n
		case "dead":
			s.NomadJobsDead = n
		case "pending":
			s.NomadJobsPending = n
		}
	}
	rows.Close()

	// Nomad events today
	today := time.Now().Truncate(24 * time.Hour)
	d.sql.QueryRow(`SELECT count(*) FROM nomad_job_events WHERE recorded_at >= ?`, today).
		Scan(&s.NomadEventsToday) //nolint:errcheck

	// Top cloud-init profiles
	rows, err = d.sql.Query(`
SELECT cloud_init_profile, count(*) AS n FROM provisioned_vms
WHERE cloud_init_profile != '' GROUP BY cloud_init_profile ORDER BY n DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p ProfileCount
		if err := rows.Scan(&p.Name, &p.Count); err != nil {
			rows.Close()
			return nil, err
		}
		s.TopCIProfiles = append(s.TopCIProfiles, p)
	}
	rows.Close()

	// Top salt profiles
	rows, err = d.sql.Query(`
SELECT salt_profile, count(*) AS n FROM salt_apply_events
WHERE salt_profile != '' GROUP BY salt_profile ORDER BY n DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p ProfileCount
		if err := rows.Scan(&p.Name, &p.Count); err != nil {
			rows.Close()
			return nil, err
		}
		s.TopSaltProfiles = append(s.TopSaltProfiles, p)
	}
	rows.Close()

	return s, nil
}

func (d *DB) GetProvisionedVM(vmid int) (*ProvisionedVM, error) {
	var vm ProvisionedVM
	err := d.sql.QueryRow(`
SELECT id,vmid,name,node,template_id,cloud_init_profile,salt_profile,
       salt_applied_at,cloud_init_applied_at,notes,provisioned_at,
       coalesce(vm_ip,''),coalesce(vm_type,''),coalesce(last_status,''),last_seen_at
FROM provisioned_vms WHERE vmid=? ORDER BY provisioned_at DESC LIMIT 1`, vmid).
		Scan(
			&vm.ID, &vm.VMID, &vm.Name, &vm.Node, &vm.TemplateID,
			&vm.CloudInitProfile, &vm.SaltProfile,
			&vm.SaltAppliedAt, &vm.CloudInitAppliedAt,
			&vm.Notes, &vm.ProvisionedAt,
			&vm.VMIP, &vm.VMType, &vm.LastStatus, &vm.LastSeenAt,
		)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

// ── Services ─────────────────────────────────────────────────────────────────

// Service is a tracked network service.
type Service struct {
	ID        int64
	Name      string
	Node      string
	Host      string
	Port      int
	Scheme    string // "tcp", "http", "https"
	Notes     string
	Enabled   bool
	CreatedAt time.Time
}

// HealthCheck is one recorded result from a health check.
type HealthCheck struct {
	ID         int64
	ServiceID  int64
	Up         bool
	LatencyMs  int64
	StatusCode int
	TLSExpiry  *time.Time
	ErrorMsg   string
	CheckedAt  time.Time
}

func (d *DB) SaveService(s Service) (int64, error) {
	res, err := d.sql.Exec(`
INSERT INTO services (name, node, host, port, scheme, notes, enabled, created_at)
VALUES (?,?,?,?,?,?,?,?)`,
		s.Name, s.Node, s.Host, s.Port, s.Scheme, s.Notes, boolInt(s.Enabled), time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) DeleteService(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM services WHERE id=?`, id)
	return err
}

func (d *DB) Services() ([]Service, error) {
	rows, err := d.sql.Query(`
SELECT id,name,node,host,port,scheme,notes,enabled,created_at
FROM services ORDER BY node, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var s Service
		var enabled int
		if err := rows.Scan(&s.ID, &s.Name, &s.Node, &s.Host, &s.Port,
			&s.Scheme, &s.Notes, &enabled, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Enabled = enabled == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordHealthCheck persists one result and returns its row ID.
func (d *DB) RecordHealthCheck(hc HealthCheck) (int64, error) {
	res, err := d.sql.Exec(`
INSERT INTO service_health_checks (service_id, up, latency_ms, status_code, tls_expiry, error_msg, checked_at)
VALUES (?,?,?,?,?,?,?)`,
		hc.ServiceID, boolInt(hc.Up), hc.LatencyMs, hc.StatusCode,
		hc.TLSExpiry, hc.ErrorMsg, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LatestHealthChecks returns the most recent check per service.
func (d *DB) LatestHealthChecks() (map[int64]HealthCheck, error) {
	rows, err := d.sql.Query(`
SELECT h.service_id, h.up, h.latency_ms, h.status_code, h.tls_expiry, h.error_msg, h.checked_at
FROM service_health_checks h
INNER JOIN (
    SELECT service_id, MAX(checked_at) AS max_at FROM service_health_checks GROUP BY service_id
) latest ON h.service_id=latest.service_id AND h.checked_at=latest.max_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]HealthCheck)
	for rows.Next() {
		var hc HealthCheck
		var up int
		if err := rows.Scan(&hc.ServiceID, &up, &hc.LatencyMs, &hc.StatusCode,
			&hc.TLSExpiry, &hc.ErrorMsg, &hc.CheckedAt); err != nil {
			return nil, err
		}
		hc.Up = up == 1
		out[hc.ServiceID] = hc
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── Certificates ─────────────────────────────────────────────────────────────

// Certificate is a discovered TLS certificate.
type Certificate struct {
	ID          int64
	Host        string
	Port        int
	CommonName  string
	SANNames    string // space-separated
	Issuer      string
	Serial      string
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string
	LastSeenAt  time.Time
}

// DaysUntilExpiry returns days until the cert expires (negative = already expired).
func (c Certificate) DaysUntilExpiry() int {
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// UpsertCertificate inserts or updates a discovered certificate.
func (d *DB) UpsertCertificate(c Certificate) error {
	_, err := d.sql.Exec(`
INSERT INTO certificates (host, port, common_name, san_names, issuer, serial,
                          not_before, not_after, fingerprint, last_seen_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(host, port, fingerprint) DO UPDATE SET
    common_name=excluded.common_name, san_names=excluded.san_names,
    issuer=excluded.issuer, serial=excluded.serial,
    not_before=excluded.not_before, not_after=excluded.not_after,
    last_seen_at=excluded.last_seen_at`,
		c.Host, c.Port, c.CommonName, c.SANNames, c.Issuer, c.Serial,
		c.NotBefore, c.NotAfter, c.Fingerprint, time.Now())
	return err
}

// Certificates returns all tracked certificates ordered by expiry.
func (d *DB) Certificates() ([]Certificate, error) {
	rows, err := d.sql.Query(`
SELECT id,host,port,common_name,san_names,issuer,serial,not_before,not_after,fingerprint,last_seen_at
FROM certificates ORDER BY not_after ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.ID, &c.Host, &c.Port, &c.CommonName, &c.SANNames,
			&c.Issuer, &c.Serial, &c.NotBefore, &c.NotAfter, &c.Fingerprint, &c.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── DNS cache ─────────────────────────────────────────────────────────────────

// DNSCacheRecord is one cached DNS record from Technitium.
type DNSCacheRecord struct {
	ID       int64
	Zone     string
	Name     string
	Type     string
	Value    string
	TTL      int
	SyncedAt time.Time
}

// SyncDNSRecords replaces all cached records for a zone.
func (d *DB) SyncDNSRecords(zone string, records []DNSCacheRecord) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM dns_records_cache WHERE zone=?`, zone); err != nil {
		return err
	}
	now := time.Now()
	for _, r := range records {
		if _, err := tx.Exec(`
INSERT OR IGNORE INTO dns_records_cache (zone, name, type, value, ttl, synced_at)
VALUES (?,?,?,?,?,?)`, r.Zone, r.Name, r.Type, r.Value, r.TTL, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DNSRecordsByZone returns cached records for a zone.
func (d *DB) DNSRecordsByZone(zone string) ([]DNSCacheRecord, error) {
	rows, err := d.sql.Query(`
SELECT id,zone,name,type,value,ttl,synced_at FROM dns_records_cache
WHERE zone=? ORDER BY name, type`, zone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSCacheRecord
	for rows.Next() {
		var r DNSCacheRecord
		if err := rows.Scan(&r.ID, &r.Zone, &r.Name, &r.Type, &r.Value, &r.TTL, &r.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── VM resize events ──────────────────────────────────────────────────────────

// ResizeEvent records one applied resource change.
type ResizeEvent struct {
	ID        int64
	VMID      int
	Node      string
	VMType    string
	Field     string // "cpu", "memory", "disk"
	OldValue  string
	NewValue  string
	Reason    string // "manual" or "recommended"
	AppliedAt time.Time
}

// RecordResizeEvent persists a resize action.
func (d *DB) RecordResizeEvent(ev ResizeEvent) error {
	_, err := d.sql.Exec(`
INSERT INTO vm_resize_events (vmid, node, vm_type, field, old_value, new_value, reason, applied_at)
VALUES (?,?,?,?,?,?,?,?)`,
		ev.VMID, ev.Node, ev.VMType, ev.Field, ev.OldValue, ev.NewValue, ev.Reason, time.Now())
	return err
}

// ResizeHistory returns the most recent resize events, optionally filtered by vmid (0 = all).
func (d *DB) ResizeHistory(vmid, limit int) ([]ResizeEvent, error) {
	var rows *sql.Rows
	var err error
	if vmid > 0 {
		rows, err = d.sql.Query(`
SELECT id,vmid,node,vm_type,field,old_value,new_value,reason,applied_at
FROM vm_resize_events WHERE vmid=? ORDER BY applied_at DESC LIMIT ?`, vmid, limit)
	} else {
		rows, err = d.sql.Query(`
SELECT id,vmid,node,vm_type,field,old_value,new_value,reason,applied_at
FROM vm_resize_events ORDER BY applied_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResizeEvent
	for rows.Next() {
		var ev ResizeEvent
		if err := rows.Scan(&ev.ID, &ev.VMID, &ev.Node, &ev.VMType,
			&ev.Field, &ev.OldValue, &ev.NewValue, &ev.Reason, &ev.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── PXE profiles ──────────────────────────────────────────────────────────────

// PXEProfile is a stored PXE boot profile.
type PXEProfile struct {
	ID          string
	Name        string
	Description string
	KernelURL   string
	InitrdURLs  string // JSON array
	KernelArgs  string
	Template    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (d *DB) SavePXEProfile(p PXEProfile) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	_, err := d.sql.Exec(`
INSERT INTO pxe_profiles (id,name,description,kernel_url,initrd_urls,kernel_args,template,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    name=excluded.name, description=excluded.description,
    kernel_url=excluded.kernel_url, initrd_urls=excluded.initrd_urls,
    kernel_args=excluded.kernel_args, template=excluded.template,
    updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Description, p.KernelURL, p.InitrdURLs, p.KernelArgs,
		p.Template, p.CreatedAt, now)
	return err
}

func (d *DB) DeletePXEProfile(id string) error {
	_, err := d.sql.Exec(`DELETE FROM pxe_profiles WHERE id=?`, id)
	return err
}

func (d *DB) PXEProfiles() ([]PXEProfile, error) {
	rows, err := d.sql.Query(`SELECT id,name,description,kernel_url,initrd_urls,kernel_args,template,created_at,updated_at FROM pxe_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PXEProfile
	for rows.Next() {
		var p PXEProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.KernelURL,
			&p.InitrdURLs, &p.KernelArgs, &p.Template, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── PXE groups ────────────────────────────────────────────────────────────────

// PXEGroup assigns a profile to machines matched by MAC/selector.
type PXEGroup struct {
	ID         string
	Name       string
	ProfileID  string
	MACAddress string
	Selector   string // JSON
	Metadata   string // JSON
	CreatedAt  time.Time
}

func (d *DB) SavePXEGroup(g PXEGroup) error {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	_, err := d.sql.Exec(`
INSERT INTO pxe_groups (id,name,profile_id,mac_address,selector,metadata,created_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    name=excluded.name, profile_id=excluded.profile_id,
    mac_address=excluded.mac_address, selector=excluded.selector, metadata=excluded.metadata`,
		g.ID, g.Name, g.ProfileID, g.MACAddress, g.Selector, g.Metadata, g.CreatedAt)
	return err
}

func (d *DB) DeletePXEGroup(id string) error {
	_, err := d.sql.Exec(`DELETE FROM pxe_groups WHERE id=?`, id)
	return err
}

func (d *DB) PXEGroups() ([]PXEGroup, error) {
	rows, err := d.sql.Query(`SELECT id,name,profile_id,mac_address,selector,metadata,created_at FROM pxe_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PXEGroup
	for rows.Next() {
		var g PXEGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.ProfileID, &g.MACAddress,
			&g.Selector, &g.Metadata, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ── PXE boot events ───────────────────────────────────────────────────────────

// PXEBootEvent records one observed PXE boot.
type PXEBootEvent struct {
	ID         int64
	MACAddress string
	IPAddress  string
	ProfileID  string
	GroupID    string
	Hostname   string
	BootedAt   time.Time
}

func (d *DB) RecordPXEBoot(ev PXEBootEvent) error {
	_, err := d.sql.Exec(`
INSERT INTO pxe_boot_events (mac_address,ip_address,profile_id,group_id,hostname,booted_at)
VALUES (?,?,?,?,?,?)`,
		ev.MACAddress, ev.IPAddress, ev.ProfileID, ev.GroupID, ev.Hostname, time.Now())
	return err
}

func (d *DB) RecentPXEBoots(limit int) ([]PXEBootEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,mac_address,ip_address,profile_id,group_id,hostname,booted_at
FROM pxe_boot_events ORDER BY booted_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PXEBootEvent
	for rows.Next() {
		var ev PXEBootEvent
		if err := rows.Scan(&ev.ID, &ev.MACAddress, &ev.IPAddress, &ev.ProfileID,
			&ev.GroupID, &ev.Hostname, &ev.BootedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── Security events ───────────────────────────────────────────────────────────

// SecurityEvent is a recorded security alert from CrowdSec or Wazuh.
type SecurityEvent struct {
	ID          int64
	Source      string // "crowdsec" or "wazuh"
	EventType   string // "ban", "alert", "agent_down", "agent_up"
	Severity    string // "low", "medium", "high", "critical"
	Description string
	Target      string
	RawData     string
	RecordedAt  time.Time
}

func (d *DB) RecordSecurityEvent(ev SecurityEvent) error {
	_, err := d.sql.Exec(`
INSERT INTO security_events (source,event_type,severity,description,target,raw_data,recorded_at)
VALUES (?,?,?,?,?,?,?)`,
		ev.Source, ev.EventType, ev.Severity, ev.Description,
		ev.Target, ev.RawData, time.Now())
	return err
}

func (d *DB) RecentSecurityEvents(limit int) ([]SecurityEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,source,event_type,severity,description,target,raw_data,recorded_at
FROM security_events ORDER BY recorded_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecurityEvent
	for rows.Next() {
		var ev SecurityEvent
		if err := rows.Scan(&ev.ID, &ev.Source, &ev.EventType, &ev.Severity,
			&ev.Description, &ev.Target, &ev.RawData, &ev.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (d *DB) SecurityEventsBySource(source string, limit int) ([]SecurityEvent, error) {
	rows, err := d.sql.Query(`
SELECT id,source,event_type,severity,description,target,raw_data,recorded_at
FROM security_events WHERE source=? ORDER BY recorded_at DESC LIMIT ?`, source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecurityEvent
	for rows.Next() {
		var ev SecurityEvent
		if err := rows.Scan(&ev.ID, &ev.Source, &ev.EventType, &ev.Severity,
			&ev.Description, &ev.Target, &ev.RawData, &ev.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── Per-node SSH credentials ──────────────────────────────────────────────────

// NodeSSHCred holds per-node SSH login info; the password lives in the macOS Keychain.
type NodeSSHCred struct {
	Node       string
	SSHUser    string
	KeyringKey string // key passed to keyring.Load/Save to retrieve the password
}

func (d *DB) SaveNodeSSHCred(c NodeSSHCred) error {
	_, err := d.sql.Exec(`
INSERT INTO node_ssh_credentials (node, ssh_user, keyring_key)
VALUES (?,?,?)
ON CONFLICT(node) DO UPDATE SET ssh_user=excluded.ssh_user, keyring_key=excluded.keyring_key`,
		c.Node, c.SSHUser, c.KeyringKey)
	return err
}

func (d *DB) DeleteNodeSSHCred(node string) error {
	_, err := d.sql.Exec(`DELETE FROM node_ssh_credentials WHERE node=?`, node)
	return err
}

func (d *DB) NodeSSHCreds() ([]NodeSSHCred, error) {
	rows, err := d.sql.Query(`SELECT node, ssh_user, keyring_key FROM node_ssh_credentials ORDER BY node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeSSHCred
	for rows.Next() {
		var c NodeSSHCred
		if err := rows.Scan(&c.Node, &c.SSHUser, &c.KeyringKey); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetNodeSSHCred(node string) (*NodeSSHCred, error) {
	var c NodeSSHCred
	err := d.sql.QueryRow(`SELECT node, ssh_user, keyring_key FROM node_ssh_credentials WHERE node=?`, node).
		Scan(&c.Node, &c.SSHUser, &c.KeyringKey)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

// ── Security scan results ─────────────────────────────────────────────────────

// ScanResult is one finding from a port scan or VM firewall audit.
type ScanResult struct {
	ID          int64
	Target      string
	Node        string
	VMID        int
	ScanType    string // "port", "vm_firewall"
	CheckName   string
	Status      string // "pass", "warn", "fail", "info"
	Severity    string // "info", "low", "medium", "high", "critical"
	Detail      string
	Remediation string
	RemAction   string // programmatic action: "enable_vm_firewall", ""
	Remediated  bool
	ScannedAt   time.Time
}

func (d *DB) SaveScanResults(results []ScanResult) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now()
	for _, r := range results {
		if _, err := tx.Exec(`
INSERT INTO scan_results (target, node, vmid, scan_type, check_name, status, severity,
                          detail, remediation, rem_action, remediated, scanned_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Target, r.Node, r.VMID, r.ScanType, r.CheckName, r.Status, r.Severity,
			r.Detail, r.Remediation, r.RemAction, boolInt(r.Remediated), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) RecentScanResults(limit int) ([]ScanResult, error) {
	rows, err := d.sql.Query(`
SELECT id,target,node,vmid,scan_type,check_name,status,severity,detail,remediation,rem_action,remediated,scanned_at
FROM scan_results ORDER BY scanned_at DESC, severity DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResultRows(rows)
}

func (d *DB) ScanResultsForTarget(target string, limit int) ([]ScanResult, error) {
	rows, err := d.sql.Query(`
SELECT id,target,node,vmid,scan_type,check_name,status,severity,detail,remediation,rem_action,remediated,scanned_at
FROM scan_results WHERE target=? ORDER BY scanned_at DESC, severity DESC LIMIT ?`, target, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResultRows(rows)
}

func (d *DB) MarkScanResultRemediated(id int64) error {
	_, err := d.sql.Exec(`UPDATE scan_results SET remediated=1 WHERE id=?`, id)
	return err
}

func (d *DB) ClearScanResults() error {
	_, err := d.sql.Exec(`DELETE FROM scan_results`)
	return err
}

// ── Service hosts ─────────────────────────────────────────────────────────────

// ServiceHost tracks the management status of a service endpoint's host.
type ServiceHost struct {
	Host             string
	VMID             int
	Node             string
	Managed          bool   // true = managed mode (Salt + security agents)
	Registered       bool   // true = onboarded into ratataskr
	ProvisionPending bool   // true = provision has been requested
	RegistrationType string // "managed" | "unmanaged"
	SaltApplied      bool
	WazuhEnrolled    bool
	DiscoveredPorts  string // JSON: [{port,service}, ...]
	Notes            string
	RegisteredAt     *time.Time
	LastDiscovered   *time.Time
}

func (d *DB) SaveServiceHost(h ServiceHost) error {
	_, err := d.sql.Exec(`
INSERT INTO service_hosts
    (host, vmid, node, managed, registered, provision_pending, registration_type,
     salt_applied, wazuh_enrolled, discovered_ports, notes, registered_at, last_discovered)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(host) DO UPDATE SET
    vmid=excluded.vmid, node=excluded.node,
    managed=excluded.managed, registered=excluded.registered,
    provision_pending=excluded.provision_pending,
    registration_type=excluded.registration_type,
    salt_applied=excluded.salt_applied, wazuh_enrolled=excluded.wazuh_enrolled,
    discovered_ports=excluded.discovered_ports, notes=excluded.notes,
    registered_at=excluded.registered_at, last_discovered=excluded.last_discovered`,
		h.Host, h.VMID, h.Node, boolInt(h.Managed), boolInt(h.Registered),
		boolInt(h.ProvisionPending), h.RegistrationType,
		boolInt(h.SaltApplied), boolInt(h.WazuhEnrolled),
		h.DiscoveredPorts, h.Notes, h.RegisteredAt, h.LastDiscovered)
	return err
}

func (d *DB) GetServiceHost(host string) (*ServiceHost, error) {
	var h ServiceHost
	var managed, registered, provPending, saltApplied, wazuhEnrolled int
	err := d.sql.QueryRow(`
SELECT host,vmid,node,managed,registered,provision_pending,registration_type,
       salt_applied,wazuh_enrolled,discovered_ports,notes,registered_at,last_discovered
FROM service_hosts WHERE host=?`, host).Scan(
		&h.Host, &h.VMID, &h.Node, &managed, &registered, &provPending,
		&h.RegistrationType, &saltApplied, &wazuhEnrolled,
		&h.DiscoveredPorts, &h.Notes, &h.RegisteredAt, &h.LastDiscovered)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.Managed = managed == 1
	h.Registered = registered == 1
	h.ProvisionPending = provPending == 1
	h.SaltApplied = saltApplied == 1
	h.WazuhEnrolled = wazuhEnrolled == 1
	return &h, nil
}

func (d *DB) ServiceHosts() (map[string]ServiceHost, error) {
	rows, err := d.sql.Query(`
SELECT host,vmid,node,managed,registered,provision_pending,registration_type,
       salt_applied,wazuh_enrolled,discovered_ports,notes,registered_at,last_discovered
FROM service_hosts ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ServiceHost)
	for rows.Next() {
		var h ServiceHost
		var managed, registered, provPending, saltApplied, wazuhEnrolled int
		if err := rows.Scan(
			&h.Host, &h.VMID, &h.Node, &managed, &registered, &provPending,
			&h.RegistrationType, &saltApplied, &wazuhEnrolled,
			&h.DiscoveredPorts, &h.Notes, &h.RegisteredAt, &h.LastDiscovered); err != nil {
			return nil, err
		}
		h.Managed = managed == 1
		h.Registered = registered == 1
		h.ProvisionPending = provPending == 1
		h.SaltApplied = saltApplied == 1
		h.WazuhEnrolled = wazuhEnrolled == 1
		out[h.Host] = h
	}
	return out, rows.Err()
}

// ProvisionedVMByIP looks for a provisioned VM whose vm_ip matches the given address.
func (d *DB) ProvisionedVMByIP(ip string) (*ProvisionedVM, error) {
	var vm ProvisionedVM
	err := d.sql.QueryRow(`
SELECT id,vmid,name,node,template_id,cloud_init_profile,salt_profile,
       salt_applied_at,cloud_init_applied_at,notes,provisioned_at,
       coalesce(vm_ip,''),coalesce(vm_type,''),coalesce(last_status,''),last_seen_at
FROM provisioned_vms WHERE vm_ip=? ORDER BY provisioned_at DESC LIMIT 1`, ip).Scan(
		&vm.ID, &vm.VMID, &vm.Name, &vm.Node, &vm.TemplateID,
		&vm.CloudInitProfile, &vm.SaltProfile,
		&vm.SaltAppliedAt, &vm.CloudInitAppliedAt,
		&vm.Notes, &vm.ProvisionedAt,
		&vm.VMIP, &vm.VMType, &vm.LastStatus, &vm.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &vm, err
}

// UpdateDiscoveredPorts updates the discovered_ports and last_discovered fields for a service host.
// If the host row does not yet exist the update is a no-op (caller should SaveServiceHost first).
func (d *DB) UpdateDiscoveredPorts(host, ports string) error {
	now := time.Now()
	_, err := d.sql.Exec(
		`UPDATE service_hosts SET discovered_ports=?, last_discovered=? WHERE host=?`,
		ports, now, host)
	return err
}

// RecentBannedHosts returns the IPs/hostnames that have had a CrowdSec ban recorded in
// the security_events table within the last `hours` hours.
func (d *DB) RecentBannedHosts(hours int) ([]string, error) {
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	rows, err := d.sql.Query(
		`SELECT DISTINCT target FROM security_events
         WHERE source='crowdsec' AND event_type='ban' AND recorded_at > ?
         ORDER BY target`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ── Maintenance ───────────────────────────────────────────────────────────────

// MaintenanceTaskFlags describes which tasks a maintenance run should perform.
type MaintenanceTaskFlags struct {
	OSUpdates       bool `json:"os_updates"`
	PkgUpdates      bool `json:"pkg_updates"`
	ProxmoxUpgrade  bool `json:"proxmox_upgrade"`
	Reboot          bool `json:"reboot"`
}

// MaintenanceSchedule is a repeating or one-shot maintenance schedule.
type MaintenanceSchedule struct {
	ID        int64
	Name      string
	RunAt     *time.Time
	Repeat    string // "weekly" | "monthly" | "" (once)
	NodeOrder string // JSON []string
	TaskFlags string // JSON MaintenanceTaskFlags
	HAAware   bool
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MaintenanceRun is a single execution of a maintenance run.
type MaintenanceRun struct {
	ID          int64
	ScheduleID  int64
	Name        string
	Status      string // pending|running|done|failed|cancelled
	NodeOrder   string // JSON []string — snapshot of node order at run time
	TaskFlags   string // JSON MaintenanceTaskFlags
	HAAware     bool
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
}

// MaintenanceRunStep is a single task step within a maintenance run.
type MaintenanceRunStep struct {
	ID        int64
	RunID     int64
	Node      string
	Task      string // evacuate|os_update|pkg_update|proxmox_upgrade|reboot|unevacuate
	Status    string // pending|running|done|skipped|failed
	Output    string
	StartedAt *time.Time
	DoneAt    *time.Time
}

func (d *DB) SaveMaintenanceSchedule(s MaintenanceSchedule) (int64, error) {
	now := time.Now()
	if s.ID == 0 {
		res, err := d.sql.Exec(`
INSERT INTO maintenance_schedules
    (name, run_at, repeat, node_order, task_flags, ha_aware, enabled, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
			s.Name, s.RunAt, s.Repeat, s.NodeOrder, s.TaskFlags,
			boolInt(s.HAAware), boolInt(s.Enabled), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := d.sql.Exec(`
UPDATE maintenance_schedules SET
    name=?, run_at=?, repeat=?, node_order=?, task_flags=?,
    ha_aware=?, enabled=?, updated_at=?
WHERE id=?`,
		s.Name, s.RunAt, s.Repeat, s.NodeOrder, s.TaskFlags,
		boolInt(s.HAAware), boolInt(s.Enabled), now, s.ID)
	return s.ID, err
}

func (d *DB) MaintenanceSchedules() ([]MaintenanceSchedule, error) {
	rows, err := d.sql.Query(`
SELECT id,name,run_at,repeat,node_order,task_flags,ha_aware,enabled,created_at,updated_at
FROM maintenance_schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceSchedule
	for rows.Next() {
		var s MaintenanceSchedule
		var haAware, enabled int
		if err := rows.Scan(&s.ID, &s.Name, &s.RunAt, &s.Repeat, &s.NodeOrder, &s.TaskFlags,
			&haAware, &enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.HAAware = haAware == 1
		s.Enabled = enabled == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) DeleteMaintenanceSchedule(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM maintenance_schedules WHERE id=?`, id)
	return err
}

func (d *DB) CreateMaintenanceRun(r MaintenanceRun) (int64, error) {
	res, err := d.sql.Exec(`
INSERT INTO maintenance_runs
    (schedule_id, name, status, node_order, task_flags, ha_aware, created_at)
VALUES (?,?,?,?,?,?,?)`,
		r.ScheduleID, r.Name, r.Status, r.NodeOrder, r.TaskFlags,
		boolInt(r.HAAware), time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateMaintenanceRunStatus(id int64, status string, completedAt *time.Time) error {
	_, err := d.sql.Exec(`
UPDATE maintenance_runs SET status=?, completed_at=? WHERE id=?`,
		status, completedAt, id)
	return err
}

func (d *DB) StartMaintenanceRun(id int64) error {
	now := time.Now()
	_, err := d.sql.Exec(`UPDATE maintenance_runs SET status='running', started_at=? WHERE id=?`, now, id)
	return err
}

func (d *DB) RecentMaintenanceRuns(limit int) ([]MaintenanceRun, error) {
	rows, err := d.sql.Query(`
SELECT id,schedule_id,name,status,node_order,task_flags,ha_aware,started_at,completed_at,created_at
FROM maintenance_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceRun
	for rows.Next() {
		var r MaintenanceRun
		var haAware int
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.Name, &r.Status, &r.NodeOrder, &r.TaskFlags,
			&haAware, &r.StartedAt, &r.CompletedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.HAAware = haAware == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) MaintenanceRunSteps(runID int64) ([]MaintenanceRunStep, error) {
	rows, err := d.sql.Query(`
SELECT id,run_id,node,task,status,output,started_at,done_at
FROM maintenance_run_steps WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceRunStep
	for rows.Next() {
		var s MaintenanceRunStep
		if err := rows.Scan(&s.ID, &s.RunID, &s.Node, &s.Task, &s.Status,
			&s.Output, &s.StartedAt, &s.DoneAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) CreateMaintenanceSteps(runID int64, steps []MaintenanceRunStep) error {
	for _, s := range steps {
		_, err := d.sql.Exec(`
INSERT INTO maintenance_run_steps (run_id, node, task, status, output)
VALUES (?,?,?,?,?)`, runID, s.Node, s.Task, s.Status, s.Output)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) UpdateMaintenanceStep(stepID int64, status, output string) error {
	var doneAt *time.Time
	if status == "done" || status == "failed" || status == "skipped" {
		t := time.Now()
		doneAt = &t
	}
	now := time.Now()
	_, err := d.sql.Exec(`
UPDATE maintenance_run_steps SET status=?, output=?, started_at=COALESCE(started_at,?), done_at=? WHERE id=?`,
		status, output, now, doneAt, stepID)
	return err
}

func scanResultRows(rows *sql.Rows) ([]ScanResult, error) {
	var out []ScanResult
	for rows.Next() {
		var r ScanResult
		var remediated int
		if err := rows.Scan(&r.ID, &r.Target, &r.Node, &r.VMID, &r.ScanType, &r.CheckName,
			&r.Status, &r.Severity, &r.Detail, &r.Remediation, &r.RemAction,
			&remediated, &r.ScannedAt); err != nil {
			return nil, err
		}
		r.Remediated = remediated == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Node metrics history ─────────────────────────────────────────────────────

type NodeMetricsPoint struct {
	Node      string
	CPURatio  float64
	MemRatio  float64
	MemUsedMB int64
	MemMaxMB  int64
	RecordedAt time.Time
}

func (d *DB) RecordNodeMetrics(node string, cpuRatio, memRatio float64, memUsedMB, memMaxMB int64) error {
	_, err := d.sql.Exec(`
INSERT INTO node_metrics_history (node, cpu_ratio, mem_ratio, mem_used_mb, mem_max_mb, recorded_at)
VALUES (?,?,?,?,?,?)`, node, cpuRatio, memRatio, memUsedMB, memMaxMB, time.Now())
	return err
}

// NodeMetricsHistory returns the last n CPU/mem samples for a node.
func (d *DB) NodeMetricsHistory(node string, n int) ([]NodeMetricsPoint, error) {
	rows, err := d.sql.Query(`
SELECT node, cpu_ratio, mem_ratio, mem_used_mb, mem_max_mb, recorded_at
FROM node_metrics_history WHERE node=?
ORDER BY recorded_at DESC LIMIT ?`, node, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeMetricsPoint
	for rows.Next() {
		var p NodeMetricsPoint
		if err := rows.Scan(&p.Node, &p.CPURatio, &p.MemRatio, &p.MemUsedMB, &p.MemMaxMB, &p.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	// Return oldest-first for sparkline rendering
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PruneNodeMetrics deletes history older than maxAge.
func (d *DB) PruneNodeMetrics(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)
	_, err := d.sql.Exec(`DELETE FROM node_metrics_history WHERE recorded_at < ?`, cutoff)
	return err
}

// ── Service uptime ───────────────────────────────────────────────────────────

func (d *DB) RecordServiceUptime(serviceID int64, up bool) error {
	v := 0
	if up {
		v = 1
	}
	_, err := d.sql.Exec(`
INSERT INTO service_uptime_events (service_id, up, checked_at) VALUES (?,?,?)`,
		serviceID, v, time.Now())
	return err
}

// ServiceUptimePct returns the uptime percentage (0-1) for a service over the last 24h.
func (d *DB) ServiceUptimePct(serviceID int64) float64 {
	cutoff := time.Now().Add(-24 * time.Hour)
	var total, upCount int
	d.sql.QueryRow(`SELECT COUNT(*), SUM(up) FROM service_uptime_events WHERE service_id=? AND checked_at>?`,
		serviceID, cutoff).Scan(&total, &upCount) //nolint:errcheck
	if total == 0 {
		return -1 // no data
	}
	return float64(upCount) / float64(total)
}

// ── Runbooks ─────────────────────────────────────────────────────────────────

type Runbook struct {
	ID          int64
	Name        string
	Description string
	StepsJSON   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RunbookRun struct {
	ID        int64
	RunbookID int64
	Status    string
	Output    string
	StartedAt *time.Time
	DoneAt    *time.Time
	CreatedAt time.Time
}

func (d *DB) SaveRunbook(rb Runbook) (int64, error) {
	now := time.Now()
	if rb.ID == 0 {
		res, err := d.sql.Exec(`
INSERT INTO runbooks (name, description, steps_json, created_at, updated_at) VALUES (?,?,?,?,?)`,
			rb.Name, rb.Description, rb.StepsJSON, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := d.sql.Exec(`
UPDATE runbooks SET name=?, description=?, steps_json=?, updated_at=? WHERE id=?`,
		rb.Name, rb.Description, rb.StepsJSON, now, rb.ID)
	return rb.ID, err
}

func (d *DB) DeleteRunbook(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM runbooks WHERE id=?`, id)
	return err
}

func (d *DB) Runbooks() ([]Runbook, error) {
	rows, err := d.sql.Query(`SELECT id,name,description,steps_json,created_at,updated_at FROM runbooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runbook
	for rows.Next() {
		var rb Runbook
		if err := rows.Scan(&rb.ID, &rb.Name, &rb.Description, &rb.StepsJSON, &rb.CreatedAt, &rb.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rb)
	}
	return out, rows.Err()
}

func (d *DB) CreateRunbookRun(runbookID int64) (int64, error) {
	res, err := d.sql.Exec(`
INSERT INTO runbook_runs (runbook_id, status, output, created_at) VALUES (?,?,?,?)`,
		runbookID, "pending", "", time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateRunbookRun(runID int64, status, output string) error {
	var doneAt *time.Time
	var startedAt *time.Time
	now := time.Now()
	if status == "running" {
		startedAt = &now
	}
	if status == "done" || status == "failed" {
		doneAt = &now
	}
	_, err := d.sql.Exec(`
UPDATE runbook_runs SET status=?, output=?, started_at=COALESCE(started_at,?), done_at=? WHERE id=?`,
		status, output, startedAt, doneAt, runID)
	return err
}

func (d *DB) RecentRunbookRuns(runbookID int64, limit int) ([]RunbookRun, error) {
	rows, err := d.sql.Query(`
SELECT id, runbook_id, status, output, started_at, done_at, created_at
FROM runbook_runs WHERE runbook_id=? ORDER BY created_at DESC LIMIT ?`, runbookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunbookRun
	for rows.Next() {
		var r RunbookRun
		if err := rows.Scan(&r.ID, &r.RunbookID, &r.Status, &r.Output, &r.StartedAt, &r.DoneAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Alert routes ──────────────────────────────────────────────────────────────

type AlertRoute struct {
	ID         int64
	Name       string
	Source     string
	Severity   string
	WebhookURL string
	Enabled    bool
	LastFired  *time.Time
	CreatedAt  time.Time
}

func (d *DB) SaveAlertRoute(r AlertRoute) (int64, error) {
	now := time.Now()
	if r.ID == 0 {
		res, err := d.sql.Exec(`
INSERT INTO alert_routes (name, source, severity, webhook_url, enabled, created_at) VALUES (?,?,?,?,?,?)`,
			r.Name, r.Source, r.Severity, r.WebhookURL, boolInt(r.Enabled), now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := d.sql.Exec(`
UPDATE alert_routes SET name=?, source=?, severity=?, webhook_url=?, enabled=? WHERE id=?`,
		r.Name, r.Source, r.Severity, r.WebhookURL, boolInt(r.Enabled), r.ID)
	return r.ID, err
}

func (d *DB) DeleteAlertRoute(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM alert_routes WHERE id=?`, id)
	return err
}

func (d *DB) AlertRoutes() ([]AlertRoute, error) {
	rows, err := d.sql.Query(`SELECT id,name,source,severity,webhook_url,enabled,last_fired,created_at FROM alert_routes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRoute
	for rows.Next() {
		var r AlertRoute
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &r.Source, &r.Severity, &r.WebhookURL, &enabled, &r.LastFired, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) TouchAlertRoute(id int64) error {
	now := time.Now()
	_, err := d.sql.Exec(`UPDATE alert_routes SET last_fired=? WHERE id=?`, now, id)
	return err
}

// ── VM lifecycle policies ─────────────────────────────────────────────────────

type VMLifecyclePolicy struct {
	ID        int64
	Name      string
	Tag       string
	Action    string
	IdleHours int
	Enabled   bool
	LastRun   *time.Time
	CreatedAt time.Time
}

func (d *DB) SaveLifecyclePolicy(p VMLifecyclePolicy) (int64, error) {
	now := time.Now()
	if p.ID == 0 {
		res, err := d.sql.Exec(`
INSERT INTO vm_lifecycle_policies (name, tag, action, idle_hours, enabled, created_at) VALUES (?,?,?,?,?,?)`,
			p.Name, p.Tag, p.Action, p.IdleHours, boolInt(p.Enabled), now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := d.sql.Exec(`
UPDATE vm_lifecycle_policies SET name=?, tag=?, action=?, idle_hours=?, enabled=? WHERE id=?`,
		p.Name, p.Tag, p.Action, p.IdleHours, boolInt(p.Enabled), p.ID)
	return p.ID, err
}

func (d *DB) DeleteLifecyclePolicy(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM vm_lifecycle_policies WHERE id=?`, id)
	return err
}

func (d *DB) LifecyclePolicies() ([]VMLifecyclePolicy, error) {
	rows, err := d.sql.Query(`SELECT id,name,tag,action,idle_hours,enabled,last_run,created_at FROM vm_lifecycle_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VMLifecyclePolicy
	for rows.Next() {
		var p VMLifecyclePolicy
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Tag, &p.Action, &p.IdleHours, &enabled, &p.LastRun, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) TouchLifecyclePolicy(id int64) error {
	now := time.Now()
	_, err := d.sql.Exec(`UPDATE vm_lifecycle_policies SET last_run=? WHERE id=?`, now, id)
	return err
}
