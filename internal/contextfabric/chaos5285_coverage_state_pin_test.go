package contextfabric

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// coverageStateLines reads back every `context fabric group read coverage
// state` line the REAL handler wrote, decoded attribute by attribute.
//
// Decoded rather than substring-matched: a pin that greps for a key proves the
// key was written, not that it carried a value, and every field on this line
// exists precisely to carry one.
func coverageStateLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := make([]map[string]any, 0, 4)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["msg"] == "context fabric group read coverage state" {
			lines = append(lines, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading captured log: %v", err)
	}
	return lines
}

// TestBothReadsCoverageStatesReachInfoBeforeTheFold pins the disclosure that
// survives a lossy merge.
//
// MergeCoverage keeps the WORST state per source name, and both reads report
// under the same `canonical_fact:<kind>` names, so a group read that found no
// health data ERASES the member read's `available` for health. The served
// answer is right to be conservative; the trace must still be able to say
// which population the gap was in, because "neither population had health
// data" and "the members had it and the groups did not" are different answers.
//
// Asserted through the REAL handler at production level with NON-TRIVIAL
// values: the two arms carry DIFFERENT states for the SAME source, which is
// the only configuration in which the fold actually destroys information. A
// fixture where both arms agreed would pass against an implementation that
// emitted one arm twice.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestBothReadsCoverageStatesReachInfoBeforeTheFold(t *testing.T) {
	logs := captureDefaultJSONLogger(t)

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		// The GROUP read is the one rooted on team subjects. It reports
		// the same source as the member read, in a WORSE state.
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceNoData}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
		return bundle
	}}

	engine, request := groupReadEngineFixture(t, NewSlogEngineTelemetry(slog.Default()), recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	lines := coverageStateLines(t, logs.String())
	for _, line := range lines {
		t.Logf("level=%v read=%v source=%v source_state=%v family=%v group_kind=%v",
			line["level"], line["read"], line["source"], line["source_state"], line["family"], line["group_kind"])
	}
	if len(lines) != 2 {
		t.Fatalf("got %d coverage-state lines, want 2 -- one per read, emitted before the fold", len(lines))
	}

	byArm := map[string]map[string]any{}
	for _, line := range lines {
		arm, _ := line["read"].(string)
		byArm[arm] = line
	}
	member, hasMember := byArm[string(GroupReadArmMember)]
	group, hasGroup := byArm[string(GroupReadArmGroup)]
	if !hasMember || !hasGroup {
		t.Fatalf("lines carry reads %v, want both %q and %q -- without the discriminator the two observations are as indistinguishable as the merged coverage they exist to explain",
			byArm, GroupReadArmMember, GroupReadArmGroup)
	}

	// THE VALUES, and the pair that matters: the SAME source in DIFFERENT
	// states. This is what the fold destroys.
	if got := member["source_state"]; got != string(SourceAvailable) {
		t.Errorf("member read source_state = %v, want %q -- the state the fold is about to erase must be on the line with its real value",
			got, SourceAvailable)
	}
	if got := group["source_state"]; got != string(SourceNoData) {
		t.Errorf("group read source_state = %v, want %q", got, SourceNoData)
	}
	if member["source"] != "canonical_fact:health" || group["source"] != "canonical_fact:health" {
		t.Errorf("sources = %v / %v, want both %q -- the pin is about ONE source name observed twice; two different names would not collide in the fold at all",
			member["source"], group["source"], "canonical_fact:health")
	}
	if got := member["group_kind"]; got != string(SubjectTeam) {
		t.Errorf("group_kind = %v, want %q -- a field expected at its zero value pins nothing", got, SubjectTeam)
	}

	// PRODUCTION LEVEL. A mutation demoting this to Debug leaves every
	// assertion above satisfied under a Debug-enabled capture while making
	// the line invisible in a deployed build, which is the whole failure
	// this line exists to prevent.
	for _, line := range lines {
		if got := line["level"]; got != slog.LevelInfo.String() {
			t.Errorf("level = %v, want %q -- a reader who must raise the log level to learn which population a coverage gap was in cannot answer it about a turn that already happened",
				got, slog.LevelInfo.String())
		}
	}
}

// TestTheFoldReallyDoesEraseTheBetterState is the DISCRIMINATING CONTROL for
// the pin above, and it is what makes that pin worth having.
//
// If the merged coverage kept both states, or kept the better one, the line
// would be redundant and the pin would be asserting a disclosure nobody needs.
// This drives the same two observations through the real merge and shows the
// member read's `available` is genuinely gone afterwards.
func TestTheFoldReallyDoesEraseTheBetterState(t *testing.T) {
	t.Parallel()

	member := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}, DegradedReasons: []string{}}
	group := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceNoData}}, DegradedReasons: []string{}}

	merged := MergeCoverage("org_1", member, group)
	t.Logf("merged sources = %#v", merged.Sources)
	if len(merged.Sources) != 1 {
		t.Fatalf("CONTROL BROKEN: merged coverage carries %d sources for one name, so the fold is not lossy and the pin above is asserting a disclosure nothing needs", len(merged.Sources))
	}
	if merged.Sources[0].State != SourceNoData {
		t.Fatalf("CONTROL BROKEN: merged state = %q, want %q -- if the fold kept the better state the pin above would be describing a loss that does not happen",
			merged.Sources[0].State, SourceNoData)
	}
}
