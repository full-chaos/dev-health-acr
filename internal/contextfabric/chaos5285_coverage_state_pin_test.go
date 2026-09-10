package contextfabric

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	// The ENGINE'S CONFIGURED logger, with the process default captured
	// separately and asserted empty. slog.Default() never reaches acr-api's
	// JSON handler, so a pin that both captures the default and hands the
	// default to the telemetry cannot tell a production-wired emitter from
	// one calling slog.Default() directly.
	logs := captureEngineLogger(t)

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		// The GROUP read is the one rooted on team subjects. It reports
		// the same source as the member read, in a WORSE state.
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				// WITH the reason the registry's appendFactCoverage always
				// attaches to a non-available observation: the served
				// document's coverage now carries this observation, and the
				// contract refuses a non-available source with no reason.
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceNoData, Reason: "canonical fact capability returned no_data"}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
		return bundle
	}}

	engine, request := groupReadEngineFixture(t, logs.telemetry, recorder)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	lines := coverageStateLines(t, logs.configured.String())
	for _, line := range lines {
		t.Logf("level=%v read=%v source=%v source_state=%v family=%v group_kind=%v",
			line["level"], line["read"], line["source"], line["source_state"], line["family"], line["group_kind"])
	}
	if len(lines) != 2 {
		t.Fatalf("got %d coverage-state lines, want 2 -- one per read, emitted before the fold", len(lines))
	}
	if stray := coverageStateLines(t, logs.fallback.String()); len(stray) != 0 {
		t.Errorf("%d coverage-state line(s) reached the PROCESS DEFAULT logger -- acr-api's JSON handler never reads it, so those lines do not exist in the deployed service", len(stray))
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

// allowanceLines reads back every `context fabric cohort member allowance`
// line the REAL handler wrote, decoded rather than substring-matched.
func allowanceLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := make([]map[string]any, 0, 2)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["msg"] == "context fabric cohort member allowance" {
			lines = append(lines, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading captured log: %v", err)
	}
	return lines
}

// TestTheClampedMemberAllowanceIsExplainedAtInfo pins the line that makes an
// otherwise inexplicable answer explicable.
//
// A grouped plan reserves a synthesis headroom of twenty items, and the
// cohort's member allowance is the budget minus that reserve. Every grouped
// turn at or below a twenty-item budget therefore gets an allowance of ONE --
// not because one member is what the budget affords, but because the
// subtraction went to zero and the floor caught it. The set cover then leaves
// one member per group, and a reader sees a single project under each team
// with nothing anywhere saying why.
//
// The values are what make it useful. An allowance of one is unremarkable
// under a one-item budget and is a reserve swallowing the whole budget under a
// twenty-item one; only `max_items` beside `synthesis_headroom` separates
// them, and `allowance_clamped` says which happened rather than leaving a
// reader to redo the arithmetic.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestTheClampedMemberAllowanceIsExplainedAtInfo(t *testing.T) {
	// The ENGINE'S CONFIGURED logger, with the process default captured
	// separately and asserted empty. slog.Default() never reaches acr-api's
	// JSON handler, so a pin that both captures the default and hands the
	// default to the telemetry cannot tell a production-wired emitter from
	// one calling slog.Default() directly.
	logs := captureEngineLogger(t)

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// Below the grouped headroom, so the allowance is supplied by the floor.
	options := EngineOptions{MaxItems: 6, SynthesisDeadlineReserve: time.Second}
	members := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, members, nil, SubjectProject, &options, nil)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	lines := allowanceLines(t, logs.configured.String())
	for _, line := range lines {
		t.Logf("level=%v max_items=%v headroom=%v allowance=%v clamped=%v groups=%v before=%v after=%v",
			line["level"], line["max_items"], line["synthesis_headroom"], line["member_allowance"],
			line["allowance_clamped"], line["groups"], line["members_before"], line["members_after"])
	}
	if len(lines) != 1 {
		t.Fatalf("got %d allowance lines, want exactly 1 -- emitted on every turn that has a cohort, narrowed or not", len(lines))
	}
	if stray := allowanceLines(t, logs.fallback.String()); len(stray) != 0 {
		t.Errorf("%d allowance line(s) reached the PROCESS DEFAULT logger -- acr-api's JSON handler never reads it", len(stray))
	}
	line := lines[0]

	if got := line["allowance_clamped"]; got != true {
		t.Errorf("allowance_clamped = %v, want true -- this budget is below the reserve, and without the flag a reader cannot tell the floor from a genuinely tiny budget", got)
	}
	// NON-TRIVIAL VALUES, and the pair that carries the explanation.
	if got := line["max_items"]; got != float64(6) {
		t.Errorf("max_items = %v, want 6", got)
	}
	// THE RESERVE ATE THE WHOLE BUDGET. A grouped plan's profile reserve is
	// twenty items and the contract forbids reserving more than the budget
	// holds, so at any budget at or below the reserve the headroom lands on
	// the budget itself and the subtraction leaves nothing for members. That
	// is the entire explanation, and it is unrecoverable from the allowance
	// alone -- which is why both numbers are on the line.
	if got := line["synthesis_headroom"]; got != line["max_items"] {
		t.Errorf("synthesis_headroom = %v against max_items = %v, want them equal -- below the profile reserve the headroom is the whole budget, and a reader without both numbers cannot tell that from a budget that was simply tiny",
			got, line["max_items"])
	}
	if got := line["member_allowance"]; got != float64(1) {
		t.Errorf("member_allowance = %v, want 1", got)
	}
	// `groups` is why members_after does not equal the allowance: the set
	// cover keeps one member per group.
	if got := line["groups"]; got != float64(2) {
		t.Errorf("groups = %v, want 2 -- without it, a cohort of 2 under an allowance of 1 looks like the allowance being ignored", got)
	}
	if got := line["members_before"]; got != float64(2) {
		t.Errorf("members_before = %v, want 2", got)
	}
	if got := line["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want %q -- an operator who must raise the level to learn why an answer was thin cannot ask it of a turn that already happened",
			got, slog.LevelInfo.String())
	}
}

// TestTheUnclampedMemberAllowanceReportsItselfUnclamped is the DISCRIMINATING
// CONTROL: without it, `allowance_clamped` is satisfied by an implementation
// that hardcodes true, which would label every ordinary turn as clamped and
// make the flag worthless in exactly the population it exists to separate.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestTheUnclampedMemberAllowanceReportsItselfUnclamped(t *testing.T) {
	// The ENGINE'S CONFIGURED logger, with the process default captured
	// separately and asserted empty. slog.Default() never reaches acr-api's
	// JSON handler, so a pin that both captures the default and hands the
	// default to the telemetry cannot tell a production-wired emitter from
	// one calling slog.Default() directly.
	logs := captureEngineLogger(t)

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// ABOVE the grouped headroom, so the subtraction produces a real number.
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Second}
	members := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, members, nil, SubjectProject, &options, nil)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	lines := allowanceLines(t, logs.configured.String())
	if len(lines) != 1 {
		t.Fatalf("CONTROL BROKEN: got %d allowance lines, want 1", len(lines))
	}
	line := lines[0]
	t.Logf("control: max_items=%v headroom=%v allowance=%v clamped=%v",
		line["max_items"], line["synthesis_headroom"], line["member_allowance"], line["allowance_clamped"])
	if got := line["allowance_clamped"]; got != false {
		t.Fatalf("CONTROL BROKEN: allowance_clamped = %v on a budget above the reserve -- a flag that is always true separates nothing", got)
	}
	if got := line["member_allowance"]; got == float64(1) {
		t.Fatalf("CONTROL BROKEN: member_allowance = %v above the reserve, which is the clamped value -- this fixture is not exercising the unclamped arm", got)
	}
}
