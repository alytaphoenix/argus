package api

// WiringStateTemplate is the Salt/Jinja state pushed to the master by argus.
// Deploy target: /srv/salt/services/wiring.sls.
//
// The wiring package applies it with an inline pillar, so no
// /srv/pillar/top.sls entry is required. The pillar supplies:
//
//	technitium_url          Technitium base URL
//	technitium_token_file   root-only token file path (never the token)
//	traefik_dir             dynamic config directory
//	traefik_static_config   Traefik static config (ACME when configured)
//	services                map of slug -> host/port/scheme/fqdn/cert
//
// Apply: salt '<master_minion>' state.apply services.wiring
const WiringStateTemplate = `# services.wiring — managed by argus
# Regenerated automatically from the service registry; do not edit.
#
# The pillar (argus_services.sls) contains the Traefik static config, the
# Technitium token *path* (never the token), DNS metadata, and one entry per
# service. Traefik obtains certificates through the step-ca ACME resolver when
# [stepca] acme_provisioner/acme_url is configured.

{% set dns_url          = pillar.get('technitium_url', '') %}
{% set dns_token_file   = pillar.get('technitium_token_file', '') %}
{% set traefik_dir      = pillar.get('traefik_dir', '/etc/traefik/dynamic') %}
{% set traefik_acme_dir = pillar.get('traefik_acme_dir', '/etc/traefik/acme') %}
{% set traefik_static   = pillar.get('traefik_static_path', '/etc/traefik/traefik.yml') %}
{% set static_config    = pillar.get('traefik_static_config', '') %}
{% set cert_resolver    = pillar.get('stepca_cert_resolver', '') %}

traefik-dirs:
  file.directory:
    - names:
      - {{ traefik_dir }}
      - {{ traefik_acme_dir }}
    - makedirs: True

{% if static_config %}
traefik-static:
  file.managed:
    - name: {{ traefik_static }}
    - makedirs: True
    - contents: |
{{ static_config | indent(8, true) }}
    - watch_in:
      - cmd: traefik-restart
{% endif %}

traefik-restart:
  cmd.wait:
    - name: docker restart traefik
    - onlyif: docker ps --filter name=traefik --filter status=running -q | grep -q .

{% for name, svc in pillar.get('services', {}).items() %}
{% set fqdn       = svc.get('fqdn', name) %}
{% set host       = svc.get('host', '') %}
{% set port       = svc.get('port', 80) %}
{% set scheme     = svc.get('scheme', 'http') %}
{% set zone       = svc.get('dns_zone', '') %}
{% set dns_type   = svc.get('dns_type', 'A') %}
{% set dns_value  = svc.get('dns_value', host) %}

{% if dns_url and dns_token_file and zone and dns_value %}
{{ name }}_dns:
  cmd.run:
    - name: >-
        curl -sf -G "{{ dns_url }}/api/zones/records/add"
        --data-urlencode "token@{{ dns_token_file }}"
        --data-urlencode "zone={{ zone }}"
        --data-urlencode "domain={{ fqdn }}"
        --data-urlencode "type={{ dns_type }}"
        --data-urlencode "{% if dns_type == 'CNAME' %}cname{% else %}ipAddress{% endif %}={{ dns_value }}"
        --data-urlencode "overwrite=true"
    - unless: >-
        curl -sf -G "{{ dns_url }}/api/zones/records/get"
        --data-urlencode "token@{{ dns_token_file }}"
        --data-urlencode "zone={{ zone }}"
        --data-urlencode "domain={{ fqdn }}" |
        python3 -c "import sys,json; d=json.load(sys.stdin); rs=d.get('response',{}).get('records',[]); t='{{ dns_type }}'; v='{{ dns_value }}'; ok=any(r.get('type')==t and (r.get('rData',{}).get('ipAddress')==v or r.get('rData',{}).get('cname')==v) for r in rs); sys.exit(0 if ok else 1)"
    - output_loglevel: quiet
{% endif %}

{% if svc.get('traefik', false) and host %}
{{ name }}_traefik:
  file.managed:
    - name: {{ traefik_dir }}/{{ name }}.yml
    - makedirs: True
    - require:
      - file: traefik-dirs
    - contents: |
        http:
          routers:
            {{ name }}:
              rule: "Host(` + "`" + `{{ fqdn }}` + "`" + `)"
              entryPoints:
                - websecure
              service: {{ name }}
{% if svc.get('cert', false) and cert_resolver %}
              tls:
                certResolver: {{ cert_resolver }}
{% else %}
              tls: {}
{% endif %}
          services:
            {{ name }}:
              loadBalancer:
                servers:
                  - url: "{{ scheme }}://{{ host }}:{{ port }}"
{% endif %}

{% endfor %}

# Remove dynamic files for services that were deleted from the registry.
traefik-cleanup:
  cmd.run:
    - name: |
        set -e
        keep="{{ pillar.get('services', {}).keys() | join(' ') }}"
        for f in {{ traefik_dir }}/*.yml; do
          [ -e "$f" ] || continue
          b=$(basename "$f" .yml)
          case " $keep " in *" $b "*) ;; *) rm -f "$f" ;; esac
        done
    - require:
      - file: traefik-dirs
`

// NASMountTemplate is a Salt/Jinja state that mounts TrueNAS NFS shares on a
// minion. It reads from the cluster pillar key "storage" which argus
// populates via the Orchestrate → Pillar screen (t = sync TrueNAS, p = push).
//
// Required pillar structure (in /srv/pillar/argus_cluster.sls):
//
//	storage:
//	  tank_media:
//	    nfs_host: 192.168.1.10
//	    nfs_export: /mnt/tank/media
//	    mount_path: /mnt/tank/media
//	    nfs_options: ro,noatime      # optional, defaults shown
//
// Apply: salt '<minion>' state.apply storage.nas_mounts
const NASMountTemplate = `# storage.nas_mounts — managed by argus
# Apply this state on any minion that needs TrueNAS NFS shares mounted.
# Re-generate + push the cluster pillar from the Orchestrate screen first.

{% for name, share in pillar.get('storage', {}).items() %}
{% set mount_path = share.get('mount_path', '/mnt/' + name) %}
{% set nfs_host   = share.get('nfs_host', '') %}
{% set nfs_export = share.get('nfs_export', share.get('mount_path', '/mnt/' + name)) %}
{% set nfs_opts   = share.get('nfs_options', 'ro,noatime,nfsvers=4') %}

{% if nfs_host %}
{{ mount_path }}_dir:
  file.directory:
    - name: {{ mount_path }}
    - makedirs: true

{{ mount_path }}:
  mount.mounted:
    - device: {{ nfs_host }}:{{ nfs_export }}
    - fstype: nfs
    - opts: {{ nfs_opts }}
    - mkmnt: false
    - require:
      - file: {{ mount_path }}_dir
{% endif %}
{% endfor %}
`
