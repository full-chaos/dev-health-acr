package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- CHAOS-5364: a served answer must never fail its OWN validator on
// duplicate driver ids. ---
//
// THE DEFECT THESE PINS WERE WRITTEN RED AGAINST. validateDrivers
// (contracts/v1) refuses a result whose drivers share a driver_id, and that
// refusal is classified ErrInvalidResult -> HTTP 500. SynthesisDraft.
// ValidateAgainst checks a driver's subjects, path ids, evidence ids, claim
// grounding and group-membership closure -- and never checked its identity.
// RuntimeAnswerSynthesizer.Synthesize then copies the model's drivers into
// result.Drivers verbatim. So a model that stamped two drivers with one id
// produced a draft that passed every synthesis gate and an answer ACR refused
// as its own defect.
//
// Observed live: req_cd3346de0bdb8228fc0e394f24926f67 on the kiac rig against
// dump dh_0906, where synthesis logged `outcome=success drivers=3` and the
// next line was `failure_stage=validation failure_classification=invalid_result
// validation_rule="...driver IDs must be unique"`.
//
// On the unfixed tree every test in this file that drives the producer fails
// with that exact string, which is what makes them pins rather than
// descriptions.

// duplicateIDDraft is closureFixture's valid draft carrying a SECOND,
// genuinely different contribution under the id the first already uses --
// the "two distinct contributions, one identity" reading. Category
// `narrative` is deliberately outside
// ContextFabricDriverCategoryRequiresClaimedFact, so the second entry needs
// no claim of its own and the fixture isolates identity from grounding.
func duplicateIDDraft() (SynthesisInput, SynthesisDraft) {
	input, draft := closureFixture()
	second := draft.Drivers[0]
	second.Standing = DriverContributing
	second.Category = string(contractsv1.ContextFabricDriverCategoryNarrative)
	second.ClaimedFactIDs = nil
	second.Title = "A second, different contribution"
	second.Summary = "A judgment the model made separately, stamped with an id it had already used."
	draft.Drivers = append(draft.Drivers, second)
	return input, draft
}

// TestSynthesizeServesADraftWhoseDriversShareAnID is the ticket case at the
// producer: the answer must SERVE, and it must serve through the very
// validator that produced the 500.
func TestSynthesizeServesADraftWhoseDriversShareAnID(t *testing.T) {
	t.Parallel()
	input, draft := duplicateIDDraft()
	authored := draft.Drivers[0].DriverID

	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the collision resolved at the producer rather than the whole answer lost", err)
	}
	// BOTH contributions survive. Dropping one would delete a judgment the
	// model made because it repeated a string -- the trade
	// deconflictDriverJudgmentID's own doc comment already settled.
	if len(result.Drivers) != 2 {
		t.Fatalf("Drivers = %d, want both contributions kept: %+v", len(result.Drivers), result.Drivers)
	}
	if result.Drivers[0].DriverID != authored {
		t.Errorf("first driver id = %q, want the model's own id %q kept -- the LATER entry is what yields", result.Drivers[0].DriverID, authored)
	}
	if result.Drivers[0].DriverID == result.Drivers[1].DriverID {
		t.Fatalf("both drivers still carry %q", result.Drivers[0].DriverID)
	}
	if result.Drivers[1].Title != "A second, different contribution" {
		t.Errorf("second driver = %+v, want the distinct contribution preserved, not the first one duplicated", result.Drivers[1])
	}
	// The exact validator that refused the live request.
	assertResultValidatesOnDriverIdentity(t, input, result)
}

// TestSynthesizeCollapsesARestatedDriver is the other reading: two entries
// that are the same contribution written twice. Nothing is lost by keeping
// one, and keeping two under deconflicted ids would publish a contribution
// stack that claims two judgments where the model made one.
func TestSynthesizeCollapsesARestatedDriver(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.Drivers = append(draft.Drivers, draft.Drivers[0])
	authored := draft.Drivers[0].DriverID

	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(result.Drivers) != 1 || result.Drivers[0].DriverID != authored {
		t.Fatalf("Drivers = %+v, want the single contribution the model actually made, under its own id %q", result.Drivers, authored)
	}
	assertResultValidatesOnDriverIdentity(t, input, result)

	if len(telemetry.driverIdentityCollisions) != 1 {
		t.Fatalf("driverIdentityCollisions = %v, want exactly one record per Synthesize call", telemetry.driverIdentityCollisions)
	}
	got := telemetry.driverIdentityCollisions[0]
	// The KIND is the point. A reader who sees only a total cannot tell a
	// harmless restatement from a stack whose entries lost their identity.
	if got.Restated != 1 || got.Reidentified != 0 {
		t.Errorf("collisions = %+v, want Restated 1 / Reidentified 0 -- an identical entry is a restatement, never a second judgment", got)
	}
}

// TestSynthesizeReportsDriverIdentityCollisionKinds is the reidentified half
// of the same observable.
func TestSynthesizeReportsDriverIdentityCollisionKinds(t *testing.T) {
	t.Parallel()
	input, draft := duplicateIDDraft()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.driverIdentityCollisions) != 1 {
		t.Fatalf("driverIdentityCollisions = %v, want exactly one record", telemetry.driverIdentityCollisions)
	}
	got := telemetry.driverIdentityCollisions[0]
	if got.Restated != 0 || got.Reidentified != 1 || got.Total() != 1 {
		t.Errorf("collisions = %+v (total %d), want Restated 0 / Reidentified 1", got, got.Total())
	}
}

// TestSynthesizeReportsAnExplicitZeroWhenNoDriverIdentityCollided is the
// UNGUARDED half of the observable, and it is the one that catches the
// regression this whole change is exposed to: a producer wired past
// ResolveDriverIdentityCollisions emits an ordinary clean answer and, if the
// line were gated on a non-zero count, an ordinary clean LOG. The zero is the
// statement that identity was checked.
func TestSynthesizeReportsAnExplicitZeroWhenNoDriverIdentityCollided(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.driverIdentityCollisions) != 1 {
		t.Fatalf("driverIdentityCollisions = %v, want exactly ONE record on a clean pass -- an absent line and a checked-nothing-collided line must not be the same signal", telemetry.driverIdentityCollisions)
	}
	if got := telemetry.driverIdentityCollisions[0]; got.Total() != 0 || got.Restated != 0 || got.Reidentified != 0 {
		t.Errorf("collisions = %+v, want an explicit zero", got)
	}
}

// TestDiscoveredCohortRankingServesWhenTheModelDuplicatesADriverID drives the
// family the live sightings came from: a discovered cohort whose ranking
// narrates its own drivers, under a model draft that both duplicated an id
// AND picked the cohort's own namespace for it -- the both-ends collision.
//
// The chain is the production one: the producer
// (RuntimeAnswerSynthesizer.Synthesize) resolves the model's own identities,
// narration is then seeded from those resolved drivers exactly as
// synthesizeAndAssemble seeds it from result.Drivers, and the combined array
// is what validateDrivers judges. Every step but the append is real code.
func TestDiscoveredCohortRankingServesWhenTheModelDuplicatesADriverID(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())
	subject := ranked.Members[0].Subject

	// The id narration will mint for rank 1, position 1 -- a string the
	// synthesis contract nowhere reserves, so a model may author it, and here
	// it authors it TWICE.
	collidingID := cohortDriverJudgmentID(ranked.Members[0], 0)
	modelDriver := DriverJudgment{
		DriverID:         collidingID,
		Standing:         DriverPrincipal,
		Category:         string(contractsv1.ContextFabricDriverCategoryNarrative),
		Title:            "A model-authored driver in the cohort namespace",
		Summary:          "Legal model output: nothing reserves the cohort-driver- prefix.",
		AffectedSubjects: []SubjectRef{subject},
		EvidenceRefIDs:   member.EvidenceRefIDs,
		Derivation:       DerivationModelExtracted,
		EpistemicStatus:  EpistemicInferred,
		Confidence:       0.5,
		Current:          true,
	}
	secondModelDriver := modelDriver
	secondModelDriver.Standing = DriverContributing
	secondModelDriver.Title = "A second model-authored driver under the same id"
	secondModelDriver.Summary = "A different judgment the model stamped with the id it had already used."

	input := SynthesisInput{
		Request: validInvestigationRequest(),
		Interpretation: InterpretedQuestion{
			Shape: ShapeDiscoveredCohort, RequestedJudgment: "ranking",
			TimeContext:      TimeContext{Axis: TemporalCurrent},
			FactRequirements: []FactRequirement{{Kind: FactHealth}},
		},
		Graph: GraphContext{
			Resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{subject}},
			Paths:      []RelationshipPath{}, EvidenceRefIDs: member.EvidenceRefIDs,
			Cohort:   ranked,
			Coverage: availableCoverage(),
		},
		Facts: CanonicalFactBundle{Facts: facts, Coverage: availableCoverage(), Version: "ops-v1"},
	}
	draft := SynthesisDraft{
		Status: InvestigationComplete, DirectJudgment: "Team A leads the ranking.",
		CurrentState:       "Team A carries the highest attention rank.",
		StrongestPressures: []string{"Health risk is high for Team A."},
		Drivers:            []DriverJudgment{modelDriver, secondModelDriver},
		RemainingWork:      []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: member.EvidenceRefIDs,
		DeterministicAnswer: "Team A leads the ranking on health risk.", Warnings: []string{},
	}

	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the discovered-cohort answer served", err)
	}
	if len(result.Drivers) != 2 {
		t.Fatalf("Drivers after synthesis = %d, want both model contributions kept: %+v", len(result.Drivers), result.Drivers)
	}

	narrated, mintedClaims, event := narrateCohortDriverJudgments(ranked, result.Drivers, len(result.ClaimedFacts), citations, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationEmitted || len(narrated) == 0 {
		t.Fatalf("expected narrated judgments, got event=%+v narrated=%v", event, narrated)
	}
	// Both appends, in synthesizeAndAssemble's own order: a narrated driver
	// cites the claim narration minted for it, so appending the judgments
	// without the claims would fail on the citation closure rather than on
	// identity.
	result.Drivers = append(result.Drivers, narrated...)
	result.ClaimedFacts = append(result.ClaimedFacts, mintedClaims...)
	if len(result.Drivers) != 2+len(narrated) {
		t.Fatalf("combined stack = %d drivers, want every contribution from both producers", len(result.Drivers))
	}
	assertResultValidatesOnDriverIdentity(t, input, result)
}

// TestDeconflictDriverJudgmentIDRespectsTheIDLengthBound is the negative
// control on the trim: a model may author a driver_id sitting exactly on
// ContextFabricModelMintedIDMaxLength, and an untrimmed base + suffix would
// deconflict it into an id the result validator refuses -- swapping the
// collision 500 for a length 500, which is not a fix.
func TestDeconflictDriverJudgmentIDRespectsTheIDLengthBound(t *testing.T) {
	t.Parallel()
	base := strings.Repeat("d", contractsv1.ContextFabricModelMintedIDMaxLength)
	taken := map[string]struct{}{base: {}}

	first := deconflictDriverJudgmentID(base, taken)
	assertLegalDriverID(t, first)
	if first == base {
		t.Fatalf("deconflict returned the taken base id")
	}
	taken[first] = struct{}{}

	second := deconflictDriverJudgmentID(base, taken)
	assertLegalDriverID(t, second)
	if second == base || second == first {
		t.Fatalf("deconflict returned an already-taken id")
	}
	// The trim must not make two different long bases collapse onto one
	// variant: the digest is taken over the FULL base, not the trimmed one.
	other := strings.Repeat("d", contractsv1.ContextFabricModelMintedIDMaxLength-1) + "e"
	otherFirst := deconflictDriverJudgmentID(other, map[string]struct{}{other: {}})
	assertLegalDriverID(t, otherFirst)
	if otherFirst == first {
		t.Fatalf("two different bases sharing a trimmed prefix deconflicted to the same id %q", first)
	}
}

// TestResolveDriverIdentityCollisionsIsOrderPreservingAndDeterministic is the
// helper's own unit: order is the contribution stack's reading order
// (principal first), and a replay of the same draft must produce the same ids
// as the stored answer.
func TestResolveDriverIdentityCollisionsIsOrderPreservingAndDeterministic(t *testing.T) {
	t.Parallel()
	_, draft := duplicateIDDraft()

	firstPass, firstCounts := ResolveDriverIdentityCollisions(draft.Drivers)
	secondPass, secondCounts := ResolveDriverIdentityCollisions(draft.Drivers)
	if firstCounts != secondCounts {
		t.Fatalf("counts differ across passes: %+v then %+v", firstCounts, secondCounts)
	}
	if len(firstPass) != len(secondPass) {
		t.Fatalf("lengths differ across passes: %d then %d", len(firstPass), len(secondPass))
	}
	for i := range firstPass {
		if firstPass[i].DriverID != secondPass[i].DriverID {
			t.Errorf("driver %d: %q then %q -- deconfliction must be replay-stable", i, firstPass[i].DriverID, secondPass[i].DriverID)
		}
		if firstPass[i].Title != draft.Drivers[i].Title {
			t.Errorf("driver %d reordered: got %q, want %q", i, firstPass[i].Title, draft.Drivers[i].Title)
		}
	}
	if firstPass[0].Standing != DriverPrincipal {
		t.Errorf("first driver standing = %q, want the principal contribution still first", firstPass[0].Standing)
	}
}

// TestResolveDriverIdentityCollisionsLeavesACleanDraftAlone is the
// discrimination control: the fix must be inert on the ordinary answer.
func TestResolveDriverIdentityCollisionsLeavesACleanDraftAlone(t *testing.T) {
	t.Parallel()
	_, draft := duplicateIDDraft()
	draft.Drivers[1].DriverID = "driver_87654321"

	resolved, counts := ResolveDriverIdentityCollisions(draft.Drivers)
	if counts.Total() != 0 {
		t.Errorf("counts = %+v, want zero on a draft whose ids are already unique", counts)
	}
	if len(resolved) != len(draft.Drivers) {
		t.Fatalf("resolved %d drivers, want %d", len(resolved), len(draft.Drivers))
	}
	for i := range resolved {
		if resolved[i].DriverID != draft.Drivers[i].DriverID {
			t.Errorf("driver %d id changed from %q to %q on a clean draft", i, draft.Drivers[i].DriverID, resolved[i].DriverID)
		}
	}
}

// assertResultValidatesOnDriverIdentity runs the ACTUAL validator that
// produced the live 500 and reports the identity axis specifically, so a
// failure names this defect rather than "the result did not validate".
//
// The engine-owned fields are stamped here because the SYNTHESIZER does not
// own them: synthesizeAndAssemble assigns SchemaVersion/ResultID/RequestID/
// GeneratedAt/Question/Interpretation/SubjectResolution/Cohort after
// Synthesize returns, and a result missing them fails on identity bounds
// before the driver rule is ever reached -- which would make these pins pass
// or fail for a reason that has nothing to do with driver identity. The
// values mirror that call site's own assignments.
func assertResultValidatesOnDriverIdentity(t *testing.T, input SynthesisInput, result InvestigationResult) {
	t.Helper()
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = "result_5364pin"
	result.RequestID = "request_5364pin"
	result.GeneratedAt = time.Now().UTC()
	result.Question = input.Request.Question
	result.Interpretation = input.Interpretation
	result.SubjectResolution = input.Graph.Resolution
	if result.Cohort == nil {
		result.Cohort = input.Graph.Cohort
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	seen := make(map[string]int, len(result.Drivers))
	for _, driver := range result.Drivers {
		seen[driver.DriverID]++
		assertLegalDriverID(t, driver.DriverID)
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("driver id %q appears %d times -- validateDrivers refuses the whole result for a duplicate", id, count)
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() = %v, want nil -- this is the validator whose %q refusal surfaced as invalid_result/500", err, "driver IDs must be unique")
	}
}

// assertLegalDriverID measures the id in RUNES, the same unit
// stringLengthBetween uses on the contract side -- a byte count would pass an
// id the validator refuses.
func assertLegalDriverID(t *testing.T, id string) {
	t.Helper()
	length := utf8.RuneCountInString(id)
	if length < contractsv1.ContextFabricModelMintedIDMinLength || length > contractsv1.ContextFabricModelMintedIDMaxLength {
		t.Errorf("driver id %q length = %d runes, outside the contract's [%d,%d]", id, length, contractsv1.ContextFabricModelMintedIDMinLength, contractsv1.ContextFabricModelMintedIDMaxLength)
	}
}
