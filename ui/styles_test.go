package ui

import (
	"strings"
	"testing"
)

func TestSanitizeTextDropsControlSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\x00a\nb\tc\u202e"
	got := sanitizeText(in)
	for _, bad := range []string{"\x1b", "\x00", "\n", "\t", "\u202e"} {
		if strings.ContainsRune(got, []rune(bad)[0]) {
			t.Fatalf("sanitizeText left %q in %q", bad, got)
		}
	}
	if got != "[31mred[0ma b c" {
		t.Fatalf("sanitizeText = %q", got)
	}
}

func TestTruncateSanitizes(t *testing.T) {
	if got := truncate("\x1b[2Jevil", 20); strings.ContainsRune(got, '\x1b') {
		t.Fatalf("truncate left an escape: %q", got)
	}
}
