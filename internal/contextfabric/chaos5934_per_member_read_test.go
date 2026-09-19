package contextfabric

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func rankTestProject(id string) SubjectRef {
	return SubjectRef{Kind: SubjectProject, CanonicalID: "project:" + id, Label: id}
}

func rankTestProjectMember(id string) CohortMember {
	return CohortMember{Subject: rankTestProject(id), Rank: 1, InclusionReasons: []string{"matched"}}
}

func readsFor(kind FactKind, subjects ...SubjectRef) FactReadSubjects {
	reads := FactReadSubjects{}
	reads.add(kind, subjects)
	return reads
}

func hasMissing(member CohortMember, signal string) bool {
	for _, name := range member.MissingSignals {
		if name == signal {
			return true
		}
	}
	return false
}

// A member whose own subject was never read for operational_deficiencies is
// not credited the zero, even though the investigation-wide state for that
// kind is available; a member whose own subject was read is.
func TestRankCohortWithReads_ZeroNeedsTheMembersOwnRead(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{rankTestProjectMember("read"), rankTestProjectMember("unread")}}
	reads := readsFor(FactOperationalDeficiencies, rankTestProject("read"))
	got, event, _ := RankCohortWithReads(cohort, nil, availableCoverage(), reads)
	if hasMissing(got.Members[0], RankingSignalDeficiencySeverity) {
		t.Fatalf("read member missing = %v, want the zero credited", got.Members[0].MissingSignals)
	}
	if !hasMissing(got.Members[1], RankingSignalDeficiencySeverity) {
		t.Fatalf("unread member missing = %v, want %s missing", got.Members[1].MissingSignals, RankingSignalDeficiencySeverity)
	}
	if event.DeficiencyZeroWithheld != 1 || !event.ReadAttributionCarried {
		t.Fatalf("event withheld=%d carried=%v, want 1/true", event.DeficiencyZeroWithheld, event.ReadAttributionCarried)
	}
	if event.SignalsAvailable[RankingSignalDeficiencySeverity] != 1 {
		t.Fatalf("SignalsAvailable = %v, want exactly one member credited", event.SignalsAvailable)
	}
}

// Attribution present but empty: nobody was read, nobody is credited.
func TestRankCohortWithReads_EmptyReadsCreditNoOne(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{rankTestProjectMember("a"), rankTestProjectMember("b")}}
	_, event, _ := RankCohortWithReads(cohort, nil, availableCoverage(), FactReadSubjects{})
	if event.SignalsAvailable[RankingSignalDeficiencySeverity] != 0 || event.DeficiencyZeroWithheld != 2 {
		t.Fatalf("event = %#v, want no credit and 2 withheld", event)
	}
}

// The member's own read does not override the batch state: a truncated read
// still cannot promise zero fired rules.
func TestRankCohortWithReads_OwnReadStillNeedsCleanBatchState(t *testing.T) {
	t.Parallel()
	subject := rankTestProject("a")
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{{Subject: subject, Rank: 1}}}
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceTruncated}}}
	got, event, _ := RankCohortWithReads(cohort, nil, coverage, readsFor(FactOperationalDeficiencies, subject))
	if !hasMissing(got.Members[0], RankingSignalDeficiencySeverity) {
		t.Fatalf("missing = %v, want deficiency missing on a truncated batch", got.Members[0].MissingSignals)
	}
	if event.DeficiencyZeroWithheld != 0 {
		t.Fatalf("withheld = %d, want 0 (the member was read; the batch state is what refused)", event.DeficiencyZeroWithheld)
	}
}

// A fired rule attributed to the member is the member's own evidence and is
// scored whatever the read set says.
func TestRankCohortWithReads_OwnFiredRuleIsNotWithheld(t *testing.T) {
	t.Parallel()
	subject := rankTestProject("a")
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{{Subject: subject, Rank: 1}}}
	facts := []CanonicalFact{{Kind: FactOperationalDeficiencies, Subject: subject, Fields: map[string]FactValue{"severity": StringFactValue("critical")}}}
	got, event, _ := RankCohortWithReads(cohort, facts, availableCoverage(), FactReadSubjects{})
	if hasMissing(got.Members[0], RankingSignalDeficiencySeverity) {
		t.Fatalf("missing = %v, want the member's own fired rule scored", got.Members[0].MissingSignals)
	}
	if event.DeficiencyZeroWithheld != 0 {
		t.Fatalf("withheld = %d, want 0", event.DeficiencyZeroWithheld)
	}
}

// A read of one kind never credits another kind's family.
func TestRankCohortWithReads_ReadIsPerKind(t *testing.T) {
	t.Parallel()
	subject := rankTestProject("a")
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{{Subject: subject, Rank: 1}}}
	got, _, _ := RankCohortWithReads(cohort, nil, availableCoverage(), readsFor(FactHealth, subject))
	if !hasMissing(got.Members[0], RankingSignalDeficiencySeverity) {
		t.Fatalf("missing = %v, want deficiency missing: only health was read", got.Members[0].MissingSignals)
	}
}

// No attribution carried: the coverage-only rule stands and the event says so.
func TestRankCohortWithReads_NilReadsKeepsCoverageOnlyRule(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{rankTestProjectMember("a")}}
	got, event, _ := RankCohortWithReads(cohort, nil, availableCoverage(), nil)
	if hasMissing(got.Members[0], RankingSignalDeficiencySeverity) {
		t.Fatalf("missing = %v, want the coverage-only zero", got.Members[0].MissingSignals)
	}
	if event.ReadAttributionCarried || event.DeficiencyZeroWithheld != 0 {
		t.Fatalf("event = %#v, want carried=false withheld=0", event)
	}
}

func TestRankCohortWithReads_FormulaVersionMovedWithTheRule(t *testing.T) {
	t.Parallel()
	if RankingFormulaVersion != "cohort-ranking.v3" {
		t.Fatalf("RankingFormulaVersion = %q, want cohort-ranking.v3", RankingFormulaVersion)
	}
}

func TestFactReadSubjects_MergeGroupBundleUnionsAttribution(t *testing.T) {
	t.Parallel()
	a, b := rankTestProject("a"), rankTestProject("b")
	into := CanonicalFactBundle{ReadSubjects: readsFor(FactOperationalDeficiencies, a)}
	group := CanonicalFactBundle{ReadSubjects: readsFor(FactOperationalDeficiencies, b)}
	if mergeGroupBundle(&into, group, "org_1") {
		t.Fatal("mergeGroupBundle refused a composable pair")
	}
	if !into.ReadSubjects.covers(FactOperationalDeficiencies, a) || !into.ReadSubjects.covers(FactOperationalDeficiencies, b) {
		t.Fatalf("merged reads = %v, want both subjects", into.ReadSubjects)
	}
	if group.ReadSubjects.covers(FactOperationalDeficiencies, a) {
		t.Fatal("merge mutated the group bundle's own attribution")
	}
}

func deficienciesRegistry(t *testing.T) *FactCapabilityRegistry {
	t.Helper()
	provider := &factProviderStub{
		capability: FactCapability{Kind: FactOperationalDeficiencies, Name: "ops-deficiencies", Version: "def-v1", SupportedSubjectKinds: []SubjectKind{SubjectTeam}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
		result:     FactProviderResult{State: SourceAvailable, Watermark: "wm", Version: "def-v1", Facts: []CanonicalFact{}},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	return registry
}

// Certified at the real producer: the registry's own read of a cohort of
// projects, scoped to the one team subject the provider supports, attributes
// the read to that team only, and the ranking built from that bundle credits
// no project with the team's clean read.
func TestFactCapabilityRegistry_ProjectCohortIsNotCreditedTheAnchorTeamsRead(t *testing.T) {
	t.Parallel()
	anchor := rankTestSubject("anchor")
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{rankTestProjectMember("proj-a"), rankTestProjectMember("proj-b")}}
	request := canonicalFactRequest(anchor, FactOperationalDeficiencies)
	request.Cohort = cohort
	bundle, err := deficienciesRegistry(t).ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if state, _ := coverageState(bundle.Coverage, FactOperationalDeficiencies); state != SourceAvailable {
		t.Fatalf("coverage state = %q, want available (the premise of the leak)", state)
	}
	if !bundle.ReadSubjects.covers(FactOperationalDeficiencies, anchor) {
		t.Fatalf("ReadSubjects = %v, want the anchor team covered", bundle.ReadSubjects)
	}
	got, event, _ := RankCohortWithReads(cohort, bundle.Facts, bundle.Coverage, bundle.ReadSubjects)
	for _, member := range got.Members {
		if !hasMissing(member, RankingSignalDeficiencySeverity) {
			t.Fatalf("project %s credited the anchor team's read: missing=%v", member.Subject.CanonicalID, member.MissingSignals)
		}
	}
	if event.DeficiencyZeroWithheld != 2 {
		t.Fatalf("withheld = %d, want 2", event.DeficiencyZeroWithheld)
	}
}

// The same producer over a team cohort: every member's own subject was read,
// so each keeps the zero.
func TestFactCapabilityRegistry_TeamCohortMembersKeepTheirOwnZero(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("t1"), rankTestMember("t2")}}
	request := canonicalFactRequest(rankTestSubject("t1"), FactOperationalDeficiencies)
	request.Subjects = nil
	request.Cohort = cohort
	bundle, err := deficienciesRegistry(t).ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	got, event, _ := RankCohortWithReads(cohort, bundle.Facts, bundle.Coverage, bundle.ReadSubjects)
	for _, member := range got.Members {
		if hasMissing(member, RankingSignalDeficiencySeverity) {
			t.Fatalf("team %s lost its own read's zero: missing=%v", member.Subject.CanonicalID, member.MissingSignals)
		}
	}
	if event.DeficiencyZeroWithheld != 0 {
		t.Fatalf("withheld = %d, want 0", event.DeficiencyZeroWithheld)
	}
}

// A pruned kind reads nothing, so it records no attribution.
func TestFactCapabilityRegistry_PrunedKindRecordsNoReads(t *testing.T) {
	t.Parallel()
	project := rankTestProject("proj-a")
	request := canonicalFactRequest(project, FactOperationalDeficiencies)
	bundle, err := deficienciesRegistry(t).ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.ReadSubjects) != 0 {
		t.Fatalf("ReadSubjects = %v, want none for a pruned kind", bundle.ReadSubjects)
	}
	if bundle.ReadSubjects == nil || !reflect.DeepEqual(bundle.ReadSubjects, FactReadSubjects{}) {
		t.Fatalf("ReadSubjects = %#v, want a non-nil empty attribution", bundle.ReadSubjects)
	}
}

// The ranked line carries the decision's values, not only its keys.
func TestCohortRankedLineCarriesTheReadAttributionDecision(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCohortRanked(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			CohortRankedEvent{CohortKind: SubjectProject, MemberCount: 3, FormulaVersion: RankingFormulaVersion, ReadAttributionCarried: true, DeficiencyZeroWithheld: 3})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if got := records[0]["read_attribution_carried"]; got != true {
		t.Fatalf("read_attribution_carried = %v, want true", got)
	}
	if got := records[0]["deficiency_zero_withheld"]; got != float64(3) {
		t.Fatalf("deficiency_zero_withheld = %v, want 3", got)
	}
}

// An unread member on a batch that never promised a zero is not "withheld":
// the batch state, not the missing read, is what refused it.
func TestRankCohortWithReads_UnreadMemberOnRefusedBatchIsNotCountedWithheld(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{rankTestProjectMember("proj-a")}}
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceTruncated}}}
	_, event, _ := RankCohortWithReads(cohort, nil, coverage, FactReadSubjects{})
	if event.DeficiencyZeroWithheld != 0 {
		t.Fatalf("withheld = %d, want 0", event.DeficiencyZeroWithheld)
	}
}

// A group read composed into a turn that carried no attribution of its own
// hands the turn the group's attribution.
func TestMergeGroupBundle_AdoptsGroupAttributionWhenTurnCarriesNone(t *testing.T) {
	t.Parallel()
	subject := rankTestProject("proj-a")
	into := CanonicalFactBundle{}
	group := CanonicalFactBundle{ReadSubjects: readsFor(FactOperationalDeficiencies, subject)}
	if mergeGroupBundle(&into, group, "org_1") {
		t.Fatal("mergeGroupBundle refused a composable pair")
	}
	if !into.ReadSubjects.covers(FactOperationalDeficiencies, subject) {
		t.Fatalf("merged reads = %v, want the group's subject", into.ReadSubjects)
	}
}

// A bundle the engine ranks carries its read attribution into the ranking:
// the served cohort's members are attributed per subject, end to end.
func TestEngineRanksAgainstTheBundlesReadAttribution(t *testing.T) {
	t.Parallel()
	readProject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:read", Label: "Read"}
	unreadProject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:unread", Label: "Unread"}
	cohort := &Cohort{
		Kind: SubjectProject, Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: readProject, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: unreadProject, Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "projects_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:        []CanonicalFact{},
				Coverage:     Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:      "ops-v1",
				Versions:     map[FactKind]string{},
				Watermarks:   map[FactKind]string{},
				ReadSubjects: readsFor(FactOperationalDeficiencies, readProject),
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Some projects are under pressure.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Some projects are under pressure.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: &resultStoreStub{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_59340001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_59340001"
	request.Question = "which projects are struggling?"
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.cohortRanked) != 1 {
		t.Fatalf("cohortRanked events = %#v, want exactly 1", telemetry.cohortRanked)
	}
	event := telemetry.cohortRanked[0]
	if !event.ReadAttributionCarried || event.DeficiencyZeroWithheld != 1 {
		t.Fatalf("event carried=%v withheld=%d, want true/1", event.ReadAttributionCarried, event.DeficiencyZeroWithheld)
	}
	if event.SignalsAvailable[RankingSignalDeficiencySeverity] != 1 {
		t.Fatalf("SignalsAvailable = %v, want exactly the read member credited", event.SignalsAvailable)
	}
}

// The narrowing retry re-ranks the surviving members against the same read
// attribution the first pass used.
func TestNarrowSynthesisInputReRanksAgainstTheBundlesReadAttribution(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "c1", "d1")
	params := synthesisAssemblyParams{
		Graph: GraphContext{Cohort: cohort},
		Facts: CanonicalFactBundle{
			Coverage:     Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceAvailable}}},
			ReadSubjects: FactReadSubjects{},
		},
	}
	result := narrowSynthesisInput(params, &AnswerPlan{})
	if !result.Narrow {
		t.Fatal("result.Narrow = false, want a re-rank")
	}
	if !result.Ranked.ReadAttributionCarried || result.Ranked.DeficiencyZeroWithheld != 2 {
		t.Fatalf("Ranked carried=%v withheld=%d, want true/2 (the two survivors, none of them read)", result.Ranked.ReadAttributionCarried, result.Ranked.DeficiencyZeroWithheld)
	}
}
