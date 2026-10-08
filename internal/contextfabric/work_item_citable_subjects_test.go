package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTupleValidatorAcceptsADriverNamingTheCommittedAnchor(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	anchor := result.SubjectResolution.Committed[0]
	result.Drivers = []DriverJudgment{{DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "t", Summary: "s", AffectedSubjects: []SubjectRef{anchor}, EvidenceRefIDs: []string{}, ClaimedFactIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true}}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("driver naming the committed anchor refused: %v", err)
	}
}

func TestADriverNamingTheCommittedAnchorIsServedNotFailed(t *testing.T) {
	result, err, _ := budgetTrimInvestigate(t, budgetTrimShape{members: 30, claims: 3, maxItems: 30, findings: 1, anchorDriver: true})
	if err != nil {
		t.Fatalf("investigation failed: %v", err)
	}
	if !hasDriverID(result, "driver_anchor01") {
		t.Fatalf("the driver naming the anchor was not served: %+v", result.Drivers)
	}
}

func TestSynthesisAndTupleValidatorShareOneCitableSubjectSet(t *testing.T) {
	result := workItemTuplePayloadFixture(t)
	anchor := result.SubjectResolution.Committed[0]
	member := result.Cohort.Members[0].Subject
	admitted := synthesisSubjects(SynthesisInput{Graph: GraphContext{Resolution: result.SubjectResolution, Cohort: result.Cohort}})
	for _, subject := range []SubjectRef{anchor, member} {
		if _, ok := admitted[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("synthesis does not admit %v", subject)
		}
		probe := result
		probe.Drivers = []DriverJudgment{{DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "t", Summary: "s", AffectedSubjects: []SubjectRef{subject}, EvidenceRefIDs: []string{}, ClaimedFactIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true}}
		if err := ValidateWorkItemTuplePayload(probe, storage.Principal{OrgID: "org-1"}); err != nil {
			t.Fatalf("validator refuses %v that synthesis admits: %v", subject, err)
		}
	}
}

func TestAModelDraftBreakingATupleRuleIsServedDegradedNotFailed(t *testing.T) {
	result, err, _ := budgetTrimInvestigate(t, budgetTrimShape{members: 30, claims: 3, maxItems: 30, findings: 1, foreignDriver: true, degradable: true})
	if err != nil {
		t.Fatalf("investigation failed instead of degrading: %v", err)
	}
	if result.Status != InvestigationDegraded || hasDriverID(result, "driver_foreign01") {
		t.Fatalf("status %q drivers %+v: want degraded without the refused driver", result.Status, result.Drivers)
	}
	if !IsSynthesisModelFailureAnswer(result) {
		t.Fatalf("degraded answer does not carry the closed model-failure warning: %v", result.Warnings)
	}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("degraded answer is itself invalid: %v", err)
	}
}

func TestAServerCausedTupleRuleStaysAHardFailure(t *testing.T) {
	for _, rule := range []WorkItemTupleRule{WorkItemRuleCardinalityClaimNotAnchor, WorkItemRuleCardinalityClaimValue, WorkItemRuleAnchorDisagree, WorkItemRuleMemberIsAnchor, WorkItemRuleCandidateEvidenceOutsideMembers, WorkItemRuleRelationshipPaths, WorkItemRuleEvidenceLabelOutsideMembers} {
		if WorkItemTupleRuleModelCaused(rule) {
			t.Fatalf("%s is server-caused and must not degrade", rule)
		}
	}
	for _, rule := range []WorkItemTupleRule{WorkItemRuleDriverSubjectOutsideMembers, WorkItemRuleFindingEvidenceOutsideMembers, WorkItemRuleStatusClaimOutsideMembers, WorkItemRuleResultEvidenceOutsideMembers} {
		if !WorkItemTupleRuleModelCaused(rule) {
			t.Fatalf("%s is model-caused and must degrade", rule)
		}
	}
}

func hasDriverID(result InvestigationResult, id string) bool {
	for _, driver := range result.Drivers {
		if driver.DriverID == id {
			return true
		}
	}
	return false
}

type fixedDegradedComposer struct {
	synthesizerFunc
	telemetry *recordingTelemetry
}

func (f fixedDegradedComposer) ComposeDegraded(_ context.Context, principal storage.Principal, _ SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	if !failure.DeferEvent {
		f.telemetry.RecordSynthesisModelFailure(context.Background(), principal, SynthesisModelFailureEvent{Class: failure.Class, Rule: failure.Rule, Stage: failure.Stage})
	}
	return InvestigationResult{Status: InvestigationDegraded, Coverage: Coverage{Partial: true, Sources: []SourceObservation{}, DegradedReasons: []string{}}, DirectJudgment: "degraded", CurrentState: synthesisFailureCurrentState, DeterministicAnswer: "degraded", StrongestPressures: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{synthesisFailureWarning(failure.Class)}}, nil
}

func validationStageFixture(t *testing.T) (*Engine, *recordingTelemetry, InvestigationResult, *workItemDegradeBasis) {
	t.Helper()
	result := workItemTuplePayloadFixture(t)
	result.Drivers = []DriverJudgment{{DriverID: "driver_foreign01", Standing: DriverPrincipal, Category: "status", Title: "t", Summary: "s", AffectedSubjects: []SubjectRef{{Kind: SubjectWorkItem, CanonicalID: "work-item-outside-list", Label: "Outside"}}, EvidenceRefIDs: []string{}, ClaimedFactIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true}}
	result.StrongestPressures = []string{"model pressure"}
	result.DirectJudgment, result.CurrentState, result.DeterministicAnswer = "model prose", "model prose", "model prose"
	result.Limitations = []string{"model caveat", "server caveat"}
	telemetry := &recordingTelemetry{}
	engine := &Engine{synthesizer: fixedDegradedComposer{telemetry: telemetry}, telemetry: telemetry}
	return engine, telemetry, result, &workItemDegradeBasis{DraftLimitations: []string{"model caveat"}, DraftWarnings: result.Warnings}
}

func TestTheValidationStageDegradeServesTheComposedFactsOnlyAnswer(t *testing.T) {
	engine, telemetry, result, basis := validationStageFixture(t)
	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)
	if !ok {
		t.Fatal("not degraded")
	}
	if len(out.StrongestPressures) != 0 || out.DirectJudgment != "degraded" || out.DeterministicAnswer != "degraded" || out.CurrentState != synthesisFailureCurrentState {
		t.Fatalf("model-authored text survived: pressures %v judgment %q state %q", out.StrongestPressures, out.DirectJudgment, out.CurrentState)
	}
	if !slices.Equal(out.Limitations, []string{"server caveat"}) {
		t.Fatalf("limitations %v: want the server's only", out.Limitations)
	}
	if len(telemetry.synthesisModelFailures) != 1 || telemetry.synthesisModelFailures[0].Rule != string(WorkItemRuleDriverSubjectOutsideMembers) || telemetry.synthesisModelFailures[0].Stage != "validation" {
		t.Fatalf("validation-stage event %+v", telemetry.synthesisModelFailures)
	}
}

func TestTheValidationStageDegradeKeepsTheServerDisclosuresAndMarksCoveragePartial(t *testing.T) {
	engine, telemetry, result, basis := validationStageFixture(t)
	result.Limitations = []string{"model caveat", contractsv1.ContextFabricSynthesisInputBoundedLimitation}
	result.Coverage.Partial = false
	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)
	if !ok {
		t.Fatal("not degraded")
	}
	if !slices.Equal(out.Limitations, []string{contractsv1.ContextFabricSynthesisInputBoundedLimitation}) {
		t.Fatalf("limitations %v: want the bounded-input disclosure kept and the model caveat removed", out.Limitations)
	}
	if !out.Coverage.Partial {
		t.Fatal("coverage.partial not carried from the composed degraded answer")
	}
	if len(telemetry.synthesisModelFailures) != 1 {
		t.Fatalf("events %+v: want exactly one, recorded by the caller after the answer was accepted", telemetry.synthesisModelFailures)
	}
}

func TestEveryTupleRuleIsClassifiedAsTheOracleSays(t *testing.T) {
	modelCaused := map[WorkItemTupleRule]bool{}
	for _, rule := range []string{"result_evidence_outside_members", "claim_without_id", "claim_repeated", "status_claim_not_work_item", "work_claim_not_work_item", "status_claim_outside_members", "work_claim_outside_members", "status_claim_table_data", "work_claim_table_data", "claim_kind_unsupported", "finding_no_member_subject", "driver_no_member_subject", "finding_subject_outside_members", "driver_subject_outside_members", "finding_evidence_outside_members", "driver_evidence_outside_members", "finding_claim_outside_members", "driver_claim_outside_members"} {
		modelCaused[WorkItemTupleRule(rule)] = true
	}
	rules := WorkItemTupleRules()
	if len(rules) != 41 {
		t.Fatalf("%d rules: a new rule needs a classification here", len(rules))
	}
	for _, rule := range rules {
		if WorkItemTupleRuleModelCaused(rule) != modelCaused[rule] {
			t.Fatalf("%s: model-caused=%v, oracle says %v", rule, WorkItemTupleRuleModelCaused(rule), modelCaused[rule])
		}
	}
}

func TestTheValidationStageDegradeKeepsWithinTheWarningCap(t *testing.T) {
	engine, _, result, basis := validationStageFixture(t)
	result.Warnings = make([]string, contractsv1.ContextFabricWarningsMaxCount)
	for i := range result.Warnings {
		result.Warnings[i] = fmt.Sprintf("model warning %03d", i)
	}
	basis.DraftWarnings = slices.Clone(result.Warnings)
	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)
	if !ok || len(out.Warnings) != 1 || out.Warnings[0] != synthesisFailureWarning(SynthesisFailureRejected) {
		t.Fatalf("ok=%v warnings=%d %v: want the draft's warnings replaced by the one composed warning", ok, len(out.Warnings), out.Warnings)
	}
	for i := range result.Warnings {
		result.Warnings[i] = fmt.Sprintf("server warning %03d", i)
	}
	basis.DraftWarnings = nil
	out, ok = engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)
	if !ok || len(out.Warnings) > contractsv1.ContextFabricWarningsMaxCount || out.Warnings[0] != synthesisFailureWarning(SynthesisFailureRejected) {
		t.Fatalf("ok=%v warnings=%d: cap broken or degraded warning not first", ok, len(out.Warnings))
	}
}

func TestASuppliedSynthesisIsNeverDegradedAtValidation(t *testing.T) {
	engine, telemetry, result, _ := validationStageFixture(t)
	if _, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, nil); ok || len(telemetry.synthesisModelFailures) != 0 {
		t.Fatal("a supplied synthesis (no degrade basis) was degraded")
	}
}

func TestAServerCausedBreachIsNotDegradedAtValidation(t *testing.T) {
	engine, telemetry, result, basis := validationStageFixture(t)
	result.Drivers = nil
	result.SubjectResolution.Candidates[0].EvidenceRefIDs = []string{"evidence-outside-members"}
	if _, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis); ok || len(telemetry.synthesisModelFailures) != 0 {
		t.Fatal("a server-caused rule was degraded")
	}
}

func TestTheSynthesisStageDegradeNamesItsRuleAndStage(t *testing.T) {
	shape := budgetTrimShape{members: 30, claims: 3, maxItems: 30, findings: 1, foreignDriver: true, degradable: true, telemetry: &recordingTelemetry{}}
	if _, err, _ := budgetTrimInvestigate(t, shape); err != nil {
		t.Fatal(err)
	}
	events := shape.telemetry.synthesisModelFailures
	if len(events) != 1 || events[0].Rule != string(WorkItemRuleDriverSubjectOutsideMembers) || events[0].Stage != "synthesis" {
		t.Fatalf("synthesis-stage events %+v: want one with the rule and stage synthesis", events)
	}
}

func TestTheRealComposerCarriesTheRuleAndStageIntoItsEvent(t *testing.T) {
	telemetry := &recordingTelemetry{}
	input := largeSynthesisInputFixture(3)
	synthesizer := RuntimeAnswerSynthesizer{Telemetry: telemetry}
	if _, err := synthesizer.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, input, workItemModelBreachFailure(WorkItemRuleDriverSubjectOutsideMembers, "validation", false)); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.synthesisModelFailures) != 1 || telemetry.synthesisModelFailures[0].Rule != "driver_subject_outside_members" || telemetry.synthesisModelFailures[0].Stage != "validation" {
		t.Fatalf("events %+v", telemetry.synthesisModelFailures)
	}
}

func TestTheDegradeBasisKeepsTheServiceAuthoredDisclosures(t *testing.T) {
	draft := InvestigationResult{Limitations: []string{"model caveat", contractsv1.ContextFabricSynthesisInputBoundedLimitation}, Warnings: []string{"model warning"}}
	basis := newWorkItemDegradeBasis(SynthesisInput{}, draft)
	if !slices.Equal(basis.DraftLimitations, []string{"model caveat"}) || !slices.Equal(basis.DraftWarnings, []string{"model warning"}) {
		t.Fatalf("basis %+v: only the model's own limitation may be marked removable", basis)
	}
}

func TestADeferredEventIsLeftToTheCaller(t *testing.T) {
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{Telemetry: telemetry}
	failure := workItemModelBreachFailure(WorkItemRuleDriverSubjectOutsideMembers, "validation", false)
	failure.DeferEvent = true
	if _, err := synthesizer.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(3), failure); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.synthesisModelFailures) != 0 {
		t.Fatalf("composer recorded %+v for a deferred event", telemetry.synthesisModelFailures)
	}
}
