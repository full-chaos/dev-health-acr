package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The ENGINE-LEVEL proof that narration is bounded by the item budget.
//
// What this replaces is the reason it exists. The previous proof built a
// CohortDriverNarrationEvent by hand and fed it straight to the telemetry sink.
// That witnesses the formatter and nothing else: it cannot see whether the
// engine ever hands the allocation to the composer, whether the composer reads
// it, or whether the document the line describes is the document that gets
// served. Every one of those is a place the wiring can be absent while the
// hand-built event still formats perfectly.
//
// So this drives the REAL Engine.Investigate, over the REAL RankCohort (the
// engine ranks between the fact read and synthesis), and reads the emitted
// LINE TEXT rather than the event struct -- a field populated on the struct and
// never logged is not telemetry.

// narrationMemberIDs are the cohort's members. Six is not decoration: the
// static caps allow sixteen narrated members at three drivers each, so a
// fixture with one or two members cannot tell a budget-bounded narration from
// an unbounded one -- both narrate everybody. Six members with real facts
// produce more narration than a thirty-item ceiling can afford, which is the
// only shape where the bound is observable.
var narrationMemberIDs = []string{"A", "B", "C", "D", "E", "F"}

// narrationSeverities give the members DIFFERENT scores, so RankCohort produces
// a strict AttentionRank order. Without distinct scores the member narration
// selects is decided by a tie-break, and "the highest-ranked member" would not
// be a knowable fact about this fixture.
var narrationSeverities = map[string]string{
	"A": "critical", "B": "high", "C": "elevated",
	"D": "elevated", "E": "low", "F": "low",
}

// narrationCohortFacts is the canonical bundle the engine reads. Health drives
// the score difference; investment contributes equally to every member and
// exists to push available weight over the qualification threshold, so every
// member RANKS rather than only the ones with severe health.
func narrationCohortFacts() []CanonicalFact {
	facts := make([]CanonicalFact, 0, len(narrationMemberIDs)*2)
	for _, id := range narrationMemberIDs {
		facts = append(facts, healthFact(id, narrationSeverities[id]))
		facts = append(facts, investmentFact(id, balancedThemes(), 0))
	}
	return facts
}

// narrationCohort builds the cohort the graph reader returns.
//
// EvidenceRefIDs is set on every member ON PURPOSE and is asserted rather than
// assumed below: the composer SKIPS a member with no evidence refs, so a
// fixture that forgot them would narrate nothing and every count in this test
// would be a well-formed zero.
func narrationCohort(memberIDs []string) *Cohort {
	members := make([]CohortMember, 0, len(memberIDs))
	for index, id := range memberIDs {
		member := rankTestMember(id)
		// POOL rank, strictly increasing: the result validator requires a
		// cohort's members to carry distinct, ascending Rank values, and
		// rankTestMember gives every member Rank 1. This is pool order and
		// is NOT the attention order -- RankCohort writes AttentionRank
		// separately, and narration selects on that.
		member.Rank = index + 1
		member.EvidenceRefIDs = []string{"evidence_" + id + "_roster"}
		members = append(members, member)
	}
	return &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: members}
}

// narrationSynthesisDrivers are the drivers SYNTHESIS returns, before narration
// appends anything. Category "relationship" is outside the category->FactKind
// table, so they need no claimed fact and the fixture's item count stays
// exactly what this function writes.
func narrationSynthesisDrivers() []DriverJudgment {
	drivers := make([]DriverJudgment, 0, 2)
	for index := 0; index < 2; index++ {
		drivers = append(drivers, DriverJudgment{
			DriverID: "d_synth_" + strconv.Itoa(index), Standing: DriverPrincipal, Category: "relationship",
			Title: "Narration fixture driver", Summary: "A driver supplied by the narration fixture.",
			// A subject the cohort does NOT contain. The v1 bounds require
			// at least one affected subject, and naming a cohort member
			// here would move these two drivers into the member bucket --
			// so a stranger keeps synthesis' own items out of the counts
			// narration is measured by.
			AffectedSubjects: []SubjectRef{narrationStranger},
			EvidenceRefIDs:   []string{narrationEvidenceRefs()[0]},
			Derivation:       DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			Confidence: 0.9, Current: true,
		})
	}
	return drivers
}

// narrationStranger is a subject outside the cohort, so an item naming it is a
// global item rather than a member one.
var narrationStranger = SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_unowned", Label: "repo_unowned"}

// narrationEvidenceRefs is every evidence ref this fixture cites, in one place.
// The result carries all of them because a narrated judgment cites the evidence
// of the MEMBER it narrates, and which member that is depends on the ranking --
// so a result listing only the first member's ref would fail validation for a
// reason that has nothing to do with the budget.
func narrationEvidenceRefs() []string {
	refs := make([]string, 0, len(narrationMemberIDs))
	for _, id := range narrationMemberIDs {
		refs = append(refs, "evidence_"+id+"_roster")
	}
	return refs
}

// narrationSynthesisFindings are two global findings. They are here so the
// pre-narration document costs more than the cohort rows alone, which is what
// makes the ceiling bite at a realistic number rather than at one chosen to
// make the arithmetic tidy.
func narrationSynthesisFindings() []Finding {
	findings := make([]Finding, 0, 2)
	for index := 0; index < 2; index++ {
		findings = append(findings, Finding{
			FindingID: "f_global_" + strconv.Itoa(index), Kind: "relationship",
			Summary:  "A finding supplied by the narration fixture.",
			Subjects: nil, EvidenceRefIDs: []string{narrationEvidenceRefs()[0]},
		})
	}
	return findings
}

// narrationEngine wires the REAL engine over this fixture, logging through a
// real slog handler so a test can read the formatted line.
func narrationEngine(t *testing.T, cohort *Cohort, facts []CanonicalFact, maxItems int, sink *bytes.Buffer) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactHealth}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: facts, Coverage: availableCoverage(),
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Some teams are under pressure.",
				CurrentState: "Nominal.", StrongestPressures: []string{},
				Drivers: narrationSynthesisDrivers(), RemainingWork: narrationSynthesisFindings(),
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: narrationEvidenceRefs(),
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Some teams are under pressure, based on available context.",
				Warnings:            []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(sink, nil))),
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: maxItems,
		Now:         func() time.Time { return time.Unix(400, 0).UTC() },
		NewResultID: func() string { return "result_52280001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

var narrationCountPattern = regexp.MustCompile(`\bnarration_allocated_items=(-?\d+)`)

var narrationFieldPattern = regexp.MustCompile(`\b(narration_allocator|outcome)=("?)([a-z_]+)("?)`)

var narrationEmittedPattern = regexp.MustCompile(`\b(judgments_emitted|facts_minted|members_narrated|members_skipped_no_evidence)=(-?\d+)`)

// narrationLine returns the ONE cohort-driver-narration line.
//
// Exactly one, and that is an assertion rather than a convenience. Stage three
// can re-run synthesis, and a retry DISCARDS the first pass's held events -- so
// "more than one narration line reached the sink" would mean the held-telemetry
// discipline had broken, and reading the last of several would hide it.
func narrationLine(t *testing.T, emitted string) string {
	t.Helper()
	found := []string{}
	for _, candidate := range strings.Split(emitted, "\n") {
		if strings.Contains(candidate, "context fabric cohort driver narration") {
			found = append(found, candidate)
		}
	}
	if len(found) == 0 {
		t.Fatalf("no cohort driver narration line was emitted at all -- the engine did not reach the "+
			"composer this test is about.\nemitted:\n%s", emitted)
	}
	if len(found) != 1 {
		t.Fatalf("%d cohort driver narration lines were emitted, want exactly 1: a retry must DISCARD the "+
			"first pass's held events.\nlines:\n%s", len(found), strings.Join(found, "\n"))
	}
	return found[0]
}

func narrationFieldsOf(t *testing.T, line string) (map[string]string, map[string]int) {
	t.Helper()
	tokens := map[string]string{}
	for _, match := range narrationFieldPattern.FindAllStringSubmatch(line, -1) {
		tokens[match[1]] = match[3]
	}
	counts := map[string]int{}
	for _, match := range narrationEmittedPattern.FindAllStringSubmatch(line, -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("%s is not an integer on the emitted line: %v", match[1], err)
		}
		counts[match[1]] = value
	}
	if match := narrationCountPattern.FindStringSubmatch(line); match != nil {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("narration_allocated_items is not an integer: %v", err)
		}
		counts["narration_allocated_items"] = value
	} else {
		t.Fatalf("the emitted line carries no narration_allocated_items, so a reader cannot see the bound "+
			"that applied.\nline: %s", line)
	}
	if _, ok := tokens["narration_allocator"]; !ok {
		t.Fatalf("the emitted line carries no narration_allocator.\nline: %s", line)
	}
	return tokens, counts
}

// narratedDriversIn counts the drivers in the SERVED document that this
// composer minted, identified by not being one of synthesis' own ids.
func narratedDriversIn(result InvestigationResult) int {
	fromSynthesis := map[string]bool{}
	for _, driver := range narrationSynthesisDrivers() {
		fromSynthesis[driver.DriverID] = true
	}
	narrated := 0
	for _, driver := range result.Drivers {
		if !fromSynthesis[driver.DriverID] {
			narrated++
		}
	}
	return narrated
}

// TestTheItemBudgetBoundsNarrationThroughTheRealEngine is the proof of record.
//
// The arithmetic below is written out over the fixture's OWN literals rather
// than obtained by calling AllocateItems, so the expectation and the value
// under test cannot share a defect.
//
//	ceiling                       30
//	member rows committed          6   (the six cohort members)
//	after the commitment          24
//	active buckets                 2   (global and member; this cohort has no group axis)
//	narration grant       24 / (2+1) =  8
//
// Eight items of narration, and a narrated member costs its drivers AND one
// minted claim per driver -- so at the composer's three drivers per member the
// allowance is 8 / (3*2) = 1 member. The static caps alone would have allowed
// sixteen, which is every member this cohort has.
func TestTheItemBudgetBoundsNarrationThroughTheRealEngine(t *testing.T) {
	t.Parallel()
	const ceiling = 30
	const wantNarrationGrant = 8

	var sink bytes.Buffer
	cohort := narrationCohort(narrationMemberIDs)
	// PREMISE, asserted rather than assumed: the composer skips a member
	// with no evidence refs, so a fixture that lost them would narrate
	// nothing and every assertion below would pass on zeros.
	for _, member := range cohort.Members {
		if len(member.EvidenceRefIDs) == 0 {
			t.Fatalf("fixture member %q carries no evidence refs, so narration would skip it",
				member.Subject.CanonicalID)
		}
	}
	engine := narrationEngine(t, cohort, narrationCohortFacts(), ceiling, &sink)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}

	// PREMISE, asserted: RankCohort must have produced ranked drivers, or
	// the composer returns no_drivers and this test proves nothing about a
	// bound.
	ranked := 0
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			ranked += len(member.Drivers)
		}
	}
	if ranked == 0 {
		t.Fatal("the served cohort carries no ranked drivers at all: the fixture never reached narration")
	}

	line := narrationLine(t, sink.String())
	tokens, counts := narrationFieldsOf(t, line)

	// 1. The line names the ITEM BUDGET as the bound that bound.
	if got := tokens["narration_allocator"]; got != string(CohortDriverNarrationAllocatorPlanBudget) {
		t.Errorf("narration_allocator = %q, want %q: at this ceiling the allocator grant is strictly "+
			"tighter than the static caps, and naming the caps here is the regression this field "+
			"exists to expose.\nline: %s", got, CohortDriverNarrationAllocatorPlanBudget, line)
	}
	// 2. And it publishes the grant, not merely the fact of one.
	if got := counts["narration_allocated_items"]; got != wantNarrationGrant {
		t.Errorf("narration_allocated_items = %d, want %d (ceiling %d − %d committed rows = %d, "+
			"over 2 active buckets + narration).\nline: %s",
			got, wantNarrationGrant, ceiling, len(narrationMemberIDs), ceiling-len(narrationMemberIDs), line)
	}
	// 3. The bound ACTUALLY BOUND. The static caps allow sixteen narrated
	//    members; this cohort has six; the grant allows one. A count equal
	//    to the cohort size would mean the allocator was consulted, named,
	//    and then ignored.
	if got := counts["members_narrated"]; got != 1 {
		t.Errorf("members_narrated = %d, want 1: the caps alone would have narrated all %d members.\nline: %s",
			got, len(narrationMemberIDs), line)
	}
	if counts["judgments_emitted"] == 0 {
		t.Errorf("judgments_emitted = 0 on an arm that reports a positive allowance and a narrated "+
			"member.\nline: %s", line)
	}

	// 4. The line describes the DOCUMENT THAT WAS SERVED, not an earlier
	//    shape. This is the assertion a hand-built event can never make.
	if got, want := counts["judgments_emitted"], narratedDriversIn(result); got != want {
		t.Errorf("the line reports %d judgments emitted and the served document carries %d narrated "+
			"drivers.\nline: %s", got, want, line)
	}

	// 5. Those items are charged ONCE each. Narration appends to
	//    result.Drivers, so a composer that appended twice, or that left a
	//    driver behind in the cohort as well, would still emit a tidy line.
	ledger := contractsv1.ReconcileContextFabricResultItems(result)
	if !ledger.Reconciled() {
		t.Fatalf("the served document's account does not reconcile: status=%q %s",
			ledger.Status, ledger.Disagreement)
	}
	driverDebits := 0
	for _, debit := range ledger.Debits {
		if debit.Collection == contractsv1.ContextFabricChargedDrivers {
			driverDebits++
		}
	}
	if driverDebits != len(result.Drivers) {
		t.Errorf("the ledger charges %d driver debits for a document carrying %d drivers",
			driverDebits, len(result.Drivers))
	}

	// 6. And the whole finished answer fits the ceiling it was granted
	//    against -- which is the point of every number above.
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	budgeted := contractsv1.CountContextFabricResultItems(result).Budgeted()
	if budgeted > ceiling {
		t.Errorf("the served document carries %d budgeted items against a ceiling of %d", budgeted, ceiling)
	}
	if measurement.Overrun(ResponseBudget{MaxItems: ceiling, MaxSerializedBytes: 0}) != contractsv1.ContextFabricBudgetFits {
		t.Errorf("the served document does not fit its own ceiling of %d", ceiling)
	}
}

// TestARoomyCeilingLeavesTheStaticCapsInCharge is the tie convention, and it is
// the finding that made the field trustworthy.
//
// `plan_budget` is claimed ONLY when the grant is STRICTLY TIGHTER than the
// caps. An earlier version set it whenever a positive ceiling existed, so a
// generous ceiling still reported plan_budget -- and a field a reader would
// trust and should not is worse than no field at all.
func TestARoomyCeilingLeavesTheStaticCapsInCharge(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	engine := narrationEngine(t, narrationCohort(narrationMemberIDs), narrationCohortFacts(), 300, &sink)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}

	line := narrationLine(t, sink.String())
	tokens, counts := narrationFieldsOf(t, line)
	if got := tokens["narration_allocator"]; got != string(CohortDriverNarrationAllocatorStaticCaps) {
		t.Errorf("narration_allocator = %q, want %q: at a ceiling of 300 the caps are what bind, and a "+
			"positive ceiling alone must never claim the budget did.\nline: %s",
			got, CohortDriverNarrationAllocatorStaticCaps, line)
	}
	// The grant is still PUBLISHED on this arm -- an operator seeing
	// static_caps must still be able to read what the budget would have
	// allowed, or the two arms are not comparable.
	if counts["narration_allocated_items"] <= 0 {
		t.Errorf("narration_allocated_items = %d on an arm with a positive ceiling.\nline: %s",
			counts["narration_allocated_items"], line)
	}
	// And every member is narrated here, which is what makes the previous
	// test's count of one a MEASURED bound rather than a fixture artefact.
	if got := counts["members_narrated"]; got != len(narrationMemberIDs) {
		t.Errorf("members_narrated = %d at a roomy ceiling, want all %d: if this arm also narrated one "+
			"member, the bounded arm would prove nothing.\nline: %s",
			got, len(narrationMemberIDs), line)
	}
}

// TestACohortWithNoRankedDriversReportsNotApplicable keeps the third outcome on
// the record.
//
// The empty allocator value was once reported as `unclassified` -- the
// vocabulary's word for a corrupted or future value -- which conflates a
// documented normal outcome with corrupt input and drowns an operator filtering
// for real corruption in ordinary no-driver runs.
func TestACohortWithNoRankedDriversReportsNotApplicable(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	// No canonical facts at all, so RankCohort has nothing to rank from and
	// no member acquires a driver.
	engine := narrationEngine(t, narrationCohort(narrationMemberIDs), []CanonicalFact{}, 30, &sink)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}
	if got := narratedDriversIn(result); got != 0 {
		t.Fatalf("the served document carries %d narrated drivers on a cohort with none ranked", got)
	}

	line := narrationLine(t, sink.String())
	tokens, _ := narrationFieldsOf(t, line)
	if got := tokens["narration_allocator"]; got != string(CohortDriverNarrationAllocatorNotApplicable) {
		t.Errorf("narration_allocator = %q, want %q -- and never %q, which means corrupt input.\nline: %s",
			got, CohortDriverNarrationAllocatorNotApplicable, "unclassified", line)
	}
	if got := tokens["outcome"]; got != string(CohortDriverNarrationNoDrivers) {
		t.Errorf("outcome = %q, want %q.\nline: %s", got, CohortDriverNarrationNoDrivers, line)
	}
}
