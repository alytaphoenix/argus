package db

import (
	"database/sql"
	"testing"
	"time"
)

// openTestDB creates an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:?_journal=WAL")
	if err != nil {
		t.Fatalf("open in-memory DB: %v", err)
	}
	d := &DB{sql: conn}
	if err := d.migrate(); err != nil {
		conn.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return d
}

// ── Migration smoke test ───────────────────────────────────────────────────────

func TestMigrate(t *testing.T) {
	// openTestDB runs all migrations — if they succeed without error, the test passes.
	db := openTestDB(t)
	if db == nil {
		t.Fatal("openTestDB returned nil")
	}

	// Verify key tables exist by querying them.
	tables := []string{
		"cloud_init_profiles",
		"salt_profiles",
		"provisioned_vms",
		"salt_apply_events",
		"nomad_job_events",
		"services",
		"pxe_profiles",
		"pxe_groups",
		"security_events",
		"service_hosts",
		"app_settings",
		"maintenance_schedules",
		"maintenance_runs",
		"maintenance_run_steps",
	}
	for _, tbl := range tables {
		var n int
		err := db.sql.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n)
		if err != nil {
			t.Errorf("table %q: %v", tbl, err)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTestDB(t)
	// Running migrate a second time should not fail (all CREATE TABLE IF NOT EXISTS).
	if err := db.migrate(); err != nil {
		t.Errorf("second migrate: %v", err)
	}
}

// ── App settings ──────────────────────────────────────────────────────────────

func TestDefaultSettingsSeeded(t *testing.T) {
	db := openTestDB(t)
	settings, err := db.AllSettings()
	if err != nil {
		t.Fatalf("AllSettings: %v", err)
	}
	if len(settings) == 0 {
		t.Fatal("expected default settings to be seeded, got none")
	}
	// All expected default keys should exist.
	keys := make(map[string]string)
	for _, s := range settings {
		keys[s.Key] = s.Value
	}
	for _, def := range defaultSettings {
		if _, ok := keys[def.key]; !ok {
			t.Errorf("default setting %q not seeded", def.key)
		}
	}
}

func TestGetSetSetting(t *testing.T) {
	db := openTestDB(t)

	// Get existing key (seeded by migrate).
	val := db.GetSetting("wazuh.critical_level", "0")
	if val == "" || val == "0" {
		t.Errorf("GetSetting(wazuh.critical_level): got %q, expected a non-zero default", val)
	}

	// Set and re-get.
	if err := db.SetSetting("wazuh.critical_level", "15"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got := db.GetSetting("wazuh.critical_level", "0")
	if got != "15" {
		t.Errorf("after SetSetting, GetSetting = %q, want 15", got)
	}
}

func TestGetSettingMissingKey(t *testing.T) {
	db := openTestDB(t)
	val := db.GetSetting("nonexistent.key", "fallback")
	if val != "fallback" {
		t.Errorf("GetSetting(missing) = %q, want fallback", val)
	}
}

func TestLoadAppSettings(t *testing.T) {
	db := openTestDB(t)
	settings := db.LoadAppSettings()

	// Check defaults are reasonable.
	if settings.WazuhCriticalLevel <= 0 {
		t.Errorf("WazuhCriticalLevel = %d, want > 0", settings.WazuhCriticalLevel)
	}
	if settings.WazuhHighLevel <= 0 {
		t.Errorf("WazuhHighLevel = %d, want > 0", settings.WazuhHighLevel)
	}
	if settings.WazuhMediumLevel <= 0 {
		t.Errorf("WazuhMediumLevel = %d, want > 0", settings.WazuhMediumLevel)
	}
	if settings.ScanTimeoutMs <= 0 {
		t.Errorf("ScanTimeoutMs = %d, want > 0", settings.ScanTimeoutMs)
	}
	if settings.BanLookbackHours <= 0 {
		t.Errorf("BanLookbackHours = %d, want > 0", settings.BanLookbackHours)
	}
	if settings.HealthCheckTimeoutS <= 0 {
		t.Errorf("HealthCheckTimeoutS = %d, want > 0", settings.HealthCheckTimeoutS)
	}

	// Override one and reload.
	if err := db.SetSetting("wazuh.critical_level", "99"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	reloaded := db.LoadAppSettings()
	if reloaded.WazuhCriticalLevel != 99 {
		t.Errorf("after override, WazuhCriticalLevel = %d, want 99", reloaded.WazuhCriticalLevel)
	}
}

func TestLoadAppSettingsBadValue(t *testing.T) {
	db := openTestDB(t)
	// Set a non-numeric value — should fall back to the default.
	if err := db.SetSetting("wazuh.critical_level", "notanumber"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	settings := db.LoadAppSettings()
	// Default for WazuhCriticalLevel is 12.
	if settings.WazuhCriticalLevel != 12 {
		t.Errorf("WazuhCriticalLevel with bad value = %d, want fallback 12", settings.WazuhCriticalLevel)
	}
}

// ── Cloud-init profiles ───────────────────────────────────────────────────────

func TestCloudInitProfileCRUD(t *testing.T) {
	db := openTestDB(t)

	p := CloudInitProfile{
		Name:         "ubuntu-base",
		CIUser:       "alyta",
		CISSHKey:     "ssh-ed25519 AAAA...",
		CIIPConfig:   "ip=dhcp",
		Nameserver:   "1.1.1.1",
		SearchDomain: "homelab.local",
		Notes:        "test profile",
	}

	// Create
	if err := db.SaveCloudInitProfile(p); err != nil {
		t.Fatalf("SaveCloudInitProfile: %v", err)
	}

	// Read all
	profiles, err := db.CloudInitProfiles()
	if err != nil {
		t.Fatalf("CloudInitProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("len(profiles) = %d, want 1", len(profiles))
	}
	if profiles[0].Name != "ubuntu-base" {
		t.Errorf("Name = %q, want ubuntu-base", profiles[0].Name)
	}
	if profiles[0].CIUser != "alyta" {
		t.Errorf("CIUser = %q, want alyta", profiles[0].CIUser)
	}

	// Get by name
	got, err := db.GetCloudInitProfile("ubuntu-base")
	if err != nil {
		t.Fatalf("GetCloudInitProfile: %v", err)
	}
	if got == nil {
		t.Fatal("GetCloudInitProfile returned nil")
	}
	if got.SearchDomain != "homelab.local" {
		t.Errorf("SearchDomain = %q, want homelab.local", got.SearchDomain)
	}

	// Get non-existent
	missing, err := db.GetCloudInitProfile("does-not-exist")
	if err != nil {
		t.Fatalf("GetCloudInitProfile(missing): %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing profile")
	}

	// Update
	got.Notes = "updated"
	if err := db.SaveCloudInitProfile(*got); err != nil {
		t.Fatalf("SaveCloudInitProfile (update): %v", err)
	}
	updated, _ := db.GetCloudInitProfile("ubuntu-base")
	if updated.Notes != "updated" {
		t.Errorf("after update, Notes = %q, want updated", updated.Notes)
	}

	// Delete
	if err := db.DeleteCloudInitProfile("ubuntu-base"); err != nil {
		t.Fatalf("DeleteCloudInitProfile: %v", err)
	}
	profiles, _ = db.CloudInitProfiles()
	if len(profiles) != 0 {
		t.Errorf("after delete, len(profiles) = %d, want 0", len(profiles))
	}
}

// ── Provisioned VM tracking ───────────────────────────────────────────────────

func TestProvisionedVMCRUD(t *testing.T) {
	db := openTestDB(t)

	vm := ProvisionedVM{
		VMID:  1001,
		Name:  "test-vm",
		Node:  "pve1",
		VMIP:  "192.168.1.100",
		VMType: "qemu",
	}

	if err := db.RecordProvisionedVM(vm); err != nil {
		t.Fatalf("RecordProvisionedVM: %v", err)
	}

	vms, err := db.ProvisionedVMs()
	if err != nil {
		t.Fatalf("ProvisionedVMs: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("len(vms) = %d, want 1", len(vms))
	}
	if vms[0].VMID != 1001 {
		t.Errorf("VMID = %d, want 1001", vms[0].VMID)
	}

	// GetProvisionedVM
	found, err := db.GetProvisionedVM(1001)
	if err != nil {
		t.Fatalf("GetProvisionedVM: %v", err)
	}
	if found == nil {
		t.Fatal("GetProvisionedVM returned nil")
	}
	if found.Name != "test-vm" {
		t.Errorf("Name = %q, want test-vm", found.Name)
	}

	// UpdateVMStatus
	if err := db.UpdateVMStatus(1001, "running", "192.168.1.100", "qemu"); err != nil {
		t.Fatalf("UpdateVMStatus: %v", err)
	}
}

// ── Service host registration ─────────────────────────────────────────────────

func TestServiceHostCRUD(t *testing.T) {
	db := openTestDB(t)

	h := ServiceHost{
		Host:             "192.168.1.50",
		VMID:             200,
		Node:             "pve1",
		Managed:          true,
		Registered:       true,
		RegistrationType: "managed",
	}

	if err := db.SaveServiceHost(h); err != nil {
		t.Fatalf("SaveServiceHost: %v", err)
	}

	got, err := db.GetServiceHost("192.168.1.50")
	if err != nil {
		t.Fatalf("GetServiceHost: %v", err)
	}
	if got == nil {
		t.Fatal("GetServiceHost returned nil")
	}
	if got.VMID != 200 {
		t.Errorf("VMID = %d, want 200", got.VMID)
	}
	if !got.Managed {
		t.Error("Managed = false, want true")
	}

	// ServiceHosts map
	hosts, err := db.ServiceHosts()
	if err != nil {
		t.Fatalf("ServiceHosts: %v", err)
	}
	if _, ok := hosts["192.168.1.50"]; !ok {
		t.Error("ServiceHosts map missing 192.168.1.50")
	}
}

func TestUpdateDiscoveredPorts(t *testing.T) {
	db := openTestDB(t)

	// Host must exist first.
	h := ServiceHost{Host: "10.0.0.1", Registered: true, RegistrationType: "unmanaged"}
	if err := db.SaveServiceHost(h); err != nil {
		t.Fatalf("SaveServiceHost: %v", err)
	}

	if err := db.UpdateDiscoveredPorts("10.0.0.1", "tcp:22,tcp:80,tcp:443"); err != nil {
		t.Fatalf("UpdateDiscoveredPorts: %v", err)
	}

	got, err := db.GetServiceHost("10.0.0.1")
	if err != nil {
		t.Fatalf("GetServiceHost: %v", err)
	}
	if got.DiscoveredPorts != "tcp:22,tcp:80,tcp:443" {
		t.Errorf("DiscoveredPorts = %q, want tcp:22,tcp:80,tcp:443", got.DiscoveredPorts)
	}
}

// ── Security events ───────────────────────────────────────────────────────────

func TestSecurityEventCRUD(t *testing.T) {
	db := openTestDB(t)

	ev := SecurityEvent{
		Source:      "crowdsec",
		EventType:   "ban",
		Severity:    "high",
		Description: "IP banned for SSH brute force",
		Target:      "198.51.100.1",
		RecordedAt:  time.Now(),
	}

	if err := db.RecordSecurityEvent(ev); err != nil {
		t.Fatalf("RecordSecurityEvent: %v", err)
	}

	events, err := db.RecentSecurityEvents(10)
	if err != nil {
		t.Fatalf("RecentSecurityEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Source != "crowdsec" {
		t.Errorf("Source = %q, want crowdsec", events[0].Source)
	}

	// By source
	bySrc, err := db.SecurityEventsBySource("crowdsec", 10)
	if err != nil {
		t.Fatalf("SecurityEventsBySource: %v", err)
	}
	if len(bySrc) != 1 {
		t.Errorf("len(bySrc) = %d, want 1", len(bySrc))
	}
}

func TestRecentBannedHosts(t *testing.T) {
	db := openTestDB(t)

	// Record a ban event.
	ev := SecurityEvent{
		Source:      "crowdsec",
		EventType:   "ban",
		Severity:    "high",
		Description: "IP banned",
		Target:      "1.2.3.4",
		RecordedAt:  time.Now(),
	}
	if err := db.RecordSecurityEvent(ev); err != nil {
		t.Fatalf("RecordSecurityEvent: %v", err)
	}

	// Record a non-ban event (should not appear).
	ev2 := SecurityEvent{
		Source:     "wazuh",
		EventType:  "alert",
		Severity:   "medium",
		Target:     "5.6.7.8",
		RecordedAt: time.Now(),
	}
	if err := db.RecordSecurityEvent(ev2); err != nil {
		t.Fatalf("RecordSecurityEvent: %v", err)
	}

	banned, err := db.RecentBannedHosts(24)
	if err != nil {
		t.Fatalf("RecentBannedHosts: %v", err)
	}
	if len(banned) != 1 {
		t.Fatalf("len(banned) = %d, want 1", len(banned))
	}
	if banned[0] != "1.2.3.4" {
		t.Errorf("banned[0] = %q, want 1.2.3.4", banned[0])
	}
}

// ── Maintenance schedules and runs ────────────────────────────────────────────

func TestMaintenanceScheduleCRUD(t *testing.T) {
	db := openTestDB(t)

	now := time.Now()
	sched := MaintenanceSchedule{
		Name:      "Weekly Maintenance",
		RunAt:     &now,
		Repeat:    "weekly",
		NodeOrder: `["pve1","pve2"]`,
		TaskFlags: `{"os_updates":true,"reboot":true}`,
		HAAware:   true,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	id, err := db.SaveMaintenanceSchedule(sched)
	if err != nil {
		t.Fatalf("SaveMaintenanceSchedule: %v", err)
	}
	if id == 0 {
		t.Fatal("SaveMaintenanceSchedule returned id=0")
	}

	schedules, err := db.MaintenanceSchedules()
	if err != nil {
		t.Fatalf("MaintenanceSchedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("len(schedules) = %d, want 1", len(schedules))
	}
	if schedules[0].Name != "Weekly Maintenance" {
		t.Errorf("Name = %q, want Weekly Maintenance", schedules[0].Name)
	}

	// Delete
	if err := db.DeleteMaintenanceSchedule(id); err != nil {
		t.Fatalf("DeleteMaintenanceSchedule: %v", err)
	}
	schedules, _ = db.MaintenanceSchedules()
	if len(schedules) != 0 {
		t.Errorf("after delete, len(schedules) = %d, want 0", len(schedules))
	}
}

func TestMaintenanceRunCRUD(t *testing.T) {
	db := openTestDB(t)

	now := time.Now()
	run := MaintenanceRun{
		Name:      "Ad-hoc run",
		Status:    "pending",
		NodeOrder: `["pve1"]`,
		TaskFlags: `{"reboot":true}`,
		HAAware:   false,
		CreatedAt: now,
	}

	id, err := db.CreateMaintenanceRun(run)
	if err != nil {
		t.Fatalf("CreateMaintenanceRun: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateMaintenanceRun returned id=0")
	}

	// StartMaintenanceRun
	if err := db.StartMaintenanceRun(id); err != nil {
		t.Fatalf("StartMaintenanceRun: %v", err)
	}

	// RecentMaintenanceRuns
	runs, err := db.RecentMaintenanceRuns(10)
	if err != nil {
		t.Fatalf("RecentMaintenanceRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].Status != "running" {
		t.Errorf("after StartMaintenanceRun, Status = %q, want running", runs[0].Status)
	}

	// UpdateMaintenanceRunStatus
	completedAt := time.Now()
	if err := db.UpdateMaintenanceRunStatus(id, "done", &completedAt); err != nil {
		t.Fatalf("UpdateMaintenanceRunStatus: %v", err)
	}
	runs, _ = db.RecentMaintenanceRuns(10)
	if runs[0].Status != "done" {
		t.Errorf("after update, Status = %q, want done", runs[0].Status)
	}
}

func TestMaintenanceRunSteps(t *testing.T) {
	db := openTestDB(t)

	now := time.Now()
	run := MaintenanceRun{
		Name:      "Test run",
		Status:    "pending",
		NodeOrder: `["pve1"]`,
		CreatedAt: now,
	}
	runID, err := db.CreateMaintenanceRun(run)
	if err != nil {
		t.Fatalf("CreateMaintenanceRun: %v", err)
	}

	steps := []MaintenanceRunStep{
		{RunID: runID, Node: "pve1", Task: "os_update", Status: "pending"},
		{RunID: runID, Node: "pve1", Task: "reboot", Status: "pending"},
	}
	if err := db.CreateMaintenanceSteps(runID, steps); err != nil {
		t.Fatalf("CreateMaintenanceSteps: %v", err)
	}

	got, err := db.MaintenanceRunSteps(runID)
	if err != nil {
		t.Fatalf("MaintenanceRunSteps: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(got))
	}
	if got[0].Task != "os_update" {
		t.Errorf("step[0].Task = %q, want os_update", got[0].Task)
	}

	// UpdateMaintenanceStep
	if err := db.UpdateMaintenanceStep(got[0].ID, "done", "all packages updated"); err != nil {
		t.Fatalf("UpdateMaintenanceStep: %v", err)
	}
	got, _ = db.MaintenanceRunSteps(runID)
	if got[0].Status != "done" {
		t.Errorf("after update, step Status = %q, want done", got[0].Status)
	}
	if got[0].Output != "all packages updated" {
		t.Errorf("after update, step Output = %q", got[0].Output)
	}
}

// ── PXE profiles and groups ───────────────────────────────────────────────────

func TestPXEProfileCRUD(t *testing.T) {
	db := openTestDB(t)

	now := time.Now()
	p := PXEProfile{
		ID:          "ubuntu-22.04",
		Name:        "Ubuntu 22.04 LTS",
		Description: "Ubuntu Jammy Jellyfish",
		KernelURL:   "http://matchbox.local/assets/vmlinuz",
		InitrdURLs:  "http://matchbox.local/assets/initrd.img",
		KernelArgs:  "ip=dhcp",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := db.SavePXEProfile(p); err != nil {
		t.Fatalf("SavePXEProfile: %v", err)
	}

	profiles, err := db.PXEProfiles()
	if err != nil {
		t.Fatalf("PXEProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("len(profiles) = %d, want 1", len(profiles))
	}
	if profiles[0].Name != "Ubuntu 22.04 LTS" {
		t.Errorf("Name = %q, want Ubuntu 22.04 LTS", profiles[0].Name)
	}

	if err := db.DeletePXEProfile("ubuntu-22.04"); err != nil {
		t.Fatalf("DeletePXEProfile: %v", err)
	}
	profiles, _ = db.PXEProfiles()
	if len(profiles) != 0 {
		t.Errorf("after delete, len(profiles) = %d, want 0", len(profiles))
	}
}

func TestPXEGroupCRUD(t *testing.T) {
	db := openTestDB(t)

	now := time.Now()
	g := PXEGroup{
		ID:         "node1-group",
		Name:       "node1",
		ProfileID:  "ubuntu-22.04",
		MACAddress: "aa:bb:cc:dd:ee:ff",
		Selector:   `{"mac":"aa:bb:cc:dd:ee:ff"}`,
		CreatedAt:  now,
	}

	if err := db.SavePXEGroup(g); err != nil {
		t.Fatalf("SavePXEGroup: %v", err)
	}

	groups, err := db.PXEGroups()
	if err != nil {
		t.Fatalf("PXEGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	if groups[0].MACAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MACAddress = %q, want aa:bb:cc:dd:ee:ff", groups[0].MACAddress)
	}

	if err := db.DeletePXEGroup("node1-group"); err != nil {
		t.Fatalf("DeletePXEGroup: %v", err)
	}
	groups, _ = db.PXEGroups()
	if len(groups) != 0 {
		t.Errorf("after delete, len(groups) = %d, want 0", len(groups))
	}
}

// ── Salt apply events ─────────────────────────────────────────────────────────

func TestSaltApplyEvents(t *testing.T) {
	db := openTestDB(t)

	ev := SaltApplyEvent{
		Minion:      "web-01",
		States:      "system.update",
		SaltProfile: "web-servers",
		JID:         "20240101120000000001",
		Success:     true,
		TriggeredAt: time.Now(),
	}

	id, err := db.RecordSaltApply(ev)
	if err != nil {
		t.Fatalf("RecordSaltApply: %v", err)
	}
	if id == 0 {
		t.Fatal("RecordSaltApply returned id=0")
	}

	events, err := db.SaltApplyEventsForMinion("web-01", 10)
	if err != nil {
		t.Fatalf("SaltApplyEventsForMinion: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Minion != "web-01" {
		t.Errorf("Minion = %q, want web-01", events[0].Minion)
	}

	// UpdateSaltApplyResult
	if err := db.UpdateSaltApplyResult(id, false, "timeout"); err != nil {
		t.Fatalf("UpdateSaltApplyResult: %v", err)
	}
	events, _ = db.SaltApplyEventsForMinion("web-01", 10)
	if events[0].Success {
		t.Error("after update, Success = true, want false")
	}
	if events[0].ErrorMsg != "timeout" {
		t.Errorf("ErrorMsg = %q, want timeout", events[0].ErrorMsg)
	}
}

// ── Service CRUD ──────────────────────────────────────────────────────────────

func TestServiceCRUD(t *testing.T) {
	d := openTestDB(t)

	svc := Service{Name: "Grafana", Node: "pve-1", Host: "10.0.0.5", Port: 3000, Scheme: "http", Enabled: true}
	id, err := d.SaveService(svc)
	if err != nil {
		t.Fatalf("SaveService: %v", err)
	}
	if id == 0 {
		t.Fatal("SaveService returned id=0")
	}

	svcs, err := d.Services()
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(svcs) != 1 {
		t.Fatalf("len(Services) = %d, want 1", len(svcs))
	}
	got := svcs[0]
	if got.Name != "Grafana" || got.Host != "10.0.0.5" || got.Port != 3000 || got.Scheme != "http" {
		t.Errorf("unexpected service: %+v", got)
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}

	// Second service
	svc2 := Service{Name: "Prometheus", Host: "10.0.0.5", Port: 9090, Scheme: "http", Enabled: true}
	if _, err := d.SaveService(svc2); err != nil {
		t.Fatalf("SaveService second: %v", err)
	}
	svcs, _ = d.Services()
	if len(svcs) != 2 {
		t.Fatalf("len(Services) = %d, want 2", len(svcs))
	}

	// Delete
	if err := d.DeleteService(id); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	svcs, _ = d.Services()
	if len(svcs) != 1 {
		t.Fatalf("after delete: len(Services) = %d, want 1", len(svcs))
	}
	if svcs[0].Name != "Prometheus" {
		t.Errorf("remaining service = %q, want Prometheus", svcs[0].Name)
	}
}

// ── Certificate CRUD ──────────────────────────────────────────────────────────

func TestCertificateCRUD(t *testing.T) {
	d := openTestDB(t)

	now := time.Now()
	cert := Certificate{
		Host:        "grafana.home.arpa",
		Port:        443,
		CommonName:  "grafana.home.arpa",
		SANNames:    "grafana.home.arpa",
		Issuer:      "Homelab CA",
		Serial:      "01",
		NotBefore:   now,
		NotAfter:    now.Add(90 * 24 * time.Hour),
		Fingerprint: "abc123",
	}
	if err := d.UpsertCertificate(cert); err != nil {
		t.Fatalf("UpsertCertificate: %v", err)
	}

	certs, err := d.Certificates()
	if err != nil {
		t.Fatalf("Certificates: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("len(Certificates) = %d, want 1", len(certs))
	}
	if certs[0].CommonName != "grafana.home.arpa" {
		t.Errorf("CommonName = %q, want grafana.home.arpa", certs[0].CommonName)
	}

	// Upsert same host+port with different fingerprint — should update, not insert
	cert.Fingerprint = "newfingerprint"
	if err := d.UpsertCertificate(cert); err != nil {
		t.Fatalf("UpsertCertificate update: %v", err)
	}
	certs, _ = d.Certificates()
	if len(certs) != 1 {
		t.Fatalf("after upsert: len(Certificates) = %d, want 1 (dedup by host+port)", len(certs))
	}
	if certs[0].Fingerprint != "newfingerprint" {
		t.Errorf("Fingerprint = %q, want newfingerprint", certs[0].Fingerprint)
	}

	// DaysUntilExpiry
	days := certs[0].DaysUntilExpiry()
	if days < 88 || days > 91 {
		t.Errorf("DaysUntilExpiry = %d, want ~90", days)
	}
}

// ── Node metrics ──────────────────────────────────────────────────────────────

func TestNodeMetrics(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 5; i++ {
		if err := d.RecordNodeMetrics("pve-1", float64(i)*0.1, 0.5, 1000, 8192); err != nil {
			t.Fatalf("RecordNodeMetrics: %v", err)
		}
	}

	pts, err := d.NodeMetricsHistory("pve-1", 10)
	if err != nil {
		t.Fatalf("NodeMetricsHistory: %v", err)
	}
	if len(pts) != 5 {
		t.Fatalf("len(pts) = %d, want 5", len(pts))
	}
	// Returned oldest-first; last point has cpu_ratio 0.4
	if pts[len(pts)-1].CPURatio < 0.39 || pts[len(pts)-1].CPURatio > 0.41 {
		t.Errorf("last CPURatio = %v, want ~0.4", pts[len(pts)-1].CPURatio)
	}
	// Wrong node returns empty
	pts2, _ := d.NodeMetricsHistory("pve-99", 10)
	if len(pts2) != 0 {
		t.Errorf("wrong node: len(pts) = %d, want 0", len(pts2))
	}
	// Prune all
	if err := d.PruneNodeMetrics(0); err != nil {
		t.Fatalf("PruneNodeMetrics: %v", err)
	}
	pts, _ = d.NodeMetricsHistory("pve-1", 10)
	if len(pts) != 0 {
		t.Fatalf("after prune: len(pts) = %d, want 0", len(pts))
	}
}

// ── Service uptime ────────────────────────────────────────────────────────────

func TestServiceUptime(t *testing.T) {
	d := openTestDB(t)

	// No data → -1
	pct := d.ServiceUptimePct(999)
	if pct != -1 {
		t.Errorf("no data: pct = %v, want -1", pct)
	}

	svcID, _ := d.SaveService(Service{Name: "test", Host: "10.0.0.1", Port: 80, Scheme: "tcp", Enabled: true})

	// 3 up, 1 down → 75%
	for i := 0; i < 3; i++ {
		d.RecordServiceUptime(svcID, true)  //nolint:errcheck
	}
	d.RecordServiceUptime(svcID, false) //nolint:errcheck

	pct = d.ServiceUptimePct(svcID)
	if pct < 0.74 || pct > 0.76 {
		t.Errorf("pct = %v, want 0.75", pct)
	}
}

// ── Runbook CRUD ──────────────────────────────────────────────────────────────

func TestRunbookCRUD(t *testing.T) {
	d := openTestDB(t)

	rb := Runbook{Name: "update-nodes", Description: "Run apt upgrade", StepsJSON: `["step1"]`}
	id, err := d.SaveRunbook(rb)
	if err != nil {
		t.Fatalf("SaveRunbook: %v", err)
	}
	if id == 0 {
		t.Fatal("SaveRunbook returned id=0")
	}

	rbs, err := d.Runbooks()
	if err != nil {
		t.Fatalf("Runbooks: %v", err)
	}
	if len(rbs) != 1 || rbs[0].Name != "update-nodes" {
		t.Fatalf("Runbooks: %+v", rbs)
	}

	// Update existing
	rb.ID = id
	rb.Name = "update-nodes-v2"
	if _, err := d.SaveRunbook(rb); err != nil {
		t.Fatalf("SaveRunbook update: %v", err)
	}
	rbs, _ = d.Runbooks()
	if len(rbs) != 1 || rbs[0].Name != "update-nodes-v2" {
		t.Fatalf("after update: %+v", rbs)
	}

	// Runs
	runID, err := d.CreateRunbookRun(id)
	if err != nil {
		t.Fatalf("CreateRunbookRun: %v", err)
	}
	if err := d.UpdateRunbookRun(runID, "running", "started…"); err != nil {
		t.Fatalf("UpdateRunbookRun running: %v", err)
	}
	if err := d.UpdateRunbookRun(runID, "done", "finished OK"); err != nil {
		t.Fatalf("UpdateRunbookRun done: %v", err)
	}

	runs, err := d.RecentRunbookRuns(id, 10)
	if err != nil {
		t.Fatalf("RecentRunbookRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].Status != "done" || runs[0].Output != "finished OK" {
		t.Errorf("run = %+v", runs[0])
	}
	if runs[0].DoneAt == nil {
		t.Error("DoneAt = nil after status=done")
	}

	// Delete
	if err := d.DeleteRunbook(id); err != nil {
		t.Fatalf("DeleteRunbook: %v", err)
	}
	rbs, _ = d.Runbooks()
	if len(rbs) != 0 {
		t.Errorf("after delete: len(rbs) = %d, want 0", len(rbs))
	}
}

// ── Alert route CRUD ──────────────────────────────────────────────────────────

func TestAlertRouteCRUD(t *testing.T) {
	d := openTestDB(t)

	r := AlertRoute{Name: "pagerduty", Source: "prometheus", Severity: "critical", WebhookURL: "https://events.pagerduty.com/v2/enqueue", Enabled: true}
	id, err := d.SaveAlertRoute(r)
	if err != nil {
		t.Fatalf("SaveAlertRoute: %v", err)
	}

	routes, err := d.AlertRoutes()
	if err != nil {
		t.Fatalf("AlertRoutes: %v", err)
	}
	if len(routes) != 1 || !routes[0].Enabled {
		t.Fatalf("AlertRoutes: %+v", routes)
	}
	if routes[0].LastFired != nil {
		t.Error("LastFired should be nil before Touch")
	}

	// Update
	r.ID = id
	r.Enabled = false
	if _, err := d.SaveAlertRoute(r); err != nil {
		t.Fatalf("SaveAlertRoute update: %v", err)
	}
	routes, _ = d.AlertRoutes()
	if routes[0].Enabled {
		t.Error("after update, Enabled = true, want false")
	}

	// Touch
	if err := d.TouchAlertRoute(id); err != nil {
		t.Fatalf("TouchAlertRoute: %v", err)
	}
	routes, _ = d.AlertRoutes()
	if routes[0].LastFired == nil {
		t.Error("LastFired = nil after Touch")
	}

	// Delete
	if err := d.DeleteAlertRoute(id); err != nil {
		t.Fatalf("DeleteAlertRoute: %v", err)
	}
	routes, _ = d.AlertRoutes()
	if len(routes) != 0 {
		t.Errorf("after delete: len(routes) = %d, want 0", len(routes))
	}
}

// ── VM lifecycle policy CRUD ──────────────────────────────────────────────────

func TestLifecyclePolicyCRUD(t *testing.T) {
	d := openTestDB(t)

	p := VMLifecyclePolicy{Name: "shutdown-idle", Tag: "dev", Action: "shutdown", IdleHours: 2, Enabled: true}
	id, err := d.SaveLifecyclePolicy(p)
	if err != nil {
		t.Fatalf("SaveLifecyclePolicy: %v", err)
	}

	policies, err := d.LifecyclePolicies()
	if err != nil {
		t.Fatalf("LifecyclePolicies: %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("len = %d, want 1", len(policies))
	}
	if policies[0].Name != "shutdown-idle" || policies[0].IdleHours != 2 || !policies[0].Enabled {
		t.Errorf("policy: %+v", policies[0])
	}
	if policies[0].LastRun != nil {
		t.Error("LastRun should be nil before Touch")
	}

	// Update
	p.ID = id
	p.IdleHours = 4
	if _, err := d.SaveLifecyclePolicy(p); err != nil {
		t.Fatalf("SaveLifecyclePolicy update: %v", err)
	}
	policies, _ = d.LifecyclePolicies()
	if policies[0].IdleHours != 4 {
		t.Errorf("IdleHours = %d, want 4", policies[0].IdleHours)
	}

	// Touch
	if err := d.TouchLifecyclePolicy(id); err != nil {
		t.Fatalf("TouchLifecyclePolicy: %v", err)
	}
	policies, _ = d.LifecyclePolicies()
	if policies[0].LastRun == nil {
		t.Error("LastRun = nil after Touch")
	}

	// Delete
	if err := d.DeleteLifecyclePolicy(id); err != nil {
		t.Fatalf("DeleteLifecyclePolicy: %v", err)
	}
	policies, _ = d.LifecyclePolicies()
	if len(policies) != 0 {
		t.Errorf("after delete: len = %d, want 0", len(policies))
	}
}

// ── Scan results ──────────────────────────────────────────────────────────────

func TestScanResultsCRUD(t *testing.T) {
	d := openTestDB(t)

	results := []ScanResult{
		{Target: "10.0.0.5", Node: "pve-1", VMID: 101, ScanType: "port", CheckName: "open-22", Status: "warn", Severity: "low", Detail: "SSH open"},
		{Target: "10.0.0.5", Node: "pve-1", VMID: 101, ScanType: "port", CheckName: "open-80", Status: "fail", Severity: "high", Detail: "HTTP open"},
		{Target: "10.0.0.6", Node: "pve-1", VMID: 102, ScanType: "vm_firewall", CheckName: "fw-disabled", Status: "fail", Severity: "critical", Detail: "no firewall"},
	}
	if err := d.SaveScanResults(results); err != nil {
		t.Fatalf("SaveScanResults: %v", err)
	}

	all, err := d.RecentScanResults(10)
	if err != nil {
		t.Fatalf("RecentScanResults: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	// All 3 results returned
	found := map[string]bool{}
	for _, r := range all {
		found[r.Severity] = true
	}
	for _, sev := range []string{"low", "high", "critical"} {
		if !found[sev] {
			t.Errorf("severity %q not found in results", sev)
		}
	}

	// Filter by target
	forTarget, err := d.ScanResultsForTarget("10.0.0.5", 10)
	if err != nil {
		t.Fatalf("ScanResultsForTarget: %v", err)
	}
	if len(forTarget) != 2 {
		t.Fatalf("ScanResultsForTarget: len = %d, want 2", len(forTarget))
	}

	// Mark remediated
	if err := d.MarkScanResultRemediated(all[0].ID); err != nil {
		t.Fatalf("MarkScanResultRemediated: %v", err)
	}
	all2, _ := d.RecentScanResults(10)
	remediated := false
	for _, r := range all2 {
		if r.ID == all[0].ID && r.Remediated {
			remediated = true
		}
	}
	if !remediated {
		t.Error("MarkScanResultRemediated did not set Remediated=true")
	}

	// Clear
	if err := d.ClearScanResults(); err != nil {
		t.Fatalf("ClearScanResults: %v", err)
	}
	all, _ = d.RecentScanResults(10)
	if len(all) != 0 {
		t.Errorf("after clear: len = %d, want 0", len(all))
	}
}
