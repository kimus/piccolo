// Package fileperm owns the permission rule for files that can hold a secret.
//
// It lives here rather than in manifest or configdoc because both of those write
// credential-bearing files and neither should own a rule the other needs: the
// layering runs downward (configdoc must not depend on piccolo.yaml's package),
// and the rule itself — strip access, never grant it — is a security invariant
// with a single correct answer. A second copy is how the gitignore list drifted
// into three disagreeing ones.
package fileperm

import "os"

// Private is the mode for a file that may hold a secret: owner only.
const Private os.FileMode = 0o600

// Tighten removes group and other bits WITHOUT granting anything the owner did
// not already have.
//
// The obvious implementation, chmod 0600, is wrong: on a deliberately read-only
// 0444 file — a supported way to stop Piccolo editing a config it owns — it adds
// the owner write bit back and quietly undoes that protection. So 0444 becomes
// 0400 and 0644 becomes 0600, and read-only stays read-only.
func Tighten(perm os.FileMode) os.FileMode {
	return perm & 0o700
}

// EnsurePrivate tightens an existing file to owner-only access. It reports whether
// anything changed, so a caller can say so instead of acting behind the user's back.
//
// A write is not always due: commands that only verify state return before writing
// their file, and a mode asserted only at write time would then leave a passphrase
// world-readable forever while doctor politely suggested chmod.
func EnsurePrivate(path string) (changed bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	want := Tighten(fi.Mode().Perm())
	if want == fi.Mode().Perm() {
		return false, nil
	}
	if err := os.Chmod(path, want); err != nil {
		return false, err
	}
	return true, nil
}

// WritePrivate writes data to path and then ASSERTS owner-only mode.
//
// The chmod is not decoration: os.WriteFile applies its perm argument only when the
// file is CREATED, so rewriting a pre-existing 0644 file leaves it 0644 — which made
// every "we write secrets at 0600" claim false for workspaces that already existed.
func WritePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, Private); err != nil {
		return err
	}
	return os.Chmod(path, Private)
}
