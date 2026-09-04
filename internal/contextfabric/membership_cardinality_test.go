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
	return result, telemetry
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
	const members = 5
	result, telemetry := runCountingInvestigation(t, members, 0)

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
	if event.Served != row.Served || event.Declared != row.Declared {
		t.Fatalf("telemetry served/declared = %d/%d, served document says %d/%d -- "+
			"the run's artifacts hold two answers to `how many`",
			event.Served, event.Declared, row.Served, row.Declared)
	}
	if event.Outcome != row.Outcome || event.Requirement != row.Requirement {
		t.Fatalf("telemetry outcome/requirement = %q/%q, served document says %q/%q",
			event.Outcome, event.Requirement, row.Outcome, row.Requirement)
	}
	if event.Served != members {
		t.Fatalf("telemetry served = %d, want the %d members the fixture resolved", event.Served, members)
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
