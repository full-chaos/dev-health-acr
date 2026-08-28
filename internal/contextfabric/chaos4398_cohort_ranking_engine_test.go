package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestEngineRanksDiscoveredCohortBetweenFactReadAndSynthesis is CHAOS-4398's
// end-to-end engine-wiring proof: a discovered_cohort investigation whose
// graph discovery returns a Cohort must reach Synthesize with that cohort's
// Members RANKED (RankingComputed/Score/AttentionRank/RankingBasis/
// DataCompleteness set) -- computed strictly BETWEEN the fact read and
// Synthesize, over facts the engine already read, never inside the graph
// reader and never by the (fake, in this test) model. It also proves
// subject-model-and-cohort-answers.md §3a's fact-requirement injection: the
// interpreter's own FactRequirements name only FactHealth, yet the fact
// reader observes all five ranking-formula kinds in the merged Requirements
// -- without that injection RankCohort would only ever see health data for
// any cohort question the model under-requested.
//
// Members stays in POOL order (Rank unchanged); AttentionRank carries the
// score-derived order. The synthesizer itself never sets Cohort, so an
// unranked result would mean RankCohort never ran.
func TestEngineRanksDiscoveredCohortBetweenFactReadAndSynthesis(t *testing.T) {
	t.Parallel()
	strugglingTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:STRUGGLING", Label: "Struggling"}
	healthyTeam := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:HEALTHY", Label: "Healthy"}
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "kind census match",
		Members: []CohortMember{
			// Pool order deliberately has the HEALTHY (lower-score) team
			// first -- AttentionRank must still put STRUGGLING first
			// without touching this array order.
			{Subject: healthyTeam, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: strugglingTeam, Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
		TimeContext: TimeContext{Axis: TemporalCurrent},
		// Deliberately narrow -- only FactHealth -- to prove the engine
		// injects the other four ranking kinds rather than trusting the
		// model's own pick (§3a).
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
	var synthesisSawCohort *Cohort
	var observedKinds map[FactKind]bool
	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			if request.Cohort == nil || len(request.Cohort.Members) != 2 {
				t.Fatalf("fact request cohort = %#v, want the 2-member cohort wired through", request.Cohort)
			}
			observedKinds = make(map[FactKind]bool, len(request.Requirements))
			for _, requirement := range request.Requirements {
				observedKinds[requirement.Kind] = true
			}
			return CanonicalFactBundle{
				// investment_mix contributes EQUALLY (identical balanced
				// themes, crossing no sub-label threshold) to both teams,
				// pushing available weight to 25+30=55 -- clearing the
				// 50-point qualification threshold (design doc §8) for
				// BOTH without disturbing the health-severity-driven score
				// difference the AttentionRank assertions below depend on.
				Facts: []CanonicalFact{
					{Kind: FactHealth, Subject: strugglingTeam, Fields: map[string]FactValue{"severity": StringFactValue("high")}},
					{Kind: FactHealth, Subject: healthyTeam, Fields: map[string]FactValue{"severity": StringFactValue("low")}},
					investmentFact("STRUGGLING", balancedThemes(), 0),
					investmentFact("HEALTHY", balancedThemes(), 0),
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			synthesisSawCohort = input.Graph.Cohort
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Some teams are under pressure.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Some teams are under pressure.", Warnings: []string{},
				// Cohort deliberately left unset: the engine, not the
				// synthesizer, must be what carries the RANKED cohort into
				// result.Cohort (engine.go's `if result.Cohort == nil {
				// result.Cohort = graphContext.Cohort }`).
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_43980001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	// A cohort investigation needs a CONFIRMED window before it reaches
	// fact-read/synthesis (CHAOS-3900/CHAOS-4040) -- an unconfirmed window
	// is a legitimate reason to stop short of ranking, but is not what
	// THIS test is about.
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_43980001"
	request.Question = "which teams are struggling?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// §3a: the fact read must have requested all five ranking-formula
	// kinds, not just the interpreter's own narrow FactHealth pick.
	for _, kind := range []FactKind{FactHealth, FactWorkload, FactReadiness, FactOperationalDeficiencies, FactInvestment} {
		if !observedKinds[kind] {
			t.Fatalf("fact requirements = %#v, missing injected kind %q", observedKinds, kind)
		}
	}

	// Synthesize must have seen the ALREADY-RANKED cohort (RankCohort runs
	// before Synthesize, not after).
	if synthesisSawCohort == nil || len(synthesisSawCohort.Members) != 2 {
		t.Fatalf("synthesis saw cohort = %#v, want the 2-member ranked cohort", synthesisSawCohort)
	}

	if result.Cohort == nil || len(result.Cohort.Members) != 2 {
		t.Fatalf("result.Cohort = %#v, want the ranked 2-member cohort", result.Cohort)
	}
	// Pool order (array order) must be UNCHANGED -- HEALTHY first, exactly
	// as the cohort discovery built it.
	first, second := result.Cohort.Members[0], result.Cohort.Members[1]
	if first.Subject.CanonicalID != "team:HEALTHY" || second.Subject.CanonicalID != "team:STRUGGLING" {
		t.Fatalf("result.Cohort.Members order = [%q, %q], want unchanged pool order [HEALTHY, STRUGGLING]", first.Subject.CanonicalID, second.Subject.CanonicalID)
	}
	if first.Rank != 1 || second.Rank != 2 {
		t.Fatalf("Ranks = %d, %d, want UNCHANGED pool-order values 1, 2", first.Rank, second.Rank)
	}
	// AttentionRank carries the score order: STRUGGLING (severity=high)
	// outranks HEALTHY (severity=low).
	if !first.RankingComputed || !second.RankingComputed {
		t.Fatalf("RankingComputed = %v, %v, want true, true", first.RankingComputed, second.RankingComputed)
	}
	if second.AttentionRank != 1 || first.AttentionRank != 2 {
		t.Fatalf("AttentionRank = HEALTHY:%d STRUGGLING:%d, want HEALTHY:2 STRUGGLING:1", first.AttentionRank, second.AttentionRank)
	}
	if first.Score == nil || second.Score == nil || *second.Score <= *first.Score {
		t.Fatalf("Scores = HEALTHY:%v STRUGGLING:%v -- want STRUGGLING strictly higher", first.Score, second.Score)
	}
	for _, m := range result.Cohort.Members {
		if m.DataCompleteness == "" {
			t.Fatalf("member %q has no DataCompleteness -- RankCohort did not run", m.Subject.CanonicalID)
		}
	}
	if err := result.Cohort.Validate(); err != nil {
		t.Fatalf("result.Cohort.Validate() = %v", err)
	}

	// Telemetry: exactly one CohortRankedEvent, member_count=2, the current
	// formula version, and health_risk counted as an available signal for
	// both members.
	if len(telemetry.cohortRanked) != 1 {
		t.Fatalf("cohortRanked events = %#v, want exactly 1", telemetry.cohortRanked)
	}
	event := telemetry.cohortRanked[0]
	if event.MemberCount != 2 || event.FormulaVersion != RankingFormulaVersion {
		t.Fatalf("event = %#v", event)
	}
	if event.SignalsAvailable[RankingSignalHealthRisk] != 2 {
		t.Fatalf("SignalsAvailable = %#v, want health_risk: 2", event.SignalsAvailable)
	}
}
