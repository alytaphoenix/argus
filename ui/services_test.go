package ui

import "testing"

func TestServiceSlug(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Grafana", "grafana"},
		{"Proxmox Web UI", "proxmox-web-ui"},
		{"signal-cli REST API", "signal-cli-rest-api"},
		{"MinIO S3 API", "minio-s3-api"},
		{"Hermes API Server", "hermes-api-server"},
		{"  leading/trailing  ", "leading-trailing"},
		{"ALL CAPS", "all-caps"},
		{"foo--bar", "foo-bar"},
		{"123service", "123service"},
		{"!@#$%", ""},
		{"a", "a"},
	}
	for _, tc := range cases {
		got := serviceSlug(tc.input)
		if got != tc.want {
			t.Errorf("serviceSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
