package formula

// BuiltinRegistry returns argus's opinionated formula catalog.
//
// The baseline is deliberately small and safe: packages/chrony/unattended
// upgrades plus node exporter. Everything that publishes ports, installs a
// container runtime, or replaces a service is opt-in from the registry.
func BuiltinRegistry() (*Registry, error) {
	r, err := NewRegistry(catalog()...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func catalog() []Formula {
	return []Formula{
		{
			ID:          "base",
			Name:        "Base system",
			Description: "Baseline packages, time sync, and unattended security updates.",
			Category:    "Platform",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    10,
			Target:      "any",
			Provides:    []string{"base"},
			States:      []string{"argus.base"},
			Files: map[string]string{
				"init.sls": baseSLS,
			},
			Docs: "Installs ca-certificates, chrony, curl, jq, and unattended-upgrades. It does not change SSH or firewall settings.",
		},
		{
			ID:          "salt-minion",
			Name:        "Salt minion",
			Description: "Connects a host to the argus Salt master.",
			Category:    "Platform",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    15,
			DependsOn:   []string{"base"},
			Target:      "minion",
			Provides:    []string{"salt-minion"},
			States:      []string{"argus.salt_minion"},
			Pillar:      map[string]any{"salt_master": "127.0.0.1"},
			Files:       map[string]string{"init.sls": saltMinionSLS},
			Docs:        "Requires the salt-minion package to already be present; the bootstrap installer handles the first minion.",
		},
		{
			ID:          "docker",
			Name:        "Docker Engine",
			Description: "Docker runtime and compose for containerized platform services.",
			Category:    "Runtime",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    20,
			DependsOn:   []string{"base"},
			Target:      "any",
			Provides:    []string{"docker"},
			States:      []string{"argus.docker"},
			Files:       map[string]string{"init.sls": dockerSLS},
			Docs:        "Installs the Debian docker.io and docker-compose packages. Docker socket access is a privileged boundary.",
		},
		{
			ID:          "node-exporter",
			Name:        "Prometheus node exporter",
			Description: "Host CPU, memory, disk, and network metrics for Prometheus.",
			Category:    "Observability",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    30,
			DependsOn:   []string{"base"},
			Target:      "any",
			Provides:    []string{"node-exporter"},
			States:      []string{"argus.node_exporter"},
			Health:      &Health{TCP: "127.0.0.1:9100", Path: "/metrics"},
			Files:       map[string]string{"init.sls": nodeExporterSLS},
			Docs:        "Installs prometheus-node-exporter and exposes :9100 on the minion.",
		},
		{
			ID:          "technitium",
			Name:        "Technitium DNS",
			Description: "Authoritative DNS for the homelab domain.",
			Category:    "Network",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    40,
			DependsOn:   []string{"docker"},
			Target:      "any",
			Provides:    []string{"dns"},
			States:      []string{"argus.technitium"},
			Health:      &Health{TCP: "127.0.0.1:5380"},
			Files: map[string]string{
				"init.sls":     technitiumSLS,
				"compose.yaml": technitiumCompose,
			},
			Docs: "Runs technitium/dns-server with DNS on :53 and the admin UI on :5380. Configure zones before switching clients to it.",
		},
		{
			ID:          "uptime-kuma",
			Name:        "Uptime Kuma",
			Description: "Self-hosted uptime and service monitoring.",
			Category:    "Observability",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    40,
			DependsOn:   []string{"docker"},
			Target:      "any",
			Provides:    []string{"uptime-kuma"},
			States:      []string{"argus.uptime_kuma"},
			Health:      &Health{TCP: "127.0.0.1:3001"},
			Files: map[string]string{
				"init.sls":     uptimeKumaSLS,
				"compose.yaml": uptimeKumaCompose,
			},
			Docs: "Runs louislam/uptime-kuma:1 on :3001 with a named volume.",
		},
		{
			ID:          "traefik",
			Name:        "Traefik",
			Description: "Reverse proxy and TLS termination for platform services.",
			Category:    "Network",
			Version:     "1.0.0",
			Kind:        KindBuiltin,
			Priority:    40,
			DependsOn:   []string{"docker"},
			Target:      "any",
			Provides:    []string{"traefik"},
			States:      []string{"argus.traefik"},
			Health:      &Health{TCP: "127.0.0.1:80"},
			Files: map[string]string{
				"init.sls":     traefikSLS,
				"compose.yaml": traefikCompose,
			},
			Docs: "Runs traefik:v3 with :80/:443. The docker socket is mounted read-only; this is still a privileged interface.",
		},
		{
			ID:          "truenas",
			Name:        "TrueNAS",
			Description: "Existing TrueNAS storage appliance, wired into argus services.",
			Category:    "Storage",
			Version:     "external",
			Kind:        KindIntegration,
			Priority:    50,
			Target:      "any",
			Provides:    []string{"truenas"},
			Health:      &Health{TCP: "443", Path: "/api/v2.0/system/info"},
			Docs:        "Integration only: configure [truenas] url + api_key in argus.",
		},
		{
			ID:          "plex",
			Name:        "Plex Media Server",
			Description: "Existing Plex server, wired into argus media and health checks.",
			Category:    "Media",
			Version:     "external",
			Kind:        KindIntegration,
			Priority:    60,
			Target:      "any",
			Provides:    []string{"plex"},
			Health:      &Health{TCP: "32400"},
			Docs:        "Integration only: configure [plex] url + token in argus.",
		},
		{
			ID:          "servarr",
			Name:        "Servarr suite",
			Description: "Existing Sonarr/Radarr/Prowlarr stack.",
			Category:    "Media",
			Version:     "external",
			Kind:        KindIntegration,
			Priority:    60,
			Target:      "any",
			Provides:    []string{"servarr"},
			Health:      &Health{TCP: "8989"},
			Docs:        "Integration only: configure [servarr.*] sections in argus.",
		},
		{
			ID:          "hermes",
			Name:        "Hermes agent",
			Description: "Existing Hermes automation agent.",
			Category:    "Automation",
			Version:     "external",
			Kind:        KindIntegration,
			Priority:    70,
			Target:      "any",
			Provides:    []string{"hermes"},
			Health:      &Health{TCP: "7583"},
			Docs:        "Integration only: configure [hermes] url + api_key in argus.",
		},
	}
}

const baseSLS = `# Managed by argus — opinionated platform baseline.
argus-base-packages:
  pkg.installed:
    - names:
      - ca-certificates
      - chrony
      - curl
      - jq
      - unattended-upgrades

argus-base-chrony:
  service.running:
    - name: chrony
    - enable: True
    - require:
      - pkg: argus-base-packages

argus-base-unattended:
  service.running:
    - name: unattended-upgrades
    - enable: True
    - require:
      - pkg: argus-base-packages
`

const saltMinionSLS = `# Managed by argus — connect this host to the Salt master.
argus-salt-minion-config:
  file.managed:
    - name: /etc/salt/minion.d/argus.conf
    - contents:
      - "master: {{ pillar.get('salt_master', '127.0.0.1') }}"
      - "id: {{ grains.get('id', grains.get('host', 'minion')) }}"
    - makedirs: True

argus-salt-minion:
  pkg.installed:
    - name: salt-minion

argus-salt-minion-service:
  service.running:
    - name: salt-minion
    - enable: True
    - require:
      - pkg: argus-salt-minion
      - file: argus-salt-minion-config
`

const dockerSLS = `# Managed by argus — container runtime.
argus-docker-packages:
  pkg.installed:
    - names:
      - docker.io
      - docker-compose

argus-docker-service:
  service.running:
    - name: docker
    - enable: True
    - require:
      - pkg: argus-docker-packages
`

const nodeExporterSLS = `# Managed by argus — host metrics.
argus-node-exporter:
  pkg.installed:
    - name: prometheus-node-exporter

argus-node-exporter-service:
  service.running:
    - name: prometheus-node-exporter
    - enable: True
    - require:
      - pkg: argus-node-exporter
`

const technitiumSLS = `# Managed by argus — Technitium DNS container.
include:
  - argus.docker

/opt/argus/technitium:
  file.directory:
    - makedirs: True

argus-technitium-compose:
  file.managed:
    - name: /opt/argus/technitium/compose.yaml
    - source: salt://argus/technitium/compose.yaml
    - makedirs: True

argus-technitium-up:
  cmd.run:
    - name: docker-compose up -d
    - cwd: /opt/argus/technitium
    - unless: docker-compose ps --services --filter status=running | grep -qx technitium
    - require:
      - file: argus-technitium-compose
      - pkg: argus-docker-packages
      - service: argus-docker-service
`

const technitiumCompose = `services:
  technitium:
    image: technitium/dns-server:latest
    container_name: technitium
    restart: unless-stopped
    ports:
      - "53:53/tcp"
      - "53:53/udp"
      - "5380:5380/tcp"
    volumes:
      - technitium-config:/etc/dns
volumes:
  technitium-config:
`

const uptimeKumaSLS = `# Managed by argus — Uptime Kuma container.
include:
  - argus.docker

/opt/argus/uptime-kuma:
  file.directory:
    - makedirs: True

argus-uptime-kuma-compose:
  file.managed:
    - name: /opt/argus/uptime-kuma/compose.yaml
    - source: salt://argus/uptime_kuma/compose.yaml
    - makedirs: True

argus-uptime-kuma-up:
  cmd.run:
    - name: docker-compose up -d
    - cwd: /opt/argus/uptime-kuma
    - unless: docker-compose ps --services --filter status=running | grep -qx uptime-kuma
    - require:
      - file: argus-uptime-kuma-compose
      - pkg: argus-docker-packages
      - service: argus-docker-service
`

const uptimeKumaCompose = `services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    container_name: uptime-kuma
    restart: unless-stopped
    ports:
      - "3001:3001"
    volumes:
      - uptime-kuma:/app/data
volumes:
  uptime-kuma:
`

const traefikSLS = `# Managed by argus — Traefik container.
include:
  - argus.docker

/opt/argus/traefik:
  file.directory:
    - makedirs: True

argus-traefik-compose:
  file.managed:
    - name: /opt/argus/traefik/compose.yaml
    - source: salt://argus/traefik/compose.yaml
    - makedirs: True

argus-traefik-up:
  cmd.run:
    - name: docker-compose up -d
    - cwd: /opt/argus/traefik
    - unless: docker-compose ps --services --filter status=running | grep -qx traefik
    - require:
      - file: argus-traefik-compose
      - pkg: argus-docker-packages
      - service: argus-docker-service
`

const traefikCompose = `services:
  traefik:
    image: traefik:v3
    container_name: traefik
    restart: unless-stopped
    command:
      - --api.dashboard=false
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --entrypoints.web.address=:80
      - --entrypoints.websecure.address=:443
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
`
