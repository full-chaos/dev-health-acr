package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func runWorkItemGapDispatch(t *testing.T, census WorkItemMembershipCensus, withMember bool) (InvestigationResult, *recordingTelemetry, *staticResultStore) {
	t.Helper()
	frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), nil, "").Frame
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(ctx)
			m := WorkItemMembershipResult{Census: census}
			if withMember {
				m.Members = []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}}
			}
			return lease, m, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationNoMatch, DirectJudgment: "Nothing found.", CurrentState: "Nothing found.", DeterministicAnswer: "Nothing found.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}),
		Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_tuple_gap_001" }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatal(err)
	}
	return result, telemetry, store
}

func TestWorkItemAllDeniedCensusServesDegradedDisclosure(t *testing.T) {
	result, telemetry, store := runWorkItemGapDispatch(t, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, CappedPopulation: 1675, AuthorizedPopulation: 0, DeniedPopulation: 1675}, false)
	if result.Status != InvestigationDegraded {
		t.Fatalf("status=%q want degraded", result.Status)
	}
	want := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 1675, Authorized: 0, Denied: 1675}.Limitation()
	if !strings.Contains(want, "1675") || !containsString(result.Limitations, want) {
		t.Fatalf("limitations=%q want %q", result.Limitations, want)
	}
	if containsString(result.Limitations, WorkItemMembershipLimitation()) {
		t.Fatalf("denied population was disclosed as unmeasured: %q", result.Limitations)
	}
	if result.Cohort != nil {
		t.Fatalf("cohort=%+v", result.Cohort)
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			t.Fatalf("a count of zero was asserted for a denied population: %+v", claim)
		}
	}
	if len(telemetry.workItemAuthorizationGaps) != 1 {
		t.Fatalf("gap events=%d", len(telemetry.workItemAuthorizationGaps))
	}
	event := telemetry.workItemAuthorizationGaps[0]
	if event.Reason != "none_authorized" || event.CensusState != WorkItemMembershipCensusExact || event.Observed != 1675 || event.Authorized != 0 || event.Denied != 1675 || event.ServedStatus != InvestigationDegraded || event.ServedMembers != 0 || !event.LimitationPresent {
		t.Fatalf("event=%+v", event)
	}
	if store.savedSemantic == nil || store.savedSemantic.State == nil || store.savedSemantic.State.WorkItemCensus == nil {
		t.Fatal("census not saved")
	}
	if census := store.savedSemantic.State.WorkItemCensus; census.State != WorkItemMembershipCensusUnmeasured || census.Value != 0 {
		t.Fatalf("saved census=%+v", census)
	}
}

func TestWorkItemPartiallyDeniedCensusDisclosesPartition(t *testing.T) {
	result, telemetry, _ := runWorkItemGapDispatch(t, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, CappedPopulation: 4, AuthorizedPopulation: 1, DeniedPopulation: 3}, true)
	want := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 4, Authorized: 1, Denied: 3}.Limitation()
	if !containsString(result.Limitations, want) {
		t.Fatalf("limitations=%q want %q", result.Limitations, want)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("cohort=%+v", result.Cohort)
	}
	if len(telemetry.workItemAuthorizationGaps) != 1 {
		t.Fatalf("gap events=%d", len(telemetry.workItemAuthorizationGaps))
	}
	event := telemetry.workItemAuthorizationGaps[0]
	if event.Reason != "partially_authorized" || event.Authorized != 1 || event.Denied != 3 || event.ServedMembers != 1 || !event.LimitationPresent {
		t.Fatalf("event=%+v", event)
	}
}

func TestWorkItemEmptyProjectCensusStaysUndisclosed(t *testing.T) {
	result, telemetry, _ := runWorkItemGapDispatch(t, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true}, false)
	if hasWorkItemAuthorizationGapLimitation(result.Limitations) || len(telemetry.workItemAuthorizationGaps) != 0 {
		t.Fatalf("empty project disclosed a gap: %q %d", result.Limitations, len(telemetry.workItemAuthorizationGaps))
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 0 {
		t.Fatalf("cohort=%+v", result.Cohort)
	}
}

func TestWorkItemAuthorizationGapOfRequiresMeasuredDenied(t *testing.T) {
	cases := map[string]struct {
		census WorkItemMembershipCensus
		want   bool
	}{
		"denied":           {WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, DeniedPopulation: 2}, true},
		"floor denied":     {WorkItemMembershipCensus{State: WorkItemMembershipCensusFloor, PopulationMeasured: true, AuthorizedPopulation: 1, DeniedPopulation: 2}, true},
		"none denied":      {WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true}, false},
		"not measured":     {WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, DeniedPopulation: 2}, false},
		"state unmeasured": {WorkItemMembershipCensus{State: WorkItemMembershipCensusUnmeasured, PopulationMeasured: true, DeniedPopulation: 2}, false},
	}
	for name, tc := range cases {
		if _, ok := workItemAuthorizationGapOf(tc.census); ok != tc.want {
			t.Errorf("%s: ok=%v want %v", name, ok, tc.want)
		}
	}
}

func TestWorkItemReuseRefusesStaleAuthorizationGapAnswers(t *testing.T) {
	census := &WorkItemTupleCensus{State: WorkItemMembershipCensusExact}
	stored := func(limitations ...string) InvestigationResult { return InvestigationResult{Limitations: limitations} }
	current := func(authorized, denied int) WorkItemMembershipResult {
		return WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: authorized, DeniedPopulation: denied, CappedPopulation: authorized + denied}}
	}
	gap := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 3, Denied: 3}
	if workItemReuseMembershipEqual(stored(), census, current(0, 3)) {
		t.Error("an answer served before denied members were disclosed was reused")
	}
	if workItemReuseMembershipEqual(stored(gap.Limitation()), census, current(0, 0)) {
		t.Error("a disclosure was reused once the members are not denied")
	}
	if !workItemReuseMembershipEqual(stored(), census, current(0, 0)) {
		t.Error("an undisclosed empty project stopped being reusable")
	}
	partial := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 4, Authorized: 1, Denied: 3}
	stale := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 3, Authorized: 1, Denied: 2}
	if workItemReuseMembershipEqual(stored(stale.Limitation()), &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 1, Retained: 1}, WorkItemMembershipResult{Census: current(1, 3).Census, Members: []WorkItemMembershipMember{{CanonicalID: "a"}}}) {
		t.Errorf("a disclosure with different counts was reused: %s vs %s", stale.Limitation(), partial.Limitation())
	}
}

func TestWorkItemAuthorizationGapLimitationFitsTheBound(t *testing.T) {
	text := workItemAuthorizationGap{Observed: 1 << 30, Authorized: 1 << 30, Denied: 1 << 30}.Limitation()
	if len([]rune(text)) > contractsv1.ContextFabricLimitationMaxLength {
		t.Fatalf("%d runes", len([]rune(text)))
	}
	if !strings.HasPrefix(fmt.Sprint(text), workItemAuthorizationGapPrefix) {
		t.Fatal("prefix")
	}
}

func TestServedGapLimitationIsNotDoubledWithTheUnmeasuredDisclosure(t *testing.T) {
	gap := workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 5, Denied: 5}
	stored := InvestigationResult{Limitations: []string{gap.Limitation()}}
	census := &WorkItemTupleCensus{Version: WorkItemTupleCensusVersion, State: WorkItemMembershipCensusUnmeasured, RequestedRepositoryScope: []string{}, AuthorizationDigest: strings.Repeat("a", 64)}
	served := ServeWorkItemTupleCensus(stored, census)
	if len(served.Limitations) != 1 || served.Limitations[0] != gap.Limitation() {
		t.Fatalf("limitations=%q", served.Limitations)
	}
	if served.Status == InvestigationDegraded {
		t.Fatalf("a stored answer's status was rewritten on read: %q", served.Status)
	}
	plain := ServeWorkItemTupleCensus(InvestigationResult{Limitations: []string{}}, census)
	if len(plain.Limitations) != 1 || plain.Limitations[0] != WorkItemMembershipLimitation() {
		t.Fatalf("an unmeasured census lost its own disclosure: %q", plain.Limitations)
	}
}

func TestWorkItemAuthorizationGapInfoLine(t *testing.T) {
	var out bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&out, nil)))
	telemetry.RecordWorkItemAuthorizationGap(context.Background(), storage.Principal{OrgID: "org-1"}, WorkItemAuthorizationGapEvent{Reason: "none_authorized", CensusState: WorkItemMembershipCensusExact, Observed: 7, Authorized: 2, Denied: 5, ServedStatus: InvestigationDegraded, ServedMembers: 3, LimitationPresent: true})
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"level": "INFO", "msg": "context fabric work item authorization gap", "org_id": "org-1", "reason": "none_authorized", "census_state": "exact", "observed_population": float64(7), "authorized_population": float64(2), "denied_population": float64(5), "served_status": "degraded", "served_members": float64(3), "limitation_disclosed": true}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s=%v want %v", key, line[key], value)
		}
	}
}

func TestWorkItemAuthorizationGapEventNamesTheReasonAndServedShape(t *testing.T) {
	census := &WorkItemTupleCensus{gap: &workItemAuthorizationGap{State: WorkItemMembershipCensusFloor, Observed: 9, Authorized: 4, Denied: 5}}
	served := InvestigationResult{Status: InvestigationComplete, Cohort: &Cohort{Members: []CohortMember{{}, {}}}, Limitations: []string{census.gap.Limitation()}}
	event, ok := newWorkItemAuthorizationGapEvent(census, served)
	if !ok || event.Reason != "partially_authorized" || event.CensusState != WorkItemMembershipCensusFloor || event.Observed != 9 || event.Authorized != 4 || event.Denied != 5 || event.ServedStatus != InvestigationComplete || event.ServedMembers != 2 || !event.LimitationPresent {
		t.Fatalf("event=%+v ok=%v", event, ok)
	}
	if _, ok := newWorkItemAuthorizationGapEvent(&WorkItemTupleCensus{}, served); ok {
		t.Fatal("a census without a gap produced an event")
	}
	none := &WorkItemTupleCensus{gap: &workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Observed: 5, Denied: 5}}
	if event, _ := newWorkItemAuthorizationGapEvent(none, InvestigationResult{}); event.Reason != "none_authorized" || event.LimitationPresent {
		t.Fatalf("event=%+v", event)
	}
}
