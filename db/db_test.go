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
