package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4809: grouped-narrowing telemetry must disclose what the selection
// actually DID, on every path that runs it -- not only on the happy path
// where the effect happens to be inferable from before > after.
//
// Every test in this file is RED ON origin/main (57091487) BY ASSERTION,
// never by symbol absence: each one reads only fields or log keys that
// already exist there, or reads the log line as a map. That is deliberate.
// A test that fails to COMPILE at the parent proves the symbol is new; it
// proves nothing about behaviour. This defect IS a behaviour defect -- main
// computes a real selection and then publishes a placeholder pair that
// flattens it to a no-op -- so the proof has to be a failing assertion.

// chaos4809OverlappingGroupedCohort builds a cohort of eight members whose
// four groups genuinely OVERLAP, so the overlap-aware set cover has
// something to exploit and its effect is real rather than incidental.
//
// Groups t1={a,b} t2={b,c} t3={d,e} t4={e,f}: the minimum cover is {b,e} --
// two members covering four groups -- so a selection at the halving target
// of four admits that cover plus fill, and genuinely cuts the cohort in
// half. g and h are UNGROUPED on purpose: they are cohort members like any
// other and are narrowed like a flat tail, the case NarrowGroupedCohort's
// own doc comment calls out as previously lost.
//
// Four groups is inside ContextFabricSetCoverGroupGuard (12), so the basis
// the selection reports is overlap_aware_set_cover rather than the
// beyond-guard largest_group_round_robin fallback. Anchoring on that
// matters: the ticket's complaint is precisely that a reported basis and a
// reported (before, after) pair contradicted each other.
func chaos4809OverlappingGroupedCohort() *Cohort {
	cohort := budgetStageCohort(8)
	group := func(id string, members ...string) contractsv1.ContextFabricCohortGroup {
		return contractsv1.ContextFabricCohortGroup{
			Subject:            SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id},
			MemberCanonicalIDs: members,
			Total:              len(members),
			Complete:           true,
		}
	}
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		group("t1", "a_project", "b_project"),
		group("t2", "b_project", "c_project"),
		group("t3", "d_project", "e_project"),
		group("t4", "e_project", "f_project"),
	}
	return cohort
}

// chaos4809SoleEvent returns the single recorded event matching match,
// failing when there is not exactly one. "Exactly one" rather than "the
// last one": a duplicate terminal event is its own defect and must not be
// silently absorbed by a loop that keeps the last match.
func chaos4809SoleEvent(t *testing.T, telemetry *recordingTelemetry, what string, match func(PlanNarrowingEvent) bool) PlanNarrowingEvent {
	t.Helper()
	var found []PlanNarrowingEvent
	for _, event := range telemetry.planNarrowings {
		if match(event) {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("recorded %d %s events, want exactly 1 (all events: %+v)", len(found), what, telemetry.planNarrowings)
	}
	return found[0]
}

// TestCHAOS4809RefusalEventReportsTheSelectionItComputed is PATH 2.
//
// The refusal path calls planRefusal AFTER narrowSynthesisInput has already
// run, so a real overlap-aware set-cover selection has genuinely computed a
// narrowed cohort by the time the event is built. On origin/main planRefusal
// is handed (members, members) and the event reports before == after, so the
// artifact says the selection did nothing while the basis field says set
// cover ran. Both cannot be true, and an operator has no way to tell which
// to believe -- which is what made CHAOS-4727's acceptance criterion
// ("overlap_aware_set_cover observed SELECTING") unobservable by
// construction on this path.
//
// A zero deadline reserve declines the retry here, so the narrowed cohort is
// computed and then NOT served. That does not make before/after a
// placeholder: RetryAttempted=false is what tells the reader the narrowed
// cohort never reached synthesis, and it is already on the event. The
// selection's effect and whether that effect was served are two different
// facts; the event carries both rather than flattening one into the other.
func TestCHAOS4809RefusalEventReportsTheSelectionItComputed(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// maxItems=20 is chosen so the CARDINALITY clamp and the stage-2
	// synthesis-input clamp both stand down and the full eight-member
	// cohort reaches stage 3 -- otherwise an earlier stage does the
	// narrowing and this test would pin the wrong boundary. Verified by
	// asserting Before=8 below.
	engine := budgetStageEngine(t, chaos4809OverlappingGroupedCohort(), 20, budgetStageOptions(20, 0), &calls, telemetry)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want a planned refusal", err)
	}
	event := chaos4809SoleEvent(t, telemetry, "refusal", func(e PlanNarrowingEvent) bool { return e.RefusalPlanned })

	if event.RetryDeclined != RetryDeclinedNoReserve {
		t.Fatalf("RetryDeclined = %q, want no_reserve -- this fixture must reach the refusal through a DECLINED retry, not an attempted one", event.RetryDeclined)
	}
	if event.Basis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("Basis = %q, want overlap_aware_set_cover", event.Basis)
	}
	if event.Before != 8 {
		t.Fatalf("Before = %d, want 8 -- the cohort the selection was given", event.Before)
	}
	// THE DEFECT. main reports 8 here: planRefusal is called with the
	// pre-selection count twice, so a selection that halved the cohort is
	// published as a no-op.
	if event.After != 4 {
		t.Fatalf("After = %d, want 4 -- the count the overlap-aware selection ACTUALLY admitted; reporting Before again publishes a real selection as a no-op", event.After)
	}
	// RetryAttempted is what distinguishes "computed and served" from
	// "computed and discarded". Pinned so a future change cannot make After
	// honest by making this dishonest.
	if event.RetryAttempted {
		t.Fatal("RetryAttempted = true, want false -- the retry was declined, so the narrowed cohort was never served")
	}
}

// TestCHAOS4809NothingToNarrowStillReportsAnUnchangedPair stops PATH 2's fix
// from becoming a new lie in the opposite direction.
//
// A grouped cohort already at its floor -- every group down to one member --
// RUNS the overlap-aware selection and finds nothing left to cut. Here
// before == after is TRUE, and it must survive the fix: publishing a
// narrowing that did not happen is the same class of defect as suppressing
// one that did.
func TestCHAOS4809NothingToNarrowStillReportsAnUnchangedPair(t *testing.T) {
	t.Parallel()
	cohort := budgetStageCohort(3)
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "t1", Label: "t1"}, MemberCanonicalIDs: []string{"a_project"}, Total: 1, Complete: true},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "t2", Label: "t2"}, MemberCanonicalIDs: []string{"b_project"}, Total: 1, Complete: true},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "t3", Label: "t3"}, MemberCanonicalIDs: []string{"c_project"}, Total: 1, Complete: true},
	}
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, cohort, 20, budgetStageOptions(4, time.Second), &calls, telemetry)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want a planned refusal", err)
	}
	event := chaos4809SoleEvent(t, telemetry, "refusal", func(e PlanNarrowingEvent) bool { return e.RefusalPlanned })

	if event.RetryDeclined != RetryDeclinedNothingToNarrow {
		t.Fatalf("RetryDeclined = %q, want nothing_to_narrow", event.RetryDeclined)
	}
	if event.Before != 3 || event.After != 3 {
		t.Fatalf("(Before, After) = (%d, %d), want (3, 3) -- the selection ran and genuinely admitted every member", event.Before, event.After)
	}
	if event.Basis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("Basis = %q, want overlap_aware_set_cover", event.Basis)
	}
}

// TestCHAOS4809RetryFailureEventReportsTheSelectionItComputed is PATH 3.
//
// This path is strictly worse than path 2: the retry actually RAN against
// the narrowed cohort -- synthesis was performed on the selected member set
// -- and then the second model call failed. main still publishes
// (before, before), so the one path on which the narrowed cohort was
// genuinely used is also a path on which no artifact records how many
// members it used.
//
// The synthesizer fails on its SECOND call only, which is what puts the
// engine in the retryErr branch rather than the first-pass rejection branch
// handled elsewhere.
func TestCHAOS4809RetryFailureEventReportsTheSelectionItComputed(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// Same maxItems=20 reasoning as path 2, plus a non-zero reserve so the
	// retry is ADMITTED rather than declined: this path only exists once
	// the retry actually runs.
	engine := budgetStageEngine(t, chaos4809OverlappingGroupedCohort(), 20, budgetStageOptions(20, time.Second), &calls, telemetry)
	attempts := 0
	engine.synthesizer = chaos4809FailOnSecondCall(engine.synthesizer, &attempts)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err == nil {
		t.Fatal("Investigate() error = nil, want the retry's own propagated failure")
	}
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want the retry's OWN fault propagated, not a budget refusal -- this fixture must reach the retry-FAILURE branch, not the terminal-refusal one", err)
	}
	if attempts != 2 {
		t.Fatalf("synthesis attempted %d times, want 2 -- the retry must have RUN for this path to be under test", attempts)
	}
	event := chaos4809SoleEvent(t, telemetry, "retry-failure", func(e PlanNarrowingEvent) bool { return e.RetryFailed })

	if event.Basis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("Basis = %q, want overlap_aware_set_cover", event.Basis)
	}
	if !event.RetryAttempted {
		t.Fatal("RetryAttempted = false, want true -- the retry ran against the narrowed cohort before failing")
	}
	if event.Before != 8 {
		t.Fatalf("Before = %d, want 8", event.Before)
	}
	// THE DEFECT, on the path where the narrowed cohort was actually USED.
	if event.After != 4 {
		t.Fatalf("After = %d, want 4 -- synthesis ran against these members, so the artifact must say how many there were", event.After)
	}
}

// chaos4809FailOnSecondCall wraps a synthesizer so the FIRST call succeeds
// (producing the oversized answer that forces the retry) and the second
// fails. A plain error, not a rejection: the retry-failure branch exists
// precisely to propagate the retry's own fault rather than dress it up as a
// budget refusal.
// attempts counts calls to the WRAPPER, not to the inner synthesizer: the
// inner one is never reached on the failing attempt, so the harness's own
// call counter would report 1 and read as "the retry never ran".
func chaos4809FailOnSecondCall(inner AnswerSynthesizer, attempts *int) AnswerSynthesizer {
	return synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		*attempts++
		if *attempts > 1 {
			return InvestigationResult{}, errors.New("model unavailable on the retry")
		}
		return inner.Synthesize(ctx, principal, input)
	})
}

// TestCHAOS4809PlanNarrowingLogSaysWhetherTheBasisWasObserved pins the OTHER
// half of the ticket's complaint, the one the (before, after) fix alone does
// not close.
//
// planStageBasis falls back to largest_group_round_robin for ANY grouped
// axis whose caller ran no selection -- a measured fit, for instance. So a
// reader of the log line cannot tell an order the selection REPORTED from
// one this code defaulted to on the caller's behalf, and "basis says set
// cover ran" is exactly the inference the ticket shows to be unsafe. This is
// the provenance rule: encode where the value came from, not just the value.
//
// Asserted on the LOG LINE as a map, so the test compiles at the parent and
// is red there by the key being absent rather than by symbol absence.
func TestCHAOS4809PlanNarrowingLogSaysWhetherTheBasisWasObserved(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: "v1"}

	cases := []struct {
		name         string
		groupAxis    bool
		groupedBasis contractsv1.ContextFabricNarrowingBasis
		wantBasis    string
		wantObserved bool
	}{
		{
			name:         "a grouped selection that ran reports its own order as observed",
			groupAxis:    true,
			groupedBasis: contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover,
			wantBasis:    "overlap_aware_set_cover",
			wantObserved: true,
		},
		{
			name:         "a grouped caller that ran no selection reports the DEFAULTED order, and says so",
			groupAxis:    true,
			groupedBasis: "",
			wantBasis:    "largest_group_round_robin",
			wantObserved: false,
		},
		{
			name:         "a flat cohort's declared lexical order is a default too",
			groupAxis:    false,
			groupedBasis: "",
			wantBasis:    "canonical_id_lexical",
			wantObserved: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buffer, nil)))
			event := PlanNarrowingEventFrom(plan, contractsv1.ContextFabricPlanNarrowingAssembledResult,
				8, 4, testCase.groupAxis, false, contractsv1.ContextFabricBudgetOverrunItems, testCase.groupedBasis)

			telemetry.RecordPlanNarrowing(context.Background(), storage.Principal{OrgID: "org_1"}, event)

			var line map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &line); err != nil {
				t.Fatalf("decode log line: %v (raw=%s)", err, buffer.String())
			}
			if got := line["basis"]; got != testCase.wantBasis {
				t.Fatalf("basis = %#v, want %q", got, testCase.wantBasis)
			}
			if got := line["basis_observed"]; got != testCase.wantObserved {
				t.Fatalf("basis_observed = %#v, want %v -- a reader cannot otherwise tell a REPORTED order from a DEFAULTED one, which is the inference CHAOS-4809 shows to be unsafe", got, testCase.wantObserved)
			}
		})
	}
}
