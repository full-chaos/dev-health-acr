package contextfabric

// The `count` obligation's server step, wired.
//
// WHAT THESE TESTS ARE FOR. §13.2.3 names `membership_cardinality` as the
// server step that satisfies `count`, and until this change NOTHING executed
// it: the number reached the reader only through the model narrating over
// whatever facts the plan happened to read. The shadow said so in as many
// words ("a cardinality is carried in the answer text, not in a countable
// field"), and the six-authority parity proof had to treat five cells as
// blocking on the strength of it.
//
// EVERY TEST HERE DRIVES THE PUBLIC ENTRY POINT OR A PRODUCTION FUNCTION.
// None constructs the outcome row it asserts on -- that is the vacuity this
// package has paid for repeatedly (a regression test that builds the
// decision it asserts on stays green when the production bug returns). The
// shadow tests are the exception and are honest about it: there the shadow
// IS the unit under test and the row is its input.

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// countingFrame is the C5 shape from the recorded corpus -- a scoped member
// set, counted -- built THROUGH DeriveFrameObligations, never by hand-typing
// an obligation list, so a change to §13.2.3's tables moves this with it.
func countingFrame(memberKind SubjectKind) *QuestionFrame {
	frame := frameWith(
		[]InvestigationGoal{GoalCountOrAggregate},
		scopedExpression(memberKind),
		TemporalIntentCurrent,
		nil,
	)
	return &frame
}

// registryDeriver derives requirement rows through the PRODUCTION
// derivation, exactly as FactCapabilityRegistry.DeriveRequirements does.
//
// It carries no capabilities, and that is sound for these tests rather than
// a shortcut: deriveRequirement's computed branch returns before it consults
// a capability at all, so the `count` row it produces is byte-identical to
// the one the live registry produces. The read rows come back unavailable,
// which is honest for a registry with no producers and is not what any
// assertion here reads.
type registryDeriver struct {
	capabilities []FactCapability
}

func (d registryDeriver) DeriveRequirements(frame QuestionFrame) []DerivedRequirement {
	return DeriveRequirements(frame, GenerateObligationSeed(d.capabilities), d.capabilities)
}

// countOutcomeRows returns every outcome row on the result whose obligation
// is `count`, at the given stage.
//
// It reads the SERVED document and nothing else. A test that reached into
// the engine for the number would be measuring the engine's opinion of what
// it sent rather than what it sent.
func countOutcomeRows(result InvestigationResult, stage contractsv1.ContextFabricOutcomeStage) []RequirementOutcomeRow {
	var found []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Obligation == string(ObligationCount) && row.Stage == stage {
			found = append(found, row)
		}
	}
	return found
}

// countingCohort builds a member set of the requested size.
func countingCohort(kind SubjectKind, size int) *Cohort {
	members := make([]CohortMember, 0, size)
	for index := 0; index < size; index++ {
		members = append(members, CohortMember{
			Subject: SubjectRef{
				Kind:        kind,
				CanonicalID: "team:COUNTED_" + string(rune('A'+index)),
				Label:       "Counted " + string(rune('A'+index)),
			},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	return &Cohort{Kind: kind, Rationale: "scope census match", Members: members, Complete: true}
}

// runCountingInvestigation drives Engine.Investigate end to end for a
// counting question over a resolved member set, and returns the SERVED
// result.
//
// maxMembers is threaded onto the request so a caller can force the engine's
// own narrowing to run; zero means "wide enough that nothing narrows".
func runCountingInvestigation(t *testing.T, cohortSize int, maxMembers int) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	cohort := countingCohort(SubjectTeam, cohortSize)
	engine := newCountingEngine(t, cohort, frame, telemetry)
	return runCountingRequest(t, engine, maxMembers), telemetry
}

// newCountingEngine builds the engine the counting fixtures drive. It is
// factored out so a test can call finalizeResult DIRECTLY -- the re-entry
// guard is about calling it twice, which no end-to-end drive can express.
func newCountingEngine(t *testing.T, cohort *Cohort, frame *QuestionFrame, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}

	graph := graphReaderStub{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{},
			Committed:  []SubjectRef{anchor},
		},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	interpreted := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "count",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: interpreted,
			outcome: QuestionFamilyOutcome{
				Frame:            frame,
				FrameObligations: frame.Obligations,
				Family:           QuestionFamilyScopedCohortStatus,
				Source:           QuestionFamilySourceModel,
			},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Counted.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Counted.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_50210001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// runCountingRequest drives one counting investigation and asserts the
// fixture reached a complete answer.
func runCountingRequest(t *testing.T, engine *Engine, maxMembers int) InvestigationResult {
	t.Helper()
	// A cohort investigation needs a CONFIRMED window before it reaches
	// fact-read/assembly (CHAOS-3900/CHAOS-4040). An unconfirmed window is a
	// legitimate reason to stop short, and is not what these tests are
	// about -- the status guard below is what caught this fixture reaching
	// `clarification_required` and reddening for the wrong reason.
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_50210001"
	request.Question = "count question"
	if maxMembers > 0 {
		request.Options.MaxCohortMembers = maxMembers
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Investigate() status = %q, want %q -- this fixture never reached assembly, so it proves nothing",
			result.Status, InvestigationComplete)
	}
	return result
}

// TestTheCountObligationReachesTheServedDocumentAsACountableField is THE
// wiring pin, and it carries the harm.
//
// It drives the public entry point with a frame that DERIVES `count` over a
// resolved member set of a known size, and reads the served document for a
// countable answer. At the parent it fails because nothing executes the
// step: the number exists only in prose the model was free to write or not.
//
// It also binds the DECLARATION to the behaviour in the same test. A
// declaration saying `server_executed` beside a tree that executes nothing is
// exactly the defect the amendment's own adversarial round found, and
// asserting the two separately is what would let them drift apart again.
func TestTheCountObligationReachesTheServedDocumentAsACountableField(t *testing.T) {
	t.Parallel()
	const members = 3
	result, _ := runCountingInvestigation(t, members, 0)

	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) == 0 {
		t.Fatal("the served document carries NO assembled-result outcome row for the `count` obligation: " +
			"the frame demanded a cardinality and the answer carries no countable field, so the number " +
			"reaches the reader only as narrated prose")
	}
	if len(rows) != 1 {
		t.Fatalf("the served document carries %d assembled-result `count` rows, want exactly 1 -- "+
			"two rows for one requirement give a reader two answers to one question", len(rows))
	}
	row := rows[0]
	if row.Served != members || row.Declared != members {
		t.Fatalf("count row served/declared = %d/%d, want %d/%d -- the cardinality of the resolved member set",
			row.Served, row.Declared, members, members)
	}
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count row outcome = %q, want %q: nothing narrowed this member set, so the count is exact over it",
			row.Outcome, contractsv1.ContextFabricRequirementSatisfied)
	}
	// The mirror of the narrowed case: a lossless row names no cause, so it
	// must not claim an observed one. The validator enforces the pairing;
	// asserting it here is what makes the builder's OWN choice covered
	// rather than only the validator's reaction to it.
	if row.CauseObserved {
		t.Fatal("an exact count claims an observed cause while naming none")
	}
	// The identity's third segment is the counted subject kind. A count
	// whose reader cannot tell WHAT was counted is not an answer.
	wantIdentity := string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam)
	if row.Requirement != wantIdentity {
		t.Fatalf("count row requirement = %q, want %q", row.Requirement, wantIdentity)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the count row this engine served does not validate: %v", err)
	}

	// The declaration and the behaviour, asserted together.
	inputs, declared := InputsForComputedStep(ComputedStepMembershipCardinality)
	if !declared {
		t.Fatal("membership_cardinality has no input declaration")
	}
	if inputs.Execution != ComputedStepServerExecuted {
		t.Fatalf("membership_cardinality declares Execution %q while the server just executed it and served the result: "+
			"a declaration that disagrees with the tree is what let an unexecuted step authorize a retirement",
			inputs.Execution)
	}
}

// TestANarrowedMemberSetIsCountedAsALowerBound is the honesty half.
//
// A count served over a set the engine narrowed is NOT the cardinality the
// question asked for, and reporting it as exact would be a confident wrong
// answer -- strictly worse than the narration it replaces. The row must say
// `narrowed`, must serve fewer than it declared, and must name the mechanism
// that cut it from a vocabulary that already ships.
func TestANarrowedMemberSetIsCountedAsALowerBound(t *testing.T) {
	t.Parallel()
	const discovered = 8
	const ceiling = 3
	result, _ := runCountingInvestigation(t, discovered, ceiling)

	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want exactly 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("count row outcome = %q over a narrowed member set, want %q -- an exact claim over a reduced set "+
			"is a confident wrong answer, which is worse than the narration this replaces",
			row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.Served >= row.Declared {
		t.Fatalf("count row served/declared = %d/%d: a `narrowed` row that served everything it declared narrowed nothing",
			row.Served, row.Declared)
	}
	if row.Impact == contractsv1.ContextFabricAnswerImpactNone {
		t.Fatal("a narrowed count claims no impact; the reader is shown a smaller population than was found")
	}
	if row.CauseOverrun == "" && row.CauseNarrowing == "" && row.CauseCoverage == "" {
		t.Fatal("a narrowed count names no cause -- that is the generic truncation bit the outcome layer exists to replace")
	}
	// CauseObserved distinguishes a cause a mechanism REPORTED from one the
	// assembly layer defaulted to. It is written by this builder and was
	// asserted nowhere -- the same field-completeness gap codex round 2
	// found on the telemetry side, third instance.
	if !row.CauseObserved {
		t.Fatal("a narrowed count reports its cause as DEFAULTED; the plan recorded the narrowing that produced it")
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the narrowed count row this engine served does not validate: %v", err)
	}
}

// TestTheShadowHoldsTheAnswerAgainstTheCountItWasAskedFor is the harm at the
// shadow layer.
//
// The shadow DECLINED on every counting frame, because `count` was declared
// unobservable. A declined derivation is honest while the count is prose;
// once the server computes it and serves it, declining is the instrument
// refusing to read a field that is right there.
//
// THE SHADOW IS THE UNIT UNDER TEST HERE, so the count row is INPUT and is
// built by hand. What production actually produces is pinned by
// TestTheCountObligationReachesTheServedDocumentAsACountableField, which
// drives Investigate and reads the served document.
//
// It asserts the basis is `served` rather than merely "not unobservable".
// The weaker form passed at the parent for an unrelated reason -- the arm
// order puts `unobservable` LAST, so any earlier arm satisfies a negative
// assertion and the test proves nothing about the count.
func TestTheShadowHoldsTheAnswerAgainstTheCountItWasAskedFor(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus}
	result := InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan, ClaimedFacts: oneFact(),
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			Outcomes: []RequirementOutcomeRow{{
				Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
				Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
				Obligation:  string(ObligationCount),
				Outcome:     contractsv1.ContextFabricRequirementSatisfied,
				Impact:      contractsv1.ContextFabricAnswerImpactNone,
				Served:      4, Declared: 4,
			}},
		},
	}

	shadow := DeriveServerStatus(result, []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage})
	if shadow.Basis == ServerStatusBasisUnobservable {
		t.Fatal("the shadow DECLINED on a frame requiring `count` while the served document carries a countable " +
			"count row: the derivation is refusing to read a field the server just wrote")
	}
	if shadow.Basis != ServerStatusBasisServed {
		t.Fatalf("basis = %q, want %q -- every obligation this frame required is present in the answer", shadow.Basis, ServerStatusBasisServed)
	}
	if !shadow.Derived {
		t.Fatal("the shadow produced no verdict for an answer that served its count")
	}
}

// TestAnAnswerMissingItsCountIsNeitherServedNorDeclined is the mirror, and
// it is what stops the fix above from becoming a new way to claim `served`
// on the strength of not having looked.
//
// A frame that demanded a count, and an answer carrying none, must be
// reported as MISSING it -- not declined as unobservable (the instrument can
// see this now) and not called served (nothing counted).
//
// The assertion is written negatively on purpose: it names no new basis
// token, so it compiles and runs at the parent, where it fails behaviourally
// rather than failing to build.
func TestAnAnswerMissingItsCountIsNeitherServedNorDeclined(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus}
	result := InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan, ClaimedFacts: oneFact(),
	}

	shadow := DeriveServerStatus(result, []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage})
	if shadow.Basis == ServerStatusBasisServed {
		t.Fatal("a frame requiring `count`, answered without one, was reported `served`")
	}
	if shadow.Basis == ServerStatusBasisUnobservable {
		t.Fatal("a frame requiring `count`, answered without one, was DECLINED as unobservable -- " +
			"the derivation can observe a served count now, so an absent one is a finding about the ANSWER, " +
			"not a limit of the instrument")
	}
}

// TestTheCountReachesTelemetryFromTheServedDocument is the same-change
// telemetry bar, verified at the CONSUMER.
//
// It drives Engine.Investigate and reads the recorder back. A recorder
// nothing reads is a discarding fake -- deleting the production emit would
// leave a package of green tests -- so the assertion is on what the sink
// RECEIVED, and the battery mutates the emit away to prove it.
//
// It also asserts the event AGREES with the served document. The line is
// built by reading the row rather than recounting the cohort precisely so
// the two cannot disagree; a test that only checked the number was plausible
// would not notice if they did.
func TestTheCountReachesTelemetryFromTheServedDocument(t *testing.T) {
	t.Parallel()
	// A NARROWED shape, deliberately, and this is the whole point of the
	// fixture rather than a detail.
	//
	// The first version of this test ran an EXACT count, where served and
	// declared are the same number. A mutation swapping one for the other in
	// the event builder was therefore invisible -- the battery reported it
	// SURVIVED, and it was right to. Two fields that happen to hold the same
	// value in a fixture cannot discriminate a swap between them, which is
	// the mirror of the aliasing trap where every identifier in a fixture is
	// deliberately distinct.
	//
	// The discriminating power is asserted below BEFORE the agreement is,
	// so a future change that makes this fixture stop narrowing fails here
	// rather than silently going vacuous again.
	const discovered = 8
	const ceiling = 3
	result, telemetry := runCountingInvestigation(t, discovered, ceiling)

	if len(telemetry.membershipCardinalities) != 1 {
		t.Fatalf("membership cardinality events = %d, want exactly 1 for one served answer -- "+
			"zero means the count reached the reader with nothing in the run's own artifacts to diagnose it, "+
			"and more than one means a retry double-counted",
			len(telemetry.membershipCardinalities))
	}
	event := telemetry.membershipCardinalities[0]

	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want 1", len(rows))
	}
	row := rows[0]
	// THE FIXTURE MUST BE ABLE TO TELL THE TWO FIELDS APART. Asserted first:
	// an agreement check over two equal numbers proves nothing about which
	// of them the builder read.
	if row.Served == row.Declared {
		t.Fatalf("the served row says %d/%d -- served and declared are equal, so this fixture cannot "+
			"discriminate a swap between them and every agreement assertion below is vacuous",
			row.Served, row.Declared)
	}
	if event.Served != row.Served || event.Declared != row.Declared {
		t.Fatalf("telemetry served/declared = %d/%d, served document says %d/%d -- "+
			"the run's artifacts hold two answers to `how many`",
			event.Served, event.Declared, row.Served, row.Declared)
	}
	if event.Outcome != row.Outcome || event.Requirement != row.Requirement {
		t.Fatalf("telemetry outcome/requirement = %q/%q, served document says %q/%q",
			event.Outcome, event.Requirement, row.Outcome, row.Requirement)
	}
	// EVERY FIELD THE BUILDER COPIES, not the ones that came to mind.
	// Codex round 2 found the cause fields unasserted here: setting both to
	// empty in production kept `3/8` on the line and silently dropped the
	// basis that explains WHY it was reduced, and the whole package accepted
	// it. The earlier sweep after the first telemetry finding asked "can the
	// fixture discriminate these two values" and never asked "does this
	// assertion cover every field the builder writes" -- the wrong question,
	// so it found nothing.
	if event.Basis != row.CauseNarrowing {
		t.Fatalf("telemetry basis = %q, served row says %q -- the operator's line lost the decision basis "+
			"that explains why the count was reduced", event.Basis, row.CauseNarrowing)
	}
	if event.Overrun != row.CauseOverrun {
		t.Fatalf("telemetry overrun = %q, served row says %q", event.Overrun, row.CauseOverrun)
	}
	if event.Family == "" {
		t.Fatal("telemetry names no family, so a regression cannot be attributed to a family-table row")
	}
	// The basis must actually be PRESENT on this narrowed fixture, or the
	// pair above is two empty strings agreeing with each other -- exactly
	// the vacuity the first telemetry finding was about.
	if row.CauseNarrowing == "" {
		t.Fatal("the narrowed fixture carries no narrowing basis, so the basis assertion above is vacuous")
	}
	if event.Served != ceiling || event.Declared != discovered {
		t.Fatalf("telemetry served/declared = %d/%d, want %d/%d -- the members the answer carries, "+
			"over the members the turn found", event.Served, event.Declared, ceiling, discovered)
	}
	// The coverage half. A cardinality without it reads as a claim about the
	// population, which the step does not make.
	if result.Cohort == nil {
		t.Fatal("the fixture served no cohort")
	}
	if event.CohortComplete != result.Cohort.Complete || event.CohortTruncated != result.Cohort.Truncated {
		t.Fatalf("telemetry cohort_complete/cohort_truncated = %v/%v, served cohort says %v/%v",
			event.CohortComplete, event.CohortTruncated, result.Cohort.Complete, result.Cohort.Truncated)
	}
}

// TestMembershipCardinalityLineCarriesItsRequiredKeys is the REQUIRED half of
// the log line's key set.
//
// `cohort_complete`/`cohort_truncated` are required rather than optional, and
// that is the point of the line: the step counts the RESOLVED member set, so
// a number without those two reads as a population claim it does not make.
func TestMembershipCardinalityLineCarriesItsRequiredKeys(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordMembershipCardinality(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			MembershipCardinalityEvent{
				Family:      QuestionFamilyScopedCohortStatus,
				Requirement: "count/member/team",
				Outcome:     contractsv1.ContextFabricRequirementSatisfied,
				Served:      4, Declared: 4, CohortComplete: true,
			})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	for _, required := range []string{
		"org_id", "family", "requirement", "outcome", "served", "declared",
		"cohort_complete", "cohort_truncated",
	} {
		if _, present := records[0][required]; !present {
			t.Errorf("the membership cardinality line is missing key %q", required)
		}
	}
	// The narrowing keys are absent on an exact count, so a reader filtering
	// on `basis` sees reductions alone.
	if _, present := records[0]["basis"]; present {
		t.Error("an exact count named a narrowing basis; no selection ran")
	}
}

// TestMembershipCardinalityLineCarriesNoKeyOutsideItsAllowList is the
// ALLOW-LIST half. Counts, closed enums and a server-derived requirement
// coordinate only -- no subject label, no question, no member id.
func TestMembershipCardinalityLineCarriesNoKeyOutsideItsAllowList(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordMembershipCardinality(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			MembershipCardinalityEvent{
				Family:      QuestionFamilyScopedCohortStatus,
				Requirement: "count/member/team",
				Outcome:     contractsv1.ContextFabricRequirementNarrowed,
				Served:      3, Declared: 8,
				Basis:           contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
				Overrun:         contractsv1.ContextFabricBudgetOverrunItems,
				CohortTruncated: true,
			})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "request_id": true,
		"org_id": true, "family": true, "requirement": true, "outcome": true,
		"served": true, "declared": true, "cohort_complete": true,
		"cohort_truncated": true, "basis": true, "overrun": true,
	}
	for key := range records[0] {
		if !allowed[key] {
			t.Fatalf("membership cardinality telemetry emits unpermitted key %q -- if this field is genuinely safe, add it to the allow-list deliberately", key)
		}
	}
	// The allow-list is only half a guard if the line could be empty: the
	// narrowing keys must actually be PRESENT on a narrowed count.
	for _, required := range []string{"basis", "overrun"} {
		if _, present := records[0][required]; !present {
			t.Errorf("a narrowed count did not name %q", required)
		}
	}
}

// TestTheCountRowsBudgetCostIsBoundedAndCharged is the response-shape
// measurement this change owes.
//
// The rule it satisfies: any change that grows the served shape measures the
// result against every per-request budget and states the numbers. It ASSERTS
// them rather than logging them -- a number a downstream depends on that is
// only printed is narration, and this one is a pin on how much one counted
// requirement costs a caller.
//
// THE ITEMS AXIS IS UNAFFECTED, and that is a fact about the charging rule
// rather than an assumption: CountContextFabricResultItems charges
// candidates, drivers, paths, remaining work, readiness gaps, conflicts,
// claimed facts and cohort members. An outcome row is none of those, so a
// counted answer is charged exactly what the same answer without the count
// would be. The bytes axis does grow, by one row, and this bounds it.
func TestTheCountRowsBudgetCostIsBoundedAndCharged(t *testing.T) {
	t.Parallel()
	result, _ := runCountingInvestigation(t, 3, 0)

	withCount, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}

	// The same served document with the count row removed -- the honest
	// counterfactual, built by subtraction from what the engine produced
	// rather than by constructing a second fixture that could differ in
	// some other way.
	stripped := result
	kept := make([]RequirementOutcomeRow, 0, len(result.Completeness.Outcomes))
	removed := 0
	for _, row := range result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult &&
			row.Obligation == string(ObligationCount) {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	if removed != 1 {
		t.Fatalf("removed %d count rows building the counterfactual, want 1", removed)
	}
	stripped.Completeness.Outcomes = kept
	withoutCount, err := contractsv1.MeasureContextFabricResponse(stripped)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse(stripped) error = %v", err)
	}

	if withCount.Items != withoutCount.Items {
		t.Fatalf("item counts differ with and without the count row (%+v vs %+v) -- "+
			"an outcome row is not a charged item, so this change must not move the items axis",
			withCount.Items, withoutCount.Items)
	}
	delta := withCount.Bytes - withoutCount.Bytes
	if delta <= 0 {
		t.Fatalf("the count row cost %d bytes; a row that costs nothing was not serialized", delta)
	}
	// The pin. If a field is added to the row, or the requirement identity
	// grows, this moves -- and it should be updated in the commit that
	// causes it, with the new number stated.
	const countRowByteCeiling = 256
	if delta > countRowByteCeiling {
		t.Fatalf("the count row cost %d bytes, over the %d-byte ceiling this change pinned. "+
			"If a field was added on purpose, update the ceiling in the same commit and say so",
			delta, countRowByteCeiling)
	}
	t.Logf("BUDGET: one counted requirement costs %d bytes and 0 charged items "+
		"(items %d, bytes %d with the count; items %d, bytes %d without)",
		delta, withCount.Items.Budgeted(), withCount.Bytes,
		withoutCount.Items.Budgeted(), withoutCount.Bytes)
}

// TestComputeMembershipCardinalityReadsOnlyMemberNarrowings isolates the
// step's own rules, one fixture per rule.
//
// WHY THIS EXISTS SEPARATELY FROM THE ENGINE-DRIVEN TESTS. The engine
// fixtures above exercise the common shapes, and they do NOT reach the
// stage-1 case at all: a request whose cohort ceiling is already below the
// plan's budget records no `cardinality` step, so the exclusion that keeps a
// CEILING pair from being read as member counts is unreachable end to end.
// A rule no fixture reaches is a rule pinned by nothing -- it would survive
// being deleted, and the first question with a real pre-read clamp would
// publish "declared 50, served 3" for a three-member organization.
//
// Each row differs from the exact case in ONE respect, so a mutation that
// collapses two rules into one still fails at least one row.
func TestComputeMembershipCardinalityReadsOnlyMemberNarrowings(t *testing.T) {
	t.Parallel()
	cohort := countingCohort(SubjectTeam, 3)

	cases := []struct {
		name         string
		cohort       *Cohort
		narrowing    []contractsv1.ContextFabricPlanNarrowing
		wantCounted  bool
		wantServed   int
		wantDeclared int
		why          string
	}{
		{
			name:   "no resolved member set is ABSENT, never a count of zero",
			cohort: nil, wantCounted: false,
			why: "a population that could not be resolved and one that is genuinely empty are different answers",
		},
		{
			name:   "nothing narrowed, so the count is exact over the resolved set",
			cohort: cohort, wantCounted: true, wantServed: 3, wantDeclared: 3,
		},
		{
			name:   "a stage-1 CEILING pair is not a member count",
			cohort: cohort,
			narrowing: []contractsv1.ContextFabricPlanNarrowing{{
				Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
				Before: 50, After: 10,
			}},
			wantCounted: true, wantServed: 3, wantDeclared: 3,
			why: "stage 1 records the requested ceiling and the clamp, not members; reading it would publish `declared 50` for a three-member cohort",
		},
		{
			name:   "a GROUP narrowing counts groups, not members",
			cohort: cohort,
			narrowing: []contractsv1.ContextFabricPlanNarrowing{{
				Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
				Before: 9, After: 4, Groups: true,
			}},
			wantCounted: true, wantServed: 3, wantDeclared: 3,
			why: "the group axis was narrowed; the member count is untouched by it",
		},
		{
			name:   "a member narrowing supplies the declared population",
			cohort: cohort,
			narrowing: []contractsv1.ContextFabricPlanNarrowing{{
				Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
				Before: 8, After: 3,
				Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			}},
			wantCounted: true, wantServed: 3, wantDeclared: 8,
		},
		{
			name:   "the EARLIEST member narrowing wins, because its Before is the largest count observed",
			cohort: cohort,
			narrowing: []contractsv1.ContextFabricPlanNarrowing{
				{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Before: 50, After: 12},
				{Stage: contractsv1.ContextFabricPlanNarrowingSynthesisInput, Before: 12, After: 6,
					Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical},
				{Stage: contractsv1.ContextFabricPlanNarrowingAssembledResult, Before: 6, After: 3,
					Basis: contractsv1.ContextFabricNarrowingBasisAttentionRank},
			},
			wantCounted: true, wantServed: 3, wantDeclared: 12,
			why: "12 is the largest member count this turn actually saw; 50 is a ceiling and 6 is mid-narrowing",
		},
	}

	reached := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, counted := ComputeMembershipCardinality(testCase.cohort, testCase.narrowing)
			if counted != testCase.wantCounted {
				t.Fatalf("counted = %v, want %v (%s)", counted, testCase.wantCounted, testCase.why)
			}
			if !counted {
				if got.Served != 0 || got.Declared != 0 {
					t.Fatalf("an absent count carried numbers %d/%d", got.Served, got.Declared)
				}
				return
			}
			if got.Served != testCase.wantServed || got.Declared != testCase.wantDeclared {
				t.Fatalf("served/declared = %d/%d, want %d/%d (%s)",
					got.Served, got.Declared, testCase.wantServed, testCase.wantDeclared, testCase.why)
			}
			if got.Kind != SubjectTeam {
				t.Fatalf("counted kind = %q, want %q -- a count whose reader cannot tell WHAT was counted is not an answer", got.Kind, SubjectTeam)
			}
		})
		reached++
	}
	if reached != len(cases) {
		t.Fatalf("%d of %d rows reached their assertions", reached, len(cases))
	}
}

// TestFinalizingTwiceStatesOneCardinality is the re-entry guard, tested
// rather than asserted in a comment.
//
// finalizeResult runs more than once on real paths: the stage-3 retry
// finalizes a fresh result, and the outcome layer's candidate reduction
// re-finalizes one that ALREADY carries rows. The second shape is the
// dangerous one -- the seed is skipped because the set is non-empty, so
// nothing else would stop a second cardinality being appended for the same
// requirement, and a reader would have two answers to one question with no
// way to tell which described the document they received.
//
// It calls finalizeResult TWICE on the same result, which is the only way to
// express the defect: no single end-to-end drive re-enters it on an
// already-rowed document.
func TestFinalizingTwiceStatesOneCardinality(t *testing.T) {
	t.Parallel()
	frame := countingFrame(SubjectTeam)
	engine := newCountingEngine(t, countingCohort(SubjectTeam, 4), frame, &recordingTelemetry{})

	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: QuestionFamilyTableVersion}

	// A ZERO-MEMBER cohort is a case in its own right, and codex round 1
	// found it missing. A resolved-but-empty population genuinely counts
	// zero -- it is not the ABSENT case, which is a nil cohort -- so it
	// gets a row like any other, and the guard has to suppress a duplicate
	// for it too. Testing only a populated cohort let a mutant that
	// suppressed duplicates ONLY when `Declared > 0` survive the whole
	// package suite: the guard looked right and was untested at zero.
	for _, size := range []int{4, 0} {
		size := size
		t.Run(fmt.Sprintf("%d members", size), func(t *testing.T) {
			cohort := countingCohort(SubjectTeam, size)
			result := InvestigationResult{
				Status: InvestigationComplete, ResultID: "result_50210002", Cohort: cohort,
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			}

			once := engine.finalizeResult(result, plan, frame)
			if got := len(countOutcomeRows(once, contractsv1.ContextFabricOutcomeStageAssembledResult)); got != 1 {
				t.Fatalf("after ONE finalization: %d count rows, want 1", got)
			}
			twice := engine.finalizeResult(once, plan, frame)
			rows := countOutcomeRows(twice, contractsv1.ContextFabricOutcomeStageAssembledResult)
			if len(rows) != 1 {
				t.Fatalf("after TWO finalizations: %d count rows, want 1 -- re-entry appended a second "+
					"cardinality for the same requirement, and a reader now has two answers to one question",
					len(rows))
			}
			// The surviving row must be the real one, not a zeroed
			// placeholder: a guard that kept exactly one row by keeping the
			// WRONG one would pass a bare count check.
			if rows[0].Served != size || rows[0].Declared != size {
				t.Fatalf("the surviving row says %d/%d, want %d/%d",
					rows[0].Served, rows[0].Declared, size, size)
			}
			// And the seed rows must not have been duplicated either -- the
			// same re-entry runs through seedRequirementOutcomes' own guard.
			if planning := len(countOutcomeRows(twice, contractsv1.ContextFabricOutcomeStagePlanning)); planning != 1 {
				t.Fatalf("planning-stage `count` rows = %d, want 1", planning)
			}
		})
	}
}

// TestATruncatedCohortsCountIsNotAPopulationClaim pins the reported limit at
// the CONSUMER, not only in a doc comment.
//
// `satisfied` on a count row means "counted the RESOLVED member set exactly".
// It does NOT mean "this is the population". The distinction is real and it
// is the one thing about this row a reader could get wrong: where the graph
// read stopped at the cohort ceiling, the resolved set is a lower bound and
// the count over it is still exact.
//
// A doc comment cannot enforce that. What can is this: whenever the served
// document states a count, the SAME document must carry the cohort whose
// complete/truncated flags say whether the counted set is the whole of it. A
// count that arrived without them would be unreadable except as a population
// claim -- which is precisely the misreading the limit describes.
func TestATruncatedCohortsCountIsNotAPopulationClaim(t *testing.T) {
	t.Parallel()
	frame := countingFrame(SubjectTeam)
	// A cohort the DISCOVERY truncated: the read stopped at the ceiling, so
	// these three are a lower bound on the population. Nothing narrowed
	// afterwards, so the count over the resolved set is exact.
	cohort := countingCohort(SubjectTeam, 3)
	cohort.Complete = false
	cohort.Truncated = true
	telemetry := &recordingTelemetry{}
	engine := newCountingEngine(t, cohort, frame, telemetry)
	result := runCountingRequest(t, engine, 0)

	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("outcome = %q, want %q -- nothing narrowed the resolved set, so the count over it IS exact; "+
			"reporting `narrowed` here would blame this answer for a discovery bound it did not apply",
			row.Outcome, contractsv1.ContextFabricRequirementSatisfied)
	}
	if row.Served != row.Declared {
		t.Fatalf("served/declared = %d/%d; nothing narrowed, so they must agree", row.Served, row.Declared)
	}

	// THE CONSUMER-SIDE ASSERTION. The count is on the document; so is the
	// signal that says what it is a count OF.
	if result.Cohort == nil {
		t.Fatal("the served document states a count and carries NO cohort -- a reader has the number and " +
			"nothing that says whether the counted set is the whole population, so the only available " +
			"reading is the population claim this row does not make")
	}
	if !result.Cohort.Truncated {
		t.Fatal("the served cohort does not report the discovery truncation, so the `satisfied` count reads " +
			"as a population claim")
	}
	if result.Cohort.Complete {
		t.Fatal("a truncated cohort reported Complete")
	}
	// And the same pair reaches the OPERATOR on the telemetry line, so the
	// two consumers of this number cannot disagree about its standing. This
	// is the half a doc comment cannot hold: the answer's reader and the
	// operator must both be able to tell an exact count of a truncated set
	// from a count of a whole population.
	if len(telemetry.membershipCardinalities) != 1 {
		t.Fatalf("membership cardinality events = %d, want 1", len(telemetry.membershipCardinalities))
	}
	event := telemetry.membershipCardinalities[0]
	if !event.CohortTruncated || event.CohortComplete {
		t.Fatalf("telemetry reports cohort_complete=%v cohort_truncated=%v for a truncated cohort -- "+
			"an operator reading this line would take the number for a population",
			event.CohortComplete, event.CohortTruncated)
	}
	if event.Outcome != row.Outcome || event.Served != row.Served {
		t.Fatalf("telemetry (%q, %d) disagrees with the served row (%q, %d)",
			event.Outcome, event.Served, row.Outcome, row.Served)
	}
}

// TestAReusedAnswersCountStillDescribesTheMembersItCarries guards the count
// against the one surface that shrinks a served document AFTER the count was
// computed.
//
// WHY THIS TEST EXISTS AT ALL. The reuse degrade re-checks authorization on a
// STORED answer and strips evidence the caller may no longer see; where
// stripping empties an object the contract requires to carry evidence, it
// drops the whole object -- candidates, drivers, findings, paths, and it has
// a branch for cohort MEMBERS. The count row is computed at assembly and
// stored with the answer, so a path that removed members afterwards would
// serve a cardinality describing a member set the caller never receives:
// measure-then-shrink, on a surface that SERVES rather than refuses.
//
// MEASURED, NOT ASSUMED: it cannot happen today. The degrade strips
// `member.EvidenceRefIDs`, and those are OPTIONAL on a cohort member -- both
// nil and an empty slice validate -- so stripping can never turn a valid
// member invalid, `strippingBrokeIt` is never true for a member, and the
// member-drop branch beside it is unreachable from this path. That is a fact
// about the contract's bounds, not about this change, and it is the reason
// no re-statement of the count is wired into the degrade.
//
// It is guarded rather than trusted because the bound could move: make member
// evidence required, and members become droppable, and this count goes stale
// silently. This test reds the moment that happens.
func TestAReusedAnswersCountStillDescribesTheMembersItCarries(t *testing.T) {
	t.Parallel()
	stored := storedResultWithCandidateEvidence()
	stored.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "reuse guard", Complete: true,
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:A", Label: "A"},
				Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:B", Label: "B"},
				Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes,
		RequirementOutcomeRow{
			Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
			Obligation:  string(ObligationCount),
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
			Served:      2, Declared: 2,
		})
	stored.Completeness = ComputeAnswerCompleteness(stored)

	degraded, counts, _, ok := degradeReusedResult(stored, map[string]struct{}{reuseNodeRef: {}})
	if !ok {
		t.Fatal("degrade refused; this fixture is meant to degrade")
	}
	// The fixture must actually STRIP something, or the guard runs against a
	// path that did nothing and proves nothing about it.
	if counts.Refs() == 0 && counts.objectDrops() == 0 {
		t.Fatal("the degrade removed nothing, so this fixture does not exercise the path it guards")
	}
	if degraded.Cohort == nil {
		t.Fatal("the degraded answer carries no cohort")
	}

	rows := countOutcomeRows(degraded, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows after degrade = %d, want 1", len(rows))
	}
	if got, want := rows[0].Served, len(degraded.Cohort.Members); got != want {
		t.Fatalf("the served count says %d and the served answer carries %d members -- the reuse degrade "+
			"shrank the member set after the count was computed, so the caller is told a cardinality "+
			"for a set they did not receive", got, want)
	}
	if counts.DroppedMembers != 0 {
		t.Fatalf("the reuse degrade dropped %d cohort member(s). That branch was unreachable when the count "+
			"was wired -- member evidence refs are optional, so stripping cannot invalidate a member -- and "+
			"the count row is NOT re-stated on this path. If members are droppable now, the count needs a "+
			"reuse-stage row and this test is the thing that noticed", counts.DroppedMembers)
	}
}

// TestAReusedAnswerStatesItsCardinality is codex round-1 finding 1, pinned.
//
// THE DEFECT. Wiring the step made `count` a server result and flipped its
// declaration to `server_executed`. The reuse path serves a STORED document
// unchanged, and a document stored before the step was wired carries no
// assembled-result count row -- so a counting question answered from cache
// serves a cohort and no cardinality, while the declaration says the server
// computes one. The declaration would be false for every reused answer.
//
// THE FIX IS A BACKFILL, NOT A VERSION FENCE, and the precedent is four
// lines away: the same serving path already recomputes `Completeness` for
// rows persisted before that block existed, on the reasoning that a pure
// function of fields the row already carries is a backfill and never an
// invention. The cardinality is exactly that -- it counts the member set the
// stored document itself carries. Fencing the reuse key instead would throw
// away every cached answer to re-derive something already derivable from it.
//
// It lands as an `assembled_result` row because that is what it describes:
// the member set of the assembled document being served. The idempotence
// guard makes it a no-op for a document stored after the wiring, so the
// backfill cannot double-state a cardinality.
func TestAReusedAnswerStatesItsCardinality(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()

	// The PRE-WIRE stored shape: planning rows (the derivation seeded them),
	// a real cohort, and NO assembled-result count row -- exactly what a row
	// persisted before this change looks like.
	candidate.Cohort = countingCohort(SubjectTeam, 3)
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	if len(countOutcomeRows(candidate, contractsv1.ContextFabricOutcomeStageAssembledResult)) != 0 {
		t.Fatal("the stored fixture already carries an assembled-result count row; it does not model a pre-wire row")
	}

	// The reuse recheck re-authorizes EVERY subject the stored document
	// names, cohort members included, so the resolver has to still commit
	// them or the fixture takes a fresh path and proves nothing. That guard
	// is the reason this test asserts `served.Reused` before anything else.
	committed := []SubjectRef{project}
	for _, member := range candidate.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if served.Cohort == nil || len(served.Cohort.Members) != 3 {
		t.Fatalf("served cohort = %#v, want the stored 3-member cohort", served.Cohort)
	}

	rows := countOutcomeRows(served, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("a REUSED answer to a counting question states %d cardinalities, want 1 -- it serves a "+
			"three-member cohort and no count, while the step's declaration says the server computes one",
			len(rows))
	}
	if rows[0].Served != 3 || rows[0].Declared != 3 {
		t.Fatalf("reused count says %d/%d, want 3/3 -- the members the served document carries",
			rows[0].Served, rows[0].Declared)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(rows[0]); err != nil {
		t.Fatalf("the backfilled row does not validate: %v", err)
	}
}

// TestAReusedNarrowedAnswerIsBackfilledAsNarrowed is the discriminating half
// of the reuse backfill.
//
// The fixture above has no narrowing, so the backfilled row is `satisfied`
// and a bug that ignored the stored plan's narrowing history entirely would
// still pass it -- the same shape as the M10 vacuity the first battery run
// caught. A stored document whose plan recorded a member narrowing must be
// backfilled as `narrowed` over the population that plan observed, not as an
// exact count of the survivors: reporting exact over a reduced set is the
// confident wrong answer this whole change exists to avoid, and it would be
// no better for arriving from cache.
func TestAReusedNarrowedAnswerIsBackfilledAsNarrowed(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	candidate.Cohort = countingCohort(SubjectTeam, 3)
	candidate.Cohort.Complete = false
	candidate.Cohort.Truncated = true
	// The stored plan's OWN narrowing history: this document was cut from
	// eight members to the three it carries.
	candidate.AnswerPlan = &AnswerPlan{
		Family:        QuestionFamilyScopedCohortStatus,
		FamilyVersion: QuestionFamilyTableVersion,
		Narrowing: []contractsv1.ContextFabricPlanNarrowing{{
			Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 8, After: 3,
		}},
	}
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)

	committed := []SubjectRef{project}
	for _, member := range candidate.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	rows := countOutcomeRows(served, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("reused narrowed answer states %d cardinalities, want 1", len(rows))
	}
	row := rows[0]
	if row.Served == row.Declared {
		t.Fatalf("backfilled row says %d/%d -- the stored plan recorded a narrowing from 8 to 3, and a row "+
			"whose two numbers agree cannot distinguish an exact count from an ignored history",
			row.Served, row.Declared)
	}
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("backfilled outcome = %q, want %q over a stored narrowing", row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.Served != 3 || row.Declared != 8 {
		t.Fatalf("backfilled row says %d/%d, want 3/8 -- the members carried, over the population the stored plan observed",
			row.Served, row.Declared)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the backfilled narrowed row does not validate: %v", err)
	}
}

// TestAnAnswerThatDerivedNoCountStatesNone pins the gate that keeps every
// non-answering exit from claiming a cardinality.
//
// The serving-exit sweep after codex round 1 found a third path carrying a
// cohort: the subjectless terminal forwards `graphContext.Cohort` while every
// answer-bearing field stays empty by construction. It must NOT state a count
// -- it did not answer the question, it asked for clarification or reported
// no match -- and it does not, because it never reaches finalizeResult and so
// never seeds requirement rows, and this gate appends nothing without one.
//
// That is a STRUCTURAL reason rather than luck, and this test is what keeps it
// structural: seed a count row on such a path and the cardinality would start
// appearing on terminals, which is why the gate is pinned here rather than
// left as a property of who happens to call what.
func TestAnAnswerThatDerivedNoCountStatesNone(t *testing.T) {
	t.Parallel()
	cohort := countingCohort(SubjectTeam, 3)

	// No requirement rows at all: the shape every terminal exit has.
	rows, _, counted := appendMembershipCardinality(nil, cohort, nil)
	if counted || len(rows) != 0 {
		t.Fatalf("a result with no derived requirements stated a cardinality (%d rows) -- an exit that did "+
			"not answer the question must not claim a count of the cohort it happens to carry", len(rows))
	}

	// Rows, but none of them a `count`: a frame that asked for something else.
	other := []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationState) + "/" + string(SubjectRoleSubject) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationState),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	rows, _, counted = appendMembershipCardinality(other, cohort, nil)
	if counted || len(rows) != len(other) {
		t.Fatalf("a frame that derived no `count` obligation was given a cardinality anyway (%d rows)", len(rows))
	}

	// The POSITIVE control, in the same test: with a seeded count row the
	// gate DOES append. Without it, both assertions above would pass on a
	// gate that never appends anything at all.
	seeded := []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	rows, cardinality, counted := appendMembershipCardinality(seeded, cohort, nil)
	if !counted || len(rows) != 2 || cardinality.Served != 3 {
		t.Fatalf("positive control: a seeded count row produced counted=%v rows=%d served=%d, want true/2/3 -- "+
			"without this the assertions above cannot tell a correct gate from one that never appends",
			counted, len(rows), cardinality.Served)
	}
}

// TestACountThatCannotBeServedSaysSo is codex round-2 finding 1, pinned.
//
// THE DEFECT, and it is the founding defect of this change one level in.
// `organization_scope` is not a cohort variant, so no member set is ever
// discovered for it -- while a count coordinate at the MEMBER role is
// perfectly legal there ("how many repositories are in the organization").
// The frame therefore derives a `count` requirement, the seed marks it
// `satisfied` because the registry CAN serve it, assembly finds no member
// set and appended nothing, and the served answer came back complete with a
// count requirement and no countable result. The step's declaration says the
// server executes it; for that whole frame shape, nothing did.
//
// Silence was the bug. "Absent, never zero" was right about not inventing a
// number and wrong about saying nothing: a requirement the answer could not
// meet has to be STATED as unmet, or the planning row's `satisfied` is the
// last word and it is false.
//
// The cause is routed through the derivation's OWN mapping for exactly this
// concept (`computed_population_absent`), not a second hand-picked code, so
// the two cannot drift.
func TestACountThatCannotBeServedSaysSo(t *testing.T) {
	t.Parallel()
	seeded := []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectRepository),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}

	rows, _, counted := appendMembershipCardinality(seeded, nil, nil)
	if counted {
		t.Fatal("a nil member set reported a counted cardinality")
	}
	if len(rows) != 2 {
		t.Fatalf("a derived count with no resolvable member set produced %d rows, want 2 -- the planning row "+
			"says `satisfied` because the registry CAN serve it, and with nothing appended that claim is the "+
			"last word the answer carries about a requirement it never met", len(rows))
	}
	stated := rows[1]
	if stated.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
		t.Fatalf("stage = %q, want %q", stated.Stage, contractsv1.ContextFabricOutcomeStageAssembledResult)
	}
	if stated.Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Fatalf("outcome = %q, want %q -- nothing was counted, and `narrowed` would claim a reduction that "+
			"never happened while `satisfied` would claim a count that does not exist",
			stated.Outcome, contractsv1.ContextFabricRequirementUnavailable)
	}
	if stated.Impact == contractsv1.ContextFabricAnswerImpactNone {
		t.Fatal("an unavailable count claims no impact; the reader asked how many and is told nothing")
	}
	if stated.Served != 0 || stated.Declared != 0 {
		t.Fatalf("an unavailable count carries numbers %d/%d -- nothing was counted, so inventing one is the "+
			"confident wrong answer this whole change exists to avoid", stated.Served, stated.Declared)
	}
	// The cause must come from the derivation's own mapping for this
	// concept, so a second authority for "the population is absent" cannot
	// appear beside the first.
	if want := unavailableRequirementCause(RequirementReasonComputedPopulationAbsent); stated.CauseCoverage != want {
		t.Fatalf("cause = %q, want %q -- the derivation already maps `computed_population_absent`, and a "+
			"hand-picked second code here is how two records of one fact begin to drift",
			stated.CauseCoverage, want)
	}
	if !stated.CauseObserved {
		t.Fatal("an unavailable count reports its cause as DEFAULTED; assembly looked for a member set and found none")
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(stated); err != nil {
		t.Fatalf("the unavailable row does not validate: %v", err)
	}

	// And the whole set must now read as degraded rather than complete: an
	// answer that could not meet a required obligation is not complete.
	if got := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); got != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("completeness state = %q, want degraded -- the answer carries an unmet requirement", got)
	}
}

// TestTheCardinalityEventCopiesEveryFieldItClaimsTo is the field-completeness
// half, and it exists because the end-to-end fixture cannot populate one of
// them.
//
// The engine's own narrowing records a basis but no overrun (only the budget
// stage sets one), so the end-to-end assertion on `Overrun` compares two
// empty strings and cannot discriminate a builder that drops the field. This
// drives the builder DIRECTLY with a row carrying both cause fields, which is
// the only way to make that one assertion mean something.
//
// Stated rather than hidden: the direct test is weaker than the end-to-end
// one -- it proves the builder copies, not that the engine reaches it. The
// end-to-end test above is what proves the reach; this is what proves the
// copy is total.
func TestTheCardinalityEventCopiesEveryFieldItClaimsTo(t *testing.T) {
	t.Parallel()
	row := RequirementOutcomeRow{
		Stage:          contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:    "count/member/team",
		Obligation:     string(ObligationCount),
		Outcome:        contractsv1.ContextFabricRequirementNarrowed,
		Impact:         contractsv1.ContextFabricAnswerImpactScope,
		CauseNarrowing: contractsv1.ContextFabricNarrowingBasisAttentionRank,
		CauseOverrun:   contractsv1.ContextFabricBudgetOverrunItems,
		CauseObserved:  true,
		Served:         2, Declared: 9,
	}
	result := InvestigationResult{
		Cohort:       &Cohort{Kind: SubjectTeam, Complete: false, Truncated: true},
		Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: []RequirementOutcomeRow{row}},
	}

	event, ok := membershipCardinalityEventFrom(result, QuestionFamilyScopedCohortStatus)
	if !ok {
		t.Fatal("the builder found no count row on a document that carries one")
	}
	// Each field is checked against a value DISTINCT from every other field's,
	// so a builder that crossed two of them fails rather than coincidentally
	// agreeing.
	for _, check := range []struct {
		field string
		got   any
		want  any
	}{
		{"Family", event.Family, QuestionFamilyScopedCohortStatus},
		{"Requirement", event.Requirement, row.Requirement},
		{"Outcome", event.Outcome, row.Outcome},
		{"Served", event.Served, row.Served},
		{"Declared", event.Declared, row.Declared},
		{"Basis", event.Basis, row.CauseNarrowing},
		{"Overrun", event.Overrun, row.CauseOverrun},
		{"CohortComplete", event.CohortComplete, false},
		{"CohortTruncated", event.CohortTruncated, true},
	} {
		if check.got != check.want {
			t.Errorf("event.%s = %v, want %v -- the builder does not copy this field", check.field, check.got, check.want)
		}
	}
}
