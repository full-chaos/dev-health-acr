package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5774: an executed-answer observation found a cohort ranking (an
// ATTENTION/adverse-pressure measure) served as a best-to-worst PERFORMANCE
// judgment, with an insufficient_evidence member named "weakest performer".
// These tests cover the two independent mechanisms this ticket adds: the
// deterministic score-meaning/judgment-mismatch data RankCohort and the
// engine mint (never model-authored), and the structural guard that refuses
// a model-authored driver's ranking superlative about a member the formula
// never scored.

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

// --- requireNoSuperlativeClaimOverUnrankableMember / ValidateAgainst ---

// unrankableMemberFixture builds a SynthesisInput/valid draft pair whose
// cohort has two members: one QUALIFIED (a real Score), one
// INSUFFICIENT_EVIDENCE (no Score at all -- the formula never scored it).
func unrankableMemberFixture() (SynthesisInput, SynthesisDraft, SubjectRef, SubjectRef) {
	input, draft := closureFixture()
	qualified := input.Graph.Resolution.Committed[0]
	unrankable := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_unrankable", Label: "Unrankable"}
	score := 60.0
	input.Graph.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "fixture", Complete: true,
		ScoreMeaning: CohortScoreMeaningAttention,
		Members: []CohortMember{
			{
				Subject: qualified, Rank: 1, InclusionReasons: []string{"matched"},
				RankingComputed: true, AttentionRank: 1, Score: &score,
				DataCompleteness: CohortDataComplete, Outcome: CohortOutcomeQualified,
			},
			{
				Subject: unrankable, Rank: 2, InclusionReasons: []string{"matched"},
				RankingComputed: true, AttentionRank: 2,
				DataCompleteness: CohortDataDegraded, Outcome: CohortOutcomeInsufficientEvidence,
				MissingSignals: []string{RankingSignalHealthRisk},
			},
		},
	}
	draft.Drivers[0].Category = "narrative"
	draft.Drivers[0].ClaimedFactIDs = nil
	return input, draft, qualified, unrankable
}

func TestValidateAgainstRejectsSuperlativeAboutInsufficientEvidenceMember(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		title string
	}{
		{"strongest", "Unrankable is the provisional strongest team"},
		{"weakest", "Unrankable ranks last, the weakest team"},
		{"best", "Unrankable is the best team in this cohort"},
		{"worst", "Unrankable is the worst performer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			input, draft, _, unrankable := unrankableMemberFixture()
			draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
			draft.Drivers[0].Title = c.title
			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for a superlative claim about an insufficient_evidence member")
			}
			if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSuperlativeOverUnrankableMember {
				t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSuperlativeOverUnrankableMember)
			}
		})
	}
}

// TestValidateAgainstAllowsSuperlativeAboutAQualifiedMember is the scope
// control: the rule is about a member the formula could NOT score, never
// about disagreeing with a superlative over a member that DOES have a real
// attention position.
func TestValidateAgainstAllowsSuperlativeAboutAQualifiedMember(t *testing.T) {
	t.Parallel()
	input, draft, qualified, _ := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{qualified}
	draft.Drivers[0].Title = "Qualified is the strongest attention signal in this cohort"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a superlative about a QUALIFIED member to be admitted", err)
	}
}

// TestValidateAgainstAllowsNonSuperlativeCommentaryAboutUnrankableMember
// proves the guard is not a ban on ever mentioning a low-evidence member --
// an honest disclosure of its outcome must still be admitted.
func TestValidateAgainstAllowsNonSuperlativeCommentaryAboutUnrankableMember(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable has insufficient evidence to compute an attention score"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want an honest outcome disclosure to be admitted", err)
	}
}

// TestContainsWordRequiresWholeWordMatch guards against the OVER-strict
// failure mode: a fixed-term substring match would reject ordinary prose
// that merely CONTAINS one of the closed terms as a substring of another
// word (e.g. "asbestos" contains "best").
func TestContainsWordRequiresWholeWordMatch(t *testing.T) {
	t.Parallel()
	input, draft, _, unrankable := unrankableMemberFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{unrankable}
	draft.Drivers[0].Title = "Unrankable's asbestos remediation backlog is unrelated to ranking"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a substring-only match (asbestos contains \"best\") to be admitted", err)
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
// stub interpretation -- exactly as a real interpreter call would set it,
// per the team-lead's ruling that the judgment's KIND is the interpreter's
// own closed-vocabulary pick, never re-derived downstream from the free
// text.
func judgmentFramingEngineFixture(t *testing.T, requestedJudgment string, kind RequestedJudgmentKind) InvestigationResult {
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
	return result
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
			result := judgmentFramingEngineFixture(t, c.requestedJudgment, c.kind)
			if result.Cohort == nil || len(result.Cohort.Members) != 3 {
				t.Fatalf("result.Cohort = %#v, want the ranked 3-member cohort", result.Cohort)
			}
			if result.Cohort.ScoreMeaning != CohortScoreMeaningAttention {
				t.Fatalf("result.Cohort.ScoreMeaning = %q, want %q", result.Cohort.ScoreMeaning, CohortScoreMeaningAttention)
			}
			if result.Cohort.JudgmentMismatch != c.wantMismatch {
				t.Fatalf("result.Cohort.JudgmentMismatch = %v, want %v for requested_judgment %q", result.Cohort.JudgmentMismatch, c.wantMismatch, c.requestedJudgment)
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
	result := judgmentFramingEngineFixture(t, "teams_under_pressure", "")
	if result.Cohort == nil {
		t.Fatalf("result.Cohort = nil")
	}
	if result.Cohort.JudgmentMismatch {
		t.Fatalf("result.Cohort.JudgmentMismatch = true, want false for a non-performance-worded requested judgment")
	}
	if !strings.Contains(strings.ToLower(RankingFormulaVersion), "cohort-ranking") {
		t.Fatalf("fixture sanity: RankingFormulaVersion = %q", RankingFormulaVersion)
	}
}
