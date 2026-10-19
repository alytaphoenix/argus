package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/alytaphoenix/argus/api"
	"github.com/alytaphoenix/argus/config"
	"github.com/alytaphoenix/argus/db"
	"github.com/alytaphoenix/argus/wiring"
)

const (
	provFieldTemplate = iota
	provFieldName
	provFieldVMID
	provFieldCPU
	provFieldMemory
	provFieldStorage
	provFieldCIProfile // pick a saved cloud-init profile (ctrl+L to load)
	provFieldCIUser
	provFieldCIPassword
	provFieldSSHKey
	provFieldIPConfig
	provFieldSaltProfile // record which salt profile is intended for this VM
	provFieldBatch       // number of VMs to provision (1 = single)
	provFieldCount
)

type ProvisionModel struct {
	client    *api.Client
	node      api.Node
	defaults  config.DefaultsConfig
	database  *db.DB
	dnsClient *api.TechnitiumClient
	domain    string

	inputs  []textinput.Model
	focused int

	// profile load notice
	profileNotice string

	submitting bool
	taskID     string
	warnings   []string
	err        error
	width      int
	height     int
	spinner    spinner.Model
}

func NewProvisionModel(client *api.Client, node api.Node, defaults config.DefaultsConfig,
	database *db.DB, dnsClient *api.TechnitiumClient, domain string) ProvisionModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	inputs := make([]textinput.Model, provFieldCount)
	for i := range inputs {
		inputs[i] = textinput.New()
		inputs[i].CharLimit = 256
	}

	inputs[provFieldTemplate].Placeholder = "template VMID (e.g. 9000)"
	inputs[provFieldTemplate].SetValue(strconv.Itoa(defaults.TemplateID))

	inputs[provFieldName].Placeholder = "new VM name"

	inputs[provFieldVMID].Placeholder = "new VMID (blank = auto)"

	inputs[provFieldCPU].Placeholder = "CPU cores"
	inputs[provFieldCPU].SetValue(strconv.Itoa(defaults.CPUCores))

	inputs[provFieldMemory].Placeholder = "memory (MiB)"
	inputs[provFieldMemory].SetValue(strconv.Itoa(defaults.MemoryMB))

	inputs[provFieldStorage].Placeholder = "storage pool (e.g. local-lvm)"
	inputs[provFieldStorage].SetValue(defaults.Storage)

	inputs[provFieldCIProfile].Placeholder = "cloud-init profile name (ctrl+L to load)"

	inputs[provFieldCIUser].Placeholder = "cloud-init user"
	inputs[provFieldCIUser].SetValue(defaults.CIUser)

	inputs[provFieldCIPassword].Placeholder = "cloud-init password"
	inputs[provFieldCIPassword].EchoMode = textinput.EchoPassword

	inputs[provFieldSSHKey].Placeholder = "SSH public key (optional)"
	inputs[provFieldSSHKey].SetValue(defaults.CISSHKey)
	inputs[provFieldSSHKey].CharLimit = 2048

	inputs[provFieldIPConfig].Placeholder = "ip=dhcp or ip=x.x.x.x/24,gw=..."
	inputs[provFieldIPConfig].SetValue(defaults.CIIPConfig)

	inputs[provFieldSaltProfile].Placeholder = "salt profile name (optional, for tracking)"

	inputs[provFieldBatch].Placeholder = "number of VMs to provision (default 1)"
	inputs[provFieldBatch].SetValue("1")

	inputs[provFieldTemplate].Focus()

	return ProvisionModel{
		client:    client,
		node:      node,
		defaults:  defaults,
		database:  database,
		dnsClient: dnsClient,
		domain:    domain,
		inputs:    inputs,
		spinner:   s,
	}
}

func (m ProvisionModel) Init() tea.Cmd {
	return textinput.Blink
}

// loadCIProfile fills the cloud-init fields from the named saved profile.
func (m ProvisionModel) loadCIProfile() (ProvisionModel, tea.Cmd) {
	name := strings.TrimSpace(m.inputs[provFieldCIProfile].Value())
	if name == "" || m.database == nil {
		m.profileNotice = "no profile name entered"
		return m, nil
	}
	p, err := m.database.GetCloudInitProfile(name)
	if err != nil {
		m.profileNotice = "db error: " + err.Error()
		return m, nil
	}
	if p == nil {
		m.profileNotice = "profile not found: " + name
		return m, nil
	}
	m.inputs[provFieldCIUser].SetValue(p.CIUser)
	m.inputs[provFieldCIPassword].SetValue(p.CIPassword)
	m.inputs[provFieldSSHKey].SetValue(p.CISSHKey)
	m.inputs[provFieldIPConfig].SetValue(p.CIIPConfig)
	m.profileNotice = "loaded cloud-init profile: " + p.Name
	return m, nil
}

// startSubmit validates the form and, if it is complete, begins cloning.
// Validation lives here (rather than being silently coerced by strconv.Atoi
// in submit) so a typo like "4x" doesn't quietly provision a VM with 0 vCPUs.
func (m ProvisionModel) startSubmit() (ProvisionModel, tea.Cmd) {
	if strings.TrimSpace(m.inputs[provFieldName].Value()) == "" {
		m.err = fmt.Errorf("VM name is required")
		return m, nil
	}
	if strings.TrimSpace(m.inputs[provFieldTemplate].Value()) == "" {
		m.err = fmt.Errorf("template VMID is required")
		return m, nil
	}

	numeric := []struct {
		idx   int
		label string
		min   int
	}{
		{provFieldTemplate, "template VMID", 1},
		{provFieldCPU, "CPU cores", 1},
		{provFieldMemory, "memory", 128},
		{provFieldBatch, "VM count", 1},
	}
	for _, f := range numeric {
		raw := strings.TrimSpace(m.inputs[f.idx].Value())
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < f.min {
			m.err = fmt.Errorf("%s must be a number >= %d", f.label, f.min)
			return m, nil
		}
	}

	if raw := strings.TrimSpace(m.inputs[provFieldVMID].Value()); raw != "" {
		if n, err := strconv.Atoi(raw); err != nil || n <= 0 {
			m.err = fmt.Errorf("new VMID must be a positive number (or blank for auto)")
			return m, nil
		}
	}

	m.submitting = true
	m.err = nil
	m.profileNotice = ""
	return m, tea.Batch(m.spinner.Tick, m.submit())
}

func (m ProvisionModel) submit() tea.Cmd {
	client := m.client
	node := m.node
	database := m.database
	dnsClient := m.dnsClient
	domain := m.domain

	templateID, _ := strconv.Atoi(m.inputs[provFieldTemplate].Value())
	name := strings.TrimSpace(m.inputs[provFieldName].Value())
	vmidStr := strings.TrimSpace(m.inputs[provFieldVMID].Value())
	cpu, _ := strconv.Atoi(m.inputs[provFieldCPU].Value())
	memory, _ := strconv.Atoi(m.inputs[provFieldMemory].Value())
	storage := strings.TrimSpace(m.inputs[provFieldStorage].Value())
	ciProfile := strings.TrimSpace(m.inputs[provFieldCIProfile].Value())
	ciUser := strings.TrimSpace(m.inputs[provFieldCIUser].Value())
	ciPassword := m.inputs[provFieldCIPassword].Value()
	sshKey := strings.TrimSpace(m.inputs[provFieldSSHKey].Value())
	ipConfig := strings.TrimSpace(m.inputs[provFieldIPConfig].Value())
	saltProfile := strings.TrimSpace(m.inputs[provFieldSaltProfile].Value())

	batchCount, _ := strconv.Atoi(strings.TrimSpace(m.inputs[provFieldBatch].Value()))
	if batchCount < 1 {
		batchCount = 1
	}

	baseVMID := 0
	if vmidStr != "" {
		baseVMID, _ = strconv.Atoi(vmidStr)
	}

	defaults := m.defaults

	return func() tea.Msg {
		// Full clones can take minutes each; allow enough headroom to wait
		// for each clone task before configuring the VM.
		timeout := time.Duration(batchCount) * 10 * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		var lastTaskID string
		var lastVMID int
		var warnings []string

		for i := 0; i < batchCount; i++ {
			vmName := name
			if batchCount > 1 {
				vmName = fmt.Sprintf("%s-%02d", name, i+1)
			}

			newVMID := 0
			if baseVMID != 0 {
				newVMID = baseVMID + i
			} else {
				id, err := client.NextVMID(ctx)
				if err != nil {
					return provisionDoneMsg{err: fmt.Errorf("vm %d nextid: %w", i+1, err)}
				}
				newVMID = id
			}

			taskID, err := client.CloneVM(ctx, api.CloneOptions{
				SourceNode: node.Name,
				SourceVMID: templateID,
				NewVMID:    newVMID,
				Name:       vmName,
				TargetNode: node.Name,
				Storage:    storage,
				Full:       storage != "",
			})
			if err != nil {
				return provisionDoneMsg{count: i, err: fmt.Errorf("vm %d clone: %w", i+1, err)}
			}

			// The VM is locked until the clone task finishes; config changes
			// (resources, cloud-init) are rejected while the lock is held.
			if exit, err := client.WaitForTask(ctx, node.Name, taskID); err != nil {
				return provisionDoneMsg{taskID: taskID, count: i,
					err: fmt.Errorf("vm %d clone wait: %w", i+1, err)}
			} else if exit != "OK" {
				return provisionDoneMsg{taskID: taskID, count: i,
					err: fmt.Errorf("vm %d clone task: %s", i+1, exit)}
			}

			if cpu > 0 || memory > 0 {
				if err := client.ResizeVMConfig(ctx, node.Name, "qemu", newVMID, cpu, memory); err != nil {
					return provisionDoneMsg{taskID: taskID, count: i,
						err: fmt.Errorf("vm %d resources (cloned as %d): %w", i+1, newVMID, err)}
				}
			}

			hasCIFields := ciUser != "" || ciPassword != "" || sshKey != "" || ipConfig != ""
			if hasCIFields {
				ciOpts := api.CloudInitOptions{
					User:         ciUser,
					Password:     ciPassword,
					SSHKeys:      sshKey,
					IPConfig:     ipConfig,
					Nameserver:   defaults.Nameserver,
					SearchDomain: defaults.SearchDomain,
				}
				if err := client.SetCloudInit(ctx, node.Name, newVMID, ciOpts); err != nil {
					return provisionDoneMsg{
						taskID: taskID,
						count:  i,
						err:    fmt.Errorf("vm %d cloud-init (cloned as %d): %w", i+1, newVMID, err),
					}
				}
			}

			if database != nil {
				_ = database.RecordProvisionedVM(db.ProvisionedVM{
					VMID:             newVMID,
					Name:             vmName,
					Node:             node.Name,
					TemplateID:       templateID,
					CloudInitProfile: ciProfile,
					SaltProfile:      saltProfile,
				})
			}

			// Give the guest a chance to boot and report an IP through the
			// QEMU guest agent, then publish <vm-name>.<domain> in Technitium.
			// This is best-effort: a missing agent or DNS outage must not make
			// the clone itself look like a failure.
			if dnsClient != nil && domain != "" && client != nil {
				label := wiring.DNSLabel(vmName)
				if label == "" {
					warnings = append(warnings, fmt.Sprintf("%s: could not derive a DNS label", vmName))
				} else {
					ip := waitForGuestIP(ctx, client, node.Name, "qemu", newVMID, 90*time.Second)
					if ip == "" {
						warnings = append(warnings, fmt.Sprintf("%s: guest agent did not report an IP; DNS record not created", vmName))
					} else {
						dnsCtx, dnsCancel := context.WithTimeout(ctx, 15*time.Second)
						err := dnsClient.AddRecord(dnsCtx, api.DNSAddOptions{
							Zone:      domain,
							Domain:    label + "." + domain,
							Type:      "A",
							IPAddress: ip,
							Overwrite: true,
						})
						dnsCancel()
						if err != nil {
							warnings = append(warnings, fmt.Sprintf("%s: DNS record %s.%s failed: %v", vmName, label, domain, err))
						} else if database != nil {
							_ = database.UpdateVMStatus(newVMID, "running", ip, "qemu")
						}
					}
				}
			}

			lastTaskID = taskID
			lastVMID = newVMID
		}

		return provisionDoneMsg{taskID: lastTaskID, vmid: lastVMID, count: batchCount, warnings: warnings}
	}
}

// waitForGuestIP polls the QEMU guest agent until it reports an IPv4 address
// or the timeout expires. Returns "" when no address becomes available.
func waitForGuestIP(ctx context.Context, client *api.Client, node, guestType string, vmid int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if ip := client.GuestPrimaryIP(ctx, node, guestType, vmid); ip != "" {
			return ip
		}
		if time.Now().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(5 * time.Second):
		}
	}
}

func (m ProvisionModel) Update(msg tea.Msg) (ProvisionModel, tea.Cmd) {
	switch msg := msg.(type) {
	case provisionDoneMsg:
		m.submitting = false
		m.err = msg.err
		m.warnings = msg.warnings
		if msg.err == nil {
			if msg.count > 1 {
				m.taskID = fmt.Sprintf("%d VMs cloned, last: %d — task %s", msg.count, msg.vmid, msg.taskID)
			} else {
				m.taskID = fmt.Sprintf("VM %d cloned — task %s", msg.vmid, msg.taskID)
			}
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		// Only ctrl+c quits: this screen is one big text form, so "q" (and
		// every other printable key) must reach the focused input. esc
		// cancels back to the guest list.
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, func() tea.Msg { return backMsg{} }
		case "ctrl+l":
			// Load a cloud-init profile by name
			return m.loadCIProfile()
		case "ctrl+s":
			return m.startSubmit()
		case "enter":
			if m.focused == provFieldCount-1 {
				return m.startSubmit()
			}
			m.inputs[m.focused].Blur()
			m.focused = (m.focused + 1) % provFieldCount
			m.inputs[m.focused].Focus()
			return m, textinput.Blink
		case "tab", "down":
			m.inputs[m.focused].Blur()
			m.focused = (m.focused + 1) % provFieldCount
			m.inputs[m.focused].Focus()
			return m, textinput.Blink
		case "shift+tab", "up":
			m.inputs[m.focused].Blur()
			m.focused = (m.focused - 1 + provFieldCount) % provFieldCount
			m.inputs[m.focused].Focus()
			return m, textinput.Blink
		}
	}

	// Forward to focused input
	var cmd tea.Cmd
	m.inputs[m.focused], cmd = m.inputs[m.focused].Update(msg)
	return m, cmd
}

func (m ProvisionModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("⚡ argus") +
		styleDim.Render(" › ") +
		styleBold.Render(m.node.Name) +
		styleDim.Render(" › ") +
		styleBold.Render("provision VM") + "\n\n")

	field := func(label string, idx int) {
		prefix := "  "
		if idx == m.focused {
			prefix = styleCyan.Render("▶ ")
		}
		sb.WriteString(fmt.Sprintf("%s%-18s %s\n",
			prefix,
			styleDim.Render(label),
			m.inputs[idx].View(),
		))
	}

	sb.WriteString(styleDim.Render("  Clone from template") + "\n")
	field("Template VMID", provFieldTemplate)
	field("New VM Name", provFieldName)
	field("New VMID", provFieldVMID)
	sb.WriteString("\n")

	sb.WriteString(styleDim.Render("  Resources") + "\n")
	field("CPU cores", provFieldCPU)
	field("Memory (MiB)", provFieldMemory)
	field("Storage pool", provFieldStorage)
	sb.WriteString("\n")

	sb.WriteString(styleDim.Render("  Cloud-init") + "\n")
	field("CI Profile", provFieldCIProfile)
	field("User", provFieldCIUser)
	field("Password", provFieldCIPassword)
	field("SSH public key", provFieldSSHKey)
	field("IP config", provFieldIPConfig)
	sb.WriteString("\n")

	sb.WriteString(styleDim.Render("  Salt") + "\n")
	field("Salt Profile", provFieldSaltProfile)
	sb.WriteString("\n")

	sb.WriteString(styleDim.Render("  Batch") + "\n")
	field("VM count", provFieldBatch)

	sb.WriteString("\n")

	if m.profileNotice != "" {
		sb.WriteString(styleRunning.Render("  ✦ "+m.profileNotice) + "\n")
	}

	if m.submitting {
		sb.WriteString("  " + m.spinner.View() + " provisioning...\n\n")
	} else if m.err != nil {
		sb.WriteString(styleError.Render("  error: "+m.err.Error()) + "\n\n")
	} else if m.taskID != "" {
		sb.WriteString(styleRunning.Render("  ✓ "+m.taskID) + "\n\n")
	}
	for _, warning := range m.warnings {
		sb.WriteString(styleError.Render("  ! "+warning) + "\n")
	}
	if len(m.warnings) > 0 {
		sb.WriteString("\n")
	}

	sb.WriteString(styleDim.Render("  "+strings.Repeat("─", max(m.width-4, 50))) + "\n")
	sb.WriteString(styleHelp.Render("  tab/↓ next  shift+tab/↑ prev  ctrl+L load CI profile  ctrl+s submit  esc cancel") + "\n")
	return sb.String()
}
