package main

import "testing"

func TestScrapeTarget(t *testing.T) {
	cases := []struct{ listen, want string }{
		{":9099", "localhost:9099"},
		{"127.0.0.1:9099", "127.0.0.1:9099"},
		{"0.0.0.0:9099", "localhost:9099"},
		{"[::]:9099", "localhost:9099"},
		{"10.0.0.42:9099", "10.0.0.42:9099"},
		{"localhost:9099", "localhost:9099"},
	}
	for _, tc := range cases {
		if got := scrapeTarget(tc.listen); got != tc.want {
			t.Errorf("scrapeTarget(%q) = %q, want %q", tc.listen, got, tc.want)
		}
	}
}
