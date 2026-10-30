package sshkey

import (
	"crypto"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testKey generates a throwaway keypair and returns its canonical public key,
// so the fixtures are keys the parser actually accepts.
func testKey(t *testing.T) string {
	t.Helper()
	pub, err := Generate(filepath.Join(t.TempDir(), "fixture"), false)
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	return pub
}

func mustParse(t *testing.T, publicKey string) ssh.PublicKey {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	return pub
}

func fileMode(t *testing.T, path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestGenerateWritesMatchingPairWithRestrictedModes(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "nested", "id_ed25519")

	publicKey, err := Generate(privatePath, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mustParse(t, publicKey)
	if strings.Contains(publicKey, "PRIVATE") {
		t.Fatalf("Generate leaked private material: %q", publicKey)
	}

	if got := fileMode(t, privatePath); got != 0o600 {
		t.Errorf("private key mode = %o, want 600", got)
	}
	if got := fileMode(t, privatePath+".pub"); got != 0o644 {
		t.Errorf("public key mode = %o, want 644", got)
	}
	if got := fileMode(t, filepath.Dir(privatePath)); got != 0o700 {
		t.Errorf("key directory mode = %o, want 700", got)
	}

	// The private half must be an unencrypted OpenSSH PEM that derives the
	// public key we were handed.
	pemBytes, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("private key is not valid PEM")
	}
	if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
		t.Error("private key is passphrase-encrypted")
	}
	raw, err := ssh.ParseRawPrivateKey(pemBytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	signer, ok := raw.(crypto.Signer)
	if !ok {
		t.Fatalf("private key is not a signer: %T", raw)
	}
	derived, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	if want := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(derived)), "\n"); want != publicKey {
		t.Errorf("private half derives %q, want %q", want, publicKey)
	}

	// The .pub file must contain exactly the returned key.
	onDisk, err := os.ReadFile(privatePath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	if strings.TrimSuffix(string(onDisk), "\n") != publicKey {
		t.Errorf(".pub contains %q, want %q", strings.TrimSpace(string(onDisk)), publicKey)
	}

	// No leftover temp files from the atomic write.
	entries, err := os.ReadDir(filepath.Dir(privatePath))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("key dir holds %d entries, want 2 (%v)", len(entries), entries)
	}
}

func TestGenerateRefusesExistingFilesWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privatePath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(privatePath, false); err == nil {
		t.Fatal("expected error when the private key already exists")
	}
	if _, err := os.ReadFile(privatePath + ".pub"); !os.IsNotExist(err) {
		t.Errorf("failed Generate created %s.pub: %v", filepath.Base(privatePath), err)
	}

	privatePath = filepath.Join(dir, "clean")
	pubPath := privatePath + ".pub"
	fixture := testKey(t)
	if err := os.WriteFile(pubPath, []byte(fixture+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(privatePath, false); err == nil {
		t.Fatal("expected error when only the public key already exists")
	}
	got, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != fixture {
		t.Errorf("existing public key was modified: %q", got)
	}
}

func TestGenerateOverwriteReplacesPair(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "id_ed25519")

	first, err := Generate(privatePath, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := Generate(privatePath, true)
	if err != nil {
		t.Fatalf("Generate(overwrite): %v", err)
	}
	if first == second {
		t.Error("overwrite generated the same key twice")
	}
	if got := fileMode(t, privatePath); got != 0o600 {
		t.Errorf("rotated private key mode = %o, want 600", got)
	}
	if _, err := Ensure(privatePath); err != nil {
		t.Errorf("rotated pair does not validate: %v", err)
	}
}

func TestEnsureReusesExistingPair(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "id_ed25519")

	generated, err := Generate(privatePath, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	before, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}

	reused, err := Ensure(privatePath)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if reused != generated {
		t.Errorf("Ensure returned %q, want the existing %q", reused, generated)
	}
	after, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("Ensure regenerated an existing keypair")
	}
}

func TestEnsureGeneratesWhenAbsent(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), ".ssh", "argus_ed25519")

	publicKey, err := Ensure(privatePath)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	mustParse(t, publicKey)
	if got := fileMode(t, privatePath); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestEnsureRejectsPartialOrMismatchedPairs(t *testing.T) {
	dir := t.TempDir()

	fixture := testKey(t)

	onlyPublic := filepath.Join(dir, "onlypublic")
	if err := os.WriteFile(onlyPublic+".pub", []byte(fixture+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(onlyPublic); err == nil {
		t.Error("expected error when only the public key exists")
	}
	if _, err := os.Stat(onlyPublic); !os.IsNotExist(err) {
		t.Error("Ensure created a private key next to an orphan .pub")
	}

	onlyPrivate := filepath.Join(dir, "onlyprivate")
	if _, err := Generate(onlyPrivate, false); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.Remove(onlyPrivate + ".pub"); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(onlyPrivate); err == nil {
		t.Error("expected error when only the private key exists")
	}
	if _, err := os.Stat(onlyPrivate + ".pub"); !os.IsNotExist(err) {
		t.Error("Ensure wrote a .pub over a partial pair")
	}

	mismatched := filepath.Join(dir, "mismatch")
	if _, err := Generate(mismatched, false); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.WriteFile(mismatched+".pub", []byte(fixture+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(mismatched); err == nil {
		t.Error("expected error when the .pub does not match the private key")
	}

	unreadable := filepath.Join(dir, "junk")
	if err := os.WriteFile(unreadable, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unreadable+".pub", []byte(fixture+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(unreadable); err == nil {
		t.Error("expected error for an unparseable private key")
	}
}

func TestNormalizePublicKeys(t *testing.T) {
	fixture := testKey(t)
	other, err := Generate(filepath.Join(t.TempDir(), "second"), false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	twoKeys := other + "\n" + fixture + " with a wordy comment\n"
	got, err := NormalizePublicKeys(twoKeys)
	if err != nil {
		t.Fatalf("NormalizePublicKeys: %v", err)
	}
	want := other + "\n" + fixture
	if got != want {
		t.Errorf("normalized =\n%q\nwant\n%q", got, want)
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("normalized output must not end with a newline")
	}
	// Idempotent: feeding the canonical form back is a no-op.
	again, err := NormalizePublicKeys(got)
	if err != nil || again != got {
		t.Errorf("second pass = %q, %v; want %q, nil", again, err, got)
	}

	for name, raw := range map[string]string{
		"empty":            "",
		"whitespace":       "  \n\t\n",
		"comment only":     "# just a comment\n",
		"prose after key":  fixture + "\nplease deploy this\n",
		"prose before key": "BEGIN PASTE\n" + fixture,
		"truncated blob":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5\n",
		"unknown type":     "ssh-nonsense AAAAC3NzaC1lZDI1NTE5AAAA\n",
		"private key":      "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n-----END OPENSSH PRIVATE KEY-----\n",
		"blank line junk":  fixture + "\n\n   x y z\n",
	} {
		if got, err := NormalizePublicKeys(raw); err == nil {
			t.Errorf("%s: expected error, got %q", name, got)
		}
	}
}

func TestFingerprint(t *testing.T) {
	fixture := testKey(t)
	fp, err := Fingerprint(fixture)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256: prefix", fp)
	}
	if _, err := Fingerprint(fixture + "\n" + fixture); err == nil {
		t.Error("expected error for multiple keys")
	}
	if _, err := Fingerprint("nope"); err == nil {
		t.Error("expected error for an invalid key")
	}
}

func TestDefaultPrivateKeyPathIsAbsolute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := DefaultPrivateKeyPath()
	if err != nil {
		t.Fatalf("DefaultPrivateKeyPath: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path = %q, want absolute", path)
	}
	if filepath.Base(path) != "argus_ed25519" || filepath.Base(filepath.Dir(path)) != ".ssh" {
		t.Errorf("path = %q, want <home>/.ssh/argus_ed25519", path)
	}
	if _, err := Ensure(path); err != nil {
		t.Fatalf("Ensure at default path: %v", err)
	}
	if got := fileMode(t, path); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}
