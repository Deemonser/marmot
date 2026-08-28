package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The store writes into the user's real config directory, so the round trip is
// exercised on a scratch account name that is cleaned up.
func TestCredentialRoundTrip(t *testing.T) {
	adapter := Adapter{}
	const account = "marmot-test-credential"
	t.Cleanup(func() { _ = adapter.DeleteCredential(account) })

	if _, err := adapter.LoadCredential(account); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound before writing, got %v", err)
	}
	if err := adapter.StoreCredential(account, "sk-first"); err != nil {
		t.Fatal(err)
	}
	value, err := adapter.LoadCredential(account)
	if err != nil || value != "sk-first" {
		t.Fatalf("read back %q, %v", value, err)
	}
	if err := adapter.StoreCredential(account, "sk-second"); err != nil {
		t.Fatal(err)
	}
	if value, err = adapter.LoadCredential(account); err != nil || value != "sk-second" {
		t.Fatalf("replacement read back %q, %v", value, err)
	}
	if err := adapter.DeleteCredential(account); err != nil {
		t.Fatal(err)
	}
	// Deleting what is already gone is what the caller wanted.
	if err := adapter.DeleteCredential(account); err != nil {
		t.Fatalf("second delete errored: %v", err)
	}
	if _, err := adapter.LoadCredential(account); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("expected ErrCredentialNotFound after delete, got %v", err)
	}
}

// The whole point of encrypting is that the secret is not sitting in the file as
// text, and that the file is not world-readable.
func TestCredentialFileIsNeitherPlainTextNorWorldReadable(t *testing.T) {
	adapter := Adapter{}
	const account = "marmot-test-opacity"
	const secret = "sk-a-very-recognisable-secret-value"
	t.Cleanup(func() { _ = adapter.DeleteCredential(account) })
	if err := adapter.StoreCredential(account, secret); err != nil {
		t.Fatal(err)
	}
	path, err := credentialPath(account)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the secret is in the file as plain text")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode is %v, expected 0600", perm)
	}
	if dir, err := os.Stat(filepath.Dir(path)); err == nil {
		if perm := dir.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("directory mode is %v, expected no group or other access", perm)
		}
	}
}

// A file written on another machine has a different key, so GCM authentication
// fails. Saying "cannot decrypt" beats "not found", which would send someone
// looking for a configuration that is sitting right there.
func TestCredentialFromAnotherMachineIsReportedAsUndecryptable(t *testing.T) {
	adapter := Adapter{}
	const account = "marmot-test-foreign"
	t.Cleanup(func() { _ = adapter.DeleteCredential(account) })
	if err := adapter.StoreCredential(account, "sk-value"); err != nil {
		t.Fatal(err)
	}
	path, err := credentialPath(account)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a ciphertext byte: indistinguishable, to the reader, from a file
	// sealed with a different machine's key.
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := adapter.LoadCredential(account)
	if err == nil {
		t.Fatalf("a tampered file decrypted to %q", value)
	}
	if errors.Is(err, ErrCredentialNotFound) {
		t.Fatal("a tampered file must not read as 'never configured'")
	}
}

// The account names are the app's own constants, but a path is built from one,
// so anything that could climb out of the directory is refused.
func TestCredentialPathRefusesTraversal(t *testing.T) {
	for _, account := range []string{"", "  ", "../escape", "a/b", `a\b`, "with.dot"} {
		if _, err := credentialPath(account); err == nil {
			t.Fatalf("account %q was accepted", account)
		}
	}
}
