package integration

import "testing"

func TestParsePackageRef(t *testing.T) {
	r, err := ParsePackageRef("kimus/piccolo-gws@3.1.0")
	if err != nil {
		t.Fatalf("ParsePackageRef: %v", err)
	}
	if r.Provider != "github" || r.Owner != "kimus" || r.Repo != "piccolo-gws" || r.Selector != "3.1.0" {
		t.Fatalf("ref = %#v", r)
	}
	if r.Package() != "github:kimus/piccolo-gws" || r.Short() != "kimus/piccolo-gws" || r.StatePath() != "integrations/kimus/piccolo-gws" {
		t.Fatalf("derived values from %#v", r)
	}
}

func TestParsePackageRefPreservesSelector(t *testing.T) {
	r, err := ParsePackageRef("github:johndoe/piccolo-maker@v2.1.10")
	if err != nil {
		t.Fatalf("ParsePackageRef: %v", err)
	}
	if r.Selector != "v2.1.10" {
		t.Fatalf("selector = %q", r.Selector)
	}
}

func TestParsePackageRefRejectsAlias(t *testing.T) {
	if _, err := ParsePackageRef("gws"); err == nil {
		t.Fatal("expected alias-like ref to be rejected")
	}
}

func TestParseReleaseManifest(t *testing.T) {
	m, err := ParseReleaseManifest([]byte(`{"schema":"piccolo.integration.release.v1","package":"github:kimus/piccolo-gws","version":"3.1.0","artifacts":[{"platform":"linux/amd64","asset":"piccolo-gws_3.1.0_linux_amd64.tar.gz","sha256":"abc"}]}`))
	if err != nil {
		t.Fatalf("ParseReleaseManifest: %v", err)
	}
	if a, ok := m.ArtifactFor("linux/amd64"); !ok || a.Asset == "" {
		t.Fatalf("artifact = %#v %v", a, ok)
	}
	if _, err := ParseReleaseManifest([]byte(`{"schema":"bad"}`)); err == nil {
		t.Fatal("expected schema error")
	}
}

func TestVerifySHA256(t *testing.T) {
	if err := VerifySHA256([]byte("hello"), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"); err != nil {
		t.Fatalf("VerifySHA256: %v", err)
	}
	if err := VerifySHA256([]byte("hello"), "bad"); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestParseDescriptorValidation(t *testing.T) {
	desc, err := ParseDescriptor([]byte(`{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","setup":{"command":["piccolo-gws","setup"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]},"commands":{"setup":{"command":"piccolo-gws","args":["setup"]},"status":{"command":"piccolo-gws","args":["status","--json"]}}}`))
	if err != nil {
		t.Fatalf("ParseDescriptor valid: %v", err)
	}
	if got := desc.StatusCommand; len(got) != 3 || got[0] != "piccolo-gws" || got[1] != "status" || got[2] != "--json" {
		t.Fatalf("status command = %#v", got)
	}
	_, err = ParseDescriptor([]byte(`{"schema":"piccolo.integration.v1","name":"gws","displayName":"Google Workspace","setup":{"command":["piccolo-gws","setup"],"requiresTty":true,"ports":["127.0.0.1:8000:8000"]},"commands":["piccolo-gws"]}`))
	if err != nil {
		t.Fatalf("ParseDescriptor valid: %v", err)
	}
	if _, err := ParseDescriptor([]byte(`{"schema":"bad","name":"gws","displayName":"G","setup":{"command":["x"]}}`)); err == nil {
		t.Fatal("expected schema error")
	}
	if _, err := ParseDescriptor([]byte(`{"schema":"piccolo.integration.v1","name":"bad name","displayName":"G","setup":{"command":["x"]}}`)); err == nil {
		t.Fatal("expected name error")
	}
}
