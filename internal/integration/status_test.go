package integration

import "testing"

func TestParseStatusValidation(t *testing.T) {
	status, err := ParseStatus([]byte(`{"schema":"piccolo.integration.status.v1","name":"gws","status":"ok","summary":"connected","checks":[{"id":"gmail","label":"Gmail","status":"ok","message":"connected"}]}`))
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if status.Name != "gws" || status.Checks[0].ID != "gmail" {
		t.Fatalf("status = %#v", status)
	}
	if _, err := ParseStatus([]byte(`{"schema":"bad","name":"gws","status":"ok","checks":[{"id":"x","label":"X","status":"ok"}]}`)); err == nil {
		t.Fatal("expected schema error")
	}
	if _, err := ParseStatus([]byte(`{"schema":"piccolo.integration.status.v1","name":"gws","status":"ok","checks":[{"id":"x","label":"X","status":"bogus"}]}`)); err == nil {
		t.Fatal("expected check status error")
	}
}
