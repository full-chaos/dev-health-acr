package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6743: the prod inventory shape, replayed through a real Engine.
//
// PROD (2026-09-25, acr 163629d2, api req_e10f944ad363ca7e2ff2d0f4067f3723,
// raw acc-c08-f1b.json): "Which repositories does this organization have?"
// with a confirmed 90-day window. Family discovered_cohort_ranking, an
// 11-repository cohort. Synthesis #1 carried 37 items against max_items=30
// (58,769 bytes, inside 65,536): 11 members, 2 claims per repository, and
// narration's drivers. Stage 3 halved the cohort 11 -> 5, the retry fitted at
// 19 items, and the inventory served 5 of its 11 repositories. The members
// ARE the answer to that question; the per-repository claims are depth.
//
// THE FIXTURE keeps that structure: an 11-repository discovered cohort, two
// uncited claims per repository, the prod ceilings (30 items, 65,536 bytes).
const chaos6743ProdQuestion = "Which repositories does this organization have?"

const (
	chaos6743Members         = 11
	chaos6743ClaimsPerMember = 2
	chaos6743MaxItems        = 30
	chaos6743MaxBytes        = 65536
)

type chaos6743Shape struct {
	members  int
	maxItems int
	// claimsPerMember overrides chaos6743ClaimsPerMember when non-zero.
	claimsPerMember int
	// citedPerMember makes a driver cite that many of each member's LAST claims,
	// so keeping them is not what synthesis order alone would keep.
	citedPerMember int
}

var chaos6743ProdShape = chaos6743Shape{members: chaos6743Members, maxItems: chaos6743MaxItems}

func chaos6743Cohort(members int) *Cohort {
	cohort := &Cohort{Kind: SubjectRepository, Rationale: "chaos-6743 prod shape", Complete: true, Members: make([]CohortMember, 0, members)}
	for index := 0; index < members; index++ {
		id := fmt.Sprintf("repository:%02d", index)
		cohort.Members = append(cohort.Members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectRepository, CanonicalID: id, Label: fmt.Sprintf("full-chaos/repo-%02d", index)},
			Rank:             index + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	return cohort
}

func chaos6743Engine(t *testing.T, calls *int, telemetry *recordingTelemetry, shape chaos6743Shape) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: chaos6743Cohort(shape.members),
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			observed := time.Unix(100, 0).UTC()
			cohort := chaos6743Cohort(shape.members)
			facts := make([]CanonicalFact, 0, len(cohort.Members))
			for index, member := range cohort.Members {
				status, phase := "green", "active"
				facts = append(facts, CanonicalFact{
					Kind: FactStatus, Subject: member.Subject,
					Fields:         map[string]FactValue{"status": {String: &status}, "phase": {String: &phase}, "owner": {String: &phase}},
					ObservedAt:     &observed,
					EvidenceRefIDs: []string{fmt.Sprintf("evidence_%02d", index)},
					SourceState:    SourceAvailable, Source: "ops", SourceVersion: "ops-v1",
				})
			}
			return CanonicalFactBundle{
				Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		// Two claims per fact the synthesis was handed (one per field), in
		// member order: the prod shape's two claims per repository.
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := make([]ClaimedFact, 0, chaos6743ClaimsPerMember*len(input.Facts.Facts))
			var cited []string
			for index, fact := range input.Facts.Facts {
				fields := [][2]string{{"status", "green"}, {"phase", "active"}, {"owner", "platform"}}
				perMember := chaos6743ClaimsPerMember
				if shape.claimsPerMember > 0 {
					perMember = shape.claimsPerMember
				}
				for field, value := range fields[:perMember] {
					id := fmt.Sprintf("claim_%02d_%d", index, field)
					claims = append(claims, ClaimedFact{
						ClaimID: id, Kind: fact.Kind, Subject: fact.Subject, Field: value[0],
						Value: ScalarValue{String: ptrString(value[1])},
					})
					if field >= perMember-shape.citedPerMember {
						cited = append(cited, id)
					}
				}
			}
			drivers := []DriverJudgment{}
			if len(cited) > 0 {
				drivers = append(drivers, DriverJudgment{
					DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "Repository status",
					Summary: "The repositories appear active.", AffectedSubjects: []SubjectRef{input.Facts.Facts[0].Subject},
					EvidenceRefIDs: []string{"evidence_00"}, ClaimedFactIDs: cited,
					Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true,
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "The organization appears to have these repositories.", CurrentState: "All repositories appear active.",
				StrongestPressures: []string{}, Drivers: drivers, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "The organization appears to have these repositories, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: shape.maxItems, MaxSerializedBytes: chaos6743MaxBytes,
		SynthesisDeadlineReserve: time.Second,
		Now:                      func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:              func() string { return "result_67430000" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func chaos6743Request() InvestigationRequest {
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = chaos6743ProdQuestion
	return request
}

func chaos6743Investigate(t *testing.T, shape chaos6743Shape) (InvestigationResult, int, *recordingTelemetry) {
	t.Helper()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6743Engine(t, &calls, telemetry, shape)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6743Request())
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused after %d syntheses: overrun=%s measured %d items / %d bytes", calls, refusal.Overrun, refusal.MeasuredItems, refusal.MeasuredBytes)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, calls, telemetry
}

// TestCHAOS6743InventoryKeepsEveryMemberByCuttingClaimDepth is the red/green
// pin. BASELINE (163629d2): two syntheses and 5 of 11 repositories served.
// FIX: one synthesis, all 11 repositories, one claim each, disclosed.
func TestCHAOS6743InventoryKeepsEveryMemberByCuttingClaimDepth(t *testing.T) {
	t.Parallel()
	result, calls, telemetry := chaos6743Investigate(t, chaos6743ProdShape)
	if result.Cohort == nil || len(result.Cohort.Members) != chaos6743Members {
		members := 0
		if result.Cohort != nil {
			members = len(result.Cohort.Members)
		}
		t.Fatalf("served %d of %d repositories after %d syntheses: members are the answer and must not be cut to pay for per-member claims", members, chaos6743Members, calls)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1: the claim-depth lever runs before any cohort retry", calls)
	}
	if result.Status != InvestigationPartial || result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial || !result.Coverage.Partial {
		t.Fatalf("status %q, completeness %q, coverage.partial=%v: a cut answer is partial", result.Status, result.Completeness.State, result.Coverage.Partial)
	}
	perMember := map[string]int{}
	for _, claim := range result.ClaimedFacts {
		perMember[claim.Subject.CanonicalID]++
	}
	for _, member := range result.Cohort.Members {
		if perMember[member.Subject.CanonicalID] != 1 {
			t.Fatalf("member %s keeps %d claims, want 1 (the cap 30 items admits for 11 members)", member.Subject.CanonicalID, perMember[member.Subject.CanonicalID])
		}
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Field != "status" {
			t.Fatalf("claim %s (%s) kept; each member keeps its FIRST claims in synthesis order", claim.ClaimID, claim.Field)
		}
	}
	declared := chaos6743Members * chaos6743ClaimsPerMember
	served := len(result.ClaimedFacts)
	var row *RequirementOutcomeRow
	for index := range result.Completeness.Outcomes {
		candidate := &result.Completeness.Outcomes[index]
		if candidate.Impact == contractsv1.ContextFabricAnswerImpactDepth && candidate.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems {
			row = candidate
		}
	}
	if row == nil || row.Declared != declared || row.Served != served || row.Outcome != contractsv1.ContextFabricRequirementNarrowed || !row.CauseObserved {
		t.Fatalf("outcome row = %+v, want narrowed/depth/items %d of %d claims", row, served, declared)
	}
	want := fmt.Sprintf("This answer shows %d of the %d facts it claimed about its %d subjects", served, declared, chaos6743Members)
	disclosed := false
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, want) && contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("no service-authored limitation starts %q; limitations = %q", want, result.Limitations)
	}
	if len(telemetry.claimDepthNarrowings) != 1 {
		t.Fatalf("claim depth lines = %d, want 1", len(telemetry.claimDepthNarrowings))
	}
	line := telemetry.claimDepthNarrowings[0]
	if !line.Served || line.ClaimsBefore != declared || line.ClaimsAfter != served || line.PerMemberCap != 1 || line.Members != chaos6743Members {
		t.Fatalf("claim depth line = %+v", line)
	}
}

// TestCHAOS6743CitedClaimsAreNeverCut: a claim a driver cites stays, whatever
// the cap. With one cited claim per member, K=1 keeps exactly the cited one.
func TestCHAOS6743CitedClaimsAreNeverCut(t *testing.T) {
	t.Parallel()
	shape := chaos6743ProdShape
	shape.citedPerMember = 1
	shape.maxItems = 23 // 11 members + 1 driver + 11 claims
	result, _, _ := chaos6743Investigate(t, shape)
	if result.Cohort == nil || len(result.Cohort.Members) != chaos6743Members {
		t.Fatalf("cohort cut: members must survive when K=1 fits")
	}
	kept := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		kept[claim.ClaimID] = true
		if claim.Field != "phase" {
			t.Fatalf("claim %s (%s) kept at K=1 over the member's cited claim", claim.ClaimID, claim.Field)
		}
	}
	if len(result.ClaimedFacts) != chaos6743Members {
		t.Fatalf("served %d claims, want %d (one cited claim per member)", len(result.ClaimedFacts), chaos6743Members)
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.ClaimedFactIDs {
			if !kept[id] {
				t.Fatalf("driver %s cites %s, which was cut", driver.DriverID, id)
			}
		}
	}
}

// TestCHAOS6743CohortHalvesOnlyWhenOneClaimPerMemberStillOverruns: at K=1 the
// document still overruns (11 members + 11 claims > 20), so the cohort retry
// runs as before.
func TestCHAOS6743CohortHalvesOnlyWhenOneClaimPerMemberStillOverruns(t *testing.T) {
	t.Parallel()
	shape := chaos6743ProdShape
	shape.maxItems = 20
	result, calls, telemetry := chaos6743Investigate(t, shape)
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2: one claim per member does not fit, so the cohort retry runs", calls)
	}
	if result.Cohort == nil || len(result.Cohort.Members) >= chaos6743Members {
		t.Fatalf("cohort not narrowed on the retry")
	}
	if len(telemetry.claimDepthNarrowings) == 0 || telemetry.claimDepthNarrowings[0].Served || telemetry.claimDepthNarrowings[0].Declined != ClaimDepthInsufficient {
		t.Fatalf("first claim depth line = %+v, want insufficient", telemetry.claimDepthNarrowings)
	}
}

// TestCHAOS6743LadderCapToOneThenHalveThenCapAgain walks the whole ladder with
// every claim on a real cohort member: 11 members x 3 claims against 14
// items. Step 1, the first document: the per-member cap falls to 1 and still
// overruns (11 + 11 > 14), so the lever declines. Step 2: the cohort halves
// 11 -> 5 and the retry synthesizes over 5 members. Step 3, the retried
// document (5 + 15 > 14): the cap falls to 1 again and fits (5 + 5). Each
// step's disclosure and per-member counts are asserted.
func TestCHAOS6743LadderCapToOneThenHalveThenCapAgain(t *testing.T) {
	t.Parallel()
	result, calls, telemetry := chaos6743Investigate(t, chaos6743Shape{members: chaos6743Members, maxItems: 14, claimsPerMember: 3})

	// Step 1: the first document's lever line.
	if len(telemetry.claimDepthNarrowings) != 2 {
		t.Fatalf("claim depth lines = %+v, want 2 (first document, retried document)", telemetry.claimDepthNarrowings)
	}
	first := telemetry.claimDepthNarrowings[0]
	if first.Served || first.Declined != ClaimDepthInsufficient || first.Pass != answerPassFirst || first.Members != 11 || first.ClaimsBefore != 33 || first.ClaimsAfter != 11 || first.PerMemberCap != 1 || first.ItemsAfter != 22 {
		t.Fatalf("step 1 line = %+v, want insufficient at cap 1: 33 -> 11 claims over 11 members, 22 items > 14", first)
	}
	// Step 2: the cohort retry ran over half the members.
	if calls != 2 || result.Cohort == nil || len(result.Cohort.Members) != 5 {
		t.Fatalf("calls=%d members=%v, want the retry over 5 of 11 members", calls, result.Cohort)
	}
	// Step 3: the retried document's lever served at cap 1.
	second := telemetry.claimDepthNarrowings[1]
	if !second.Served || second.Pass != answerPassSecond || second.Members != 5 || second.ClaimsBefore != 15 || second.ClaimsAfter != 5 || second.PerMemberCap != 1 || second.ItemsAfter != 10 {
		t.Fatalf("step 3 line = %+v, want served at cap 1: 15 -> 5 claims over 5 members, 10 items", second)
	}
	perMember := map[string]int{}
	for _, claim := range result.ClaimedFacts {
		perMember[claim.Subject.CanonicalID]++
		if claim.Field != "status" {
			t.Fatalf("claim %s (%s) kept; each member keeps its first claim", claim.ClaimID, claim.Field)
		}
	}
	for _, member := range result.Cohort.Members {
		if perMember[member.Subject.CanonicalID] != 1 {
			t.Fatalf("member %s keeps %d claims, want 1", member.Subject.CanonicalID, perMember[member.Subject.CanonicalID])
		}
	}
	if len(perMember) != 5 {
		t.Fatalf("claims about %d subjects, want exactly the 5 served members", len(perMember))
	}
	// The claim cut is disclosed in its exact composed sentence. The cohort
	// cut is disclosed structurally (complete=false, truncated=true); its
	// counted sentence needs a resolved membership cardinality, which this
	// frameless fixture does not produce with or without this lever.
	claimSentence, _ := contractsv1.ContextFabricClaimDepthLimitation(5, 15, 5, 1)
	sawClaim := false
	for _, limitation := range result.Limitations {
		if limitation == claimSentence {
			sawClaim = true
		}
	}
	if !sawClaim || result.Cohort.Complete || !result.Cohort.Truncated {
		t.Fatalf("claim sentence=%v cohort complete=%v truncated=%v; limitations = %q", sawClaim, result.Cohort.Complete, result.Cohort.Truncated, result.Limitations)
	}
	var claimRow bool
	for _, row := range result.Completeness.Outcomes {
		if row.Impact == contractsv1.ContextFabricAnswerImpactDepth && row.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems && row.Served == 5 && row.Declared == 15 {
			claimRow = true
		}
	}
	if !claimRow || result.Status != InvestigationPartial {
		t.Fatalf("claim row=%v status=%q; outcomes = %+v", claimRow, result.Status, result.Completeness.Outcomes)
	}
}

// The planner selects the LARGEST fitting cap, not the floor (codex r1 P3):
// 11 members x 3 claims against 33 items fits at K=2 (11 + 22), not K=3.
func TestCHAOS6743PlannerSelectsTheLargestFittingCap(t *testing.T) {
	t.Parallel()
	result, calls, telemetry := chaos6743Investigate(t, chaos6743Shape{members: chaos6743Members, maxItems: 33, claimsPerMember: 3})
	if calls != 1 || len(telemetry.claimDepthNarrowings) != 1 || !telemetry.claimDepthNarrowings[0].Served || telemetry.claimDepthNarrowings[0].PerMemberCap != 2 {
		t.Fatalf("calls=%d lines=%+v, want one served line at cap 2", calls, telemetry.claimDepthNarrowings)
	}
	perMember := map[string]int{}
	for _, claim := range result.ClaimedFacts {
		perMember[claim.Subject.CanonicalID]++
	}
	for _, member := range result.Cohort.Members {
		if perMember[member.Subject.CanonicalID] != 2 {
			t.Fatalf("member %s keeps %d claims, want 2", member.Subject.CanonicalID, perMember[member.Subject.CanonicalID])
		}
	}
}
