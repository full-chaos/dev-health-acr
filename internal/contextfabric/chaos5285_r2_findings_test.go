package contextfabric

import (
	"context"
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file holds the pins for the second-round review findings on the group
// read. Each was reproduced RED at 94297a8a before any fix was written; every
// pin asserting a fix has a control that passes on both sides.

// interpretWithHints is interpretThroughTheBoundary with the receipt's
// UNRECOGNIZED group-hint flag settable too, so the sweep can cover a hint the
// sanitizer dropped as out of vocabulary.
func interpretWithHints(t *testing.T, groupHint SubjectKind, groupHintUnrecognized bool, frame QuestionFrame) (map[string]any, ModelExecutionReceipt) {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = groupHint
	receipt.GroupKindUnrecognized = groupHintUnrecognized
	receipt.QuestionFrame = &frame
	sink := &fakeReceiptSink{}
	interpreter := RuntimeQuestionInterpreter{
		Runtime:        fakeModelRuntime{interpreted: groupedInterpretation(), receipt: receipt},
		Sink:           sink,
		FrameTelemetry: logs.telemetry,
	}
	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	lines := linesWithMessage(t, logs.configured.String(), frameValidationMessage)
	if len(lines) != 1 {
		t.Fatalf("frame-validation lines = %d, want 1", len(lines))
	}
	var recorded ModelExecutionReceipt
	if len(sink.recorded) > 0 {
		recorded = sink.recorded[len(sink.recorded)-1]
	}
	return lines[0], recorded
}

// TestEveryFrameKindByRequestedGroupHintIsKeptOrRefused is r2 finding P1-1,
// swept over its whole domain.
//
// The ruling is that the interpreter keeps the requested axis and I6 refuses
// it with a basis. A model whose own hint asked for a grouping, but whose frame
// expresses no group axis at all, has DROPPED that axis -- and the gate used to
// pass that frame, so the turn answered a flat question nobody asked with
// nothing but a trace token (`dropped_at_interpretation`) saying so. Every
// frame kind is crossed with every hint state here: a hint recognised by the
// vocabulary, a hint the sanitizer dropped as unrecognised, and no hint.
//
// NOT t.Parallel(): it installs the process default logger.
func TestEveryFrameKindByRequestedGroupHintIsKeptOrRefused(t *testing.T) {
	project, team := contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam
	explicit := SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
		{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(team)}},
		{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"b"}, ExpectedKind: kindPointer(team)}},
	}}}
	frames := []struct {
		name       string
		expression SubjectExpression
		grouped    bool
		self       bool
	}{
		{"named_subject", namedExpression(project), false, false},
		{"discovered_kind", discoveredExpression(team), false, false},
		{"children_of_scope", scopedExpression(project), false, false},
		{"organization_scope", orgExpression(nil), false, false},
		{"explicit_set", explicit, false, false},
		{"grouped_members (projects by team)", groupedExpression(project, team), true, false},
		{"grouped_members (teams by team)", groupedExpression(team, team), true, true},
	}
	hints := []struct {
		name         string
		kind         SubjectKind
		unrecognized bool
		requested    bool
	}{
		{"hint absent", "", false, false},
		{"hint team", team, false, true},
		{"hint unrecognized", "", true, true},
	}
	for _, frame := range frames {
		for _, hint := range hints {
			t.Run(frame.name+"/"+hint.name, func(t *testing.T) {
				line, _ := interpretWithHints(t, hint.kind, hint.unrecognized, boundaryFrame(frame.expression))
				t.Logf("frame=%s hint=%s -> outcome=%v failed_invariant=%v failure_detail=%v frame_gate=%v group_axis=%v",
					frame.name, hint.name, line["outcome"], line["failed_invariant"], line["failure_detail"], line["frame_gate"], line["group_axis"])
				switch {
				case frame.self:
					if line["frame_gate"] != "rejected:i6" || line["failure_detail"] != string(FrameFailureGroupEqualsMember) || line["group_axis"] != "refused" {
						t.Errorf("a self-group must be refused under i6 whatever the hint; got gate=%v detail=%v axis=%v", line["frame_gate"], line["failure_detail"], line["group_axis"])
					}
				case frame.grouped:
					if line["frame_gate"] != string(FrameGatePassed) || line["group_axis"] != "kept" {
						t.Errorf("a legal grouping must be kept whatever the hint; got gate=%v axis=%v", line["frame_gate"], line["group_axis"])
					}
				case hint.requested:
					if line["frame_gate"] != "rejected:i6" || line["failed_invariant"] != "i6" || line["group_axis"] != "refused" {
						t.Errorf("a frame that DROPPED the requested group axis passed the gate: gate=%v invariant=%v axis=%v -- the interpreter keeps the requested axis or i6 refuses it; answering flat is neither", line["frame_gate"], line["failed_invariant"], line["group_axis"])
					}
					if line["outcome"] != string(FrameValidationOutcomeRefusedInvalid) {
						t.Errorf("outcome = %v, want %q", line["outcome"], FrameValidationOutcomeRefusedInvalid)
					}
				default:
					if line["group_axis"] != "not_requested" {
						t.Errorf("no hint and no grouping must read not_requested; got %v", line["group_axis"])
					}
					if line["failed_invariant"] == "i6" {
						t.Errorf("a flat question with no requested axis must never be refused under i6")
					}
				}
			})
		}
	}
}

// TestAPlanSeamI6RefusalServesTheRequestedAxis is r2 finding P1-2.
//
// The plan-seam refusal names the invariant, but then cleared the plan's group
// kind before building the terminal result, so the SERVED plan read
// `group_kind=""` beside `refusal_basis=frame_invariant_violated`: a refusal
// for an axis the document says was never asked for.
func TestAPlanSeamI6RefusalServesTheRequestedAxis(t *testing.T) {
	t.Parallel()
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixtureSelfGroup(t, &recordingTelemetry{}, recorder)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	t.Logf("refusal_basis=%q plan group_kind=%q member_kind=%q", result.RefusalBasis, result.AnswerPlan.GroupKind, result.AnswerPlan.MemberKind)
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("refusal basis = %q -- the fixture must reach the plan-seam refusal", result.RefusalBasis)
	}
	if result.AnswerPlan.GroupKind != SubjectTeam {
		t.Errorf("served plan group_kind = %q, want %q -- the document refuses an axis, so it must carry the axis it refuses", result.AnswerPlan.GroupKind, SubjectTeam)
	}
}

// servedGroupReadCase is one cause for which the served document must say
// that a proposed group was not read.
type servedGroupReadCase struct {
	name  string
	want  string // the exact limitation the served document must carry
	build func(t *testing.T) (*Engine, InvestigationRequest)
}

func servedGroupReadCases() []servedGroupReadCase {
	twoMembers := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	return []servedGroupReadCase{
		{"denied (one of two groups)", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixtureDenying(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"), TeamCanonicalID("team_platform"))
		}},
		{"missing (admitted, no facts returned)", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixture(t, &recordingTelemetry{}, groupReadServing("team_security"))
		}},
		{"no group admitted", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixtureDenying(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"), TeamCanonicalID("team_security"), TeamCanonicalID("team_platform"))
		}},
		{"over the contract bound", contractsv1.ContextFabricGroupListOverBoundLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
				bundle := emptyFactBundle()
				bundle.Facts = groupReadOverBoundMemberFacts()
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
				return bundle
			}}
			return groupReadEngineFixtureOverBound(t, &recordingTelemetry{}, recorder)
		}},
		{"read failed", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixtureFull(t, &recordingTelemetry{}, &groupReadFailingReader{inner: groupReadServing()}, twoMembers, nil, SubjectProject, nil, nil)
		}},
		{"authorization unavailable", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"), twoMembers, nil, SubjectProject, nil, nil,
				func(config *groupReadFixtureConfig) {
					config.graph.authorizationErr = fmt.Errorf("injected: authorizer unavailable")
				})
		}},
		{"metadata conflict", contractsv1.ContextFabricGroupReadUnreadLimitation(contractsv1.ContextFabricSubjectTeam), func(t *testing.T) (*Engine, InvestigationRequest) {
			return groupReadEngineFixture(t, &recordingTelemetry{}, metadataConflictRecorder("health-v2"))
		}},
	}
}

// groupCompleteness snapshots every served group's own completeness, which
// the disclosure must never touch: group.complete is MEMBERSHIP completeness,
// and an unread group is a read state, not a membership one.
func groupCompleteness(result InvestigationResult) map[string]bool {
	out := map[string]bool{}
	if result.Cohort == nil {
		return out
	}
	for _, group := range result.Cohort.Groups {
		out[group.Subject.CanonicalID] = group.Complete
	}
	return out
}

func servedServiceAuthoredLimitations(result InvestigationResult) []string {
	var out []string
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
			out = append(out, limitation)
		}
	}
	return out
}

// TestAnUnreadGroupIsDisclosedOnTheServedDocument is r2 finding P1-3.
//
// Every cause for which a proposed group was not read reached the TRACE and
// nothing else: the served document said `partial=false`, carried no
// limitation, and a reader could not tell a fully read grouped answer from one
// whose group axis was never read at all. Each cause here must mark the
// document partial and carry ONE service-authored limitation naming it, and
// must leave every group's own (membership) completeness exactly as it was.
func TestAnUnreadGroupIsDisclosedOnTheServedDocument(t *testing.T) {
	t.Parallel()
	for _, row := range servedGroupReadCases() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			engine, request := row.build(t)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			service := servedServiceAuthoredLimitations(result)
			t.Logf("%s: partial=%v limitations=%q service_authored=%d groups=%v", row.name, result.Coverage.Partial, result.Limitations, len(service), groupCompleteness(result))
			if !result.Coverage.Partial {
				t.Errorf("coverage.partial = false on a turn whose group axis was not fully read -- the document reads as a complete grouped answer")
			}
			disclosures := groupReadDisclosures(result.Limitations)
			if len(disclosures) != 1 || disclosures[0] != row.want {
				t.Errorf("group-read disclosures = %q, want exactly [%q]", disclosures, row.want)
			}
			if !contractsv1.IsContextFabricServiceAuthoredLimitation(row.want) {
				t.Errorf("the disclosure is not service-authored, so a later composer may displace it")
			}
			// group.complete is MEMBERSHIP completeness and must be untouched:
			// every fixture here discovered its whole cohort.
			for id, complete := range groupCompleteness(result) {
				if !complete {
					t.Errorf("group %s served complete=false -- the disclosure repurposed membership completeness as a read state", id)
				}
			}
		})
	}
}

// TestAFullyReadGroupAxisCarriesNoGroupReadDisclosure is the CONTROL for
// P1-3, and it passes on both sides: both groups read, nothing to disclose.
func TestAFullyReadGroupAxisCarriesNoGroupReadDisclosure(t *testing.T) {
	t.Parallel()
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	if hasGroupReadDisclosure(result.Limitations) {
		t.Fatalf("CONTROL BROKEN: a fully read group axis carries a group-read disclosure: %q", result.Limitations)
	}
}

// partialFailingReader answers the member read and returns a PARTIAL bundle
// WITH an error for the group read -- a scope decision and a coverage
// observation included -- which is what ReadFacts does on a failure after it
// resolved scope.
type partialFailingReader struct{ inner *groupReadRecorder }

func (r *partialFailingReader) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	for _, subject := range request.Subjects {
		if subject.Kind == SubjectTeam {
			r.inner.requests = append(r.inner.requests, request)
			bundle := emptyFactBundle()
			bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceUnavailable, Reason: "provider failed"}}
			bundle.Scope = &FactReadScope{Events: []FactScopeExpansionEvent{{RequirementKind: FactHealth, OriginKind: SubjectTeam, Outcome: FactScopeFailed}}}
			return bundle, errGroupReadInjected
		}
	}
	return r.inner.ReadFacts(ctx, principal, request)
}

// TestAFailedGroupReadStillReportsItsScopeAndCoverage is r2 finding P1-4.
//
// A group read that resolved its scope and then failed returns the partial
// bundle alongside the error. The engine emitted the group read's scope
// decisions and its pre-fold coverage states only when the read succeeded, so
// the turn whose group read an operator most needs to diagnose left no trace
// of what that read decided.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAFailedGroupReadStillReportsItsScopeAndCoverage(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := groupReadServing()
	twoMembers := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, &partialFailingReader{inner: recorder}, twoMembers, nil, SubjectProject, nil, nil)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	raw := logs.configured.String()
	groupStates := 0
	for _, line := range linesWithMessage(t, raw, "context fabric group read coverage state") {
		if line["read"] == string(GroupReadArmGroup) {
			groupStates++
		}
	}
	// A scope decision is emitted as an Info outcome line, or at Warn as a
	// gap when the expansion left one; a failed expansion is the latter.
	groupScopes := 0
	for _, message := range []string{"context fabric fact scope expansion outcome", "context fabric fact scope expansion left a gap"} {
		for _, line := range linesWithMessage(t, raw, message) {
			if line["origin_kind"] == string(SubjectTeam) {
				groupScopes++
			}
		}
	}
	line := cohortGroupReadLine(t, logs)
	t.Logf("group read refusal=%v issued=%v group coverage-state lines=%d group scope lines=%d", line["group_read_refusal"], line["group_read_issued"], groupStates, groupScopes)
	if line["group_read_refusal"] != string(GroupReadRefusalReadFailed) {
		t.Fatalf("refusal = %v -- the fixture must reach the read_failed exit", line["group_read_refusal"])
	}
	if groupStates == 0 {
		t.Errorf("no pre-fold coverage-state line for the group read that failed -- its coverage observation is on the bundle and never reached the trace")
	}
	if groupScopes == 0 {
		t.Errorf("no scope-expansion line for the group read that failed -- its scope decision is on the bundle and never reached the trace")
	}
}

// TestGroupFactsReturnedIsWhatTheProviderSent is r2 finding P1-5.
//
// `group_facts_returned` is documented as what the provider sent, beside
// `group_facts_unadmitted_dropped`. It was counted AFTER the unadmitted filter,
// so a provider that answered three facts, two about subjects this turn never
// admitted, read `returned=1 dropped=2`: two numbers that should sum to the
// provider's answer and instead counted the drop twice.
//
// NOT t.Parallel(): it installs the process default logger.
func TestGroupFactsReturnedIsWhatTheProviderSent(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{
					groupKindFact("team_security", FactHealth),
					groupKindFact("team_never_admitted_1", FactHealth),
					groupKindFact("team_never_admitted_2", FactHealth),
				}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, logs.telemetry, recorder)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	t.Logf("returned=%v unadmitted_dropped=%v merged=%v", line["group_facts_returned"], line["group_facts_unadmitted_dropped"], line["group_facts_merged"])
	if line["group_facts_unadmitted_dropped"] != float64(2) {
		t.Fatalf("unadmitted_dropped = %v -- the fixture must drop two facts", line["group_facts_unadmitted_dropped"])
	}
	if line["group_facts_returned"] != float64(3) {
		t.Errorf("group_facts_returned = %v, want 3 -- the provider sent three facts; the two dropped ones are counted in their own field", line["group_facts_returned"])
	}
	if line["group_facts_merged"] != float64(1) {
		t.Errorf("group_facts_merged = %v, want 1", line["group_facts_merged"])
	}
}

// hasGroupReadDisclosure reports whether the served limitations carry either
// group-read disclosure, by the contract's own recognisers.
func hasGroupReadDisclosure(limitations []string) bool {
	return len(groupReadDisclosures(limitations)) > 0
}

// groupReadDisclosures returns the served limitations either group-read
// recogniser accepts.
func groupReadDisclosures(limitations []string) []string {
	var out []string
	for _, limitation := range limitations {
		if contractsv1.IsContextFabricGroupReadUnreadLimitation(limitation) || contractsv1.IsContextFabricGroupListOverBoundLimitation(limitation) {
			out = append(out, limitation)
		}
	}
	return out
}

// droppedAxisInterpreter reports what resolveFrame now decides for a model
// whose hint asked for a grouping by `groupKind` and whose frame is flat: a
// grouped family, the flat frame, and a gate refusing it under i6.
type droppedAxisInterpreter struct {
	interpretation InterpretedQuestion
	groupKind      SubjectKind
}

func (i droppedAxisInterpreter) Interpret(_ context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	refused := FrameValidationResult{
		Outcome: FrameValidationOutcomeRefusedInvalid,
		Failure: FrameValidationFailure{Invariant: FrameInvariantI6, Phase: FrameValidationPhaseA1, Detail: FrameFailureGroupAxisNotExpressed},
	}
	return i.interpretation, QuestionFamilyOutcome{
		Family:        QuestionFamilyGroupedCohortStatus,
		Source:        QuestionFamilySourceModel,
		WinningSample: FamilySample{GroupKind: i.groupKind},
		Gate:          DecideFrameGate(refused, true),
	}, nil
}

// TestEveryI6RefusalServesTheRequestedAxis is the SWEEP behind P1-2: every
// seam that refuses under i6 serves the axis it refused, so no served document
// reads `refusal_basis=frame_invariant_violated` beside an empty group kind.
func TestEveryI6RefusalServesTheRequestedAxis(t *testing.T) {
	t.Parallel()
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "project_status_by_group",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactMetrics}},
	}
	twoMembers := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	for name, interpreter := range map[string]QuestionInterpreter{
		"frame seam: a self-group":             groupReadFramedInterpreter{interpretation: interpretation, groupKind: SubjectTeam, memberKind: SubjectTeam},
		"frame seam: a dropped requested axis": droppedAxisInterpreter{interpretation: interpretation, groupKind: SubjectTeam},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			engine, request := groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, groupReadServing("team_security", "team_platform"), twoMembers, nil, SubjectProject, nil, nil,
				func(config *groupReadFixtureConfig) { config.interpreter = interpreter })
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			groupKind := SubjectKind("<no plan>")
			if result.AnswerPlan != nil {
				groupKind = result.AnswerPlan.GroupKind
			}
			t.Logf("%s: refusal_basis=%q plan group_kind=%q", name, result.RefusalBasis, groupKind)
			if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
				t.Fatalf("refusal basis = %q -- the fixture must reach an i6 refusal", result.RefusalBasis)
			}
			if groupKind != SubjectTeam {
				t.Errorf("served plan group_kind = %q, want %q", groupKind, SubjectTeam)
			}
		})
	}
}
