package api

// WiringStateTemplate is the Salt/Jinja state pushed to the master by ratataskr
// (Services screen → w). Deploy target: /srv/salt/services/wiring.sls.
//
// Required master pillar keys (add to your pillar):
//   technitium_url    http://dns.local:5380
//   technitium_token  <api-token>
//   traefik_dir       /etc/traefik/dynamic   (default)
//   stepca_url        https://ca.local:9000  (only needed for cert: true services)
//
// Add to /srv/pillar/top.sls:
//   base:
//     '<master_minion>':
//       - ratataskr_services
//
// Apply: salt '<master_minion>' state.apply services.wiring
const WiringStateTemplate = `# services.wiring — managed by ratataskr
# Regenerate pillar + re-apply with 'w' from the Services screen.
#
# Pillar shape (from /srv/pillar/ratataskr_services.sls):
#   services:
#     gitea:
#       host: 10.0.0.5
#       port: 3000
#       scheme: http
#       fqdn: gitea.home.arpa
#       dns_zone: home.arpa
#       traefik: true
#       cert: false

{% set dns_url     = pillar.get('technitium_url', '') %}
{% set dns_token   = pillar.get('technitium_token', '') %}
{% set traefik_dir = pillar.get('traefik_dir', '/etc/traefik/dynamic') %}
{% set stepca_url  = pillar.get('stepca_url', '') %}

{% for name, svc in pillar.get('services', {}).items() %}
{% set fqdn   = svc.get('fqdn', name) %}
{% set host   = svc.get('host', '') %}
{% set port   = svc.get('port', 80) %}
{% set scheme = svc.get('scheme', 'http') %}
{% set zone   = svc.get('dns_zone', '') %}

{% if dns_url and dns_token and zone and host %}
{{ name }}_dns:
  cmd.run:
    # Token is passed through the environment ($DNS_TOKEN) rather than embedded
    # in the command name, so it does not appear in job returns or process args.
    - name: >-
        curl -sf "{{ dns_url }}/api/zones/records/add?token=$DNS_TOKEN&zone={{ zone }}&domain={{ fqdn }}&type=A&ipAddress={{ host }}&overwrite=true"
    - unless: >-
        curl -sf "{{ dns_url }}/api/zones/records/get?token=$DNS_TOKEN&zone={{ zone }}&domain={{ fqdn }}" |
        python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if any(r.get('rData',{}).get('ipAddress')=='{{ host }}' for r in d.get('response',{}).get('records',[])) else 1)"
    - env:
        - DNS_TOKEN: '{{ dns_token }}'
    - output_loglevel: quiet
{% endif %}

{% if svc.get('traefik', false) %}
{{ name }}_traefik:
  file.managed:
    - name: {{ traefik_dir }}/{{ name }}.yml
    - makedirs: true
    - contents: |
        http:
          routers:
            {{ name }}:
              rule: "Host(` + "`" + `{{ fqdn }}` + "`" + `)"
              entryPoints:
                - websecure
              service: {{ name }}{% if svc.get('cert', false) %}
              tls: {}{% endif %}

          services:
            {{ name }}:
              loadBalancer:
                servers:
                  - url: "{{ scheme }}://{{ host }}:{{ port }}"
{% endif %}

{% if svc.get('cert', false) and stepca_url %}
{{ name }}_cert:
  cmd.run:
    - name: >-
        step ca certificate {{ fqdn }}
        /etc/step/certs/{{ fqdn }}.crt /etc/step/certs/{{ fqdn }}.key
        --ca-url {{ stepca_url }} --root /etc/step/certs/root_ca.crt
        --not-after 2160h --force
    # Issue when the cert is missing or within 30 days of expiry, so it renews
    # instead of only ever being created once.
    - onlyif: >-
        test ! -f /etc/step/certs/{{ fqdn }}.crt ||
        step certificate needs-renewal /etc/step/certs/{{ fqdn }}.crt --expires-in 720h
{% endif %}

{% endfor %}
`
