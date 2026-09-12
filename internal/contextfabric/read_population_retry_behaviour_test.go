package contextfabric

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// EXECUTED replacements for two source-text guards (read_population_wiring_test.go
// used to pin these by string match; an adversarial round named that a guard
// that checks text instead of behaviour).

const retryMemberRequirement = "state/member/project"

// retryWithMemberFacts runs one served investigation forced through exactly one
// budget retry, over a grouped frame (team -> project members) whose each_member
// read requirement is served from per-member health facts. It records the fact
// bundle each synthesis call was handed.
func retryWithMemberFacts(t *testing.T) (InvestigationResult, [][]string) {
	t.Helper()
	frame, _ := boundaryGroupedFrame(t, SubjectTeam, SubjectProject)
	// TWO independent kinds serve `state`, so the requirement is
	// corroborated and the kind-level evaluation is lossless -- which is what
	// hands the row to the POPULATION arm, the one that reads the bundle per
	// subject. With one kind the kind arm would answer first and the bundle
	// would never be consulted per member.
	stateReader := func(kind FactKind) FactCapability {
		return FactCapability{
			Kind:                  kind,
			SupportedSubjectKinds: []SubjectKind{SubjectProject, SubjectTeam},
			Obligations:           map[SubjectKind][]AnswerObligation{SubjectProject: {ObligationState}, SubjectTeam: {ObligationState}},
		}
	}
	deriver := registryDeriver{capabilities: []FactCapability{stateReader(FactHealth), stateReader(FactFlow)}}
	cohort := budgetStageCohort(6)
	var handed [][]string
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			},
			outcome: QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
		},
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			bundle := CanonicalFactBundle{
				Facts: []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{
					{Source: canonicalFactSourcePrefix + string(FactHealth), State: SourceAvailable},
					{Source: canonicalFactSourcePrefix + string(FactFlow), State: SourceAvailable},
				}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}
			for _, member := range cohort.Members {
				bundle.Facts = append(bundle.Facts,
					CanonicalFact{Kind: FactHealth, Subject: member.Subject, SourceState: SourceAvailable},
					CanonicalFact{Kind: FactFlow, Subject: member.Subject, SourceState: SourceAvailable})
			}
			return bundle, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			subjects := make([]string, 0, len(input.Facts.Facts))
			seenSubject := map[string]bool{}
			for _, fact := range input.Facts.Facts {
				if !seenSubject[fact.Subject.CanonicalID] {
					seenSubject[fact.Subject.CanonicalID] = true
					subjects = append(subjects, fact.Subject.CanonicalID)
				}
			}
			sort.Strings(subjects)
			handed = append(handed, subjects)
			claims := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for claim := 0; claim < 2; claim++ {
						claims = append(claims, ClaimedFact{
							ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
							Kind:    FactStatus, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				// The synthesized document reports the coverage of the bundle it
				// was handed, as the real synthesizer does.
				Coverage:            input.Facts.Coverage,
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Requirements: deriver,
		Telemetry:    &recordingTelemetry{},
	}, budgetStageOptions(12, time.Second))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, handed
}

// TestTheRetryEvaluatesReadsOverTheDocumentItServed drives a REAL forced retry
// and asserts, from the returned document, which reads the retry evaluated.
//
// The retry is a different document from the first pass: it was synthesized
// from the NARROWED bundle over the NARROWED cohort, and its read rows must
// describe that document. Three executed facts, together:
//
//  1. the retry was HANDED exactly the narrowed bundle -- the first pass's
//     facts minus the dropped members', no more and no fewer;
//  2. no dropped member is in the served cohort, the population every
//     distributive row of the retry counts over;
//  3. the served each_member row serves exactly the kept members out of the
//     declared population, every kept member read in the bundle the retry
//     evaluated, and names the dropped ones as the shortfall (narrowed).
//
// Stated plainly, because a guard is only as good as what it can see: given
// (1) and (2), the first-pass bundle and the retry bundle agree on every
// subject the retry reads, so passing either to the retry's finalization is
// observationally EQUIVALENT today. The old source-text pin guarded that
// equivalent choice. This test instead fails the moment the premise breaks --
// a retry bundle that gains or loses a kept member's facts, or a retry
// population that still names a dropped member -- which is when the choice
// would start to matter.
func TestTheRetryEvaluatesReadsOverTheDocumentItServed(t *testing.T) {
	t.Parallel()
	result, handed := retryWithMemberFacts(t)
	if len(handed) != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- this pin requires a forced retry", len(handed))
	}
	if result.Cohort == nil || len(result.Cohort.Members) == 0 {
		t.Fatal("the served document carries no cohort")
	}
	served := map[string]bool{}
	for _, member := range result.Cohort.Members {
		served[member.Subject.CanonicalID] = true
	}
	var dropped, kept []string
	for _, subject := range handed[0] {
		if served[subject] {
			kept = append(kept, subject)
		} else {
			dropped = append(dropped, subject)
		}
	}
	if len(dropped) == 0 {
		t.Fatalf("the retry dropped no member (first bundle %v, served cohort %d); the pin would be vacuous", handed[0], len(served))
	}
	// (1) the retry was handed exactly the narrowed bundle.
	if strings.Join(handed[1], ",") != strings.Join(kept, ",") {
		t.Fatalf("the retry synthesized from %v, want exactly the kept members' facts %v (first bundle %v)", handed[1], kept, handed[0])
	}
	// (2) no dropped member is in the served cohort.
	for _, subject := range dropped {
		if served[subject] {
			t.Fatalf("dropped member %s is still in the served cohort", subject)
		}
	}
	// (3) the served row counts over the served document.
	var row *RequirementOutcomeRow
	for i := range result.Completeness.Outcomes {
		candidate := result.Completeness.Outcomes[i]
		if candidate.Requirement == retryMemberRequirement && candidate.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			row = &result.Completeness.Outcomes[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("no assembled-result row for %q on the served document: %+v", retryMemberRequirement, result.Completeness.Outcomes)
	}
	// The row DECLARES the population the plan asked for -- the first pass's
	// cohort -- and SERVES the members the retry's document kept, every one of
	// which was read: narrowed, served < declared, with the dropped members
	// counted as the shortfall rather than silently removed from the
	// denominator.
	if row.Served != len(served) || row.Declared != len(handed[0]) {
		t.Fatalf("served %s row = %d/%d, want %d/%d -- the retry serves its kept members out of the declared population",
			retryMemberRequirement, row.Served, row.Declared, len(served), len(handed[0]))
	}
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("served %s row outcome = %s, want narrowed -- %d of %d declared members were served",
			retryMemberRequirement, row.Outcome, row.Served, row.Declared)
	}
}

// TestTheDenominatorIsTheCohortNotTheReturnedOrInvokedSet is T-DENOM's second
// half, EXECUTED: a distributive row's denominator is the cohort, and neither
// the model's claimed facts, nor the bundle's read scope, nor facts read for
// subjects outside the cohort can move it. Each is varied here and the row is
// read back; the controls vary the cohort and the reads of cohort members, and
// the row moves with them, so the invariance is not a row that never moves.
func TestTheDenominatorIsTheCohortNotTheReturnedOrInvokedSet(t *testing.T) {
	t.Parallel()
	health := contractsv1.ContextFabricFactHealth
	requirement := scopeRequirement(CompletionScopeEachMember, contractsv1.ContextFabricSubjectProject, SubjectRoleMember, CompletionQuantifierAtLeastOne, health)
	coverage := factCoverage(health, SourceAvailable)
	members := []SubjectRef{projectRef("project_1"), projectRef("project_2"), projectRef("project_3")}
	outside := []SubjectRef{projectRef("project_outside_1"), projectRef("project_outside_2")}

	evaluate := func(cohortMembers []SubjectRef, readFor []SubjectRef, claimed []SubjectRef, scoped []SubjectRef) string {
		bundle := CanonicalFactBundle{Facts: []CanonicalFact{}}
		for _, subject := range readFor {
			bundle.Facts = append(bundle.Facts, CanonicalFact{Kind: health, Subject: subject, SourceState: SourceAvailable})
		}
		if len(scoped) > 0 {
			bundle.Scope = &FactReadScope{DerivedSubjects: map[FactKind][]SubjectRef{health: scoped}}
		}
		result := InvestigationResult{Cohort: cohortWith(contractsv1.ContextFabricSubjectProject, cohortMembers, nil, true), Coverage: coverage}
		for _, subject := range claimed {
			result.ClaimedFacts = append(result.ClaimedFacts, ClaimedFact{ClaimID: "claim_" + subject.CanonicalID, Kind: health, Subject: subject, Field: "status"})
		}
		published := []contractsv1.ContextFabricPlanRequirement{requirement}
		evidence := readPopulationEvidenceFrom(nil, result, AnswerPlan{Requirements: published}, bundle, 0, nil)
		for _, row := range appendReadRequirementEvaluations(nil, published, coverage, evidence) {
			if row.Requirement == requirement.Requirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
				return string(row.Outcome) + " " + strconv.Itoa(row.Served) + "/" + strconv.Itoa(row.Declared)
			}
		}
		return "no row"
	}
	both := append(append([]SubjectRef{}, members...), outside...)
	for _, cell := range []struct {
		name                     string
		cohort, read, claim, scp []SubjectRef
		want                     string
	}{
		{"canonical: the cohort, every member read", members, members, nil, nil, "satisfied 3/3"},
		{"facts read for subjects OUTSIDE the cohort", members, both, nil, nil, "satisfied 3/3"},
		{"claimed facts naming subjects outside the cohort", members, members, outside, nil, "satisfied 3/3"},
		{"a read scope deriving subjects outside the cohort", members, members, nil, outside, "satisfied 3/3"},
		{"all three at once", members, both, outside, outside, "satisfied 3/3"},
		{"only outside subjects were read", members, outside, outside, outside, "narrowed 0/3"},
		// CONTROLS: the row does move when the population or its reads move.
		{"control: a fourth cohort member, read", append(append([]SubjectRef{}, members...), projectRef("project_4")), append(append([]SubjectRef{}, members...), projectRef("project_4")), nil, nil, "satisfied 4/4"},
		{"control: one cohort member unread", members, members[:2], nil, nil, "narrowed 2/3"},
	} {
		if got := evaluate(cell.cohort, cell.read, cell.claim, cell.scp); got != cell.want {
			t.Errorf("%s: row = %s, want %s", cell.name, got, cell.want)
		}
	}
}
