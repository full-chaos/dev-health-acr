package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6558: the prod byte-overrun shape, replayed through a real Engine.
//
// PROD (2026-09-24, mcp lane-mcp-accept-c69ce7b4 = api
// req_745310015e271972029c854d74886738, raw/11-g1b.json): "Which teams need
// attention over the last 30 days?" with a confirmed window receipt. Family
// discovered_cohort_ranking, a 2-team cohort, 26 facts. Synthesis #1
// assembled 86,666 bytes against max_serialized_bytes=65,536 while carrying
// 17 of 30 items (overrun=bytes). Stage 3 halved the cohort 2 -> 1, retention
// kept 26 of 26 facts, synthesis #2 came back at 122,402 bytes, and the
// answer was refused: 413 budget_refusal, narrower_continuation_axis=
// result_count, outcome_reduction_declined=not_items_axis, ~80 s.
//
// THE FIXTURE keeps the structural facts of that run: the same question text,
// a confirmed window, a two-team discovered cohort, 26 facts about subjects
// that are NOT cohort members (so retention keeps 26 of 26 on the retry, as
// prod did), claims carrying those facts' row tables, the prod ceilings
// (30 items, 65,536 bytes), and a first document that overruns on BYTES with
// few items. The window-receipt continuation is represented by the confirmed
// window itself: the budget path runs after window binding and does not read
// the receipt.
const chaos6558ProdQuestion = "Which teams need attention over the last 30 days?"

const (
	chaos6558Facts       = 26
	chaos6558RowsPerFact = 30
	chaos6558MaxBytes    = 65536
	chaos6558MaxItems    = 30
)

func chaos6558Cohort() *Cohort {
	members := make([]CohortMember, 0, 2)
	for index, id := range []string{"team_platform", "team_payments"} {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	return &Cohort{Kind: SubjectTeam, Members: members, Rationale: "chaos-6558 prod shape", Complete: true}
}

func chaos6558Repository(index int) SubjectRef {
	id := fmt.Sprintf("repo_%02d", index)
	return SubjectRef{Kind: SubjectRepository, CanonicalID: id, Label: id}
}

func chaos6558FactSubject(index int, shape chaos6558Shape) SubjectRef {
	if shape.factsOnMembers {
		return chaos6558Cohort().Members[index%2].Subject
	}
	return chaos6558Repository(index)
}

// chaos6558Rows is a per-day table: the volume the prod overrun was made of.
func chaos6558Rows(fact int, shape chaos6558Shape) []contractsv1.ContextFabricClaimedFactRow {
	rows := make([]contractsv1.ContextFabricClaimedFactRow, 0, chaos6558RowsPerFact)
	for day := 0; day < chaos6558RowsPerFact; day++ {
		date := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format("2006-01-02")
		count := int64(fact*100 + day)
		state := "open_items_awaiting_review" + strings.Repeat("_", shape.rowPadding)
		rows = append(rows, contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"day": {String: &date}, "count": {Integer: &count}, "state": {String: &state},
		}})
	}
	return rows
}

// chaos6558Shape varies the two quantities the lever's outcome depends on:
// how large one row is, and the byte ceiling.
type chaos6558Shape struct {
	rowPadding int
	maxBytes   int64
	// factsOnMembers puts the facts on the cohort's own teams instead of
	// on non-member repositories, so halving the cohort DROPS the removed
	// team's facts -- the case where a retry can reduce the rows.
	factsOnMembers bool
	// composedHead makes the synthesis carry the server-composed head for its
	// own draft status, as the production synthesizer does.
	composedHead bool
	// table selects the claims' row-table declaration: "" is the prod
	// per-day series, "undated" declares a series keyed on a column that is
	// not an instant (the rows cannot be dated, so the cut falls back to the
	// prefix), and "undeclared" carries no declaration at all.
	table string
}

// chaos6558ProdShape is the prod ceiling with rows sized so the first
// document overruns it on bytes, as prod's did.
var chaos6558ProdShape = chaos6558Shape{maxBytes: chaos6558MaxBytes}

// chaos6558SeriesTable declares the rows a per-day time series, as the prod
// facts' daily tables are, so the lever's recency rule applies to them.
func chaos6558SeriesTable() *contractsv1.ContextFabricClaimedFactTable {
	return &contractsv1.ContextFabricClaimedFactTable{
		Field: "status", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
		Key: []string{"day"}, Measures: []string{"count"}, Observations: []string{"state"},
	}
}

func chaos6558SeriesTableFor(shape chaos6558Shape) *contractsv1.ContextFabricClaimedFactTable {
	switch shape.table {
	case "undated":
		return &contractsv1.ContextFabricClaimedFactTable{
			Field: "status", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
			Key: []string{"state"}, Measures: []string{"count"}, Observations: []string{"day"},
		}
	case "undeclared":
		return nil
	}
	return chaos6558SeriesTable()
}

func chaos6558Engine(t *testing.T, calls *int, telemetry *recordingTelemetry, shape chaos6558Shape) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: chaos6558Cohort(),
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			observed := time.Unix(100, 0).UTC()
			facts := make([]CanonicalFact, 0, chaos6558Facts)
			for index := 0; index < chaos6558Facts; index++ {
				status := "green"
				facts = append(facts, CanonicalFact{
					Kind: FactStatus, Subject: chaos6558FactSubject(index, shape),
					Fields:         map[string]FactValue{"status": {String: &status}},
					ObservedAt:     &observed,
					EvidenceRefIDs: []string{fmt.Sprintf("evidence_%02d", index)},
					SourceState:    SourceAvailable, Source: "ops", SourceVersion: "ops-v1",
				})
			}
			return CanonicalFactBundle{
				Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		// One claim per fact the synthesis was handed, carrying that fact's
		// row table -- claim rows are copied from evidence, so a retry over
		// the same retained facts carries the same rows.
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := make([]ClaimedFact, 0, len(input.Facts.Facts))
			for index, fact := range input.Facts.Facts {
				claims = append(claims, ClaimedFact{
					ClaimID: fmt.Sprintf("claim_%02d", index),
					Kind:    fact.Kind, Subject: fact.Subject, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
					Rows:  chaos6558Rows(index, shape),
					Table: chaos6558SeriesTableFor(shape),
				})
			}
			draftHead := "Two teams lean toward needing attention."
			draftAnswer := "Two teams appear to need attention, based on available context."
			if shape.composedHead {
				draftHead = composeDirectJudgmentFrom(InvestigationComplete, nil, SubjectResolution{})
				draftAnswer = composeDeterministicAnswerFrom(InvestigationComplete, nil, nil, SubjectResolution{})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: draftHead, CurrentState: "Review queues appear to be growing.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: draftAnswer, Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: chaos6558MaxItems, MaxSerializedBytes: shape.maxBytes,
		SynthesisDeadlineReserve: time.Second,
		Now:                      func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:              func() string { return "result_99999999" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func chaos6558Request() InvestigationRequest {
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = chaos6558ProdQuestion
	return request
}

// TestCHAOS6558ProdShapeByteOverrunServesPartialAfterOneSynthesis is the
// red/green pin. BASELINE (fa9e1fad): a planned 413 after TWO syntheses,
// exactly the prod run. FIX: the row lever serves the answer after ONE
// synthesis, partial, with the cut disclosed on the wire.
func TestCHAOS6558ProdShapeByteOverrunServesPartialAfterOneSynthesis(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558ProdShape)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused after %d syntheses: overrun=%s measured %d items / %d bytes against %d / %d, axis=%s retry_attempted=%v -- the prod 413; a byte overrun the fact rows account for must be served partial",
				calls, refusal.Overrun, refusal.MeasuredItems, refusal.MeasuredBytes, refusal.MaxItems, refusal.MaxSerializedBytes, refusal.NarrowerContinuationAxis, refusal.RetryAttempted)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1: the row lever runs before any cohort retry", calls)
	}
	if result.Status != InvestigationPartial {
		t.Fatalf("Status = %q, want partial: a truncated answer is never complete", result.Status)
	}
	if result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("Completeness.State = %q, want partial", result.Completeness.State)
	}
	if result.Completeness.TerminalReason == "" {
		t.Fatalf("TerminalReason is empty on a partial answer")
	}
	if !result.Coverage.Partial {
		t.Fatalf("Coverage.Partial = false on a truncated answer")
	}
	encoded, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("measure served result: %v", err)
	}
	if encoded.Bytes > chaos6558MaxBytes {
		t.Fatalf("served %d bytes against a %d-byte ceiling", encoded.Bytes, chaos6558MaxBytes)
	}
	if len(result.ClaimedFacts) != chaos6558Facts {
		t.Fatalf("served %d claims, want all %d: the lever cuts rows, never claims", len(result.ClaimedFacts), chaos6558Facts)
	}
	declared := chaos6558Facts * chaos6558RowsPerFact
	served := 0
	for _, claim := range result.ClaimedFacts {
		if len(claim.Rows) == 0 {
			t.Fatalf("claim %s lost its whole table; the floor is one row per table", claim.ClaimID)
		}
		served += len(claim.Rows)
		// RECENCY: the kept rows are the NEWEST days of the series, the
		// producer listed them oldest-first.
		last := *claim.Rows[len(claim.Rows)-1].Fields["day"].String
		if want := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC).AddDate(0, 0, chaos6558RowsPerFact-1).Format("2006-01-02"); last != want {
			t.Fatalf("claim %s keeps up to %s, want the newest day %s: a series is cut from its oldest end", claim.ClaimID, last, want)
		}
	}
	if served >= declared {
		t.Fatalf("served %d of %d rows: nothing was cut", served, declared)
	}
	var row *RequirementOutcomeRow
	for index := range result.Completeness.Outcomes {
		candidate := &result.Completeness.Outcomes[index]
		if candidate.Impact == contractsv1.ContextFabricAnswerImpactDepth && candidate.CauseOverrun == contractsv1.ContextFabricBudgetOverrunBytes {
			row = candidate
		}
	}
	if row == nil {
		t.Fatalf("no depth/bytes outcome row discloses the cut; outcomes = %+v", result.Completeness.Outcomes)
	}
	if row.Declared != declared || row.Served != served || row.Outcome != contractsv1.ContextFabricRequirementNarrowed || !row.CauseObserved {
		t.Fatalf("outcome row = %+v, want narrowed %d of %d rows, cause observed", *row, served, declared)
	}
	disclosed := false
	want := fmt.Sprintf("This answer shows %d of the %d table rows", served, declared)
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, want) {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("no limitation states %q; limitations = %q", want, result.Limitations)
	}
}

// TestCHAOS6558RowsOverranButOneRowPerTableStillDoesNotFitRefusesWithoutARetry
// is ruling (a): never run a retry that cannot reduce the overrunning axis.
//
// Every table is cut to one row and the answer still does not fit, while the
// rows alone account for the overrun and the narrowed cohort keeps every
// fact. A cohort retry would re-synthesize over the same 26 facts and carry
// the same rows. BASELINE (fa9e1fad): two syntheses, then the refusal, with
// the family's member-count advice. FIX: ONE synthesis, the refusal, and
// advice that can reduce rows -- a shorter evidence window.
func TestCHAOS6558RowsOverranButOneRowPerTableStillDoesNotFitRefusesWithoutARetry(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// One row is ~2 KB, so 26 one-row tables (~53 KB) cannot fit a 40 KB
	// ceiling, while the document without its rows does.
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{rowPadding: 2000, maxBytes: 40000})

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want the planned budget refusal", err)
	}
	if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("refusal overrun = %q, want bytes", refusal.Overrun)
	}
	if calls != 1 || refusal.RetryAttempted {
		t.Fatalf("synthesizer called %d times (retry_attempted=%v), want 1: the retry keeps every fact and cannot shrink the rows that overran", calls, refusal.RetryAttempted)
	}
	if string(refusal.NarrowerContinuationAxis) != "evidence_window" {
		t.Fatalf("refusal advice = %q, want evidence_window: fewer members does not shrink rows the retained facts carry", refusal.NarrowerContinuationAxis)
	}
}

// TestCHAOS6558ARetryThatDropsFactsStillRunsAndItsDocumentGetsTheLever pins
// the other half of ruling (a): the retry is declined only when it CANNOT
// reduce the axis. Here the facts belong to the cohort's own teams, so halving
// the cohort drops the removed team's 13 facts and their rows: the retry runs,
// and its smaller document is served by the row lever instead of refused.
func TestCHAOS6558ARetryThatDropsFactsStillRunsAndItsDocumentGetsTheLever(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{rowPadding: 2000, maxBytes: 40000, factsOnMembers: true})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the retried document served by the row lever", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2: this retry drops facts and so can reduce the rows", calls)
	}
	if result.Status != InvestigationPartial || len(result.ClaimedFacts) != chaos6558Facts/2 {
		t.Fatalf("status %q with %d claims, want partial with the retained team's %d", result.Status, len(result.ClaimedFacts), chaos6558Facts/2)
	}
	for _, claim := range result.ClaimedFacts {
		if len(claim.Rows) != 1 {
			t.Fatalf("claim %s carries %d rows, want 1 (the cap this ceiling admits)", claim.ClaimID, len(claim.Rows))
		}
	}
	if len(telemetry.factRowTruncations) != 2 {
		t.Fatalf("fact row truncation lines = %d, want 2 (one per document the lever ran on)", len(telemetry.factRowTruncations))
	}
	first, second := telemetry.factRowTruncations[0], telemetry.factRowTruncations[1]
	if first.Served || first.Declined != FactRowTruncationInsufficient || first.Pass != answerPassFirst {
		t.Fatalf("first line = %+v, want insufficient on the first document", first)
	}
	if !second.Served || second.Pass != answerPassSecond || second.RowsBefore != (chaos6558Facts/2)*chaos6558RowsPerFact {
		t.Fatalf("second line = %+v, want served on the retried document", second)
	}
}
