package integration

import (
	"encoding/json"
	"fmt"
	"strings"
)

const StatusSchemaV1 = "piccolo.integration.status.v1"

type StatusResult struct {
	Schema  string        `json:"schema"`
	Name    string        `json:"name"`
	Status  string        `json:"status"`
	Summary string        `json:"summary,omitempty"`
	Checks  []StatusCheck `json:"checks"`
}

type StatusCheck struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

func ParseStatus(data []byte) (StatusResult, error) {
	var s StatusResult
	if err := json.Unmarshal(data, &s); err != nil {
		return StatusResult{}, fmt.Errorf("parse integration status: %w", err)
	}
	if err := s.Validate(); err != nil {
		return StatusResult{}, err
	}
	return s, nil
}

func (s StatusResult) Validate() error {
	if s.Schema != StatusSchemaV1 {
		return fmt.Errorf("unsupported integration status schema %q (want %q)", s.Schema, StatusSchemaV1)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("integration status name is required")
	}
	if !validStatus(s.Status) || s.Status == "skip" {
		return fmt.Errorf("integration status %q invalid", s.Status)
	}
	if len(s.Checks) == 0 {
		return fmt.Errorf("integration status checks are required")
	}
	for _, c := range s.Checks {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("integration status check id is required")
		}
		if strings.TrimSpace(c.Label) == "" {
			return fmt.Errorf("integration status check %q label is required", c.ID)
		}
		if !validStatus(c.Status) {
			return fmt.Errorf("integration status check %q has invalid status %q", c.ID, c.Status)
		}
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case "ok", "warn", "fail", "unknown", "skip":
		return true
	default:
		return false
	}
}
