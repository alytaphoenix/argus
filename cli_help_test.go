package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// fn wrote. Dispatchers print usage and return, so no subprocess is needed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	read := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		read <- string(data)
	}()

	fn()

	os.Stdout = saved
	w.Close()
	out := <-read
	r.Close()
	return out
}

// Help flags arrive as args[0] and must reach the usage text instead of being
// swallowed by a subcommand default ("list", "plan").
func TestHelpFlagsPrintUsage(t *testing.T) {
	cases := []struct {
		name string
		cmd  func([]string)
		want []string
	}{
		{"stack", runStack, []string{"Usage:", "argus stack apply NAME"}},
		{"vm", runVM, []string{"Usage:", "argus vm delete"}},
		{"bootstrap", runBootstrap, []string{"Usage:", "argus bootstrap apply"}},
	}

	for _, tc := range cases {
		for _, flag := range []string{"-h", "--help", "help"} {
			t.Run(tc.name+"/"+flag, func(t *testing.T) {
				out := captureStdout(t, func() { tc.cmd([]string{flag}) })
				for _, want := range tc.want {
					if !strings.Contains(out, want) {
						t.Errorf("%s %s printed %q, want it to contain %q",
							tc.name, flag, out, want)
					}
				}
			})
		}
	}
}
