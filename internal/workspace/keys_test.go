package workspace

import (
	"path/filepath"
	"testing"
)

func TestKeysPaths(t *testing.T) {
	l := NewLayout("/work/demo")

	if got, want := l.Keys(), filepath.Join("/work/demo", "picoclaw", "keys"); got != want {
		t.Errorf("Keys() = %q, want %q", got, want)
	}
	if got, want := l.KeyFile(), filepath.Join("/work/demo", "picoclaw", "keys", "picoclaw_ed25519.key"); got != want {
		t.Errorf("KeyFile() = %q, want %q", got, want)
	}
	// The container path is what serve hands to PicoClaw. It must land inside the
	// already-mounted picoclaw home, because the design adds no new bind mount: a
	// key the container cannot see is a credential nothing can decrypt.
	if got, want := l.ContainerKeyFile(), "/root/.picoclaw/keys/picoclaw_ed25519.key"; got != want {
		t.Errorf("ContainerKeyFile() = %q, want %q", got, want)
	}
	// Pin the relationship the whole scheme rests on: the key file is under the
	// directory that is mounted, so no future mount change can silently break it.
	if filepath.Dir(l.ContainerKeyFile()) != ContainerKeysDir {
		t.Error("ContainerKeyFile is not under ContainerKeysDir")
	}
	if ContainerKeysDir != filepath.Join(ContainerPicoclawHome, KeysDirName) {
		t.Error("ContainerKeysDir drifted from the mounted PicoClaw home")
	}
}

// The keys directory must NOT be in RuntimeDirs: creating it in every workspace
// would imply every workspace is encrypted, and an empty keys/ in a plaintext
// workspace is a standing invitation to believe credentials are protected.
func TestKeysDirNotCreatedByDefault(t *testing.T) {
	l := NewLayout(t.TempDir())
	for _, d := range l.RuntimeDirs() {
		if d == l.Keys() {
			t.Fatal("RuntimeDirs() creates the keys directory for every workspace; it should exist only once a workspace opts into encryption")
		}
	}
}
