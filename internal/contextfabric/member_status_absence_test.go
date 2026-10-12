package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type incidentStatusRow struct {
	anchor    SubjectKind
	graph     GraphContext
	status    InvestigationStatus
	frameKind SubjectKind
}

func investigateIncidentStatus(t *testing.T, row incidentStatusRow) InvestigationResult {
	t.Helper()
	frameKind := row.frameKind
	if frameKind == "" {
		frameKind = SubjectIncident
	}
	frame := memberStatusQuestionFrame(frameKind, "open")
	graph := row.graph
	graph.Paths, graph.DriverCandidates, graph.FactRequirements, graph.EvidenceRefIDs = []RelationshipPath{}, []DriverJudgment{}, []FactRequirement{}, []string{}
	if graph.Coverage.Sources == nil {
		graph.Coverage.Sources = []SourceObservation{}
	}
	if graph.Coverage.DegradedReasons == nil {
		graph.Coverage.DegradedReasons = []string{}
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: row.anchor, ScopeAnchorTerm: "Vesper"}}},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{{Kind: row.anchor, CanonicalID: string(row.anchor) + ":vesper", Label: "Vesper"}}},
			context:    graph,
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: row.status, DirectJudgment: "degraded", CurrentState: "none", DeterministicAnswer: "degraded",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "p", QueryVersion: "q", InterpretationVersion: "i", SynthesisVersion: "s"},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_incident_status_01" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_incident_status_01"
	request.Question = "which incidents of the team are open?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

func wantLimitation(t *testing.T, served InvestigationResult, want string) {
	t.Helper()
	for _, limitation := range served.Limitations {
		if limitation == want {
			return
		}
	}
	t.Fatalf("status=%s limitations=%q, want %q", served.Status, served.Limitations, want)
}

func TestIncidentStatusNoneReachedStatesTheReachBasisOfTheAnchorKind(t *testing.T) {
	t.Parallel()
	for anchor, basis := range map[SubjectKind]string{SubjectTeam: "the repositories and services it owns", SubjectProject: "the repositories of its linked pull requests"} {
		served := investigateIncidentStatus(t, incidentStatusRow{anchor: anchor, status: InvestigationDegraded,
			graph: GraphContext{MemberStatusOutsideSet: MemberStatusOutsideSet{Kind: SubjectIncident, Filtered: true, Reached: 0}}})
		want := "No incidents were reached from the " + string(anchor) + " through " + basis + ", so the status was matched against none."
		wantLimitation(t, served, want)
		if !contractsv1.IsContextFabricMemberStatusLimitation(want) {
			t.Fatalf("%q not recognised as a service sentence", want)
		}
	}
}

func TestIncidentStatusReachedButNoneHoldTheStatusServesTheClosedSetSentence(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectIncident, Members: []CohortMember{}, Exclusions: []CohortExclusion{}, Rationale: "none holds the status"}
	served := investigateIncidentStatus(t, incidentStatusRow{anchor: SubjectTeam, status: InvestigationPartial,
		graph: GraphContext{Cohort: cohort, MemberStatusOutsideSet: MemberStatusOutsideSet{Kind: SubjectIncident, Filtered: true, Reached: 4}}})
	wantLimitation(t, served, contractsv1.ContextFabricMemberStatusFilterLimitation("incident", "open"))
	for _, limitation := range served.Limitations {
		if strings.HasPrefix(limitation, "No incidents were reached") {
			t.Fatalf("reached incidents served the none-reached sentence %q", limitation)
		}
	}
}

func TestIncidentStatusPopulationNotComputedNamesTheCause(t *testing.T) {
	t.Parallel()
	detail := CoverageDetail{DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailGraphWalkCutBeforeMember, Degrading: true, Kind: SubjectIncident, Raw: "walk_cut_before_member:incident"}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	served := investigateIncidentStatus(t, incidentStatusRow{anchor: SubjectTeam, status: InvestigationDegraded,
		graph: GraphContext{Coverage: Coverage{Sources: []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}, Partial: true, DegradedReasons: []string{"walk_cut_before_member:incident"}, Details: []CoverageDetail{detail}}}})
	wantLimitation(t, served, contractsv1.ContextFabricMemberStatusNotComputedLimitation("incident", contractsv1.ComposeCoverageDetailLabel(detail)))
}

func TestDegradedAnswerWithoutAnyOwnLimitationIsServedTheLoudFallback(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	served := investigateIncidentStatus(t, incidentStatusRow{anchor: SubjectTeam, status: InvestigationDegraded, frameKind: SubjectPullRequest})
	if len(served.Limitations) == 0 {
		t.Fatalf("degraded answer served an empty limitations list")
	}
	fallback := false
	for _, limitation := range served.Limitations {
		fallback = fallback || contractsv1.IsContextFabricDegradedWithoutLimitation(limitation)
	}
	if !fallback || !strings.Contains(logs.String(), "without a limitation of its own") {
		t.Fatalf("fallback=%v log=%q", fallback, logs.String())
	}
}

func TestNoDegradedExitDependsOnTheFallbackSentence(t *testing.T) {
	t.Parallel()
	known := map[string]bool{
		"budget_trim.go": true, "claim_depth_narrowing.go": true, "fact_row_truncation.go": true,
		"model_runtime.go": true, "path_drop.go": true,
	}
	assign := regexp.MustCompile(`\.Status = Investigation(Partial|Degraded)\b`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := []string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if assign.MatchString(line) && !strings.Contains(line, "shadow.") {
				found = append(found, file)
				break
			}
		}
	}
	sort.Strings(found)
	for _, file := range found {
		if !known[file] && file != "work_item_authorization_gap.go" {
			t.Errorf("%s lowers a status to partial or degraded: give the exit its own limitation, then list it here", file)
		}
	}
	for _, file := range []string{"budget_trim.go", "claim_depth_narrowing.go", "fact_row_truncation.go", "path_drop.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "appendBoundedLimitations") {
			t.Errorf("%s lowers a status without composing a limitation", file)
		}
	}
}
