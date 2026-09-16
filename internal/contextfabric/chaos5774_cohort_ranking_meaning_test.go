package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5774: these tests cover two independent mechanisms that keep a
// cohort ranking's ATTENTION/adverse-pressure measure from being served as
// a performance judgment: the deterministic score-meaning/judgment-mismatch
// data RankCohort and the engine mint (never model-authored), and the
// structural guard that refuses a model-authored driver's ranking
// superlative about a member the formula never scored.

// --- scoreMeaningSupportsJudgmentKind ---

func TestScoreMeaningSupportsJudgmentKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		meaning CohortScoreMeaning
		kind    RequestedJudgmentKind
		want    bool
	}{
		{"attention meaning, attention kind", CohortScoreMeaningAttention, RequestedJudgmentKindAttention, true},
		{"attention meaning, performance kind", CohortScoreMeaningAttention, RequestedJudgmentKindPerformance, false},
		{"attention meaning, unspecified kind", CohortScoreMeaningAttention, "", true},
		{"unrecognized meaning, attention kind", CohortScoreMeaning("future_meaning"), RequestedJudgmentKindAttention, false},
		{"unrecognized meaning, unspecified kind", CohortScoreMeaning("future_meaning"), "", true},
		{"attention meaning, unrecognized kind", CohortScoreMeaningAttention, RequestedJudgmentKind("future_kind"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := scoreMeaningSupportsJudgmentKind(c.meaning, c.kind); got != c.want {
				t.Fatalf("scoreMeaningSupportsJudgmentKind(%q, %q) = %v, want %v", c.meaning, c.kind, got, c.want)
			}
		})
	}
}

// --- applyCohortJudgmentMismatch ---

func TestApplyCohortJudgmentMismatch(t *testing.T) {
	t.Parallel()
	t.Run("nil cohort is a no-op", func(t *testing.T) {
		t.Parallel()
		applyCohortJudgmentMismatch(nil, RequestedJudgmentKindPerformance)
	})
	t.Run("unranked cohort never gets a mismatch", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture"}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindPerformance)
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false on an unranked cohort (ScoreMeaning empty)")
		}
	})
	t.Run("performance kind over an attention cohort mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindPerformance)
		if !cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = false, want true for a performance kind over an attention cohort")
		}
	})
	t.Run("attention kind never mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, RequestedJudgmentKindAttention)
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false: the requested kind already matches the cohort's own ScoreMeaning")
		}
	})
	t.Run("unspecified kind never mismatches", func(t *testing.T) {
		t.Parallel()
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", ScoreMeaning: CohortScoreMeaningAttention}
		applyCohortJudgmentMismatch(cohort, "")
		if cohort.JudgmentMismatch {
			t.Fatalf("JudgmentMismatch = true, want false: an unspecified kind is never confident enough to flag a mismatch")
		}
	})
}

// --- RankCohort mints ScoreMeaning ---

func TestRankCohortMintsScoreMeaningAttention(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture", Members: []CohortMember{rankTestMember("A")}}
	facts := []CanonicalFact{healthFact("A", "low")}
	ranked, event, _ := RankCohort(cohort, facts, Coverage{})
	if ranked.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("cohort.ScoreMeaning = %q, want %q", ranked.ScoreMeaning, CohortScoreMeaningAttention)
	}
	if event.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("event.ScoreMeaning = %q, want %q", event.ScoreMeaning, CohortScoreMeaningAttention)
	}
}

func TestRankCohortEmptyCohortNeverMintsScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "fixture"}
	ranked, event, _ := RankCohort(cohort, nil, Coverage{})
	if ranked.ScoreMeaning != "" {
		t.Fatalf("cohort.ScoreMeaning = %q, want empty for a cohort with no members", ranked.ScoreMeaning)
	}
	if event.ScoreMeaning != "" {
		t.Fatalf("event.ScoreMeaning = %q, want empty", event.ScoreMeaning)
	}
}

// --- Engine.Investigate: judgment framing across the real pipeline ---

// judgmentFramingEngineFixture builds a 3-member discovered-cohort
// investigation (two PROVISIONAL members via health severity + investment
// mix, in opposite directions, and one INSUFFICIENT_EVIDENCE member with NO
// facts at all -- deficiencySeveritySignal's own available-zero exception
// still counts it as one available family, weight 20, below the 50/2-family
// qualification floor) and runs it with requestedJudgment/kind as the
// interpreter's own RequestedJudgment/RequestedJudgmentKind, through the
// REAL Engine.Investigate pipeline (RankCohort + applyCohortJudgmentMismatch
// both run inside engine.go, never mocked). kind is set directly on the
// stub interpretation -- exactly as a real interpreter call would set it:
// the judgment's KIND is the interpreter's own closed-vocabulary pick,
// never re-derived downstream from the free text.
func judgmentFramingEngineFixture(t *testing.T, requestedJudgment string, kind RequestedJudgmentKind) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	strugglingTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:STRUGGLING", Label: "Struggling"}
	healthyTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:HEALTHY", Label: "Healthy"}
	sparseTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SPARSE", Label: "Sparse"}
	cohort := &Cohort{
		Kind: SubjectTeam, Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: healthyTeam, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: strugglingTeam, Rank: 2, InclusionReasons: []string{"matched"}},
			{Subject: sparseTeam, Rank: 3, InclusionReasons: []string{"matched"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: requestedJudgment, RequestedJudgmentKind: kind,
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{
					healthFact("STRUGGLING", "high"),
					healthFact("HEALTHY", "low"),
					investmentFact("STRUGGLING", balancedThemes(), 0),
					investmentFact("HEALTHY", balancedThemes(), 0),
					// SPARSE gets NO facts at all. deficiencySeveritySignal's
					// own "available-zero" exception (cohort_ranking.go) still
					// counts it as one available family (weight 20) with
					// nothing else available, so availableWeight=20<50 keeps
					// it below the qualification floor -- insufficient_evidence,
					// never provisional.
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "placeholder", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "placeholder", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(577400000, 0).UTC() }, NewResultID: func() string { return "result_57740001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_57740001"
	request.Question = requestedJudgment
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, telemetry
}

func TestEngineJudgmentMismatchAcrossRequestedJudgmentFraming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		requestedJudgment string
		kind              RequestedJudgmentKind
		wantMismatch      bool
	}{
		{"rank best to worst (performance)", "Rank teams by current overall performance from strongest to weakest.", RequestedJudgmentKindPerformance, true},
		{"rank most struggling (attention)", "Rank teams by how much they are struggling, most to least.", RequestedJudgmentKindAttention, false},
		{"who needs attention (attention)", "Which teams need the most attention right now?", RequestedJudgmentKindAttention, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			result, telemetry := judgmentFramingEngineFixture(t, c.requestedJudgment, c.kind)
			if result.Cohort == nil || len(result.Cohort.Members) != 3 {
				t.Fatalf("result.Cohort = %#v, want the ranked 3-member cohort", result.Cohort)
			}
			if result.Cohort.ScoreMeaning != CohortScoreMeaningAttention {
				t.Fatalf("result.Cohort.ScoreMeaning = %q, want %q", result.Cohort.ScoreMeaning, CohortScoreMeaningAttention)
			}
			if result.Cohort.JudgmentMismatch != c.wantMismatch {
				t.Fatalf("result.Cohort.JudgmentMismatch = %v, want %v for requested_judgment %q", result.Cohort.JudgmentMismatch, c.wantMismatch, c.requestedJudgment)
			}
			// The SAME decision must reach the telemetry line too, through
			// the real Engine.Investigate call path -- not just the served
			// Cohort a caller of the API sees.
			if len(telemetry.cohortRanked) != 1 {
				t.Fatalf("telemetry.cohortRanked = %#v, want exactly 1 event", telemetry.cohortRanked)
			}
			if got := telemetry.cohortRanked[0].JudgmentMismatch; got != c.wantMismatch {
				t.Fatalf("telemetry event JudgmentMismatch = %v, want %v", got, c.wantMismatch)
			}
			if got := telemetry.cohortRanked[0].RequestedJudgmentKind; got != c.kind {
				t.Fatalf("telemetry event RequestedJudgmentKind = %q, want %q", got, c.kind)
			}
			// The sparse member never cleared the qualification floor --
			// this is the fixture's own precondition, not the behavior
			// under test, but a drifted fixture would make every
			// assertion above meaningless.
			var sparse *CohortMember
			for i := range result.Cohort.Members {
				if result.Cohort.Members[i].Subject.CanonicalID == "team:SPARSE" {
					sparse = &result.Cohort.Members[i]
				}
			}
			if sparse == nil || sparse.Outcome != CohortOutcomeInsufficientEvidence || sparse.Score != nil {
				t.Fatalf("fixture drift: sparse member = %#v, want Outcome=insufficient_evidence, Score=nil", sparse)
			}
			if err := result.Cohort.Validate(); err != nil {
				t.Fatalf("result.Cohort.Validate() = %v", err)
			}
		})
	}
}

// TestEngineJudgmentMismatchFalseWhenRequestedJudgmentIsBlank is a control:
// a request whose interpreter made no kind pick at all (RequestedJudgmentKind
// unset) must never be flagged as a mismatch.
func TestEngineJudgmentMismatchFalseWhenRequestedJudgmentIsBlank(t *testing.T) {
	t.Parallel()
	result, telemetry := judgmentFramingEngineFixture(t, "teams_under_pressure", "")
	if result.Cohort == nil {
		t.Fatalf("result.Cohort = nil")
	}
	if result.Cohort.JudgmentMismatch {
		t.Fatalf("result.Cohort.JudgmentMismatch = true, want false for a non-performance-worded requested judgment")
	}
	if len(telemetry.cohortRanked) != 1 || telemetry.cohortRanked[0].JudgmentMismatch {
		t.Fatalf("telemetry.cohortRanked = %#v, want exactly 1 event with JudgmentMismatch=false", telemetry.cohortRanked)
	}
	if got := telemetry.cohortRanked[0].RequestedJudgmentKind; got != "" {
		t.Fatalf("telemetry event RequestedJudgmentKind = %q, want empty", got)
	}
	if !strings.Contains(strings.ToLower(RankingFormulaVersion), "cohort-ranking") {
		t.Fatalf("fixture sanity: RankingFormulaVersion = %q", RankingFormulaVersion)
	}
}

// --- narrowSynthesisInput's own re-rank call site ---

// TestNarrowSynthesisInputCarriesTheJudgmentMismatchDecision drives the
// narrowing-retry re-rank directly (narrowSynthesisInput, chaos4636_budget_stage3.go)
// -- a SEPARATE RankCohort/applyCohortJudgmentMismatch call site from the
// engine's primary rank, exercised only when stage 3 narrows a cohort past
// its budget. Both fields must land on the returned event here too, not
// only on the primary rank's event.
func TestNarrowSynthesisInputCarriesTheJudgmentMismatchDecision(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1", "d1")
	params := synthesisAssemblyParams{
		Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{},
		Interpretation: InterpretedQuestion{RequestedJudgmentKind: RequestedJudgmentKindPerformance},
	}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if !result.Narrow {
		t.Fatalf("result.Narrow = false, want true -- a 4-member cohort narrowing to 2 must re-rank")
	}
	if result.Ranked.ScoreMeaning != CohortScoreMeaningAttention {
		t.Fatalf("result.Ranked.ScoreMeaning = %q, want %q", result.Ranked.ScoreMeaning, CohortScoreMeaningAttention)
	}
	if !result.Ranked.JudgmentMismatch {
		t.Fatalf("result.Ranked.JudgmentMismatch = false, want true -- a performance-kind request over an attention re-rank is a mismatch here too")
	}
	if result.Ranked.RequestedJudgmentKind != RequestedJudgmentKindPerformance {
		t.Fatalf("result.Ranked.RequestedJudgmentKind = %q, want %q", result.Ranked.RequestedJudgmentKind, RequestedJudgmentKindPerformance)
	}
}

// TestNarrowSynthesisInputJudgmentMismatchFalseForAttentionKind is the
// control: an attention-kind request over the SAME re-rank never mismatches.
func TestNarrowSynthesisInputJudgmentMismatchFalseForAttentionKind(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1", "d1")
	params := synthesisAssemblyParams{
		Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{},
		Interpretation: InterpretedQuestion{RequestedJudgmentKind: RequestedJudgmentKindAttention},
	}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if !result.Narrow {
		t.Fatalf("result.Narrow = false, want true")
	}
	if result.Ranked.JudgmentMismatch {
		t.Fatalf("result.Ranked.JudgmentMismatch = true, want false for an attention-kind request over an attention re-rank")
	}
	if result.Ranked.RequestedJudgmentKind != RequestedJudgmentKindAttention {
		t.Fatalf("result.Ranked.RequestedJudgmentKind = %q, want %q", result.Ranked.RequestedJudgmentKind, RequestedJudgmentKindAttention)
	}
}
