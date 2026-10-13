package integration

import "testing"

func TestResolveAlias(t *testing.T) {
	r, err := Resolve("gws")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Ref != "github:kimus/piccolo-gws" || r.Name != "gws" || r.DescriptorCommand[0] != "piccolo-gws" {
		t.Fatalf("resolved = %#v", r)
	}
	if !r.Unpinned {
		t.Fatal("alias should be marked unpinned")
	}
}

func TestResolveExplicitGithub(t *testing.T) {
	r, err := Resolve("github:kimus/piccolo-makerworld")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Name != "piccolo-makerworld" || r.DescriptorCommand[0] != "piccolo-makerworld" {
		t.Fatalf("resolved = %#v", r)
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
