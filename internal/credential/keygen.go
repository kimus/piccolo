package credential

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// FileName is the key file name Piccolo writes. It matches the name PicoClaw
// auto-detects at ~/.ssh/picoclaw_ed25519.key so a user reading the workspace
// recognises it, even though Piccolo points at it explicitly through
// PICOCLAW_SSH_KEY_PATH instead of relying on that search.
const FileName = "picoclaw_ed25519.key"

// Generate writes an Ed25519 key pair into keyDir as keyDir/FileName plus the
// public half beside it, creating the directory owner-only.
//
// It mirrors the pinned upstream keygen byte-for-byte: the private key is the
// OpenSSH PEM produced by ssh.MarshalPrivateKey with an empty comment, written
// 0600, and the public key is the authorized_keys line written 0644. The
// derivation hashes this file's raw bytes, so "byte-for-byte" is not cosmetic —
// a differently encoded file with the same key material is a different secret
// to the gateway.
//
// An existing key is never overwritten. Rotating it invalidates every credential
// already encrypted with it, and that is a deliberate act with its own command
// (rotate), not a side effect of re-running a setup step.
func Generate(keyDir string) (path string, err error) {
	path = filepath.Join(keyDir, FileName)
	if _, statErr := os.Stat(path); statErr == nil {
		return path, fmt.Errorf("credential: %s already exists; rotating it would invalidate every credential encrypted with it", path)
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("credential: cannot inspect %s: %w", path, statErr)
	}

	// 0700 on the directory: the private key inside is 0600, but a readable
	// directory still lets another user enumerate and replace entries.
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return "", fmt.Errorf("credential: create key directory %q: %w", keyDir, err)
	}

	pubRaw, privRaw, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("credential: generate ed25519 key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(privRaw, "")
	if err != nil {
		return "", fmt.Errorf("credential: marshal private key: %w", err)
	}
	// MkdirAll does not change permissions on a directory that already exists, so
	// an inherited 0755 keys dir would survive into the workspace. Set it
	// explicitly every time.
	if err := os.Chmod(keyDir, 0o700); err != nil {
		return "", fmt.Errorf("credential: permission on %q: %w", keyDir, err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", fmt.Errorf("credential: write private key %q: %w", path, err)
	}

	sshPub, err := ssh.NewPublicKey(pubRaw)
	if err != nil {
		return "", fmt.Errorf("credential: marshal public key: %w", err)
	}
	pubPath := path + ".pub"
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		return "", fmt.Errorf("credential: write public key %q: %w", pubPath, err)
	}
	return path, nil
}
