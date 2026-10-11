package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	singleSubjectTrimMaxItems = 30
	singleSubjectTrimMaxBytes = 65536
)

// singleSubjectTrimEngine is a one-team investigation (no cohort) whose model
// draft carries `claims` claimed facts about the team itself, none cited.
func singleSubjectTrimEngine(t *testing.T, claims, pathCount int, maxBytes int64, drivers, citesPer int, emptyCohort bool, calls *int) *Engine {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	paths := []RelationshipPath{}
	if pathCount > 0 {
		paths = chaos6558Paths()[:pathCount]
	}
	var cohort *Cohort
	if emptyCohort {
		cohort = &Cohort{Kind: SubjectRepository, Rationale: "no members", Complete: true, Members: []CohortMember{}}
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "pressure",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}},
			context: GraphContext{
				Cohort: cohort,
				Paths:  paths, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			*calls++
			drafted := make([]ClaimedFact, 0, claims)
			for index := 0; index < claims; index++ {
				drafted = append(drafted, ClaimedFact{
					ClaimID: fmt.Sprintf("claim_%02d", index), Kind: FactStatus, Subject: team, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
			judged := []DriverJudgment{}
			for index := 0; index < drivers; index++ {
				judged = append(judged, DriverJudgment{
					DriverID: fmt.Sprintf("driver_%02d", index), Standing: DriverPrincipal, Category: "status", Title: "Team status",
					Summary: "The team appears stable.", AffectedSubjects: []SubjectRef{team},
					EvidenceRefIDs: []string{"evidence_00"}, ClaimedFactIDs: citedByDriver(claims, index, citesPer),
					Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true,
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Pressure appears moderate.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: judged, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: paths, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: drafted,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Pressure appears moderate, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: singleSubjectTrimMaxItems, MaxSerializedBytes: maxBytes,
		SynthesisDeadlineReserve: time.Second,
		Now:                      func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:              func() string { return "result_trim0001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestSingleSubjectAnswerOverTheItemCeilingIsTrimmedNotRefused: a model draft
// with 31 uncited claims about one team is one item over a 30-item ceiling.
// The answer must be served within the ceiling, partial and disclosed.
func TestSingleSubjectAnswerOverTheItemCeilingIsTrimmedNotRefused(t *testing.T) {
	assertSingleSubjectTrimServes(t, 0, singleSubjectTrimMaxBytes)
}

// TestSingleSubjectAnswerOverBothCeilingsIsTrimmedNotRefused is the same draft
// with graph paths that put the BYTES over the ceiling too; the items axis is
// reported first, and the answer must still be served.
func TestSingleSubjectAnswerOverBothCeilingsIsTrimmedNotRefused(t *testing.T) {
	assertSingleSubjectTrimServes(t, len(chaos6558Paths()), singleSubjectTrimMaxBytes)
}

// TestSingleSubjectAnswerOverBothCeilingsWithFewerPathsIsTrimmedNotRefused is
// the same crossing at another size: 12 paths against a 30,000-byte ceiling.
func TestSingleSubjectAnswerOverBothCeilingsWithFewerPathsIsTrimmedNotRefused(t *testing.T) {
	assertSingleSubjectTrimServes(t, 12, 30000)
}

func assertSingleSubjectTrimServes(t *testing.T, pathCount int, maxBytes int64) {
	t.Helper()
	calls := 0
	engine := singleSubjectTrimEngine(t, singleSubjectTrimMaxItems+1, pathCount, maxBytes, 0, 0, false, &calls)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused after %d syntheses: overrun=%s items %d/%d bytes %d/%d nothing_left_to_cut=%v retry_attempted=%v", calls, refusal.Overrun, refusal.MeasuredItems, refusal.MaxItems, refusal.MeasuredBytes, refusal.MaxSerializedBytes, refusal.NothingLeftToCut, refusal.RetryAttempted)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if got := measurement.Items.Budgeted(); got > singleSubjectTrimMaxItems {
		t.Fatalf("served %d items against a %d ceiling", got, singleSubjectTrimMaxItems)
	}
	if measurement.Bytes > maxBytes {
		t.Fatalf("served %d bytes against a %d ceiling", measurement.Bytes, maxBytes)
	}
	if result.Status != InvestigationPartial {
		t.Fatalf("Status = %q, want partial: a trimmed answer is disclosed as such", result.Status)
	}
	disclosed := false
	for _, limitation := range result.Limitations {
		disclosed = disclosed || limitation == contractsv1.ContextFabricBudgetTrimClaimedFactsLimitation
	}
	if !disclosed {
		t.Fatalf("Limitations = %q, want the fixed claimed-facts trim disclosure", result.Limitations)
	}
	if pathCount > 0 && len(result.Paths) >= pathCount {
		t.Fatalf("served %d paths of %d: the byte overrun was not cut", len(result.Paths), pathCount)
	}
	if got := len(result.ClaimedFacts); got < singleSubjectTrimMaxItems/2 {
		t.Fatalf("served %d claims: the trim cut more than the overrun required", got)
	}
}

// TestSingleSubjectAnswerBelowTheMinimumAnswerStillRefuses: when no cut can
// bring the answer under the ceiling (25 drivers, each citing its own claim:
// the anchors and the drivers are never cut), the refusal remains.
func TestSingleSubjectAnswerBelowTheMinimumAnswerStillRefuses(t *testing.T) {
	calls := 0
	engine := singleSubjectTrimEngine(t, singleSubjectTrimMaxItems+1, 0, singleSubjectTrimMaxBytes, 25, 1, false, &calls)
	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal", err)
	}
}

// TestSingleSubjectTrimCutsUncitedClaimsBeforeCitedOnes: with a driver citing
// claims, an overrun the uncited claims can absorb leaves every cited claim.
func TestSingleSubjectTrimCutsUncitedClaimsBeforeCitedOnes(t *testing.T) {
	calls := 0
	engine := singleSubjectTrimEngine(t, singleSubjectTrimMaxItems+2, 0, singleSubjectTrimMaxBytes, 4, 3, false, &calls)
	// The four drivers cite the LAST twelve claims, the first ones a
	// from-the-end cut order would take: cutting cited claims first would
	// break a citation.
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	cited := []string{}
	for index := 0; index < 4; index++ {
		cited = append(cited, citedByDriver(singleSubjectTrimMaxItems+2, index, 3)...)
	}
	served := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		served[claim.ClaimID] = true
	}
	for _, id := range cited {
		if !served[id] {
			t.Fatalf("claim %q is cited by a driver and was cut while uncited claims remained", id)
		}
	}
}

// citedByDriver is the three claims, counted from the end of the list, driver
// index cites: the first one is the driver's anchor, the other two are cited
// claims a trim may cut only after every uncited claim.
func citedByDriver(claims, index, citesPer int) []string {
	ids := []string{}
	for offset := 0; offset < citesPer; offset++ {
		if n := claims - 1 - index*citesPer - offset; n >= 0 {
			ids = append(ids, fmt.Sprintf("claim_%02d", n))
		}
	}
	return ids
}

// TestAnswerWithACohortObjectIsNotTrimmedAsASingleSubject: a cohort that holds no
// member is still a cohort answer, so the single-subject cut does not apply and
// the refusal of an over-ceiling draft stands.
func TestAnswerWithACohortObjectIsNotTrimmedAsASingleSubject(t *testing.T) {
	calls := 0
	engine := singleSubjectTrimEngine(t, singleSubjectTrimMaxItems+1, 0, singleSubjectTrimMaxBytes, 0, 0, true, &calls)
	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal: a cohort object keeps the cohort levers only", err)
	}
}

// TestCensusTupleClaimsAreNeverCutAsASingleSubject: a work-item census answer
// has no cohort object on the result, and its claims (the census count) are
// pinned by the census: the item trim declines it as it did before.
func TestCensusTupleClaimsAreNeverCutAsASingleSubject(t *testing.T) {
	calls := 0
	engine := singleSubjectTrimEngine(t, singleSubjectTrimMaxItems+1, 0, singleSubjectTrimMaxBytes, 0, 0, false, &calls)
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	claims := make([]ClaimedFact, 0, singleSubjectTrimMaxItems+1)
	for index := 0; index <= singleSubjectTrimMaxItems; index++ {
		claims = append(claims, ClaimedFact{ClaimID: fmt.Sprintf("claim_%02d", index), Kind: FactStatus, Subject: team, Field: "status", Value: ScalarValue{String: ptrString("green")}})
	}
	result := InvestigationResult{ClaimedFacts: claims}
	measured := MeasuredAttempt{Overrun: contractsv1.ContextFabricBudgetOverrunItems}
	budget := ResponseBudget{MaxItems: singleSubjectTrimMaxItems, MaxSerializedBytes: singleSubjectTrimMaxBytes}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("the single-subject trim ran on a census answer (panic %v)", recovered)
		}
	}()
	attempt, err := engine.planBudgetTrim(context.Background(), storage.Principal{OrgID: "org_1"}, &AnswerPlan{}, nil, result, budget, measured, CanonicalFactBundle{}, nil, answerPassSecond, MembershipCardinality{}, true)
	if err != nil {
		t.Fatalf("planBudgetTrim() error = %v", err)
	}
	if attempt.Served || attempt.Declined != ClaimDepthNoCohort {
		t.Fatalf("attempt served=%v declined=%q, want declined %q", attempt.Served, attempt.Declined, ClaimDepthNoCohort)
	}
}
