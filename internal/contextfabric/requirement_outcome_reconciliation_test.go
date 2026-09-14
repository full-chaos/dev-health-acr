package contextfabric

// Requirement-outcome reconciliation, driven through the real engine.
//
// THE FIXTURE IS THE DIAGNOSED ROW'S SHAPE, BY FRAME. A children_of_scope frame
// over a repository anchor declares team members and a count goal, so the
// derivation predicts `count/member/team` served. Retrieval then resolves no
// member set, assembly states the count unavailable with the wire cause
// `fact_pruned`, and synthesis serves one membership fact about the anchor
// repository with status `partial`. No question text is used; the frame is
// built through the shipped derivation.
//
// Every test here drives Engine.Investigate or finalizeServed and reads what
// the served document and the recorded telemetry carry. None builds the
// transition it asserts on.

import (
	"context"
	"errors"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The fixture builders (newReconciliationEngine, runReconciliation, the anchor
// and its membership claim) live in requirement_outcome_transition_harm_test.go,
// which names nothing the parent commit lacks.

// TestAPredictedServedCountThatAssemblyCannotServeIsAnObservedTransition is the
// Class B fixture, red at the parent: the parent serves this document with no
// line that says the prediction and the assembled outcome disagree.
func TestAPredictedServedCountThatAssemblyCannotServeIsAnObservedTransition(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := newReconciliationEngine(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, telemetry, nil)
	result := runReconciliation(t, engine, 0)

	// The document the fixture must reach before any assertion means anything:
	// the derivation predicted the count served, and assembly stated it
	// unavailable with the collapsed wire code.
	if result.Status != InvestigationPartial {
		t.Fatalf("status = %q, want partial -- the fixture did not reach the diagnosed serve", result.Status)
	}
	planned := planRequirementFor(t, result, reconciliationCountRequirement)
	if !planned.Served() {
		t.Fatalf("the plan predicts %q unavailable (%q); the fixture must predict it served", reconciliationCountRequirement, planned.Unavailable)
	}
	assembled := assembledRowFor(t, result.Completeness.Outcomes, reconciliationCountRequirement)
	if assembled.Outcome != contractsv1.ContextFabricRequirementUnavailable || assembled.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactPruned {
		t.Fatalf("assembled row = %s/%s, want unavailable/fact_pruned", assembled.Outcome, assembled.CauseCoverage)
	}

	// THE HARM: the transition is on the trace, with the split cause.
	if len(telemetry.requirementOutcomeTransitions) != 1 {
		t.Fatalf("recorded %d transition line(s), want exactly 1: %+v", len(telemetry.requirementOutcomeTransitions), telemetry.requirementOutcomeTransitions)
	}
	got := telemetry.requirementOutcomeTransitions[0]
	want := RequirementOutcomeTransitionEvent{
		RequirementOutcomeTransition: RequirementOutcomeTransition{
			Requirement: reconciliationCountRequirement, Obligation: string(ObligationCount),
			Role: string(SubjectRoleMember), Subject: SubjectTeam,
			Predicted: RequirementPredictedServed, PredictedReason: "",
			AssembledOutcome: contractsv1.ContextFabricRequirementUnavailable,
			CauseCoverage:    contractsv1.ContextFabricCoverageDetailFactPruned,
			AssemblyReason:   RequirementAssemblyReasonComputedPopulationAbsent,
			Served:           0, Declared: 0, ServedFactCount: 0, MemberSetResolved: false,
		},
		Index: 1, Total: 1,
	}
	if got != want {
		t.Fatalf("transition =\n  %+v\nwant\n  %+v", got, want)
	}
	// The split is not the wire code restated.
	if string(got.AssemblyReason) == string(got.CauseCoverage) {
		t.Fatalf("cause %q equals the wire code %q; the line must name the reason below the collapsed code", got.AssemblyReason, got.CauseCoverage)
	}

	// THE DOCUMENT IS TRUTHFUL: no assembled row claims the count satisfied, and
	// the served membership fact about the anchor is not counted as serving it.
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == reconciliationCountRequirement && row.Stage != contractsv1.ContextFabricOutcomeStagePlanning &&
			row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("a %s row claims the count satisfied: %+v", row.Stage, row)
		}
	}
	if n := len(result.ClaimedFacts); n != 1 {
		t.Fatalf("served %d claimed facts, want the 1 unrelated membership fact", n)
	}
	if result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("completeness state = %q, want degraded", result.Completeness.State)
	}
}

// TestACountServedEndToEndEmitsNoTransition is the control: the same frame over
// a resolved member set, so the assembled count matches its prediction.
func TestACountServedEndToEndEmitsNoTransition(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := newReconciliationEngine(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, telemetry, nil)
	result := runReconciliation(t, engine, 0)

	assembled := assembledRowFor(t, result.Completeness.Outcomes, reconciliationCountRequirement)
	if assembled.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("assembled outcome = %q, want satisfied -- the control must serve the count", assembled.Outcome)
	}
	if n := len(telemetry.requirementOutcomeTransitions); n != 0 {
		t.Fatalf("recorded %d transition line(s) for a count served as predicted: %+v", n, telemetry.requirementOutcomeTransitions)
	}
	// The satisfied count is backed by served evidence of its kind: the
	// server-minted cardinality claim for teams.
	planned := planRequirementFor(t, result, reconciliationCountRequirement)
	if got := servedRequirementFactCount(result, planned); got != 1 {
		t.Fatalf("served fact count = %d, want 1 (the team cardinality claim)", got)
	}
	if ReconcileRequirementOutcomes(result) != nil {
		t.Fatal("ReconcileRequirementOutcomes reports a transition for the served control document")
	}
}

// TestANarrowedCountIsATransitionWithNoSplitCause pins the other assembled
// outcome and the `none` member of the split, over values that differ from the
// Class B line in every field that can differ.
func TestANarrowedCountIsATransitionWithNoSplitCause(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := newReconciliationEngine(t, countingCohort(SubjectTeam, 5), InvestigationComplete, nil, telemetry, nil)
	result := runReconciliation(t, engine, 2)

	if len(telemetry.requirementOutcomeTransitions) != 1 {
		t.Fatalf("recorded %d transition line(s), want 1: %+v", len(telemetry.requirementOutcomeTransitions), telemetry.requirementOutcomeTransitions)
	}
	got := telemetry.requirementOutcomeTransitions[0]
	assembled := assembledRowFor(t, result.Completeness.Outcomes, reconciliationCountRequirement)
	if got.AssembledOutcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("assembled outcome = %q, want narrowed", got.AssembledOutcome)
	}
	if got.AssemblyReason != RequirementAssemblyReasonNone {
		t.Fatalf("cause = %q, want none -- a narrowing names its own mechanism on the wire", got.AssemblyReason)
	}
	if got.CauseNarrowing == "" || got.CauseNarrowing != assembled.CauseNarrowing {
		t.Fatalf("cause_narrowing = %q, want the row's own %q", got.CauseNarrowing, assembled.CauseNarrowing)
	}
	if got.Served != 2 || got.Declared != 5 {
		t.Fatalf("served/declared = %d/%d, want 2/5", got.Served, got.Declared)
	}
	if got.ServedFactCount != 1 || !got.MemberSetResolved {
		t.Fatalf("served_fact_count=%d member_set_resolved=%v, want 1/true", got.ServedFactCount, got.MemberSetResolved)
	}
}

// TestTheCompletenessAuthorityDerivesFromTheReconciledRows pins that the shadow
// authority reads the same rows the transition is computed from: the Class B
// document derives degraded, against a model status of partial.
func TestTheCompletenessAuthorityDerivesFromTheReconciledRows(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := newReconciliationEngine(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, telemetry, nil)
	result := runReconciliation(t, engine, 0)

	if len(telemetry.completenessAuthorities) == 0 {
		t.Fatal("no completeness authority observation was recorded")
	}
	observation := telemetry.completenessAuthorities[len(telemetry.completenessAuthorities)-1]
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded || !observation.Derived || !observation.Disagreed {
		t.Fatalf("authority = %+v, want derived degraded, disagreeing with the model's partial", observation)
	}
	if again := DeriveCompletenessAuthority(result); again.ServerState != observation.ServerState {
		t.Fatalf("re-deriving from the served rows gives %q, the recorded observation says %q", again.ServerState, observation.ServerState)
	}
}

// planRequirementFor returns the published plan requirement for an identity.
func planRequirementFor(t *testing.T, result InvestigationResult, identity string) contractsv1.ContextFabricPlanRequirement {
	t.Helper()
	if result.AnswerPlan == nil {
		t.Fatal("the served document carries no answer plan")
	}
	for _, requirement := range result.AnswerPlan.Requirements {
		if requirement.Requirement == identity {
			return requirement
		}
	}
	t.Fatalf("the plan publishes no requirement %q", identity)
	return contractsv1.ContextFabricPlanRequirement{}
}

// TestASatisfiedRequirementWithNoServedEvidenceIsRefused is the invariant over
// its input domain, driven through finalizeServed on every serving stage the
// reuse path and the decisive path share.
func TestASatisfiedRequirementWithNoServedEvidenceIsRefused(t *testing.T) {
	t.Parallel()
	served := runReconciliation(t, newReconciliationEngine(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, &recordingTelemetry{}, nil), 0)
	engine := newReconciliationEngine(t, countingCohort(SubjectTeam, 3), InvestigationComplete, nil, &recordingTelemetry{}, nil)

	withoutCardinalityClaim := func(result InvestigationResult) InvestigationResult {
		kept := make([]ClaimedFact, 0, len(result.ClaimedFacts))
		for _, claim := range result.ClaimedFacts {
			if claim.Kind != contractsv1.ContextFabricFactCardinality {
				kept = append(kept, claim)
			}
		}
		result.ClaimedFacts = kept
		return result
	}
	readRequirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "state/subject/team", Obligation: string(ObligationState), Role: string(SubjectRoleSubject),
		Subject: SubjectTeam, Kind: string(ObligationKindRead), FactKinds: []FactKind{FactStatus},
		Scope: string(CompletionScopeSingleSubject), Quantifier: string(CompletionQuantifierAtLeastOne),
	}
	withSatisfiedRead := func(result InvestigationResult, sources []SourceObservation, claims []ClaimedFact) InvestigationResult {
		plan := *result.AnswerPlan
		plan.Requirements = append(append([]contractsv1.ContextFabricPlanRequirement{}, plan.Requirements...), readRequirement)
		result.AnswerPlan = &plan
		result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes,
			RequirementOutcomeRow{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: readRequirement.Requirement, Obligation: readRequirement.Obligation,
				Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone},
			RequirementOutcomeRow{Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: readRequirement.Requirement, Obligation: readRequirement.Obligation,
				Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 1, Declared: 1})
		result.Coverage.Sources = append(append([]SourceObservation{}, result.Coverage.Sources...), sources...)
		result.ClaimedFacts = append(append([]ClaimedFact{}, result.ClaimedFacts...), claims...)
		return result
	}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:SERVED", Label: "served"}
	statusClaim := func(subject SubjectRef) ClaimedFact {
		return ClaimedFact{ClaimID: "claim_status", Kind: FactStatus, Subject: subject, Field: "status", Value: ScalarValue{String: ptrString("green")}}
	}
	statusSource := func(state SourceState) SourceObservation {
		return SourceObservation{Source: canonicalFactSourcePrefix + string(FactStatus), State: state}
	}

	for _, cell := range []struct {
		name    string
		mutate  func(InvestigationResult) InvestigationResult
		refused bool
	}{
		{"count: claim and resolved team set (as served)", func(r InvestigationResult) InvestigationResult { return r }, false},
		{"count: claim dropped, resolved team set stands", withoutCardinalityClaim, false},
		{"count: claim dropped, member set absent", func(r InvestigationResult) InvestigationResult {
			r = withoutCardinalityClaim(r)
			r.Cohort = nil
			return r
		}, true},
		{"count: claim dropped, member set of another kind", func(r InvestigationResult) InvestigationResult {
			r = withoutCardinalityClaim(r)
			r.Cohort = countingCohort(SubjectRepository, 3)
			return r
		}, true},
		{"count: claim for another kind only", func(r InvestigationResult) InvestigationResult {
			r = withoutCardinalityClaim(r)
			r.Cohort = nil
			claim, _ := cardinalityClaim(storage.Principal{OrgID: "org_1"}, MembershipCardinality{Resolved: true, Kind: SubjectRepository, Served: 3, Declared: 3})
			r.ClaimedFacts = append(r.ClaimedFacts, claim)
			return r
		}, true},
		{"read: a served source of a declared kind", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, []SourceObservation{statusSource(SourceAvailable)}, nil)
		}, false},
		{"read: a stale source of a declared kind", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, []SourceObservation{statusSource(SourceStale)}, nil)
		}, false},
		{"read: only a failed source", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, []SourceObservation{statusSource(SourceUnavailable)}, nil)
		}, true},
		{"read: no source at all", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, nil, nil)
		}, true},
		{"read: a served source of an undeclared kind", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, []SourceObservation{{Source: canonicalFactSourcePrefix + string(FactMembership), State: SourceAvailable}}, nil)
		}, true},
		{"read: a claim of the declared kind about the subject kind", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, nil, []ClaimedFact{statusClaim(team)})
		}, false},
		{"read: a claim of the declared kind about another subject kind", func(r InvestigationResult) InvestigationResult {
			return withSatisfiedRead(r, nil, []ClaimedFact{statusClaim(reconciliationAnchor())})
		}, true},
		{"read: a claim of an undeclared kind about the subject kind", func(r InvestigationResult) InvestigationResult {
			claim := statusClaim(team)
			claim.Kind = FactMembership
			return withSatisfiedRead(r, nil, []ClaimedFact{claim})
		}, true},
		{"count: a non-cardinality claim naming the counted field", func(r InvestigationResult) InvestigationResult {
			r = withoutCardinalityClaim(r)
			r.Cohort = nil
			claim := reconciliationAnchorMembershipClaim()
			claim.Field = cardinalityClaimField(SubjectTeam)
			r.ClaimedFacts = append(r.ClaimedFacts, claim)
			return r
		}, true},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			document := cell.mutate(served)
			for _, stage := range BudgetAssertStageVocabulary() {
				_, err := engine.finalizeServed(context.Background(), storage.Principal{OrgID: "org_1"}, stage, document, nil, ResponseBudget{})
				switch {
				case cell.refused && !errors.Is(err, ErrSatisfiedRequirementUnserved):
					t.Fatalf("stage %s: err = %v, want ErrSatisfiedRequirementUnserved", stage, err)
				case !cell.refused && err != nil:
					t.Fatalf("stage %s: err = %v, want the document served", stage, err)
				}
			}
		})
	}
}

// TestTheTransitionLineFiresOnTheDecisiveExitOnly sweeps every serving exit of
// finalizeServed over the Class B document: each serves it, and only the
// decisive exit, the one that runs assembly on its request, reports the
// transition.
func TestTheTransitionLineFiresOnTheDecisiveExitOnly(t *testing.T) {
	t.Parallel()
	served := runReconciliation(t, newReconciliationEngine(t, nil, InvestigationPartial, []ClaimedFact{reconciliationAnchorMembershipClaim()}, &recordingTelemetry{}, nil), 0)
	if len(ReconcileRequirementOutcomes(served)) != 1 {
		t.Fatal("the Class B document does not reconcile to one transition; the sweep would test nothing")
	}
	for _, stage := range BudgetAssertStageVocabulary() {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			engine := newReconciliationEngine(t, nil, InvestigationPartial, nil, telemetry, nil)
			if _, err := engine.finalizeServed(context.Background(), storage.Principal{OrgID: "org_1"}, stage, served, nil, ResponseBudget{}); err != nil {
				t.Fatalf("finalizeServed(%s) error = %v", stage, err)
			}
			want := 0
			if stage == BudgetAssertDecisive {
				want = 1
			}
			if got := len(telemetry.requirementOutcomeTransitions); got != want {
				t.Fatalf("stage %s emitted %d transition line(s), want %d", stage, got, want)
			}
		})
	}
}

// TestTheAssembledAccountIsTheRowThatLosesTheMost pins the choice among several
// assembled_result rows for one identity: the most lossy row, the first on a
// tie, and planning rows never.
func TestTheAssembledAccountIsTheRowThatLosesTheMost(t *testing.T) {
	t.Parallel()
	row := func(stage contractsv1.ContextFabricOutcomeStage, outcome RequirementOutcome, served int) RequirementOutcomeRow {
		return RequirementOutcomeRow{Stage: stage, Requirement: "state/subject/team", Obligation: string(ObligationState), Outcome: outcome, Served: served, Declared: 9}
	}
	planning := contractsv1.ContextFabricOutcomeStagePlanning
	assembled := contractsv1.ContextFabricOutcomeStageAssembledResult
	for _, cell := range []struct {
		name       string
		rows       []RequirementOutcomeRow
		found      bool
		outcome    RequirementOutcome
		wantServed int
	}{
		{"no assembled row", []RequirementOutcomeRow{row(planning, contractsv1.ContextFabricRequirementUnavailable, 1)}, false, "", 0},
		{"planning unavailable beside assembled narrowed", []RequirementOutcomeRow{row(planning, contractsv1.ContextFabricRequirementUnavailable, 1), row(assembled, contractsv1.ContextFabricRequirementNarrowed, 4)}, true, contractsv1.ContextFabricRequirementNarrowed, 4},
		{"narrowed then unavailable", []RequirementOutcomeRow{row(assembled, contractsv1.ContextFabricRequirementNarrowed, 4), row(assembled, contractsv1.ContextFabricRequirementUnavailable, 0)}, true, contractsv1.ContextFabricRequirementUnavailable, 0},
		{"unavailable then narrowed", []RequirementOutcomeRow{row(assembled, contractsv1.ContextFabricRequirementUnavailable, 0), row(assembled, contractsv1.ContextFabricRequirementNarrowed, 4)}, true, contractsv1.ContextFabricRequirementUnavailable, 0},
		{"satisfied then narrowed", []RequirementOutcomeRow{row(assembled, contractsv1.ContextFabricRequirementSatisfied, 9), row(assembled, contractsv1.ContextFabricRequirementNarrowed, 5)}, true, contractsv1.ContextFabricRequirementNarrowed, 5},
		{"tie keeps the first", []RequirementOutcomeRow{row(assembled, contractsv1.ContextFabricRequirementNarrowed, 6), row(assembled, contractsv1.ContextFabricRequirementNarrowed, 3)}, true, contractsv1.ContextFabricRequirementNarrowed, 6},
		{"another identity is ignored", []RequirementOutcomeRow{{Stage: assembled, Requirement: "health/subject/team", Obligation: string(ObligationHealth), Outcome: contractsv1.ContextFabricRequirementUnavailable}}, false, "", 0},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			got, found := assembledRequirementAccount(cell.rows, "state/subject/team")
			if found != cell.found || got.Outcome != cell.outcome || got.Served != cell.wantServed {
				t.Fatalf("account = %q served=%d found=%v, want %q served=%d found=%v", got.Outcome, got.Served, found, cell.outcome, cell.wantServed, cell.found)
			}
		})
	}
	// TOTAL over the outcome vocabulary, and an unknown token is never lossless.
	for _, outcome := range contractsv1.ContextFabricPlanRequirementOutcomeVocabulary() {
		rank := outcomeLossRank(outcome)
		lossless := outcome == contractsv1.ContextFabricRequirementSatisfied || outcome == contractsv1.ContextFabricRequirementNotApplicable
		if (rank == 0) != lossless {
			t.Errorf("outcome %q ranks %d; only a lossless outcome ranks 0", outcome, rank)
		}
		if outcome == contractsv1.ContextFabricRequirementUnavailable && rank != 2 {
			t.Errorf("unavailable ranks %d, want 2 -- above narrowed and not_attempted", rank)
		}
	}
	if rank := outcomeLossRank("undeclared"); rank <= outcomeLossRank(contractsv1.ContextFabricRequirementUnavailable) {
		t.Errorf("an undeclared outcome ranks %d, at or below unavailable", rank)
	}
}

// TestAClaimServesARequirementOnlyOfItsKindAndSubject walks the claim matcher's
// input domain: the three requirement classes, each against a matching claim
// and a claim that differs in exactly one field.
func TestAClaimServesARequirementOnlyOfItsKindAndSubject(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:A", Label: "A"}
	org := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "org_1"}
	count := contractsv1.ContextFabricPlanRequirement{Requirement: "count/member/team", Obligation: string(ObligationCount), Role: string(SubjectRoleMember), Subject: SubjectTeam, Kind: string(ObligationKindComputed)}
	read := contractsv1.ContextFabricPlanRequirement{Requirement: "state/subject/team", Obligation: string(ObligationState), Role: string(SubjectRoleSubject), Subject: SubjectTeam, Kind: string(ObligationKindRead), FactKinds: []FactKind{FactStatus}}
	ranking := contractsv1.ContextFabricPlanRequirement{Requirement: "ranking/member/team", Obligation: string(ObligationRanking), Role: string(SubjectRoleMember), Subject: SubjectTeam, Kind: string(ObligationKindComputed), InputFactKinds: []FactKind{FactHealth}}
	claim := func(kind FactKind, subject SubjectRef, field string) ClaimedFact {
		return ClaimedFact{ClaimID: "c", Kind: kind, Subject: subject, Field: field}
	}
	for _, cell := range []struct {
		name        string
		requirement contractsv1.ContextFabricPlanRequirement
		claim       ClaimedFact
		want        bool
	}{
		{"count: the team cardinality claim", count, claim(contractsv1.ContextFabricFactCardinality, org, "team_count"), true},
		{"count: cardinality of another kind", count, claim(contractsv1.ContextFabricFactCardinality, org, "repository_count"), false},
		{"count: another kind naming team_count", count, claim(FactMembership, org, "team_count"), false},
		{"read: declared kind, subject kind", read, claim(FactStatus, team, "status"), true},
		{"read: declared kind, another subject kind", read, claim(FactStatus, org, "status"), false},
		{"read: undeclared kind, subject kind", read, claim(FactHealth, team, "status"), false},
		{"ranking: declared input kind, subject kind", ranking, claim(FactHealth, team, "score"), true},
		{"ranking: declared input kind, another subject kind", ranking, claim(FactHealth, org, "score"), false},
		{"ranking: undeclared kind, subject kind", ranking, claim(FactStatus, team, "score"), false},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			if got := claimServesRequirement(cell.claim, cell.requirement); got != cell.want {
				t.Fatalf("claimServesRequirement = %v, want %v", got, cell.want)
			}
		})
	}
}

// TestTheTransitionLineVocabulariesAreTheirOwnersVocabularies pins that every
// closed list the specification reads is derived from the vocabulary that owns
// it, and that an unknown key reads as an open field.
func TestTheTransitionLineVocabulariesAreTheirOwnersVocabularies(t *testing.T) {
	t.Parallel()
	outcomes := contractsv1.ContextFabricPlanRequirementOutcomeVocabulary()
	reasons := RequirementUnavailableReasonVocabulary()
	for key, want := range map[string][]string{
		"predicted":         {"served", "unavailable"},
		"predicted_reason":  append([]string{"none"}, tokenStrings(reasons[:])...),
		"assembled_outcome": tokenStrings(outcomes[:]),
		"cause":             {"none", "computed_population_absent"},
	} {
		if got := RequirementOutcomeTransitionLineVocabulary(key); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("vocabulary %q = %v, want %v", key, got, want)
		}
	}
	if got := RequirementOutcomeTransitionLineVocabulary("cause_coverage"); got != nil {
		t.Errorf("an open key returned %v, want nil", got)
	}
	if string(RequirementAssemblyReasonComputedPopulationAbsent) != string(RequirementReasonComputedPopulationAbsent) {
		t.Error("the assembly reason for an absent member set is not the derivation's own token")
	}
}
