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

// engineLoggerCapture is an EXPLICIT logger handed to the engine's telemetry,
// with the process default captured separately.
//
// The distinction is load-bearing and was learned the hard way elsewhere in
// this programme: `slog.Default()` never reaches acr-api's JSON handler, so a
// pin that captures the default and passes the default INTO the telemetry
// proves only that SOMETHING wrote a line -- it cannot tell an emitter using
// the engine's configured logger from one calling `slog.Default()` directly.
// Only the second reaches production. This capture separates them: the line
// must appear on the configured logger and must NOT appear on the default.
type engineLoggerCapture struct {
	configured *syncBuffer
	fallback   *syncBuffer
	telemetry  EngineTelemetry
}

func captureEngineLogger(t *testing.T) *engineLoggerCapture {
	t.Helper()
	configured := &syncBuffer{}
	fallback := &syncBuffer{}
	previous := slog.Default()
	// A DIFFERENT sink for the process default, so a line landing there is
	// visible as a miss rather than being silently indistinguishable.
	slog.SetDefault(slog.New(slog.NewJSONHandler(fallback, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &engineLoggerCapture{
		configured: configured,
		fallback:   fallback,
		telemetry:  NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(configured, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
}

func linesWithMessage(t *testing.T, raw, message string) []map[string]any {
	t.Helper()
	lines := make([]map[string]any, 0, 2)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["msg"] == message {
			lines = append(lines, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading captured log: %v", err)
	}
	return lines
}

// TestTheRetentionDecisionReachesTheConfiguredLogger pins the line that
// explains a narrowed grouped answer.
//
// Retention decides which evidence survives narrowing. Until this line, a
// grouped answer that lost a group kept that group's facts, synthesis was
// handed evidence about a population the document did not contain, and
// evidence closure rejected the whole result with nothing anywhere saying
// why -- the failure was reproducible only by re-running the turn.
//
// Asserted on the ENGINE'S CONFIGURED LOGGER and asserted ABSENT on the
// process default, because only the former is wired in the deployed service.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestTheRetentionDecisionReachesTheConfiguredLogger(t *testing.T) {
	logs := captureEngineLogger(t)

	// Facts for EVERY member, so narrowing actually has evidence to drop. A
	// fixture whose removed members carried no facts reports a retention pass
	// that discarded nothing, and every value on the line sits at its no-op
	// state -- which pins nothing at all.
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = []CanonicalFact{
			teamScopedFact("project_a", "team_security", "Security"),
			teamScopedFact("project_b", "team_security", "Security"),
			teamScopedFact("project_c", "team_platform", "Platform"),
			teamScopedFact("project_d", "team_platform", "Platform"),
		}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// Below the reserve, so the allowance clamps and stage 2 narrows -- which
	// is what makes a retention pass run at all.
	options := EngineOptions{MaxItems: 6}
	members := []CohortMember{}
	for index, id := range []string{"project_a", "project_b", "project_c", "project_d"} {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, members, nil, SubjectProject, &options, nil)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	lines := linesWithMessage(t, logs.configured.String(), "context fabric fact retention")
	for _, line := range lines {
		t.Logf("level=%v stage=%v group_kind=%v before=%v after=%v dropped_members=%v dropped_groups=%v retained_groups=%v",
			line["level"], line["stage"], line["group_kind"], line["facts_before"], line["facts_after"],
			line["dropped_members"], line["dropped_groups"], line["retained_groups"])
	}
	if len(lines) == 0 {
		t.Fatalf("no retention line on the engine's configured logger -- the step that decides which evidence survives narrowing is invisible in the deployed service")
	}

	// ABSENT ON THE PROCESS DEFAULT. A line that only appears there is a line
	// acr-api never sees, and the pin above would still be green.
	if stray := linesWithMessage(t, logs.fallback.String(), "context fabric fact retention"); len(stray) != 0 {
		t.Errorf("%d retention line(s) reached the PROCESS DEFAULT logger -- an emitter calling slog.Default() writes nowhere acr-api's JSON handler can read, and a capture of the default would not tell the difference",
			len(stray))
	}

	// NON-TRIVIAL VALUES on the pass that actually dropped something.
	dropping := map[string]any(nil)
	for _, line := range lines {
		if before, after := line["facts_before"], line["facts_after"]; before != after {
			dropping = line
		}
	}
	if dropping == nil {
		t.Fatalf("every retention line reports facts_before == facts_after, so this fixture never dropped anything and the line's values are pinned at their no-op state")
	}
	if got := dropping["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want %q -- an operator who must raise the level to learn what narrowing discarded cannot ask it of a turn that already happened",
			got, slog.LevelInfo.String())
	}
	if got := dropping["group_kind"]; got != string(SubjectTeam) {
		t.Errorf("group_kind = %v, want %q -- dropped_groups is ambiguous without it: on a flat cohort the field is structurally zero, and zero-because-nothing-dropped and zero-because-no-group-axis are different facts wearing the same number",
			got, SubjectTeam)
	}
	if got := dropping["retained_groups"]; got == float64(0) {
		t.Errorf("retained_groups = %v on a grouped answer -- a field expected at its zero value pins nothing", got)
	}
	if dropping["dropped_members"] == float64(0) && dropping["dropped_groups"] == float64(0) {
		t.Errorf("a pass that changed the fact count reports dropping neither members nor groups -- the counts and the totals disagree, and a reader cannot tell which rule fired")
	}
}

// TestAnUnnarrowedTurnReportsNoRetentionLoss is the DISCRIMINATING CONTROL,
// and it must pass before and after.
//
// Without it, every assertion above is satisfied by an implementation that
// reports losses on every turn -- which would make `dropped_groups` fire on
// answers that dropped nothing, and an operator chasing a phantom closure
// failure is worse served than one with no line at all.
//
// NOT t.Parallel(): it installs the process-global default logger.
func TestAnUnnarrowedTurnReportsNoRetentionLoss(t *testing.T) {
	logs := captureEngineLogger(t)

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// Above the reserve and a cohort that fits: nothing narrows.
	options := EngineOptions{MaxItems: 26}
	members := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, members, nil, SubjectProject, &options, nil)
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	for _, line := range linesWithMessage(t, logs.configured.String(), "context fabric fact retention") {
		t.Logf("control: before=%v after=%v dropped_members=%v dropped_groups=%v",
			line["facts_before"], line["facts_after"], line["dropped_members"], line["dropped_groups"])
		if line["dropped_groups"] != float64(0) {
			t.Fatalf("CONTROL BROKEN: an unnarrowed turn reported dropped_groups = %v -- an operator chasing a phantom closure failure is worse served than one with no line at all",
				line["dropped_groups"])
		}
		if line["facts_before"] != line["facts_after"] {
			t.Fatalf("CONTROL BROKEN: an unnarrowed turn reports %v facts before and %v after", line["facts_before"], line["facts_after"])
		}
	}
}
