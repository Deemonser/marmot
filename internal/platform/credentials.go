package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/marmot/internal/ports"
)

// Credentials are kept in the app's own support directory, encrypted with
// AES-256-GCM.
//
// What this protects against, honestly: the file is not readable as plain text,
// it is 0600, and the key is derived from this machine's hardware UUID, so a
// copy of the file -- lifted from a backup, a synced folder, or another user's
// disk image -- decrypts nowhere else.
//
// What it does NOT protect against, equally honestly: anything running as this
// user on this machine can derive the same key from the same public UUID and
// read the secret. That is inherent to a symmetric key the app itself must be
// able to reconstruct unattended, and no amount of key-stretching changes it.
// The macOS keychain is the mechanism that does gate per-application access;
// this is a deliberate trade of that gate for a simpler interaction, and the
// trade is recorded rather than dressed up.

// ErrCredentialNotFound means the account has no stored secret. It is a normal
// state -- the app ships with no advisor configured -- and not a failure.
var ErrCredentialNotFound = ports.ErrCredentialNotFound

const credentialSalt = "marmot-advisor-credentials-v1"

// StoreCredential writes a secret, replacing any previous value.
func (a Adapter) StoreCredential(account, secret string) error {
	path, err := credentialPath(account)
	if err != nil {
		return err
	}
	sealed, err := sealCredential(secret)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written to a sibling and renamed so an interrupted write cannot leave a
	// half-file that decrypts to nothing and reads as "you never configured it".
	temp, err := os.CreateTemp(filepath.Dir(path), ".advisor-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(sealed); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// LoadCredential reads a secret, or returns ErrCredentialNotFound.
func (a Adapter) LoadCredential(account string) (string, error) {
	path, err := credentialPath(account)
	if err != nil {
		return "", err
	}
	sealed, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrCredentialNotFound
	}
	if err != nil {
		return "", err
	}
	return openCredential(sealed)
}

// DeleteCredential removes the account's secret. Deleting something that is not
// there is what the caller wanted, not a failure.
func (a Adapter) DeleteCredential(account string) error {
	path, err := credentialPath(account)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func credentialPath(account string) (string, error) {
	clean := strings.TrimSpace(account)
	// The account names are the app's own constants, but a path is being built
	// from one, so anything that could climb out of the directory is refused
	// rather than sanitised into something surprising.
	if clean == "" || strings.ContainsAny(clean, `/\.`) {
		return "", fmt.Errorf("invalid credential account %q", account)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marmot", clean+".enc"), nil
}

// credentialKey binds the ciphertext to this machine. The UUID is not a secret
// -- see the package comment -- but it does mean a copied file is inert.
func credentialKey() ([]byte, error) {
	host, err := machineIdentifier()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(host + "|" + credentialSalt))
	return sum[:], nil
}

func sealCredential(secret string) ([]byte, error) {
	gcm, err := credentialCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

func openCredential(sealed []byte) (string, error) {
	gcm, err := credentialCipher()
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("凭据文件已损坏")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		// Authentication failure means the file was written on another machine or
		// tampered with. Saying so beats "not found", which would send the user
		// looking for a configuration that is right there.
		return "", errors.New("凭据无法解密：文件可能来自另一台机器，或已被改动")
	}
	return string(plain), nil
}

func credentialCipher() (cipher.AEAD, error) {
	key, err := credentialKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
