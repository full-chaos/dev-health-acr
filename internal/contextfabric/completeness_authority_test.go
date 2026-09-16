package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// satisfiedRow/narrowedRow/unavailableRow/notAttemptedRow/notApplicableRow
// build minimal outcome rows for the derivation under test.
// DeriveContextFabricAnswerCompletenessState reads only Requirement,
// Obligation, Outcome and Stage -- these fixtures set nothing else.

func outcomeRow(stage contractsv1.ContextFabricOutcomeStage, identity, obligation string, outcome contractsv1.ContextFabricPlanRequirementOutcome) RequirementOutcomeRow {
	return RequirementOutcomeRow{Stage: stage, Requirement: identity, Obligation: obligation, Outcome: outcome}
}

func satisfiedRow(identity, obligation string) RequirementOutcomeRow {
	return outcomeRow(contractsv1.ContextFabricOutcomeStagePlanning, identity, obligation, contractsv1.ContextFabricRequirementSatisfied)
}

// evaluatedRow builds the assembled-result row that answers a READ
// requirement's planning-stage seed -- see
// contracts/v1/completeness_state_planning_only_test.go's identical helper.
// A READ obligation (readiness, remaining_work, state, ...) needs BOTH rows
// to read as genuinely EVALUATED rather than merely seeded; see
// hasPlanningOnlyReadRequirement's own doc comment.
func evaluatedRow(identity, obligation string) RequirementOutcomeRow {
	return outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, identity, obligation, contractsv1.ContextFabricRequirementSatisfied)
}

// TestDeriveAnswerDisposition_IsTotalOverInvestigationStatus pins every
// member of the closed status vocabulary to exactly one disposition.
func TestDeriveAnswerDisposition_IsTotalOverInvestigationStatus(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		status       InvestigationStatus
		refusalBasis contractsv1.ContextFabricRefusalBasis
		want         AnswerDisposition
	}{
		{"complete", InvestigationComplete, "", AnswerDispositionAnswer},
		{"partial", InvestigationPartial, "", AnswerDispositionAnswer},
		{"degraded", InvestigationDegraded, "", AnswerDispositionAnswer},
		{"clarification required, no refusal basis", InvestigationClarificationRequired, "", AnswerDispositionClarification},
		{"no match, no refusal basis", InvestigationNoMatch, "", AnswerDispositionNoMatch},
		{"no match, with refusal basis", InvestigationNoMatch, contractsv1.ContextFabricRefusalBasisUnspecified, AnswerDispositionRefusal},
		// RefusalBasis is ORTHOGONAL to Status (its own doc comment) and is
		// forbidden alongside `complete` only, not alongside
		// `clarification_required`/`partial`/`degraded`. RefusalBasis must
		// win regardless of which of these three Status carries.
		{"clarification required, with refusal basis", InvestigationClarificationRequired, contractsv1.ContextFabricRefusalBasisUnspecified, AnswerDispositionRefusal},
		{"partial, with refusal basis", InvestigationPartial, contractsv1.ContextFabricRefusalBasisUnspecified, AnswerDispositionRefusal},
		{"degraded, with refusal basis", InvestigationDegraded, contractsv1.ContextFabricRefusalBasisUnspecified, AnswerDispositionRefusal},
		// The default arm: a status outside the five-member closed
		// vocabulary. Go's InvestigationStatus is a string type, so this
		// value is constructible even though no producer emits it; the
		// default arm must report the least-claiming disposition, never
		// `answer`, on the strength of not recognizing the value.
		{"unrecognized status", InvestigationStatus("not_a_real_status"), "", AnswerDispositionNoMatch},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveAnswerDisposition(InvestigationResult{Status: testCase.status, RefusalBasis: testCase.refusalBasis})
			if got != testCase.want {
				t.Fatalf("DeriveAnswerDisposition(status=%q, refusal_basis=%q) = %q, want %q", testCase.status, testCase.refusalBasis, got, testCase.want)
			}
			if !ValidAnswerDisposition(got) {
				t.Fatalf("DeriveAnswerDisposition returned %q, which is not a vocabulary member", got)
			}
		})
	}
}

// TestAnswerCompletenessStateToStatus_IsTotal pins the ONE explicit crossing
// this file's header describes -- every vocabulary member either maps or
// explicitly declines, never a default fallthrough.
func TestAnswerCompletenessStateToStatus_IsTotal(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		state    contractsv1.ContextFabricAnswerCompletenessState
		wantOK   bool
		wantStat InvestigationStatus
	}{
		{contractsv1.ContextFabricAnswerCompletenessComplete, true, InvestigationComplete},
		{contractsv1.ContextFabricAnswerCompletenessPartial, true, InvestigationPartial},
		{contractsv1.ContextFabricAnswerCompletenessDegraded, true, InvestigationDegraded},
		{contractsv1.ContextFabricAnswerCompletenessNotDerived, false, ""},
	} {
		t.Run(string(testCase.state), func(t *testing.T) {
			t.Parallel()
			got, ok := answerCompletenessStateToStatus(testCase.state)
			if ok != testCase.wantOK || got != testCase.wantStat {
				t.Fatalf("answerCompletenessStateToStatus(%q) = (%q, %v), want (%q, %v)", testCase.state, got, ok, testCase.wantStat, testCase.wantOK)
			}
		})
	}
	// Every vocabulary member was exercised above -- if a member is ever
	// added, this count guards that this test's table grows with it.
	if got, want := len(contractsv1.ContextFabricAnswerCompletenessStateVocabulary()), 4; got != want {
		t.Fatalf("ContextFabricAnswerCompletenessStateVocabulary() has %d members, want %d -- add a case above for the new member", got, want)
	}
}

// TestDeriveCompletenessAuthority_IdenticalEvidenceDifferentModelLabels is
// D25's first acceptance item: the server's own state is a pure function of
// the outcome rows, invariant to which label the model chose.
func TestDeriveCompletenessAuthority_IdenticalEvidenceDifferentModelLabels(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "count/subject/team", "count", contractsv1.ContextFabricRequirementUnavailable),
	}
	var observed []CompletenessAuthorityObservation
	for _, status := range []InvestigationStatus{InvestigationPartial, InvestigationDegraded} {
		result := InvestigationResult{Status: status, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
		observed = append(observed, DeriveCompletenessAuthority(result))
	}
	if observed[0].ServerState != observed[1].ServerState || observed[0].Basis != observed[1].Basis {
		t.Fatalf("identical outcome rows under different model labels produced different server completeness: %+v vs %+v", observed[0], observed[1])
	}
	if observed[0].ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("ServerState = %q, want degraded (an unavailable row is absorbing)", observed[0].ServerState)
	}
	// The model said degraded and the server agrees -- no disagreement.
	if observed[1].Disagreed {
		t.Fatalf("model status degraded matches ServerState degraded: Disagreed should be false, got true (%+v)", observed[1])
	}
	// The model said partial but the server says degraded -- this is
	// EXACTLY the partial-versus-degraded distinction status_shadow.go's
	// old rule could never see (it never contradicts a non-complete model
	// status). The new authority's measurement does.
	if !observed[0].Disagreed {
		t.Fatalf("model status partial vs ServerState degraded: Disagreed should be true, got false (%+v)", observed[0])
	}
}

// TestDeriveCompletenessAuthority_CompleteEvidenceZeroGaps is D25's second
// acceptance item: an evaluated, empty result is complete, distinguished
// from one whose outcomes were never derived at all.
func TestDeriveCompletenessAuthority_CompleteEvidenceZeroGaps(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		// PLANNING SEED + ASSEMBLED-RESULT ROW for each READ requirement:
		// this is what an EVALUATED, zero-gap answer looks like -- a
		// finding was looked for and none was there, not "nobody looked".
		satisfiedRow("readiness/subject/team", "readiness"),
		evaluatedRow("readiness/subject/team", "readiness"),
		satisfiedRow("remaining_work/subject/team", "remaining_work"),
		evaluatedRow("remaining_work/subject/team", "remaining_work"),
	}
	result := InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	observation := DeriveCompletenessAuthority(result)
	if observation.Basis != CompletenessAuthorityBasisOutcomeDerived || observation.ServerState != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("evaluated zero-gap evidence: got basis=%q state=%q, want outcome_derived/complete", observation.Basis, observation.ServerState)
	}
	if observation.Disagreed {
		t.Fatalf("model complete matches ServerState complete: Disagreed should be false, got true")
	}
}

// TestDeriveCompletenessAuthority_BoundedPopulationReduction is D25's third
// acceptance item: a known, bounded narrowing reads partial.
func TestDeriveCompletenessAuthority_BoundedPopulationReduction(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "ranking/subject/team", "ranking", contractsv1.ContextFabricRequirementNarrowed),
	}
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}})
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("a narrowed population row: ServerState = %q, want partial", observation.ServerState)
	}
	if !observation.Disagreed {
		t.Fatalf("model claimed complete over a narrowed population: Disagreed should be true")
	}
}

// TestDeriveCompletenessAuthority_UnavailableRequiredSource is D25's fourth
// acceptance item: a required source the server could not reach at all
// reads degraded, and degraded is absorbing.
func TestDeriveCompletenessAuthority_UnavailableRequiredSource(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		satisfiedRow("evidence/subject/team", "evidence"),
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "readiness/subject/team", "readiness", contractsv1.ContextFabricRequirementUnavailable),
	}
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}})
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("an unavailable required source: ServerState = %q, want degraded", observation.ServerState)
	}
}

// TestDeriveCompletenessAuthority_InformationalLimitationAlone pins that an
// informational limitation, with no non-satisfied outcome row, must NOT
// degrade the server's state -- unlike status_shadow.go's old rule, which
// read ANY limitation as a completeness signal.
func TestDeriveCompletenessAuthority_InformationalLimitationAlone(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{satisfiedRow("evidence/subject/team", "evidence")}
	result := InvestigationResult{
		Status:       InvestigationComplete,
		Limitations:  []string{"scope narrowed to the last 30 days by request"},
		Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows},
	}
	observation := DeriveCompletenessAuthority(result)
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("an informational limitation with all-satisfied outcomes: ServerState = %q, want complete (a limitation alone must not degrade)", observation.ServerState)
	}
}

// TestDeriveCompletenessAuthority_MissingComparisonOperand is D25's
// acceptance item on a comparison: one operand that was never read cannot
// certify the comparison as complete.
func TestDeriveCompletenessAuthority_MissingComparisonOperand(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		satisfiedRow("evidence/operand-a/repository", "evidence"),
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/operand-b/repository", "evidence", contractsv1.ContextFabricRequirementNotAttempted),
	}
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}})
	if observation.ServerState == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("one comparison operand never attempted: ServerState = complete, want partial or degraded")
	}
}

// TestDeriveCompletenessAuthority_PlanningOnlyReadRequirement is D25's
// planning-only/unattempted acceptance item: a READ requirement seeded but
// never evaluated cannot read complete, even though its own outcome token
// is lossless.
func TestDeriveCompletenessAuthority_PlanningOnlyReadRequirement(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{satisfiedRow("state/subject/team", "state")}
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}})
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("a READ requirement with only a planning-stage seed: ServerState = %q, want partial", observation.ServerState)
	}
}

// TestDeriveCompletenessAuthority_ProjectionOmitsRequiredContent is D25's
// projection acceptance item: a projection-stage cut that drops required
// content stays qualified after projection, never silently promoted back to
// complete.
func TestDeriveCompletenessAuthority_ProjectionOmitsRequiredContent(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		satisfiedRow("evidence/subject/team", "evidence"),
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementSatisfied),
		outcomeRow(contractsv1.ContextFabricOutcomeStageProjection, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementNarrowed),
	}
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}})
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("a projection-stage narrowing of required content: ServerState = %q, want partial (truncation must stay qualified after projection)", observation.ServerState)
	}
}

// TestDeriveCompletenessAuthority_PendingClarificationIsNotAnIncompleteAnswer
// and its refusal/no_match siblings pin the disposition/completeness split
// this file's header describes.
func TestDeriveCompletenessAuthority_NonAnswerDispositionsCarryNoCompleteness(t *testing.T) {
	t.Parallel()
	// Outcome rows that would derive `degraded` if this were an answer --
	// proving the non-answer branch does not merely happen to look empty,
	// it REFUSES to consult the rows at all.
	degradingRows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	for _, testCase := range []struct {
		name            string
		status          InvestigationStatus
		refusalBasis    contractsv1.ContextFabricRefusalBasis
		wantDisposition AnswerDisposition
	}{
		{"pending clarification", InvestigationClarificationRequired, "", AnswerDispositionClarification},
		{"refusal", InvestigationNoMatch, contractsv1.ContextFabricRefusalBasisUnspecified, AnswerDispositionRefusal},
		{"plain no match", InvestigationNoMatch, "", AnswerDispositionNoMatch},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := InvestigationResult{
				Status:       testCase.status,
				RefusalBasis: testCase.refusalBasis,
				Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: degradingRows},
				ClaimedFacts: []ClaimedFact{{ClaimID: "c1", Kind: contractsv1.ContextFabricFactWork, Field: "work"}},
			}
			observation := DeriveCompletenessAuthority(result)
			if observation.Disposition != testCase.wantDisposition {
				t.Fatalf("Disposition = %q, want %q", observation.Disposition, testCase.wantDisposition)
			}
			if observation.Basis != CompletenessAuthorityBasisNotAnAnswer {
				t.Fatalf("Basis = %q, want not_an_answer", observation.Basis)
			}
			if observation.Derived || observation.ServerState != "" || observation.Disagreed {
				t.Fatalf("a non-answer disposition must carry no completeness verdict at all, got %+v", observation)
			}
			// THE DIGEST IS NOT A COMPLETENESS VERDICT -- it is a property of
			// the document, and a non-answer disposition still HAS a
			// document. The row above is real and the claimed fact is real;
			// both must still reach the line, or a zero here would read as
			// "no rows"/"nothing claimed" when the truth is "never an
			// answer, so never asked."
			if observation.OutcomeRowsTotal != 1 {
				t.Fatalf("OutcomeRowsTotal = %d, want 1 -- the digest must not depend on disposition", observation.OutcomeRowsTotal)
			}
			if observation.DecidingRequirement != "evidence/subject/team" {
				t.Fatalf("DecidingRequirement = %q, want the real unavailable row's identity", observation.DecidingRequirement)
			}
			workIndex, ok := factKindIndex(contractsv1.ContextFabricFactWork)
			if !ok {
				t.Fatal("work is not in its own fact-kind vocabulary")
			}
			if observation.ClaimedFactsByKind[workIndex] != 1 {
				t.Fatalf("ClaimedFactsByKind[work] = %d, want 1 -- a real claimed fact must reach the digest on every disposition", observation.ClaimedFactsByKind[workIndex])
			}
		})
	}
}

// TestDeriveCompletenessAuthority_LegacyRowsWithoutSemanticState is D25's
// legacy-disclosure acceptance item: a result whose outcome set was never
// derived discloses `unavailable`, never a fabricated `complete`.
func TestDeriveCompletenessAuthority_LegacyRowsWithoutSemanticState(t *testing.T) {
	t.Parallel()
	observation := DeriveCompletenessAuthority(InvestigationResult{Status: InvestigationComplete})
	if observation.Basis != CompletenessAuthorityBasisUnavailable {
		t.Fatalf("Basis = %q, want unavailable", observation.Basis)
	}
	if observation.Derived || observation.ServerState != "" || observation.Disagreed {
		t.Fatalf("an unavailable basis must never fabricate a derived state or a disagreement, got %+v", observation)
	}
}

// TestDeriveCompletenessAuthority_EveryObservationCarriesTheDerivationVersion
// is D25's provenance acceptance item: fresh, reused or read-by-id, every
// observation discloses which derivation series it came from.
func TestDeriveCompletenessAuthority_EveryObservationCarriesTheDerivationVersion(t *testing.T) {
	t.Parallel()
	for _, result := range []InvestigationResult{
		{Status: InvestigationComplete},
		{Status: InvestigationComplete, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: []RequirementOutcomeRow{satisfiedRow("evidence/subject/team", "evidence")}}},
		{Status: InvestigationClarificationRequired},
	} {
		if got := DeriveCompletenessAuthority(result).Version; got != CompletenessAuthorityVersion {
			t.Fatalf("Version = %q, want %q", got, CompletenessAuthorityVersion)
		}
	}
	if CompletenessAuthorityVersion == ServerStatusShadowVersion {
		t.Fatalf("the new authority's series must be distinct from status_shadow.go's -- they measure different rules and must never be spliced")
	}
}

// TestDeriveCompletenessAuthority_DirectionIsTotalOverEveryPair pins the
// direction acceptance item: every ordered (model, server) pair among
// complete/partial/degraded produces the one Direction that names it, and
// every agreeing pair produces CompletenessAuthorityDirectionNone -- pinned
// through the real derivation (DeriveCompletenessAuthority), not the
// internal helper directly, so the table also proves WouldFlip stays in
// lockstep with Disagreed on every case.
func TestDeriveCompletenessAuthority_DirectionIsTotalOverEveryPair(t *testing.T) {
	t.Parallel()
	satisfiedOutcome := []RequirementOutcomeRow{satisfiedRow("evidence/subject/team", "evidence")}
	narrowedOutcome := []RequirementOutcomeRow{outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "ranking/subject/team", "ranking", contractsv1.ContextFabricRequirementNarrowed)}
	unavailableOutcome := []RequirementOutcomeRow{outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable)}
	for _, testCase := range []struct {
		name          string
		modelStatus   InvestigationStatus
		outcomes      []RequirementOutcomeRow
		wantServer    contractsv1.ContextFabricAnswerCompletenessState
		wantDirection CompletenessAuthorityDirection
		wantDisagreed bool
	}{
		{"complete agrees complete", InvestigationComplete, satisfiedOutcome, contractsv1.ContextFabricAnswerCompletenessComplete, CompletenessAuthorityDirectionNone, false},
		{"complete_to_partial", InvestigationComplete, narrowedOutcome, contractsv1.ContextFabricAnswerCompletenessPartial, CompletenessAuthorityDirectionCompleteToPartial, true},
		{"complete_to_degraded", InvestigationComplete, unavailableOutcome, contractsv1.ContextFabricAnswerCompletenessDegraded, CompletenessAuthorityDirectionCompleteToDegraded, true},
		{"partial_to_complete", InvestigationPartial, satisfiedOutcome, contractsv1.ContextFabricAnswerCompletenessComplete, CompletenessAuthorityDirectionPartialToComplete, true},
		{"partial agrees partial", InvestigationPartial, narrowedOutcome, contractsv1.ContextFabricAnswerCompletenessPartial, CompletenessAuthorityDirectionNone, false},
		{"partial_to_degraded", InvestigationPartial, unavailableOutcome, contractsv1.ContextFabricAnswerCompletenessDegraded, CompletenessAuthorityDirectionPartialToDegraded, true},
		{"degraded_to_complete", InvestigationDegraded, satisfiedOutcome, contractsv1.ContextFabricAnswerCompletenessComplete, CompletenessAuthorityDirectionDegradedToComplete, true},
		{"degraded_to_partial", InvestigationDegraded, narrowedOutcome, contractsv1.ContextFabricAnswerCompletenessPartial, CompletenessAuthorityDirectionDegradedToPartial, true},
		{"degraded agrees degraded", InvestigationDegraded, unavailableOutcome, contractsv1.ContextFabricAnswerCompletenessDegraded, CompletenessAuthorityDirectionNone, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := InvestigationResult{Status: testCase.modelStatus, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: testCase.outcomes}}
			observation := DeriveCompletenessAuthority(result)
			if observation.ServerState != testCase.wantServer {
				t.Fatalf("test setup: ServerState = %q, want %q", observation.ServerState, testCase.wantServer)
			}
			if observation.Disagreed != testCase.wantDisagreed {
				t.Fatalf("Disagreed = %v, want %v", observation.Disagreed, testCase.wantDisagreed)
			}
			if observation.WouldFlip != observation.Disagreed {
				t.Fatalf("WouldFlip = %v, Disagreed = %v -- these must never read apart", observation.WouldFlip, observation.Disagreed)
			}
			if observation.Direction != testCase.wantDirection {
				t.Fatalf("Direction = %q, want %q", observation.Direction, testCase.wantDirection)
			}
			if !ValidCompletenessAuthorityDirection(observation.Direction) {
				t.Fatalf("Direction %q is not a vocabulary member", observation.Direction)
			}
		})
	}
}

// TestCompletenessAuthorityDirectionVocabulary_MembershipIsExact is the same
// pin every other closed vocabulary in this file gets.
func TestCompletenessAuthorityDirectionVocabulary_MembershipIsExact(t *testing.T) {
	t.Parallel()
	vocabulary := CompletenessAuthorityDirectionVocabulary()
	if len(vocabulary) != CompletenessAuthorityDirectionCount {
		t.Fatalf("len(vocabulary) = %d, want CompletenessAuthorityDirectionCount %d", len(vocabulary), CompletenessAuthorityDirectionCount)
	}
	for _, member := range vocabulary {
		if !ValidCompletenessAuthorityDirection(member) {
			t.Errorf("ValidCompletenessAuthorityDirection(%q) = false, want true (a vocabulary member)", member)
		}
	}
	if ValidCompletenessAuthorityDirection(CompletenessAuthorityDirection("not_a_real_direction")) {
		t.Error("ValidCompletenessAuthorityDirection(\"not_a_real_direction\") = true, want false")
	}
}

// -- The gated flip --

// TestApplyServerCompletenessAuthority_SymmetricDowngradesPartialToDegraded
// and its mirror pin the LATERAL pair: with symmetricEnabled on (and the
// old `enabled` flag off), a model-claimed partial/degraded may be
// corrected to the other of the pair.
func TestApplyServerCompletenessAuthority_SymmetricDowngradesPartialToDegraded(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	result := InvestigationResult{Status: InvestigationPartial, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	observation := DeriveCompletenessAuthority(result)
	got := ApplyServerCompletenessAuthority(result, false, true, observation)
	if got.Status != InvestigationDegraded {
		t.Fatalf("Status = %q, want degraded", got.Status)
	}
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("Completeness.TerminalStatus = %q, must equal Status %q after the flip", got.Completeness.TerminalStatus, got.Status)
	}
}

func TestApplyServerCompletenessAuthority_SymmetricDowngradesDegradedToPartial(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "ranking/subject/team", "ranking", contractsv1.ContextFabricRequirementNarrowed),
	}
	result := InvestigationResult{Status: InvestigationDegraded, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	observation := DeriveCompletenessAuthority(result)
	got := ApplyServerCompletenessAuthority(result, false, true, observation)
	if got.Status != InvestigationPartial {
		t.Fatalf("Status = %q, want partial", got.Status)
	}
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("Completeness.TerminalStatus = %q, must equal Status %q after the flip", got.Completeness.TerminalStatus, got.Status)
	}
}

// TestApplyServerCompletenessAuthority_SymmetricDisabledLeavesLateralPairUntouched
// pins the new flag's default: with symmetricEnabled off, a disagreeing
// partial/degraded pair is left exactly as the model served it, regardless
// of the OLD flag's setting (which only ever acts on a model-claimed
// complete).
func TestApplyServerCompletenessAuthority_SymmetricDisabledLeavesLateralPairUntouched(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	for _, modelStatus := range []InvestigationStatus{InvestigationPartial, InvestigationDegraded} {
		for _, enabled := range []bool{false, true} {
			result := InvestigationResult{Status: modelStatus, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
			observation := DeriveCompletenessAuthority(result)
			got := ApplyServerCompletenessAuthority(result, enabled, false, observation)
			if got.Status != modelStatus {
				t.Fatalf("model status %q, enabled=%v, symmetricEnabled=false: flip changed Status to %q, want it left at %q", modelStatus, enabled, got.Status, modelStatus)
			}
		}
	}
}

// TestApplyServerCompletenessAuthority_SymmetricNeverPromotesToComplete pins
// that the lateral flag, however set, never joins the complete-side pair:
// a model-claimed partial/degraded whose outcome rows are all satisfied
// (mapping to complete) is never promoted, even with symmetricEnabled on.
func TestApplyServerCompletenessAuthority_SymmetricNeverPromotesToComplete(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{satisfiedRow("evidence/subject/team", "evidence")}
	for _, modelStatus := range []InvestigationStatus{InvestigationPartial, InvestigationDegraded} {
		result := InvestigationResult{Status: modelStatus, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
		observation := DeriveCompletenessAuthority(result)
		got := ApplyServerCompletenessAuthority(result, true, true, observation)
		if got.Status != modelStatus {
			t.Fatalf("model status %q with all-satisfied outcomes, both flags on: flip changed Status to %q, want it left at %q (never promote to complete)", modelStatus, got.Status, modelStatus)
		}
	}
}

// TestApplyServerCompletenessAuthority_FlagsActIndependently pins that the
// two flags gate two disjoint pairs: enabling one never lets the other
// pair's correction through.
func TestApplyServerCompletenessAuthority_FlagsActIndependently(t *testing.T) {
	t.Parallel()
	completeRows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	lateralRows := completeRows // same unavailable row; only ModelStatus differs below.

	// enabled=true, symmetricEnabled=false: corrects complete->degraded, but
	// leaves a disagreeing partial/degraded pair untouched.
	completeResult := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: completeRows}}
	gotComplete := ApplyServerCompletenessAuthority(completeResult, true, false, DeriveCompletenessAuthority(completeResult))
	if gotComplete.Status != InvestigationDegraded {
		t.Fatalf("enabled=true symmetricEnabled=false: complete-side Status = %q, want degraded", gotComplete.Status)
	}
	partialResult := InvestigationResult{Status: InvestigationPartial, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: lateralRows}}
	gotPartial := ApplyServerCompletenessAuthority(partialResult, true, false, DeriveCompletenessAuthority(partialResult))
	if gotPartial.Status != InvestigationPartial {
		t.Fatalf("enabled=true symmetricEnabled=false: lateral-pair Status = %q, want left at partial (this flag must not touch it)", gotPartial.Status)
	}

	// enabled=false, symmetricEnabled=true: the reverse -- lateral pair
	// corrects, complete-side does not.
	gotPartialSymmetric := ApplyServerCompletenessAuthority(partialResult, false, true, DeriveCompletenessAuthority(partialResult))
	if gotPartialSymmetric.Status != InvestigationDegraded {
		t.Fatalf("enabled=false symmetricEnabled=true: lateral-pair Status = %q, want degraded", gotPartialSymmetric.Status)
	}
	gotCompleteSymmetric := ApplyServerCompletenessAuthority(completeResult, false, true, DeriveCompletenessAuthority(completeResult))
	if gotCompleteSymmetric.Status != InvestigationComplete {
		t.Fatalf("enabled=false symmetricEnabled=true: complete-side Status = %q, want left at complete (this flag must not touch it)", gotCompleteSymmetric.Status)
	}
}

// TestApplyServerCompletenessAuthority_DisabledIsANoOp pins the config
// knob's default: with the flag off, the result is returned byte-for-byte
// unchanged regardless of what the outcome rows say.
func TestApplyServerCompletenessAuthority_DisabledIsANoOp(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	result := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows, TerminalStatus: InvestigationComplete}}
	observation := DeriveCompletenessAuthority(result)
	got := ApplyServerCompletenessAuthority(result, false, false, observation)
	if got.Status != InvestigationComplete {
		t.Fatalf("disabled flip changed Status to %q, want it left at complete", got.Status)
	}
}

// TestApplyServerCompletenessAuthority_DowngradesCompleteToPartial and its
// sibling pin the one direction the flip is permitted to move.
func TestApplyServerCompletenessAuthority_DowngradesCompleteToPartial(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "ranking/subject/team", "ranking", contractsv1.ContextFabricRequirementNarrowed),
	}
	result := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	observation := DeriveCompletenessAuthority(result)
	got := ApplyServerCompletenessAuthority(result, true, false, observation)
	if got.Status != InvestigationPartial {
		t.Fatalf("Status = %q, want partial", got.Status)
	}
	// TerminalStatus must stay in lockstep with Status -- the validator's
	// own invariant (validateCompleteness) -- because it was RECOMPUTED,
	// never hand-patched.
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("Completeness.TerminalStatus = %q, must equal Status %q after the flip", got.Completeness.TerminalStatus, got.Status)
	}
}

func TestApplyServerCompletenessAuthority_DowngradesCompleteToDegraded(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	result := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	observation := DeriveCompletenessAuthority(result)
	got := ApplyServerCompletenessAuthority(result, true, false, observation)
	if got.Status != InvestigationDegraded {
		t.Fatalf("Status = %q, want degraded", got.Status)
	}
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("Completeness.TerminalStatus = %q, must equal Status %q after the flip", got.Completeness.TerminalStatus, got.Status)
	}
}

// TestApplyServerCompletenessAuthority_NeverUpgrades pins the flip's
// downgrade-only direction: a model-authored partial/degraded is never
// promoted to complete, even when every outcome row is satisfied.
func TestApplyServerCompletenessAuthority_NeverUpgrades(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{satisfiedRow("evidence/subject/team", "evidence")}
	for _, modelStatus := range []InvestigationStatus{InvestigationPartial, InvestigationDegraded} {
		result := InvestigationResult{Status: modelStatus, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
		observation := DeriveCompletenessAuthority(result)
		got := ApplyServerCompletenessAuthority(result, true, false, observation)
		if got.Status != modelStatus {
			t.Fatalf("model status %q with all-satisfied outcomes: flip changed Status to %q, want it left at %q (never upgrade)", modelStatus, got.Status, modelStatus)
		}
	}
}

// TestApplyServerCompletenessAuthority_NeverTouchesNonAnswerDispositions
// pins the second guardrail: a pending clarification or a refusal is never
// mutated by the flip, however the (unconsulted) outcome rows read.
func TestApplyServerCompletenessAuthority_NeverTouchesNonAnswerDispositions(t *testing.T) {
	t.Parallel()
	degradingRows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	for _, testCase := range []struct {
		name         string
		status       InvestigationStatus
		refusalBasis contractsv1.ContextFabricRefusalBasis
	}{
		{"clarification required", InvestigationClarificationRequired, ""},
		{"refusal", InvestigationNoMatch, contractsv1.ContextFabricRefusalBasisUnspecified},
		{"plain no match", InvestigationNoMatch, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := InvestigationResult{Status: testCase.status, RefusalBasis: testCase.refusalBasis, Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: degradingRows}}
			observation := DeriveCompletenessAuthority(result)
			got := ApplyServerCompletenessAuthority(result, true, false, observation)
			if got.Status != testCase.status {
				t.Fatalf("Status changed from %q to %q; a non-answer disposition must never be touched", testCase.status, got.Status)
			}
		})
	}
}

// TestApplyServerCompletenessAuthority_ObservationMatchesResult guards the
// call-site contract every production caller (finalizeServed, the by-id
// route) actually follows: the observation handed in is always taken FROM
// the same result, in the same call, never a stale or hand-built one --
// so this exercises the function exactly that way and checks the OUTCOME
// the flip produces, not merely the observation it was handed.
func TestApplyServerCompletenessAuthority_ObservationMatchesResult(t *testing.T) {
	t.Parallel()
	rows := []RequirementOutcomeRow{
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable),
	}
	result := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x", Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows}}
	fresh := DeriveCompletenessAuthority(result)
	if !fresh.Derived || fresh.ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("setup: expected a derived degraded observation, got %+v", fresh)
	}
	got := ApplyServerCompletenessAuthority(result, true, false, fresh)
	if got.Status != InvestigationDegraded {
		t.Fatalf("Status = %q, want degraded -- a matching, freshly-taken observation must correct the result", got.Status)
	}
	if got.Completeness.TerminalStatus != got.Status {
		t.Fatalf("Completeness.TerminalStatus = %q, must equal Status %q", got.Completeness.TerminalStatus, got.Status)
	}
	// Recomputing from the (unchanged) outcome rows independently must
	// agree with what the flip just wrote -- the same rows cannot describe
	// two different states depending on which function read them.
	if want := contractsv1.DeriveContextFabricAnswerCompletenessState(got.Completeness.Outcomes); got.Completeness.State != want {
		t.Fatalf("Completeness.State = %q, want %q (re-derived from the same outcome rows)", got.Completeness.State, want)
	}
}

// TestApplyServerCompletenessAuthority_RefusesAnObservationThatDoesNotMap
// pins the function's OWN defensive guard against a hand-built (never a
// DeriveCompletenessAuthority) observation that claims Derived=true with a
// ServerState outside the three that answerCompletenessStateToStatus
// actually maps -- the guard a caller cannot reach through the paired
// DeriveCompletenessAuthority (that function only ever sets Derived=true
// alongside a ServerState it already confirmed maps), but which
// ApplyServerCompletenessAuthority still must not skip: a status is never
// written from an unmapped value.
func TestApplyServerCompletenessAuthority_RefusesAnObservationThatDoesNotMap(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{Status: InvestigationComplete, DirectJudgment: "x"}
	inconsistent := CompletenessAuthorityObservation{
		Basis:       CompletenessAuthorityBasisOutcomeDerived,
		ServerState: contractsv1.ContextFabricAnswerCompletenessState("not_a_real_state"),
		Derived:     true,
	}
	got := ApplyServerCompletenessAuthority(result, true, false, inconsistent)
	if got.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want it left at complete (an unmapped ServerState must never be written)", got.Status)
	}
}

// -- Sink-level telemetry (CHAOS-4085 discipline: a field on a struct that
// never reaches the production log line is not telemetry) --

// TestCompletenessAuthority_ProductionTelemetryEmitsEveryField is the
// sink-level pin RecordServerStatusShadow already has, extended to this
// event: SlogEngineTelemetry must actually implement the method (the
// EngineTelemetry interface assertion is the load-bearing half -- an
// interface satisfied at compile time cannot silently lose a method the
// way an optional interface's type assertion can), and the emitted record
// must carry every field the observation does.
func TestCompletenessAuthority_ProductionTelemetryEmitsEveryField(t *testing.T) {
	t.Parallel()
	var _ EngineTelemetry = SlogEngineTelemetry{}

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCompletenessAuthority(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			CompletenessAuthorityObservation{
				ModelStatus: InvestigationComplete,
				Disposition: AnswerDispositionAnswer,
				Basis:       CompletenessAuthorityBasisOutcomeDerived,
				ServerState: contractsv1.ContextFabricAnswerCompletenessDegraded,
				Derived:     true,
				Disagreed:   true,
				WouldFlip:   true,
				Direction:   CompletenessAuthorityDirectionCompleteToDegraded,
				Version:     CompletenessAuthorityVersion,
			},
		)
	})

	if len(records) != 1 {
		t.Fatalf("one observation must produce exactly one log record, got %d", len(records))
	}
	record := records[0]
	for key, want := range map[string]any{
		"org_id":       "org_sink_test",
		"model_status": string(InvestigationComplete),
		"disposition":  string(AnswerDispositionAnswer),
		"basis":        string(CompletenessAuthorityBasisOutcomeDerived),
		"server_state": string(contractsv1.ContextFabricAnswerCompletenessDegraded),
		"derived":      true,
		"disagreed":    true,
		"would_flip":   true,
		"direction":    string(CompletenessAuthorityDirectionCompleteToDegraded),
		"version":      CompletenessAuthorityVersion,
	} {
		if got, ok := record[key]; !ok || got != want {
			t.Fatalf("record[%q] = %v (present=%v), want %v -- an operator greps for this key", key, got, ok, want)
		}
	}
}

// -- Vocabulary membership --

// TestAnswerDispositionVocabulary_MembershipIsExact pins
// AnswerDispositionVocabulary/ValidAnswerDisposition together: every
// vocabulary member validates, and a value outside it does not.
func TestAnswerDispositionVocabulary_MembershipIsExact(t *testing.T) {
	t.Parallel()
	vocabulary := AnswerDispositionVocabulary()
	if len(vocabulary) != AnswerDispositionCount {
		t.Fatalf("len(vocabulary) = %d, want AnswerDispositionCount %d", len(vocabulary), AnswerDispositionCount)
	}
	for _, member := range vocabulary {
		if !ValidAnswerDisposition(member) {
			t.Errorf("ValidAnswerDisposition(%q) = false, want true (a vocabulary member)", member)
		}
	}
	if ValidAnswerDisposition(AnswerDisposition("not_a_real_disposition")) {
		t.Error("ValidAnswerDisposition(\"not_a_real_disposition\") = true, want false")
	}
}

// TestCompletenessAuthorityBasisVocabulary_MembershipIsExact is the same
// pin for CompletenessAuthorityBasisVocabulary/ValidCompletenessAuthorityBasis.
func TestCompletenessAuthorityBasisVocabulary_MembershipIsExact(t *testing.T) {
	t.Parallel()
	vocabulary := CompletenessAuthorityBasisVocabulary()
	if len(vocabulary) != CompletenessAuthorityBasisCount {
		t.Fatalf("len(vocabulary) = %d, want CompletenessAuthorityBasisCount %d", len(vocabulary), CompletenessAuthorityBasisCount)
	}
	for _, member := range vocabulary {
		if !ValidCompletenessAuthorityBasis(member) {
			t.Errorf("ValidCompletenessAuthorityBasis(%q) = false, want true (a vocabulary member)", member)
		}
	}
	if ValidCompletenessAuthorityBasis(CompletenessAuthorityBasis("not_a_real_basis")) {
		t.Error("ValidCompletenessAuthorityBasis(\"not_a_real_basis\") = true, want false")
	}
}
