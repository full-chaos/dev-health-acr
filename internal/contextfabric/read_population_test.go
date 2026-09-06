package contextfabric

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Fixtures for the read-population arms.
//
// Every one of them drives PRODUCTION appendReadRequirementEvaluations over a
// PRODUCTION readPopulationEvidence built by readPopulationEvidenceFrom, so an
// arm cannot pass against a shape only the test knows how to build.

func teamRef(id string) SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id}
}

func projectRef(id string) SubjectRef {
	return SubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: id, Label: id}
}

// operandRequirement is an `each_operand` read requirement for one subject kind.
func operandRequirement(kind SubjectKind, quantifier CompletionQuantifier, kinds ...FactKind) contractsv1.ContextFabricPlanRequirement {
	return contractsv1.ContextFabricPlanRequirement{
		Requirement: string(ObligationState) + "/" + string(SubjectRoleOperand) + "/" + string(kind),
		Obligation:  string(ObligationState),
		Role:        string(SubjectRoleOperand),
		Subject:     kind,
		Kind:        string(ObligationKindRead),
		FactKinds:   kinds,
		Scope:       string(CompletionScopeEachOperand),
		Quantifier:  string(quantifier),
	}
}

func scopeRequirement(scope CompletionScope, kind SubjectKind, role SubjectRole, quantifier CompletionQuantifier, kinds ...FactKind) contractsv1.ContextFabricPlanRequirement {
	return contractsv1.ContextFabricPlanRequirement{
		Requirement: string(ObligationState) + "/" + string(role) + "/" + string(kind),
		Obligation:  string(ObligationState),
		Role:        string(role),
		Subject:     kind,
		Kind:        string(ObligationKindRead),
		FactKinds:   kinds,
		Scope:       string(scope),
		Quantifier:  string(quantifier),
	}
}

// namedOperandFrame builds an explicit-set frame naming one operand slot per
// entry, in the shape frameRoleSlots walks.
func namedOperandFrame(kinds ...SubjectKind) *QuestionFrame {
	operands := make([]SubjectOperand, 0, len(kinds))
	for _, kind := range kinds {
		expected := kind
		operands = append(operands, SubjectOperand{
			Kind:  SubjectOperandNamed,
			Named: &NamedSubjectExpression{ExpectedKind: &expected},
		})
	}
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:     SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: operands},
	}}
}

// factsFor builds a bundle carrying one available fact per (subject, kind).
func factsFor(pairs ...any) CanonicalFactBundle {
	bundle := CanonicalFactBundle{}
	for index := 0; index+1 < len(pairs); index += 2 {
		subject := pairs[index].(SubjectRef)
		for _, kind := range pairs[index+1].([]FactKind) {
			bundle.Facts = append(bundle.Facts, CanonicalFact{
				Kind: kind, Subject: subject, SourceState: SourceAvailable,
			})
		}
	}
	return bundle
}

func kindList(kinds ...FactKind) []FactKind { return kinds }

// evaluateOperands runs the production path for an explicit-set frame.
func evaluateOperands(
	published []contractsv1.ContextFabricPlanRequirement,
	frame *QuestionFrame,
	committed []SubjectRef,
	coverage Coverage,
	facts CanonicalFactBundle,
) []RequirementOutcomeRow {
	result := InvestigationResult{
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: committed},
		Coverage:          coverage,
	}
	evidence := readPopulationEvidenceFrom(frame, result, AnswerPlan{Requirements: published}, facts)
	return appendReadRequirementEvaluations(nil, published, coverage, evidence)
}

func rowFor(t *testing.T, rows []RequirementOutcomeRow, identity string) RequirementOutcomeRow {
	t.Helper()
	for _, row := range rows {
		if row.Requirement == identity && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return row
		}
	}
	t.Fatalf("no assembled-result row for %q in %+v", identity, rows)
	return RequirementOutcomeRow{}
}

// assertRow checks every field an arm cares about in one place, so an arm that
// asserts an outcome but forgets its counts cannot pass half-checked.
func assertRow(t *testing.T, row RequirementOutcomeRow,
	outcome contractsv1.ContextFabricPlanRequirementOutcome,
	impact contractsv1.ContextFabricAnswerImpactKind,
	cause contractsv1.ContextFabricCoverageDetailCode,
	observed bool, served, declared int) {
	t.Helper()
	if row.Outcome != outcome || row.Impact != impact || row.CauseCoverage != cause ||
		row.CauseObserved != observed || row.Served != served || row.Declared != declared {
		t.Fatalf("row = {outcome %q impact %q cause %q observed %v %d/%d}, want {outcome %q impact %q cause %q observed %v %d/%d}",
			row.Outcome, row.Impact, row.CauseCoverage, row.CauseObserved, row.Served, row.Declared,
			outcome, impact, cause, observed, served, declared)
	}
	// The row must be LEGAL, not merely shaped as expected: a fixture that
	// asserts numbers the validator would refuse is testing a row nobody can
	// write.
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("row is not contract-valid: %v (row %+v)", err, row)
	}
}

// ARM 1 — THE DISCRIMINATOR. Two named team operands, one read.
//
// At the parent this reads `satisfied 2/2`: both declared kinds came back
// available, the kind standard is met, and nothing asked for whom. The whole
// change is that it now reads `narrowed 1/2 scope`.
func TestATwoOperandCompareReadOnOneSideIsNotSatisfied(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)

	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable),
		// Alpha holds both kinds; beta holds nothing. The COVERAGE says both
		// kinds were read, which is exactly the evidence that used to certify.
		factsFor(alpha, kindList(flow, health)),
	)
	row := rowFor(t, rows, requirement.Requirement)
	// Logged so the red proof is self-quoting: at the BASE this same evidence
	// reads `satisfied` 2/2 and the answer reads `complete`.
	t.Logf("AT-TIP OBSERVED: outcome=%q impact=%q cause=%q served=%d declared=%d",
		row.Outcome, row.Impact, row.CauseCoverage, row.Served, row.Declared)
	assertRow(t, row,
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricCoverageDetailFactNarrowed,
		false, 1, 2)
	if len(row.Refinements) != 0 {
		t.Fatalf("no reduction STEP ran, so the row must carry no refinement, got %+v", row.Refinements)
	}
	if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("answer state = %q, want partial", state)
	}
}

// ARM 1 sub-arm — beta REPORTED a no_data flow fact, so the cause is the
// provider's own and CauseObserved flips true. Same 1/2.
func TestATwoOperandCompareNamesAReportedCause(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)

	facts := factsFor(alpha, kindList(flow, health))
	facts.Facts = append(facts.Facts, CanonicalFact{Kind: flow, Subject: beta, SourceState: SourceNoData})

	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable),
		facts,
	)
	assertRow(t, rowFor(t, rows, requirement.Requirement),
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricCoverageDetailFactProviderReported,
		true, 1, 2)
}

// ARM 3 — the paired positive, with THREE served kinds so the parent reads
// `3/3` and this asserts `2/2`: red at the parent BY THE NUMBER, not by
// coincidence. This is the arm proving the counts changed meaning.
func TestEveryOperandReadReadsSatisfiedWithPopulationCounts(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health, workload := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth, contractsv1.ContextFabricFactWorkload
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health, workload)

	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable, workload, SourceAvailable),
		factsFor(alpha, kindList(flow, health, workload), beta, kindList(flow, health, workload)),
	)
	assertRow(t, rowFor(t, rows, requirement.Requirement),
		contractsv1.ContextFabricRequirementSatisfied,
		contractsv1.ContextFabricAnswerImpactNone,
		"", false, 2, 2)
}

// ARM 2 — THE CONTROL. A `single_subject` row stays kind-keyed and byte-
// identical to the parent's, driven BOTH with empty and with populated-but-
// irrelevant population evidence.
//
// This is what fails an implementation that refuses `satisfied` for everything
// -- option A in disguise.
func TestASingleSubjectReadStaysKindKeyed(t *testing.T) {
	t.Parallel()
	health, workload := contractsv1.ContextFabricFactHealth, contractsv1.ContextFabricFactWorkload
	requirement := readRequirement(CompletionQuantifierCorroborated)
	coverage := factCoverage(health, SourceAvailable, workload, SourceAvailable)

	for _, testCase := range []struct {
		name     string
		evidence readPopulationEvidence
	}{
		{name: "no population evidence at all", evidence: readPopulationEvidence{}},
		{
			name: "populated but irrelevant population evidence",
			evidence: readPopulationEvidenceFrom(
				namedOperandFrame(SubjectTeam, SubjectTeam),
				InvestigationResult{SubjectResolution: contractsv1.ContextFabricSubjectResolution{
					Committed: []SubjectRef{teamRef("team_alpha")}}},
				AnswerPlan{}, factsFor(teamRef("team_alpha"), kindList(health))),
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rows := appendReadRequirementEvaluations(nil,
				[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage, testCase.evidence)
			// KIND counts, 2 of 2 -- the parent's own row, unchanged.
			assertRow(t, rowFor(t, rows, requirement.Requirement),
				contractsv1.ContextFabricRequirementSatisfied,
				contractsv1.ContextFabricAnswerImpactNone,
				"", false, 2, 2)
		})
	}
}

// ARM 21 — THE MIXED-KIND SAMENESS ARM. It carries a ruling with ONE reviewer
// behind it and is NOT optional.
//
// Two NAMED operands of DIFFERENT kinds (team A, project B) derive TWO
// coordinates, so a PER-KIND intersection would hold exactly one operand and be
// trivially its own kinds -- both rows would read `satisfied 1/1` while the
// comparison shares no evidence at all. The intersection is therefore
// COMPARISON-WIDE: §13.4.2's "the SAME evidence on every operand" is a statement
// about the COMPARISON, not about one subject kind within it.
//
// This is frame C2s, the shape this review history keeps returning to.
func TestAMixedKindComparisonEnforcesSamenessAcrossOperands(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), projectRef("project_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	investment, workload := contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactWorkload

	teamReq := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health, investment, workload)
	projectReq := operandRequirement(contractsv1.ContextFabricSubjectProject, CompletionQuantifierCorroborated, flow, health, investment, workload)
	published := []contractsv1.ContextFabricPlanRequirement{teamReq, projectReq}

	rows := evaluateOperands(published,
		namedOperandFrame(SubjectTeam, contractsv1.ContextFabricSubjectProject),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable,
			investment, SourceAvailable, workload, SourceAvailable),
		// DISJOINT evidence: each operand independently meets the threshold of
		// 2, so conjunct 1 passes for both and Read == Declared == 1 on each
		// row -- which is what makes conjunct 2 reachable at all.
		factsFor(alpha, kindList(flow, health), beta, kindList(investment, workload)),
	)

	for _, requirement := range published {
		row := rowFor(t, rows, requirement.Requirement)
		// The comparison-wide intersection is EMPTY, so `len(common)` is 0
		// against a threshold of 2: `0/2` in KIND units on the depth arm.
		// Under a PER-KIND intersection both rows read `satisfied 1/1`.
		assertRow(t, row,
			contractsv1.ContextFabricRequirementNarrowed,
			contractsv1.ContextFabricAnswerImpactDepth,
			contractsv1.ContextFabricCoverageDetailFactNarrowed,
			false, 0, 2)
		if len(row.Refinements) != 0 {
			t.Fatalf("%s: sameness failure ran no reduction step, so no refinement, got %+v",
				requirement.Requirement, row.Refinements)
		}
	}
}

// ARM 21 sub-arm — DIFFERENT QUANTIFIERS over the SAME empty intersection must
// publish DIFFERENT denominators, because each row evaluates conjunct 2 with
// its OWN threshold.
//
// Arm 21 proper is uniform `corroborated` and cannot discriminate this; without
// this sub-arm an implementation that hard-codes one threshold for the shared
// intersection passes everything above.
func TestSamenessUsesEachRowsOwnThreshold(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), projectRef("project_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	investment, workload := contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactWorkload

	// corroborated (2) vs at_least_one (1), same four declared kinds.
	teamReq := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health, investment, workload)
	projectReq := operandRequirement(contractsv1.ContextFabricSubjectProject, CompletionQuantifierAtLeastOne, flow, health, investment, workload)

	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{teamReq, projectReq},
		namedOperandFrame(SubjectTeam, contractsv1.ContextFabricSubjectProject),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable,
			investment, SourceAvailable, workload, SourceAvailable),
		factsFor(alpha, kindList(flow, health), beta, kindList(investment, workload)),
	)
	// Same empty intersection, two different denominators.
	assertRow(t, rowFor(t, rows, teamReq.Requirement),
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactDepth,
		contractsv1.ContextFabricCoverageDetailFactNarrowed, false, 0, 2)
	assertRow(t, rowFor(t, rows, projectReq.Requirement),
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactDepth,
		contractsv1.ContextFabricCoverageDetailFactNarrowed, false, 0, 1)
}

// ARM 17 — sameness failure between operands of the SAME kind. A has
// flow+health, B has health+workload: each independently meets the threshold,
// so both are READ, and the intersection is {health}, size 1 against 2.
//
// Red at the parent by OUTCOME and by UNITS: the parent reads `satisfied`, and
// the illegal `narrowed 2/2 scope` is what a naive population-only fix produces.
func TestOperandsWithDifferentEvidenceAreNotSatisfied(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health, workload := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth, contractsv1.ContextFabricFactWorkload
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health, workload)

	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha, beta},
		factCoverage(flow, SourceAvailable, health, SourceAvailable, workload, SourceAvailable),
		factsFor(alpha, kindList(flow, health), beta, kindList(health, workload)),
	)
	row := rowFor(t, rows, requirement.Requirement)
	// KIND counts: len(common)=1 over threshold 2.
	assertRow(t, row,
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactDepth,
		contractsv1.ContextFabricCoverageDetailFactNarrowed,
		false, 1, 2)
	if len(row.Refinements) != 0 {
		t.Fatalf("no reduction step ran, want no refinement, got %+v", row.Refinements)
	}
}

// ARM 18 — THE SPLIT. Fewer committed refs than NAMED slots is PARTIALLY
// ENUMERATED, never absent: the frame said who the operands are, so `0/N` and
// `1/N` are counts, not absences of knowledge.
//
// The two sub-arms together are what stop the split collapsing in either
// direction: a named-slot set at ZERO must NOT take the absent arm, and a
// SCOPED operand must.
func TestAPartiallyEnumeratedOperandSetIsNotAbsent(t *testing.T) {
	t.Parallel()
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	alpha := teamRef("team_alpha")
	coverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)

	t.Run("one of two named slots committed", func(t *testing.T) {
		t.Parallel()
		rows := evaluateOperands(
			[]contractsv1.ContextFabricPlanRequirement{requirement},
			namedOperandFrame(SubjectTeam, SubjectTeam),
			[]SubjectRef{alpha}, coverage,
			factsFor(alpha, kindList(flow, health)))
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementNarrowed,
			contractsv1.ContextFabricAnswerImpactScope,
			contractsv1.ContextFabricCoverageDetailFactNarrowed,
			false, 1, 2)
	})

	t.Run("ZERO of two named slots committed is 0/2, not absent", func(t *testing.T) {
		t.Parallel()
		rows := evaluateOperands(
			[]contractsv1.ContextFabricPlanRequirement{requirement},
			namedOperandFrame(SubjectTeam, SubjectTeam),
			nil, coverage, CanonicalFactBundle{})
		row := rowFor(t, rows, requirement.Requirement)
		assertRow(t, row,
			contractsv1.ContextFabricRequirementNarrowed,
			contractsv1.ContextFabricAnswerImpactScope,
			contractsv1.ContextFabricCoverageDetailFactNarrowed,
			false, 0, 2)
		// NOT the absent arm, and the state must stay partial rather than
		// being absorbed to degraded.
		if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessPartial {
			t.Fatalf("answer state = %q, want partial -- a fully retracted named-slot set is enumerated at zero, not unverifiable", state)
		}
	})

	t.Run("a SCOPED operand IS not-enumerable and takes the absent arm", func(t *testing.T) {
		t.Parallel()
		frame := &QuestionFrame{SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{MemberKind: SubjectTeam}},
			}},
		}}
		rows := evaluateOperands(
			[]contractsv1.ContextFabricPlanRequirement{requirement},
			frame, []SubjectRef{alpha}, coverage,
			factsFor(alpha, kindList(flow, health)))
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementUnavailable,
			contractsv1.ContextFabricAnswerImpactDimension,
			contractsv1.ContextFabricCoverageDetailReadPopulationUnverified,
			true, 0, 0)
	})
}

// cohortWith builds a served cohort with members and optional groups.
func cohortWith(kind SubjectKind, members []SubjectRef, groups []SubjectRef, complete bool) *Cohort {
	cohort := &Cohort{Kind: kind, Complete: complete}
	for _, member := range members {
		cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{Subject: member})
	}
	for _, group := range groups {
		ids := make([]string, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.CanonicalID)
		}
		cohort.Groups = append(cohort.Groups, contractsv1.ContextFabricCohortGroup{
			Subject: group, MemberCanonicalIDs: ids, Complete: complete,
		})
	}
	return cohort
}

func evaluateCohort(
	published []contractsv1.ContextFabricPlanRequirement,
	cohort *Cohort, coverage Coverage, facts CanonicalFactBundle,
) []RequirementOutcomeRow {
	result := InvestigationResult{Cohort: cohort, Coverage: coverage}
	evidence := readPopulationEvidenceFrom(nil, result, AnswerPlan{Requirements: published}, facts)
	return appendReadRequirementEvaluations(nil, published, coverage, evidence)
}

// ARM 16 — GROUPED FRAMES READ 0/N, AND THAT IS THE TRUTH, NOT A BUG.
//
// Group entities never enter the fact read: the read scope is request.Subjects
// else Cohort.Members, and groups are BUILT AFTER the read off member facts. So
// every `each_group` row finds zero read witnesses.
//
// THIS IS ALSO THE PROHIBITION'S GUARD. An implementation that "repairs" the
// 0/N by projecting member facts onto the group entity reads `2/2 satisfied`
// here and fails. That projection is forbidden: it manufactures a witness for a
// subject no provider was ever asked about.
func TestAGroupedFrameReadsZeroOfItsGroups(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	projects := []SubjectRef{projectRef("project_1"), projectRef("project_2"), projectRef("project_3")}
	teams := []SubjectRef{teamRef("team_a"), teamRef("team_b")}
	cohort := cohortWith(contractsv1.ContextFabricSubjectProject, projects, teams, true)

	memberReq := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)
	groupReq := scopeRequirement(CompletionScopeEachGroup, SubjectTeam, SubjectRoleGroup, CompletionQuantifierAtLeastOne, health)

	// Facts for all three PROJECTS and none for either TEAM -- the shipped
	// architecture's own shape.
	facts := factsFor(projects[0], kindList(health), projects[1], kindList(health), projects[2], kindList(health))

	rows := evaluateCohort(
		[]contractsv1.ContextFabricPlanRequirement{memberReq, groupReq},
		cohort, factCoverage(health, SourceAvailable), facts)

	assertRow(t, rowFor(t, rows, memberReq.Requirement),
		contractsv1.ContextFabricRequirementSatisfied,
		contractsv1.ContextFabricAnswerImpactNone, "", false, 3, 3)
	assertRow(t, rowFor(t, rows, groupReq.Requirement),
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricCoverageDetailFactNarrowed,
		false, 0, 2)
	if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("answer state = %q, want partial", state)
	}
}

// ARM 4 — THE POPULATION-UNKNOWN ARM, asserting distinctness from arm 1 on BOTH
// Outcome and CauseCoverage. Its zero sub-arms are the NOT-ENUMERABLE ones --
// a nil cohort and zero groups -- never a named-slot set at zero, which arm 18
// owns.
func TestAnUnresolvedPopulationIsUnavailableNotNarrowed(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	coverage := factCoverage(health, SourceAvailable)

	t.Run("nil cohort on each_member", func(t *testing.T) {
		t.Parallel()
		requirement := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)
		rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{requirement}, nil, coverage, CanonicalFactBundle{})
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementUnavailable,
			contractsv1.ContextFabricAnswerImpactDimension,
			contractsv1.ContextFabricCoverageDetailReadPopulationUnverified,
			true, 0, 0)
		// Degraded is ABSORBING, and that is the user-visible consequence of
		// this arm -- asserted so the split's cost is pinned, not implied.
		if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessDegraded {
			t.Fatalf("answer state = %q, want degraded", state)
		}
	})

	t.Run("zero groups on each_group", func(t *testing.T) {
		t.Parallel()
		requirement := scopeRequirement(CompletionScopeEachGroup, SubjectTeam, SubjectRoleGroup, CompletionQuantifierAtLeastOne, health)
		cohort := cohortWith(contractsv1.ContextFabricSubjectProject, []SubjectRef{projectRef("project_1")}, nil, true)
		rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{requirement}, cohort, coverage, CanonicalFactBundle{})
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementUnavailable,
			contractsv1.ContextFabricAnswerImpactDimension,
			contractsv1.ContextFabricCoverageDetailReadPopulationUnverified,
			true, 0, 0)
	})

	t.Run("more committed refs than named slots is an ambiguous binding", func(t *testing.T) {
		t.Parallel()
		flow := contractsv1.ContextFabricFactFlow
		requirement := operandRequirement(SubjectTeam, CompletionQuantifierAtLeastOne, flow)
		three := []SubjectRef{teamRef("team_a"), teamRef("team_b"), teamRef("team_c")}
		rows := evaluateOperands(
			[]contractsv1.ContextFabricPlanRequirement{requirement},
			namedOperandFrame(SubjectTeam, SubjectTeam), // TWO slots, THREE committed
			three, factCoverage(flow, SourceAvailable),
			factsFor(three[0], kindList(flow), three[1], kindList(flow), three[2], kindList(flow)))
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementUnavailable,
			contractsv1.ContextFabricAnswerImpactDimension,
			contractsv1.ContextFabricCoverageDetailReadPopulationUnverified,
			true, 0, 0)
	})
}

// ARM 5 — the census exception, exercised through a READ row. The cohort's
// OWNER reports the population incomplete; every enumerated member was read, so
// the counts are EQUAL and the row is legal only through the widened exception.
//
// Includes the Complete=false, Truncated=false shape, which is the one a
// Truncated-only predicate would read as a full census.
func TestAnIncompleteCensusQualifiesTheReadRow(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	members := []SubjectRef{projectRef("project_1"), projectRef("project_2")}
	requirement := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)

	cohort := cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, false) // Complete=false, Truncated=false
	rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{requirement}, cohort,
		factCoverage(health, SourceAvailable),
		factsFor(members[0], kindList(health), members[1], kindList(health)))

	assertRow(t, rowFor(t, rows, requirement.Requirement),
		contractsv1.ContextFabricRequirementNarrowed,
		contractsv1.ContextFabricAnswerImpactScope,
		contractsv1.ContextFabricCoverageDetailPopulationTruncated,
		true, 2, 2)
}

// ARM 15 — SWITCH TOTALITY, over BOTH closed vocabularies.
//
// A fifth completion scope must not silently default to kind-keying, and a
// fourth census member must not default to "enumerated". Both are asserted from
// the vocabularies themselves rather than from a list this test maintains.
func TestEveryPopulationCensusMemberIsHandled(t *testing.T) {
	t.Parallel()
	for _, census := range populationCensusVocabulary() {
		switch census {
		case populationEnumerated, populationIncomplete, populationAbsent:
		default:
			t.Fatalf("census member %q is not handled by the population arms", census)
		}
	}
	// Every completion scope is either distributive or the one documented
	// exception. A new member defaults to DISTRIBUTIVE (fail-closed toward
	// asking who), and this asserts the exception set is exactly one.
	exceptions := 0
	for _, scope := range CompletionScopeVocabulary() {
		if !distributiveScope(string(scope)) {
			exceptions++
			if scope != CompletionScopeSingleSubject {
				t.Fatalf("scope %q is not distributive and is not the documented exception", scope)
			}
		}
	}
	if exceptions != 1 {
		t.Fatalf("exactly one completion scope may be non-distributive, got %d", exceptions)
	}
}

// TestADistributiveRowWithoutPopulationEvidenceReachesTheCallerDefectBranch is
// the arm for the hazard this change created in a NEIGHBOURING test.
//
// WHY IT EXISTS, and it is worth stating because the arm looks defensive rather
// than behavioural. A distributive requirement reaching the evaluator with NO
// population evidence means a finalization path did not thread the bundle --
// a wiring bug in this process, not a fact about the answer. The row is
// therefore dropped rather than routed to the absent arm, which would publish a
// coverage claim about the ANSWER for a defect in the SERVER.
//
// The hazard: because that branch drops the row BEFORE the population arms run,
// any existing test that drives a distributive requirement with empty evidence
// now passes whether or not the logic under it works at all. That is exactly
// how three refusals in TestUnservableAndComputedRequirementsAreNotEvaluated
// were silently hollowed out until its own positive control caught it.
//
// So this arm pins BOTH directions: empty evidence must REACH the branch and
// say so, and the SAME fixture with real evidence must reach the guard and
// produce a row. Without the control, an implementation that dropped every
// distributive row would pass the first half.
func TestADistributiveRowWithoutPopulationEvidenceReachesTheCallerDefectBranch(t *testing.T) {
	t.Parallel()
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)
	coverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	alpha := teamRef("team_alpha")

	// The premise, asserted rather than assumed: this fixture really is
	// distributive. If the scope were single_subject the branch is never
	// reached and this arm would prove nothing.
	if !distributiveScope(requirement.Scope) {
		t.Fatalf("the premise moved: scope %q is no longer distributive", requirement.Scope)
	}

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// EMPTY evidence: Present is false, so the bundle was never threaded.
	rows := appendReadRequirementEvaluations(nil,
		[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage, readPopulationEvidence{})
	if len(rows) != 0 {
		t.Fatalf("a distributive row with no population evidence produced %d rows: %+v -- "+
			"a wiring defect in this process must not publish a coverage claim about the answer",
			len(rows), rows)
	}

	// COUNTED AS SUCH: the drop must SAY it happened, at production level, and
	// must name the requirement and the scope or it cannot drive the fix.
	const line = "context fabric distributive read requirement reached the evaluator with no population evidence"
	if !strings.Contains(logs.String(), line) {
		t.Fatalf("the drop emitted no disclosure; a silently dropped row is a swallowed signal. logs: %s", logs.String())
	}
	// KEY:VALUE in the sink's own encoding, never a bare substring: a
	// substring cannot see a field disappear when its value also occurs
	// elsewhere in the record.
	var record map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(raw, line) {
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				t.Fatalf("the disclosure is not decodable JSON: %v (%s)", err, raw)
			}
		}
	}
	if record["requirement"] != requirement.Requirement {
		t.Fatalf("requirement = %v, want %q -- the line must name what was dropped", record["requirement"], requirement.Requirement)
	}
	if record["scope"] != requirement.Scope {
		t.Fatalf("scope = %v, want %q -- the line must name which scope had no owner", record["scope"], requirement.Scope)
	}

	// THE CONTROL, and it is what stops this arm certifying an implementation
	// that drops every distributive row: the SAME fixture with real evidence
	// reaches the guard and produces a row.
	rows = evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha}, coverage,
		factsFor(alpha, kindList(flow, health)))
	if len(rows) != 1 {
		t.Fatalf("the same fixture with REAL population evidence produced %d rows, want 1 -- "+
			"without this the assertion above passes on an evaluator that drops everything", len(rows))
	}
}

// TestTheDenominatorIsNeverTheReturnedOrInvokedSet (T-DENOM) pins the first
// named residual: the population comes from its OWNER, never from the facts
// that came back.
//
// This is the arm that would fail a "fix" that derived the denominator from
// returned facts -- which would make every distributive requirement
// unfalsifiable, because a read returning nothing would shrink the population
// it failed to cover until it covered it.
func TestTheDenominatorIsNeverTheReturnedOrInvokedSet(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	gamma := teamRef("team_gamma")
	flow := contractsv1.ContextFabricFactFlow
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierAtLeastOne, flow)
	coverage := factCoverage(flow, SourceAvailable)
	frame := namedOperandFrame(SubjectTeam, SubjectTeam)

	t.Run("facts for A only against committed {A,B} declares 2", func(t *testing.T) {
		t.Parallel()
		rows := evaluateOperands([]contractsv1.ContextFabricPlanRequirement{requirement},
			frame, []SubjectRef{alpha, beta}, coverage, factsFor(alpha, kindList(flow)))
		row := rowFor(t, rows, requirement.Requirement)
		if row.Declared != 2 {
			t.Fatalf("Declared = %d, want 2 -- the denominator is the frame's slot count, "+
				"never the number of subjects whose facts came back", row.Declared)
		}
		if row.Served != 1 {
			t.Fatalf("Served = %d, want 1", row.Served)
		}
	})

	t.Run("a third subject's facts are not a witness for a slot the frame never named", func(t *testing.T) {
		t.Parallel()
		// Facts for A, B AND C, but the frame names two slots and only A and
		// B committed. C must not inflate either number.
		rows := evaluateOperands([]contractsv1.ContextFabricPlanRequirement{requirement},
			frame, []SubjectRef{alpha, beta}, coverage,
			factsFor(alpha, kindList(flow), beta, kindList(flow), gamma, kindList(flow)))
		assertRow(t, rowFor(t, rows, requirement.Requirement),
			contractsv1.ContextFabricRequirementSatisfied,
			contractsv1.ContextFabricAnswerImpactNone, "", false, 2, 2)
	})

	t.Run("the bundle's own scope changes nothing", func(t *testing.T) {
		t.Parallel()
		facts := factsFor(alpha, kindList(flow))
		// The bundle's own read SCOPE -- the derived targets a capability was
		// authorized over -- names a wider world here. It is the INVOKED set,
		// not the population, and must not become a denominator.
		facts.Scope = &FactReadScope{
			DerivedSubjects: map[FactKind][]SubjectRef{flow: {alpha, beta, gamma}},
		}
		rows := evaluateOperands([]contractsv1.ContextFabricPlanRequirement{requirement},
			frame, []SubjectRef{alpha, beta}, coverage, facts)
		row := rowFor(t, rows, requirement.Requirement)
		if row.Declared != 2 {
			t.Fatalf("Declared = %d, want 2 -- the INVOKED set is not the population", row.Declared)
		}
	})
}

// TestReadPopulationAgreesWithTheCohortOwnersOwnRule is the CROSS-LAYER
// AGREEMENT test, in BOTH directions.
//
// Direction 1 catches this layer drifting (re-deriving the rule instead of
// calling the owner). It ALSO pins the owner's rule literally against the shape
// table, so a drift on the OWNER's side -- which this layer would silently
// inherit and therefore never notice -- is caught here too.
func TestReadPopulationAgreesWithTheCohortOwnersOwnRule(t *testing.T) {
	t.Parallel()
	members := []SubjectRef{projectRef("project_1"), projectRef("project_2")}
	for _, shape := range []struct {
		name                string
		cohort              *Cohort
		wantCensus          populationCensus
		wantOwnerIncomplete bool
		ownerResolves       bool
	}{
		{"nil cohort", nil, populationAbsent, false, false},
		{"complete", cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, true), populationEnumerated, false, true},
		{"incomplete", cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, false), populationIncomplete, true, true},
		{"truncated", func() *Cohort {
			c := cohortWith(contractsv1.ContextFabricSubjectProject, members, nil, true)
			c.Truncated = true
			return c
		}(), populationIncomplete, true, true},
	} {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			population := cohortMemberPopulation(shape.cohort, nil)
			if population.Census != shape.wantCensus {
				t.Fatalf("census = %q, want %q", population.Census, shape.wantCensus)
			}

			// DIRECTION 1: every field is the OWNER's own.
			cardinality, resolved := ComputeMembershipCardinality(shape.cohort, nil)
			if resolved != shape.ownerResolves {
				t.Fatalf("the owner's second return = %v, want %v", resolved, shape.ownerResolves)
			}
			if !resolved {
				if population.Census != populationAbsent {
					t.Fatalf("the owner reports NO resolved member set and this layer says %q", population.Census)
				}
				return
			}
			if (population.Census == populationIncomplete) != cardinality.PopulationIncomplete {
				t.Fatalf("this layer says census %q while the owner says PopulationIncomplete=%v -- "+
					"one of the two is re-deriving the rule", population.Census, cardinality.PopulationIncomplete)
			}
			if population.Declared != cardinality.Declared {
				t.Fatalf("Declared = %d while the owner says %d", population.Declared, cardinality.Declared)
			}

			// THE OWNER'S RULE, pinned literally against the shape table, so a
			// drift THERE is caught here rather than silently inherited.
			wantIncomplete := !shape.cohort.Complete || shape.cohort.Truncated
			if cardinality.PopulationIncomplete != wantIncomplete {
				t.Fatalf("the OWNER's rule moved: PopulationIncomplete=%v for complete=%v truncated=%v",
					cardinality.PopulationIncomplete, shape.cohort.Complete, shape.cohort.Truncated)
			}
			if wantIncomplete != shape.wantOwnerIncomplete {
				t.Fatalf("the shape table disagrees with the rule for %q", shape.name)
			}
		})
	}
}

// TestOperandPopulationAgreesWithFrameRoleSlots pins the OPERAND owner's count
// against frameRoleSlots.
//
// operandPopulation now CALLS frameRoleSlots rather than mirroring it, so the
// first assertion is close to tautological by construction -- and that is the
// point: it fails loudly if anyone reintroduces a second walk. The assertion
// that carries the real weight is the last one, that two same-kind slots do NOT
// collapse: the coordinate layer dedups them, and counting coordinates instead
// of slots would reproduce this ticket's own defect inside its fix.
func TestOperandPopulationAgreesWithFrameRoleSlots(t *testing.T) {
	t.Parallel()
	frame := namedOperandFrame(SubjectTeam, SubjectTeam, contractsv1.ContextFabricSubjectProject)
	slots := frameRoleSlots(frame.SubjectExpression)

	wantByKind := map[SubjectKind]int{}
	for _, slot := range slots {
		if slot.Role == SubjectRoleOperand {
			wantByKind[slot.Subject]++
		}
	}
	if len(wantByKind) == 0 {
		t.Fatal("the fixture produced no operand slots; this test would prove nothing")
	}
	for kind, want := range wantByKind {
		population := operandPopulation(frame, nil, kind)
		if population.Declared != want {
			t.Fatalf("operandPopulation(%q).Declared = %d while frameRoleSlots names %d slots of that kind -- "+
				"the two walks disagree", kind, population.Declared, want)
		}
	}
	// The dedup the coordinate layer applies must NOT reach the denominator:
	// two team slots collapse to ONE coordinate, and counting coordinates
	// would reproduce the defect inside its own fix.
	if wantByKind[SubjectTeam] != 2 {
		t.Fatalf("the fixture names %d team slots, want 2 -- the point is that they do NOT collapse here",
			wantByKind[SubjectTeam])
	}
}

// TestSubjectOrderNeverChangesTheRow permutes the inputs 200 times.
//
// 200 rather than one: a map-iteration dependency passes a single read about
// half the time, and a determinism claim asserted once is a coin flip recorded
// as a fact. It asserts the CAUSE too, which the worst-state fold could
// otherwise make order-dependent.
func TestSubjectOrderNeverChangesTheRow(t *testing.T) {
	t.Parallel()
	alpha, beta, gamma := teamRef("team_alpha"), teamRef("team_beta"), teamRef("team_gamma")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)
	coverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	frame := namedOperandFrame(SubjectTeam, SubjectTeam, SubjectTeam)

	orders := [][]SubjectRef{
		{alpha, beta, gamma}, {gamma, beta, alpha}, {beta, gamma, alpha}, {beta, alpha, gamma},
	}
	var first RequirementOutcomeRow
	for iteration := 0; iteration < 200; iteration++ {
		committed := orders[iteration%len(orders)]
		facts := CanonicalFactBundle{}
		// Permute the FACT order independently of the committed order.
		for _, subject := range orders[(iteration+1)%len(orders)] {
			if subject == gamma {
				continue // gamma is unread, so the row is a real shortfall
			}
			for _, kind := range []FactKind{health, flow} {
				facts.Facts = append(facts.Facts, CanonicalFact{Kind: kind, Subject: subject, SourceState: SourceAvailable})
			}
		}
		rows := evaluateOperands([]contractsv1.ContextFabricPlanRequirement{requirement},
			frame, committed, coverage, facts)
		row := rowFor(t, rows, requirement.Requirement)
		if iteration == 0 {
			first = row
			continue
		}
		if row.Outcome != first.Outcome || row.Impact != first.Impact ||
			row.CauseCoverage != first.CauseCoverage || row.CauseObserved != first.CauseObserved ||
			row.Served != first.Served || row.Declared != first.Declared {
			t.Fatalf("iteration %d produced {%q %q %q %v %d/%d}, first produced {%q %q %q %v %d/%d} -- "+
				"the row depends on input order", iteration,
				row.Outcome, row.Impact, row.CauseCoverage, row.CauseObserved, row.Served, row.Declared,
				first.Outcome, first.Impact, first.CauseCoverage, first.CauseObserved, first.Served, first.Declared)
		}
	}
	// NON-VACUITY: the fixture must be a real shortfall, or 200 identical
	// `satisfied` rows would pass while proving nothing about ordering.
	if first.Outcome != contractsv1.ContextFabricRequirementNarrowed || first.Served == first.Declared {
		t.Fatalf("the fixture is not a shortfall (%q %d/%d); an order test over a uniform row proves nothing",
			first.Outcome, first.Served, first.Declared)
	}
}

// TestAStaleFactStillReadsTheSubject closes the gap the battery found: no arm
// drove a SUBJECT whose fact came back `stale`.
//
// `stale` is SERVED evidence at the kind level (`:217`), and the per-subject
// test must agree with that classification rather than inventing a stricter
// one -- a second authority in the narrow direction is still a second
// authority. Dropping `SourceStale` from the per-subject served set survived
// every other arm.
func TestAStaleFactStillReadsTheSubject(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)

	// Beta's facts are STALE, alpha's available. Both subjects are read, so
	// the row is satisfied 2/2 -- and reads 1/2 if stale stops counting.
	facts := factsFor(alpha, kindList(flow, health))
	for _, kind := range []FactKind{flow, health} {
		facts.Facts = append(facts.Facts, CanonicalFact{Kind: kind, Subject: beta, SourceState: SourceStale})
	}
	rows := evaluateOperands(
		[]contractsv1.ContextFabricPlanRequirement{requirement},
		namedOperandFrame(SubjectTeam, SubjectTeam),
		[]SubjectRef{alpha, beta},
		// The kind-level coverage says stale, which the kind arms count as
		// served -- so the population layer must agree.
		factCoverage(flow, SourceStale, health, SourceStale),
		facts)
	assertRow(t, rowFor(t, rows, requirement.Requirement),
		contractsv1.ContextFabricRequirementSatisfied,
		contractsv1.ContextFabricAnswerImpactNone, "", false, 2, 2)
}
