package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The quota must reach the EMITTED LINE from both stage-three refusal arms.
//
// THIS FILE REPLACES A SOURCE-GREP PIN, and the replacement is the finding.
//
// The class has now been found THREE times:
//
//   - round 1: QuotaExposure was written at three sites and read at none.
//   - round 2: it reached the served emitter but was dropped on BOTH refusal
//     arms -- the round-1 pin was lexical, so it proved the field was written,
//     not that anything consumed it.
//   - round 3, found by the author while re-proving the mutation pins at the
//     merge tip: the REPLACEMENT pin was lexical too. It read the stage-three
//     source with os.ReadFile and asserted the assignment text appeared, so
//     deleting the assignment failed only that grep, and commenting it out did
//     not fail even that -- the literal text was still in the file. No
//     behavioural test in the package objected either way.
//
// A pin that proves a string exists proves nothing about a consumer. So these
// two tests drive a real over-quota answer through stage three, into EACH
// refusal arm, and read the line the engine actually emitted.
//
// What they assert is deliberately ground truth from the fixture rather than a
// recomputation of the production arithmetic: quota_groups must equal the
// number of groups the fixture put in the cohort, and quota_items_per_group
// must be a real allowance rather than a zero. Delete either arm's population
// of the event and every one of these fails, because the line then carries
// zeros -- which is exactly what a refusal was emitting before.

// refusalArmLine drives one investigation to a budget refusal and returns the
// plan-narrowing line the engine emitted for it.
//
// It builds its own engine rather than reusing budgetStageEngine because that
// helper's telemetry parameter is typed to *recordingTelemetry, and the whole
// point here is to read the FORMATTED line rather than a captured struct: a
// captured struct is one step short of the thing enforcement actually gets.
func refusalArmLine(t *testing.T, groups int, members int, claimsPerMember int, options EngineOptions, ctx context.Context) string {
	t.Helper()

	cohort := budgetStageCohort(members)
	cohort.Groups = make([]contractsv1.ContextFabricCohortGroup, 0, groups)
	for index := 0; index < groups; index++ {
		id := "group_" + strconv.Itoa(index)
		cohort.Groups = append(cohort.Groups, contractsv1.ContextFabricCohortGroup{
			Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectKind(SubjectTeam), CanonicalID: id, Label: id},
		})
	}

	var buf bytes.Buffer
	calls := 0
	engine := budgetStageEngineWithTelemetry(t, cohort, claimsPerMember, options, &calls,
		NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil))))

	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v, want a planned budget refusal -- this fixture is meant to reach a refusal arm", err)
	}
	return buf.String()
}

// refusalLineFor returns the assembled-result REFUSAL line carrying the given
// retry marker.
//
// The selector is deliberately narrow. One request emits several
// plan-narrowing events -- cardinality, synthesis_input and assembled_result --
// and the first version of this helper matched on the retry marker alone, so
// it silently read the CARDINALITY line, which carries retry_attempted=false
// like every other non-retry event and no quota at all. A struct-capturing
// test would have hidden that behind an index; reading the formatted line
// makes the wrong-stage match visible, but only if the selector names the
// stage it means.
func refusalLineFor(t *testing.T, emitted string, retryMarker string) string {
	t.Helper()
	matches := []string{}
	for _, line := range strings.Split(emitted, "\n") {
		if !strings.Contains(line, "context fabric plan narrowing") {
			continue
		}
		if strings.Contains(line, "stage=assembled_result") &&
			strings.Contains(line, "refusal_planned=true") &&
			strings.Contains(line, retryMarker) {
			matches = append(matches, line)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 assembled_result refusal line with %s, got %d.\nemitted:\n%s",
			retryMarker, len(matches), emitted)
	}
	return matches[0]
}

var quotaFieldPattern = regexp.MustCompile(`quota_(items_per_group|groups|over_quota)=(-?\d+)`)

// quotaFieldsOf reads the three quota dimensions off an emitted line.
func quotaFieldsOf(t *testing.T, line string) map[string]int {
	t.Helper()
	fields := map[string]int{}
	for _, match := range quotaFieldPattern.FindAllStringSubmatch(line, -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("quota_%s is not an integer on the emitted line: %v", match[1], err)
		}
		fields[match[1]] = value
	}
	for _, name := range []string{"items_per_group", "groups", "over_quota"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("the emitted line carries no quota_%s at all -- enforcement is handed nothing.\nline: %s", name, line)
		}
	}
	return fields
}

// assertQuotaReachedTheLine is the shared assertion for both arms.
func assertQuotaReachedTheLine(t *testing.T, line string, wantGroups int) {
	t.Helper()
	fields := quotaFieldsOf(t, line)

	if fields["groups"] != wantGroups {
		t.Errorf("quota_groups = %d, want %d -- the group count the fixture actually built.\n"+
			"A zero here is the defect this test exists for: the arm built its event without the exposure the attempt held.\nline: %s",
			fields["groups"], wantGroups, line)
	}
	if fields["items_per_group"] <= 0 {
		t.Errorf("quota_items_per_group = %d, want a real per-group allowance.\n"+
			"Zero means the arm emitted an empty QuotaExposure rather than the one the attempt computed.\nline: %s",
			fields["items_per_group"], line)
	}
	// An invariant a garbage value would violate: a group cannot be over
	// quota unless it exists.
	if fields["over_quota"] < 0 || fields["over_quota"] > fields["groups"] {
		t.Errorf("quota_over_quota = %d with quota_groups = %d -- more groups are over quota than exist.\nline: %s",
			fields["over_quota"], fields["groups"], line)
	}
}

// TestTheDeclinedRefusalArmCarriesTheQuotaToTheEmittedLine drives the arm
// where the cohort lever DECLINED (this deployment reserves no deadline), so
// the refusal is reached without a retry.
//
// MUTATION PROOF: delete the `attempt.Quota` argument at the planRefusal call
// in chaos4636_budget_stage3.go (or the stamping in planRefusal itself) and
// this fails on quota_groups, because the line then carries zeros.
func TestTheDeclinedRefusalArmCarriesTheQuotaToTheEmittedLine(t *testing.T) {
	t.Parallel()
	// Reserve 0 => RetryDeclinedNoReserve => the declined arm, no retry.
	// 6 members x 20 claims is ~120 items against a 30 ceiling, so candidate
	// narrowing cannot rescue it and the arm refuses.
	emitted := refusalArmLine(t, 2, 6, 20, budgetStageOptions(30, 0), context.Background())
	line := refusalLineFor(t, emitted, "retry_attempted=false")
	assertQuotaReachedTheLine(t, line, 2)
}

// TestTheRetryRefusalArmCarriesTheQuotaToTheEmittedLine drives the other arm:
// the retry WAS attempted, re-synthesized against a narrower cohort, and still
// did not fit.
//
// MUTATION PROOF: delete the three `event.Quota* = outcomeAttempt.Quota.*`
// assignments in the retry arm and this fails on quota_groups. Round three
// found that deleting them failed only a source grep and nothing else.
func TestTheRetryRefusalArmCarriesTheQuotaToTheEmittedLine(t *testing.T) {
	t.Parallel()
	// A reserve and a real deadline => the retry runs; 20 claims per member
	// means even the narrowed cohort cannot fit, so it refuses after trying.
	//
	// The ceiling is 30, not the 4 this fixture first used. At 4 items across
	// 2 groups the per-group allowance is legitimately ZERO, so the assertion
	// below could not tell a real zero quota from a dropped one -- and a
	// bounded zero IS a real quota here, which is the whole point of the
	// sibling fix. 30 gives a non-zero allowance while 4 members x 20 claims
	// still overruns it after the retry.
	emitted := refusalArmLine(t, 2, 4, 20, budgetStageOptions(30, time.Second), context.Background())
	line := refusalLineFor(t, emitted, "retry_attempted=true")
	assertQuotaReachedTheLine(t, line, 2)
}

// The three tests below replace three MORE source-text pins found by sweeping
// the class rather than fixing the one instance that was reported.
//
// Each of the three asserted, by reading a .go file as text, a property that
// is in fact observable by running the engine. The rule they were missing:
// read the source only for a property that has no behavioural expression --
// "this is derived in exactly ONE place", "there are exactly two call sites".
// Everything a run can show, a run should show.

// NOTE on a test that is deliberately NOT here. The `Plan: plan` mutant --
// deleting that field from engine.go's synthesisAssemblyParams literal, which
// makes every allocation unbounded and returns narration to the static caps --
// is killed by TestTheAllocationTheEngineHandsSynthesisIsThePlansOwn below,
// because an unset Plan yields an allocation with MaxItems 0. A separate
// narration-flavoured test for the same mutant would need a much heavier
// fixture (driver candidates and signal citations, so narration actually runs)
// to prove nothing the next test does not already prove.

// TestTheAllocationTheEngineHandsSynthesisIsThePlansOwn replaces the
// source-grep pin in genkitruntime that asserted the engine sets
// `Allocation:` and the projection reads `input.Allocation`.
//
// The engine is the only thing that can prove this: every test that builds its
// own SynthesisInput passes whether or not production ever populates the
// field. So this reads the SynthesisInput the ENGINE built.
func TestTheAllocationTheEngineHandsSynthesisIsThePlansOwn(t *testing.T) {
	t.Parallel()
	const ceiling = 12
	var seen []ItemAllocation
	calls := 0
	engine := budgetStageEngineWithTelemetry(t, budgetStageCohort(6), 2,
		budgetStageOptions(ceiling, 0), &calls, &recordingTelemetry{},
		func(input SynthesisInput) { seen = append(seen, input.Allocation) })

	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		validInvestigationRequestWithConfirmedWindow())

	if len(seen) == 0 {
		t.Fatal("synthesis was never called; this test cannot say anything about what it was handed")
	}
	for index, allocation := range seen {
		if allocation.MaxItems != ceiling {
			t.Errorf("synthesis call %d was handed an allocation with MaxItems = %d, want the plan's own %d.\n"+
				"Zero means the engine never set Allocation, so the model is told nothing about its budget in\n"+
				"production while every test that builds its own SynthesisInput stays green.",
				index, allocation.MaxItems, ceiling)
		}
	}
}
