// Package credential implements PicoClaw's enc:// credential format so Piccolo
// can store a workspace's API keys and channel tokens as ciphertext instead of
// plaintext.
//
// The scheme is PicoClaw's, not Piccolo's invention, and it is reproduced here
// because Piccolo has no Go dependency on PicoClaw and is not getting one: the
// pinned release lives in Docker images, not in this module graph. Everything in
// this file is pinned to github.com/sipeed/picoclaw@v0.3.1
// pkg/credential/credential.go, and internal/credential/compat/ verifies the
// reproduction against the real implementation in both directions.
//
// Wire format:
//
//	enc://base64(salt[16] || nonce[12] || AES-256-GCM ciphertext)
//
// Key derivation, per the same source:
//
//	sshHash = SHA256(private_key_file_bytes)
//	ikm     = HMAC-SHA256(key = sshHash, msg = passphrase)
//	key     = HKDF-SHA256(ikm, salt, info = "picoclaw-credential-v1", 32)
//
// Note what the second factor actually is: the SHA-256 of the key FILE'S RAW
// BYTES. Upstream never parses it as an SSH key, so the file is only ever a
// high-entropy second secret that both sides must read identically. Any rewrite
// of that file (a reformat, a trailing-newline change, a CRLF checkout) silently
// changes every credential derived from it, which is why keygen writes it once
// and nothing here touches it afterwards.
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Scheme is the prefix PicoClaw requires on an encrypted credential value.
const Scheme = "enc://"

// Constants copied from the pinned upstream implementation. Changing any of
// them produces ciphertext the gateway cannot read; the compat module fails
// loudly if that happens, so treat an edit here as a version bump.
const (
	hkdfInfo = "picoclaw-credential-v1"
	saltLen  = 16
	nonceLen = 12
	keyLen   = 32
)

// ErrPassphraseRequired is returned when no passphrase is available. Distinct
// from a decryption failure, because the remedy differs: one means "not
// supplied", the other means "supplied wrongly".
var ErrPassphraseRequired = errors.New("credential: passphrase required")

// ErrKeyRequired is returned when the SSH key file cannot be read. Like the
// passphrase, this is upstream's own distinct state ("SSH private key is
// required but not found").
var ErrKeyRequired = errors.New("credential: SSH private key not readable")

// ErrDecryptionFailed covers a wrong passphrase, a wrong key, and a payload that
// fails authentication. Upstream merges these into one error and so does this:
// AES-GCM cannot tell a bad key from a tampered ciphertext, and pretending
// otherwise would report a certainty the cryptography does not provide.
var ErrDecryptionFailed = errors.New("credential: decryption failed (wrong passphrase or SSH key?)")

// IsEncrypted reports whether a credential value is in the enc:// form.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), Scheme)
}

// Encrypt seals plaintext into an enc:// value.
//
// passphrase and keyPath are both required. An empty passphrase returns
// ErrPassphraseRequired rather than producing something that decrypts to
// nothing, because upstream refuses the same way and a mismatch there would
// write a credential the gateway rejects at boot.
func Encrypt(passphrase, keyPath, plaintext string) (string, error) {
	if passphrase == "" {
		return "", ErrPassphraseRequired
	}
	key, err := readKeyFile(keyPath)
	if err != nil {
		return "", err
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("credential: generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("credential: generate nonce: %w", err)
	}

	aesKey, err := deriveKey(passphrase, key, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("credential: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("credential: gcm init: %w", err)
	}

	// A fresh salt and nonce per call, so encrypting the same credential twice
	// yields different values. Nothing here is deterministic on purpose; a
	// workspace diff must not turn a re-encryption into a detectable no-op.
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	blob := make([]byte, 0, saltLen+nonceLen+len(sealed))
	blob = append(blob, salt...)
	blob = append(blob, nonce...)
	blob = append(blob, sealed...)
	return Scheme + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt opens an enc:// value. value may carry or omit the scheme prefix.
func Decrypt(passphrase, keyPath, value string) (string, error) {
	if passphrase == "" {
		return "", ErrPassphraseRequired
	}
	key, err := readKeyFile(keyPath)
	if err != nil {
		return "", err
	}

	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(value), Scheme))
	if err != nil {
		return "", fmt.Errorf("credential: invalid base64 payload: %w", err)
	}
	if len(blob) < saltLen+nonceLen+1 {
		return "", fmt.Errorf("credential: payload too short")
	}
	salt := blob[:saltLen]
	nonce := blob[saltLen : saltLen+nonceLen]
	sealed := blob[saltLen+nonceLen:]

	aesKey, err := deriveKey(passphrase, key, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("credential: cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("credential: gcm init: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	return string(plain), nil
}

// deriveKey mirrors upstream's deriveKey, with the key bytes supplied rather than
// read inside the function so the file access happens exactly once per call.
func deriveKey(passphrase string, keyBytes, salt []byte) ([]byte, error) {
	sshHash := sha256.Sum256(keyBytes)
	mac := hmac.New(sha256.New, sshHash[:])
	mac.Write([]byte(passphrase))
	ikm := mac.Sum(nil)

	out, err := hkdf.Key(sha256.New, ikm, salt, hkdfInfo, keyLen)
	if err != nil {
		return nil, fmt.Errorf("credential: HKDF expand failed: %w", err)
	}
	return out, nil
}

func readKeyFile(keyPath string) ([]byte, error) {
	if keyPath == "" {
		return nil, ErrKeyRequired
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrKeyRequired, keyPath, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: %q is empty", ErrKeyRequired, keyPath)
	}
	return data, nil
}
