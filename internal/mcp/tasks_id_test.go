package mcp

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestTaskIDCodec(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 30, 15, 999, time.UTC)
	render := taskRender{Budget: &contractsv1.MCPInvestigationBudget{MaxDrivers: 3, MaxSerializedBytes: 65536}, IncludeFullResult: true}
	id, raw, err := newTaskID(now, render)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > taskIDMaxBytes || strings.ContainsAny(raw, " \t") || !contractsv1.ValidContextFabricClientCorrelationID(id.submissionID) {
		t.Fatalf("task id %q (submission %q) out of its bounds", raw, id.submissionID)
	}
	parsed, ok := parseTaskID(raw)
	if !ok || parsed.submissionID != id.submissionID || !parsed.createdAt.Equal(now.Truncate(time.Second)) ||
		parsed.render.Budget == nil || *parsed.render.Budget != *render.Budget || !parsed.render.IncludeFullResult {
		t.Fatalf("parse(%q) = %+v %v", raw, parsed, ok)
	}
	seen := map[string]bool{}
	for range 1000 {
		other, otherRaw, err := newTaskID(now, taskRender{})
		if err != nil || seen[other.submissionID] {
			t.Fatalf("task id %q not unique (%v)", otherRaw, err)
		}
		seen[other.submissionID] = true
	}
	parts := strings.Split(raw, ".")
	tag := func(json string) string { return base64.RawURLEncoding.EncodeToString([]byte(json)) }
	join := func(p ...string) string { return strings.Join(p, ".") }
	for name, candidate := range map[string]string{
		"empty":                  "",
		"wrong prefix":           join("task", parts[1], parts[2], parts[3]),
		"three parts":            join(parts[0], parts[1], parts[2]),
		"five parts":             raw + ".x",
		"short random":           join(parts[0], parts[1][:25], parts[2], parts[3]),
		"upper random":           join(parts[0], strings.ToUpper(parts[1]), parts[2], parts[3]),
		"random not base32":      join(parts[0], strings.Repeat("1", 26), parts[2], parts[3]),
		"time not base36":        join(parts[0], parts[1], "!!", parts[3]),
		"time zero":              join(parts[0], parts[1], "0", parts[3]),
		"time negative":          join(parts[0], parts[1], "-1", parts[3]),
		"time leading zero":      join(parts[0], parts[1], "0"+parts[2], parts[3]),
		"tag not base64url":      join(parts[0], parts[1], parts[2], "%%%"),
		"tag not json":           join(parts[0], parts[1], parts[2], tag("{")),
		"tag unknown field":      join(parts[0], parts[1], parts[2], tag(`{"x":1}`)),
		"tag trailing value":     join(parts[0], parts[1], parts[2], tag(`{} {}`)),
		"tag wrong budget type":  join(parts[0], parts[1], parts[2], tag(`{"b":"big"}`)),
		"tag fractional integer": join(parts[0], parts[1], parts[2], tag(`{"b":{"max_drivers":1.5}}`)),
		"over 128 bytes":         join(parts[0], parts[1], parts[2], tag(`{"b":{"max_drivers":1,"max_cohort_members":2,"max_evidence_refs":3,"max_serialized_bytes":65536}, "f": true                  }`)),
	} {
		if _, ok := parseTaskID(candidate); ok {
			t.Errorf("%s: parse(%q) accepted", name, candidate)
		}
	}
}
