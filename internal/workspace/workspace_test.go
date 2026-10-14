package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLayoutPathsAndBinds(t *testing.T) {
	l := NewLayout("/ws")
	if l.Manifest() != filepath.Join("/ws", "piccolo.yaml") {
		t.Fatalf("manifest = %s", l.Manifest())
	}
	if l.Config() != filepath.Join("/ws", "picoclaw", "config.json") {
		t.Fatalf("config = %s", l.Config())
	}
	want := []string{
		// Only the media mirror is bound. The toolchain and the Nix store are
		// image content: mounting the workspace cache over /nix HID the store
		// the image baked, which is how a gateway ended up unable to execute its
		// own integration binaries.
		filepath.Join("/ws", "runtime", "media") + ":" + ContainerMedia + ":rw",
	}
	if !reflect.DeepEqual(l.RuntimeBinds(), want) {
		t.Fatalf("runtime binds = %#v", l.RuntimeBinds())
	}
	if got := l.IntegrationBind(); got != filepath.Join("/ws", "integrations")+":"+ContainerIntegrations+":rw" {
		t.Fatalf("integration bind = %s", got)
	}
}

func TestEnsureDirsAndLegacyDetection(t *testing.T) {
	dir := t.TempDir()
	if IsLegacyFlat(dir) {
		t.Fatal("empty dir must not be legacy")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsLegacyFlat(dir) {
		t.Fatal("flat config+agents must be legacy")
	}
	l := NewLayout(dir)
	if err := l.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.PicoclawHome(), l.Integrations(), l.RuntimeDevbox(), l.RuntimeNix(), l.RuntimeMedia()} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s err=%v", p, err)
		}
	}
	if !IsLegacyFlat(dir) {
		t.Fatal("legacy config should still be reported until picoclaw/config.json exists")
	}
	if err := os.WriteFile(filepath.Join(l.PicoclawHome(), "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsLegacyFlat(dir) {
		t.Fatal("structured config must not be reported legacy")
	}
}
