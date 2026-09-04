package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CONSUMER TESTS for the grouping-refusal plumbing, closing a gap the final
// review found by MUTATION rather than by reading: three separate mutants of
// the engine→telemetry→assembly path SURVIVED the entire package suite.
//
//	engine.go   groupingRefusalForDisclosure = groupingOutcome  →  _ = groupingOutcome
//	engine.go   Refusal: groupingOutcome.Refusal                →  CohortGroupingRefusalNone
//	telemetry.go  the "grouping_refusal" slog key renamed
//
// Every existing test drove the composer or the builder DIRECTLY, so all
// three left the suite green while deleting exactly the two things a refusal
// is for: the operator's line and the reader's sentence. This is the same
// lesson this branch already recorded once — a mutation deleting the
// composer's INVOCATION survived, because the tests drove the composer — and
// it recurred one layer up, which makes the rule worth stating plainly:
// a disclosure is only tested by the CONSUMER that receives it, never by the
// function that writes it.
//
// The standing telemetry rule is the same shape: verify the consumer, not the
// producer. The bar is that the defect is diagnosable from the run's own
// artifacts — so these assert the emitted event and the served answer, not
// that a composer can be called.

// groupingRefusalEngineFixture builds an engine whose plan asks to group by
// REPOSITORY while the only group evidence available is TEAM-scoped, which is
// the exact disagreement the refusal exists for.
//
// The plan's group axis is model-emitted, so it is supplied the way production
// supplies it: a grouped-cohort family outcome whose winning sample carries
// GroupKind, which PlanAnswer reads only for the grouped family.
func groupingRefusalEngineFixture(t *testing.T, telemetry EngineTelemetry) (*Engine, InvestigationRequest) {
	t.Helper()
	return groupingRefusalEngineFixtureWithGroupKind(t, telemetry, SubjectRepository)
}

// groupingRefusalEngineFixtureWithGroupKind is the same fixture parameterised
// on the PLANNED group kind, so the refusing and agreeing cases differ by
// exactly one value and nothing else.
func groupingRefusalEngineFixtureWithGroupKind(t *testing.T, telemetry EngineTelemetry, plannedKind SubjectKind) (*Engine, InvestigationRequest) {
	t.Helper()
	first := SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}
	second := SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}
	cohort := &Cohort{
		Kind: SubjectProject, Rationale: "kind census match", Complete: true,
		Members: []CohortMember{
			{Subject: first, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: second, Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "project_status_by_group",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactMetrics}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: groupedFamilyInterpreter{interpretation: interpretation, groupKind: plannedKind},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				// TEAM-scoped rows against a REPOSITORY plan: the source
				// always knew its own kind, and it is not the planned one.
				Facts: []CanonicalFact{
					teamScopedFact("project_a", "team_security", "Security"),
					teamScopedFact("project_b", "team_security", "Security"),
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status:              InvestigationPartial,
				DirectJudgment:      "The available evidence does not separate these projects.",
				CurrentState:        "Two projects were discovered.",
				DeterministicAnswer: "Two projects were discovered and no group axis was delivered.",
				StrongestPressures:  []string{},
				Drivers:             []DriverJudgment{},
				RemainingWork:       []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID:    func() string { return "result_49620001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_49620001"
	request.Question = "how are the projects doing by group?"
	return engine, request
}

// groupedFamilyInterpreter reports a GROUPED family whose winning sample
// carries the group axis, which is the only route by which PlanAnswer sets
// plan.GroupKind. The default test interpreter reports `unclassified`, whose
// subject axis is not many-grouped, so it can never reach this branch — which
// is a large part of why the branch went untested.
type groupedFamilyInterpreter struct {
	interpretation InterpretedQuestion
	groupKind      SubjectKind
}

func (i groupedFamilyInterpreter) Interpret(_ context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return i.interpretation, QuestionFamilyOutcome{
		Family:        QuestionFamilyGroupedCohortStatus,
		Source:        QuestionFamilySourceModel,
		WinningSample: FamilySample{GroupKind: i.groupKind},
	}, nil
}

// TestEngineRefusalReachesBothTheOperatorAndTheReader is the consumer test
// for two of the three surviving mutants at once, and deliberately asserts
// BOTH consumers in one run: they are two halves of one decision, and a test
// that checked only the telemetry would have stayed green through the mutant
// that deleted the reader's sentence.
func TestEngineRefusalReachesBothTheOperatorAndTheReader(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine, request := groupingRefusalEngineFixture(t, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a refusal to answer flat rather than fail", err)
	}

	// THE OPERATOR. Kills `Refusal: groupingOutcome.Refusal` →
	// CohortGroupingRefusalNone.
	if len(telemetry.groupedCohortCompletenesses) != 1 {
		t.Fatalf("groupedCohortCompletenesses = %#v, want exactly one event", telemetry.groupedCohortCompletenesses)
	}
	event := telemetry.groupedCohortCompletenesses[0]
	if event.Refusal != CohortGroupingRefusalGroupKindSourceMismatch {
		t.Errorf("event.Refusal = %q, want %q: an operator reading logs sees a grouped question that came back flat and no reason for it",
			event.Refusal, CohortGroupingRefusalGroupKindSourceMismatch)
	}
	if event.PlannedGroupKind != SubjectRepository {
		t.Errorf("event.PlannedGroupKind = %q, want %q", event.PlannedGroupKind, SubjectRepository)
	}
	if event.GroupCount != 0 {
		t.Errorf("event.GroupCount = %d, want 0: a refusal delivers no group axis", event.GroupCount)
	}

	// THE READER. Kills `groupingRefusalForDisclosure = groupingOutcome` →
	// `_ = groupingOutcome`, which leaves the telemetry above intact and
	// removes the answer's own statement — the silent flattening chris's
	// ruling forbids, and invisible to every assertion on telemetry alone.
	want := contractsv1.ContextFabricGroupingRefusalLimitation(SubjectRepository, SubjectTeam)
	if !hasLimitation(result.Limitations, want) {
		t.Errorf("served limitations = %#v, want the grouping-refusal disclosure %q: the reader asked for a breakdown, got a flat answer, and was told nothing",
			result.Limitations, want)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial is false on an answer that dropped its group axis")
	}
	// No group axis was delivered, so none may be claimed.
	if result.Cohort != nil && len(result.Cohort.Groups) != 0 {
		t.Errorf("result cohort carries %d groups after a refusal", len(result.Cohort.Groups))
	}
}

// TestEngineWithoutARefusalCarriesNoRefusalDisclosure is the attribution
// control. Without it a mutant that appended the disclosure unconditionally
// would satisfy the test above while putting a sentence about something that
// did not happen on every grouped answer.
func TestEngineWithoutARefusalCarriesNoRefusalDisclosure(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	// Same fixture, planned kind now AGREEING with the team-scoped rows.
	engine, request := groupingRefusalEngineFixtureWithGroupKind(t, telemetry, SubjectTeam)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricGroupingRefusalLimitation(limitation) {
			t.Fatalf("a grouped answer that was NOT refused carries a refusal disclosure: %q", limitation)
		}
	}
	for _, event := range telemetry.groupedCohortCompletenesses {
		if event.Refusal != CohortGroupingRefusalNone {
			t.Fatalf("event.Refusal = %q on an agreeing plan, want none", event.Refusal)
		}
	}
}

// TestSlogGroupedCohortCompletenessCarriesTheRefusalKeys is the consumer test
// for the third surviving mutant: renaming the `grouping_refusal` slog key.
//
// The key IS the interface — an operator's filter, a dashboard and an alert
// all name it in text — so a rename is a silent break of every consumer, and
// nothing that reads the struct can see it. This asserts the emitted LINE.
func TestSlogGroupedCohortCompletenessCarriesTheRefusalKeys(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordGroupedCohortCompleteness(context.Background(), storage.Principal{OrgID: "org_1"}, GroupedCohortCompletenessEvent{
		Family:           QuestionFamilyGroupedCohortStatus,
		GroupCount:       0,
		Refusal:          CohortGroupingRefusalGroupKindSourceMismatch,
		PlannedGroupKind: SubjectRepository,
	})

	line := buf.String()
	for _, want := range []string{
		"grouping_refusal=" + string(CohortGroupingRefusalGroupKindSourceMismatch),
		"planned_group_kind=" + string(SubjectRepository),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("grouped-cohort completeness line does not carry %q -- every operator filter and alert names this key in text, so a rename breaks them all silently.\nline: %s", want, line)
		}
	}
}

// TestSlogGroupedCohortCompletenessOmitsTheRefusalKeysWithoutARefusal pins
// the other half of the same field's contract, which its own doc comment
// states: an ordinary grouped answer's line is byte-for-byte what it was
// before the field existed, so a reader filtering on `grouping_refusal` sees
// refusals alone rather than a stream of empty values.
func TestSlogGroupedCohortCompletenessOmitsTheRefusalKeysWithoutARefusal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordGroupedCohortCompleteness(context.Background(), storage.Principal{OrgID: "org_1"}, GroupedCohortCompletenessEvent{
		Family:     QuestionFamilyGroupedCohortStatus,
		GroupCount: 2,
	})

	line := buf.String()
	for _, absent := range []string{"grouping_refusal", "planned_group_kind"} {
		if strings.Contains(line, absent) {
			t.Errorf("an un-refused grouped answer's line carries %q; it must be byte-identical to the pre-field line.\nline: %s", absent, line)
		}
	}
}

// TestSlogGroupedCohortCompletenessFailsClosedOnAnUnknownRefusal pins the
// fail-closed posture the emitter documents: a value outside the closed
// vocabulary is reported as `unclassified` rather than emitted verbatim, so a
// corrupted or future enum value cannot reach a log field as free text.
func TestSlogGroupedCohortCompletenessFailsClosedOnAnUnknownRefusal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordGroupedCohortCompleteness(context.Background(), storage.Principal{OrgID: "org_1"}, GroupedCohortCompletenessEvent{
		Family:  QuestionFamilyGroupedCohortStatus,
		Refusal: CohortGroupingRefusal("something_invented"),
	})

	line := buf.String()
	if strings.Contains(line, "something_invented") {
		t.Errorf("an out-of-vocabulary refusal reached the log verbatim.\nline: %s", line)
	}
	if !strings.Contains(line, "grouping_refusal=unclassified") {
		t.Errorf("line does not report the unknown refusal as unclassified.\nline: %s", line)
	}
}

// limitationComposerAssemblyOrder is the order §10b of the architecture
// diagrams draws, and the order its reasoning depends on.
//
// WHY THIS IS PINNED FROM SOURCE. The diagram originally said the refusal
// disclosure could be displaced by "commit affirmation, temporal, fact scope"
// running later. Only ONE of those three runs later; the other two run BEFORE
// the refusal and can only contribute to the list it finds already full. The
// error was caught in review, and it is the same species as the defect this
// whole file exists for: a claim about ordering that nothing checked.
//
// The property that matters is not the exact sequence — it is that
// **commit affirmation is the only composer after the grouping refusal**,
// because that is what makes "the composer that displaced it" a determinate
// statement rather than a guess among three candidates.
var limitationComposerAssemblyOrder = []string{
	"withRetrievalDegradation",
	"appendTemporalLimitations",
	"applySynthesisStatusOverride",
	"applyFactScopeDisclosure",
	"applyGroupingRefusalDisclosure",
	"applyCommitAffirmation",
}

// TestLimitationComposersRunInTheDocumentedOrder reads synthesizeAndAssemble's
// own source, in the manner this package already established, rather than
// standing up a fixture that observes six composers.
func TestLimitationComposersRunInTheDocumentedOrder(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("chaos4636_synthesis_assembly.go")
	if err != nil {
		t.Fatalf("read assembly source: %v", err)
	}
	body := string(source)

	positions := make(map[string]int, len(limitationComposerAssemblyOrder))
	for _, composer := range limitationComposerAssemblyOrder {
		// The CALL, not a mention in a comment.
		index := strings.Index(body, composer+"(")
		for index >= 0 && strings.Contains(lineContaining(body, index), "//") &&
			strings.Index(strings.TrimSpace(lineContaining(body, index)), "//") == 0 {
			next := strings.Index(body[index+1:], composer+"(")
			if next < 0 {
				index = -1
				break
			}
			index += 1 + next
		}
		if index < 0 {
			t.Fatalf("%s is never called in synthesizeAndAssemble's file: the documented composer chain no longer exists as drawn", composer)
		}
		positions[composer] = index
	}

	for i := 1; i < len(limitationComposerAssemblyOrder); i++ {
		earlier, later := limitationComposerAssemblyOrder[i-1], limitationComposerAssemblyOrder[i]
		if positions[earlier] >= positions[later] {
			t.Errorf("%s no longer runs before %s: architecture diagram §10b draws this order and its reasoning about which composer can displace the grouping-refusal disclosure depends on it",
				earlier, later)
		}
	}

	// The load-bearing half, stated on its own so a failure says WHY it
	// matters: nothing may be inserted between the refusal and commit
	// affirmation without revisiting §10b's claim that exactly one composer
	// runs later.
	refusal := positions["applyGroupingRefusalDisclosure"]
	for _, composer := range limitationComposerAssemblyOrder {
		if composer == "applyGroupingRefusalDisclosure" || composer == "applyCommitAffirmation" {
			continue
		}
		if positions[composer] > refusal {
			t.Errorf("%s now runs AFTER the grouping refusal: §10b says commit affirmation is the ONLY later composer, which is what makes the displacement attributable to it", composer)
		}
	}
}

// lineContaining returns the whole source line holding index.
func lineContaining(body string, index int) string {
	start := strings.LastIndex(body[:index], "\n") + 1
	end := strings.Index(body[index:], "\n")
	if end < 0 {
		return body[start:]
	}
	return body[start : index+end]
}
