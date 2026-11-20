package mcp

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every valid render argument set round-trips exactly, inside 128 bytes.
func TestTaskRenderTagDomainRoundTrips(t *testing.T) {
	values := [][]int{
		{0, 1, 10, contractsv1.ContextFabricProjectedDriversMaxCount},
		{0, 1, 10, 100, contractsv1.ContextFabricProjectedCohortMaxCount},
		{0, 1, 10, 100, contractsv1.ContextFabricProjectedEvidenceMaxCount},
		{0, contractsv1.MCPInvestigationBudgetMinBytes, 65536, 100000, contractsv1.MCPInvestigationBudgetMaxBytes},
	}
	cells := 0
	for _, d := range values[0] {
		for _, c := range values[1] {
			for _, e := range values[2] {
				for _, b := range values[3] {
					for _, full := range []bool{false, true} {
						render := taskRender{IncludeFullResult: full}
						if d != 0 || c != 0 || e != 0 || b != 0 {
							render.Budget = &contractsv1.MCPInvestigationBudget{MaxDrivers: d, MaxCohortMembers: c, MaxEvidenceRefs: e, MaxSerializedBytes: b}
						}
						id, raw, err := newTaskID(time.Unix(1<<40, 0), render)
						if err != nil || len(raw) > taskIDMaxBytes {
							t.Fatalf("%+v: %v (%d bytes)", render, err, len(raw))
						}
						parsed, ok := parseTaskID(raw)
						if !ok || parsed.submissionID != id.submissionID || parsed.render.IncludeFullResult != full {
							t.Fatalf("%+v: parse(%q) = %+v %v", render, raw, parsed, ok)
						}
						if (render.Budget == nil) != (parsed.render.Budget == nil) || (render.Budget != nil && *render.Budget != *parsed.render.Budget) {
							t.Fatalf("%+v: round-tripped budget %+v", render.Budget, parsed.render.Budget)
						}
						cells++
					}
				}
			}
		}
	}
	if cells != 4*5*5*5*2 {
		t.Fatalf("%d cells", cells)
	}
	for _, render := range []taskRender{{Budget: &contractsv1.MCPInvestigationBudget{MaxDrivers: -1}}, {Budget: &contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 1 << 31}}} {
		if _, err := encodeRenderTag(render); err == nil {
			t.Fatalf("%+v encoded", render.Budget)
		}
	}
}

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
	tag := func(b ...byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	join := func(p ...string) string { return strings.Join(p, ".") }
	for name, candidate := range map[string]string{
		"empty":                    "",
		"wrong prefix":             join("task", parts[1], parts[2], parts[3]),
		"three parts":              join(parts[0], parts[1], parts[2]),
		"five parts":               raw + ".x",
		"short random":             join(parts[0], parts[1][:25], parts[2], parts[3]),
		"upper random":             join(parts[0], strings.ToUpper(parts[1]), parts[2], parts[3]),
		"random not base32":        join(parts[0], strings.Repeat("1", 26), parts[2], parts[3]),
		"time not base36":          join(parts[0], parts[1], "!!", parts[3]),
		"time zero":                join(parts[0], parts[1], "0", parts[3]),
		"time negative":            join(parts[0], parts[1], "-1", parts[3]),
		"time leading zero":        join(parts[0], parts[1], "0"+parts[2], parts[3]),
		"tag empty":                join(parts[0], parts[1], parts[2], ""),
		"tag not base64url":        join(parts[0], parts[1], parts[2], "%%%"),
		"tag padded":               join(parts[0], parts[1], parts[2], "AA=="),
		"tag json null":            join(parts[0], parts[1], parts[2], tag([]byte("null")...)),
		"tag json object":          join(parts[0], parts[1], parts[2], tag([]byte(`{"b":{}}`)...)),
		"tag unknown flag bit":     join(parts[0], parts[1], parts[2], tag(0x20)),
		"tag flag without value":   join(parts[0], parts[1], parts[2], tag(byte(renderFlagDrivers))),
		"tag zero value":           join(parts[0], parts[1], parts[2], tag(byte(renderFlagDrivers), 0)),
		"tag non-canonical varint": join(parts[0], parts[1], parts[2], tag(byte(renderFlagDrivers), 0x83, 0x00)),
		"tag trailing bytes":       join(parts[0], parts[1], parts[2], tag(byte(renderFlagDrivers), 3, 1)),
		"tag field out of range":   join(parts[0], parts[1], parts[2], tag(byte(renderFlagBytes), 0x80, 0x80, 0x80, 0x80, 0x08)),
		"tag over its length":      join(parts[0], parts[1], parts[2], tag(append([]byte{byte(renderFlagsKnown)}, make([]byte, 25)...)...)),
	} {
		if _, ok := parseTaskID(candidate); ok {
			t.Errorf("%s: parse(%q) accepted", name, candidate)
		}
	}
}
