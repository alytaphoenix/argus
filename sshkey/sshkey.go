// Package sshkey generates and validates the SSH key material Argus hands to
// Proxmox cloud-init. Public keys are normalised into the canonical
// authorized_keys form Proxmox expects ("<type> <base64>"); private material
// never leaves this package's return values.
package sshkey

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DefaultPrivateKeyPath returns the absolute path of the keypair Argus manages
// for cloud-init. It is absolute on purpose: the SSH invocations elsewhere in
// argus pass the value straight to exec.Command, which does not expand "~".
func DefaultPrivateKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "argus_ed25519"), nil
}

// NormalizePublicKeys parses one or more OpenSSH public keys and returns them
// in canonical form, one per line, without a trailing newline. Blank input,
// malformed keys, non-key lines (including "#" comments) and anything else
// that would be rejected by Proxmox produce an error. Inline comments are
// dropped: cloud-init only needs the type and the blob.
func NormalizePublicKeys(raw string) (string, error) {
	var canonical []string
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return "", fmt.Errorf("line %d: %v", i+1, err)
		}
		if strings.TrimSpace(string(rest)) != "" {
			return "", fmt.Errorf("line %d: unexpected data after the key", i+1)
		}
		canonical = append(canonical, strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n"))
	}
	if len(canonical) == 0 {
		return "", errors.New("no SSH public key provided")
	}
	return strings.Join(canonical, "\n"), nil
}

// Generate creates an unencrypted Ed25519 keypair, writing the private key to
// privatePath (mode 0600) and the public key to privatePath+".pub" (mode
// 0644). The parent directory is created with mode 0700. Files are written
// through temporary files in the same directory and renamed into place, so a
// crashed run never leaves a half-written key. With overwrite=false an
// existing private or public path is an error and nothing is replaced.
// Only the canonical public key is returned.
func Generate(privatePath string, overwrite bool) (string, error) {
	if privatePath == "" {
		return "", errors.New("private key path is empty")
	}
	privateKeyPath := privatePath
	publicKeyPath := privatePath + ".pub"

	if !overwrite {
		for _, path := range []string{privateKeyPath, publicKeyPath} {
			switch _, err := os.Stat(path); {
			case err == nil:
				return "", fmt.Errorf("%s already exists; refusing to overwrite", path)
			case !errors.Is(err, os.ErrNotExist):
				return "", err
			}
		}
	}

	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(signer, keyComment())
	if err != nil {
		return "", fmt.Errorf("encode private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(block)

	sshPub, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return "", fmt.Errorf("encode public key: %w", err)
	}
	publicKey := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")

	dir := filepath.Dir(privateKeyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	// Track what this call created so a later failure can't leave a private
	// key behind with no matching .pub (Ensure treats a partial pair as an
	// error the user has to resolve).
	var created []string
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}
	if _, err := os.Stat(privateKeyPath); errors.Is(err, os.ErrNotExist) {
		created = append(created, privateKeyPath)
	}
	if _, err := os.Stat(publicKeyPath); errors.Is(err, os.ErrNotExist) {
		created = append(created, publicKeyPath)
	}

	if err := writeAtomically(privateKeyPath, privPEM, 0o600); err != nil {
		cleanup()
		return "", err
	}
	if err := writeAtomically(publicKeyPath, []byte(publicKey+"\n"), 0o644); err != nil {
		cleanup()
		return "", err
	}
	return publicKey, nil
}

// Ensure returns the public half of the keypair at privatePath, generating the
// pair when neither file exists yet. An existing pair is reused only after the
// private key is parsed and shown to derive exactly the ".pub" contents, which
// makes a resumed first-run (key written, config not yet) idempotent. A partial
// or mismatched pair is reported rather than silently repaired, because either
// file might be in use by something outside argus.
func Ensure(privatePath string) (string, error) {
	if privatePath == "" {
		return "", errors.New("private key path is empty")
	}
	privateKeyPath := privatePath
	publicKeyPath := privatePath + ".pub"

	privErr := existErr(privateKeyPath)
	pubErr := existErr(publicKeyPath)

	switch {
	case privErr != nil && pubErr != nil:
		return Generate(privateKeyPath, false)
	case privErr != nil:
		return "", fmt.Errorf("%s is missing but %s exists; complete or remove the pair to generate a new key", privateKeyPath, publicKeyPath)
	case pubErr != nil:
		return "", fmt.Errorf("%s is missing but %s exists; complete or remove the pair to generate a new key", publicKeyPath, privateKeyPath)
	}

	privPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", privateKeyPath, err)
	}
	canonical, err := publicKeyFromPrivatePEM(privPEM)
	if err != nil {
		return "", fmt.Errorf("%s: %w", privateKeyPath, err)
	}
	onDisk, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", publicKeyPath, err)
	}
	normalized, err := NormalizePublicKeys(string(onDisk))
	if err != nil {
		return "", fmt.Errorf("%s: %v", publicKeyPath, err)
	}
	if normalized != canonical {
		return "", fmt.Errorf("%s does not match %s; remove the pair or generate a new key elsewhere", publicKeyPath, privateKeyPath)
	}
	return canonical, nil
}

// Fingerprint returns the SHA-256 fingerprint of exactly one public key, the
// form operators recognise from ssh -v output.
func Fingerprint(publicKey string) (string, error) {
	normalized, err := NormalizePublicKeys(publicKey)
	if err != nil {
		return "", err
	}
	if strings.Contains(normalized, "\n") {
		return "", fmt.Errorf("expected exactly one SSH public key, got %d", strings.Count(normalized, "\n")+1)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(normalized))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// publicKeyFromPrivatePEM derives the canonical authorized_keys line for the
// unencrypted private key in pemBytes.
func publicKeyFromPrivatePEM(pemBytes []byte) (string, error) {
	raw, err := ssh.ParseRawPrivateKey(pemBytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %v", err)
	}
	signer, ok := raw.(crypto.Signer)
	if !ok {
		return "", errors.New("parse private key: not a usable signing key")
	}
	pub, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n"), nil
}

// keyComment tags the generated private key the way ssh-keygen would, so a
// user inspecting the file can tell where it came from.
func keyComment() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	return "argus@" + host
}

// existErr returns nil when path exists and a non-ErrNotExist error only when
// the filesystem itself is unhappy (so a permission error is not mistaken for
// "absent, go ahead and write").
func existErr(path string) error {
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	return err
}

// writeAtomically writes data to path through a temp file in the same
// directory, so readers only ever see a complete file.
func writeAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// No-op once the rename has succeeded.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
