package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
			graph: GraphContext{MemberStatusOutsideSet: MemberStatusOutsideSet{Kind: SubjectIncident, Filtered: true}}})
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
		graph: GraphContext{Cohort: cohort, MemberStatusOutsideSet: MemberStatusOutsideSet{Kind: SubjectIncident, Filtered: true, Reached: true}}})
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
	wantLimitation(t, served, contractsv1.ContextFabricMemberStatusNotComputedLimitation("incident", contractsv1.ContextFabricCoverageCauseReason(detail.Code)))
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

func statusLoweringSites(t *testing.T, dir string) map[string]int {
	t.Helper()
	assign := regexp.MustCompile(`\bStatus(?: =|:)\s*Investigation(?:Partial|Degraded)\b|\bfloorNoMatchTo\(`)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func floorNoMatchTo(") || strings.Contains(line, "shadow.") {
				continue
			}
			if assign.MatchString(line) {
				found[filepath.Base(file)]++
			}
		}
	}
	return found
}

// The exits that lower a status to partial or degraded. Each file composes a
// limitation of its own; a new site, a removed site, or a file outside this
// table fails until the exit is classified here.
var statusLoweringExits = map[string]int{
	"budget_trim.go": 1, "claim_depth_narrowing.go": 1, "fact_row_truncation.go": 1, "model_runtime.go": 1,
	"path_drop.go": 1, "synthesis_model_failure.go": 1, "synthesis_status_floor.go": 4, "work_item_authorization_gap.go": 1,
}

func TestNoDegradedExitDependsOnTheFallbackSentence(t *testing.T) {
	t.Parallel()
	found := statusLoweringSites(t, ".")
	total := 0
	for _, count := range found {
		total += count
	}
	if total < 10 {
		t.Fatalf("found %d status-lowering sites, want at least 10: the scan measured nothing", total)
	}
	for file, count := range found {
		if want, ok := statusLoweringExits[file]; !ok || want != count {
			t.Errorf("%s has %d status-lowering sites, table has %d: give the exit its own limitation, then list it", file, count, want)
		}
	}
	for file, want := range statusLoweringExits {
		if found[file] != want {
			t.Errorf("%s: scan found %d sites, table has %d", file, found[file], want)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "appendBoundedLimitations") {
			t.Errorf("%s lowers a status without composing a limitation of its own", file)
		}
	}
}

func TestStatusLoweringScanSeesAPlantedSiteWithoutALimitation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planted := "package contextfabric\n\nfunc plantedExit(result *InvestigationResult) {\n\tresult.Status = InvestigationDegraded\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "planted_exit.go"), []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	found := statusLoweringSites(t, dir)
	if found["planted_exit.go"] != 1 {
		t.Fatalf("scan found %v for a planted lowering site, want planted_exit.go:1", found)
	}
	if _, listed := statusLoweringExits["planted_exit.go"]; listed {
		t.Fatal("planted site is in the exit table")
	}
}

func TestAReusedCompleteAnswerCorrectedToDegradedIsServedALimitation(t *testing.T) {
	t.Parallel()
	project, candidate := outcomeAuthorityCandidate(InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessDegraded)
	candidate.Limitations = []string{}
	engine := completenessAuthorityTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	}, true, false)
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if served.Status != InvestigationDegraded || len(served.Limitations) == 0 {
		t.Fatalf("status=%s limitations=%q, want a degraded answer with a limitation", served.Status, served.Limitations)
	}
}

func TestStatusAbsenceRecognisersMatchOnlyGeneratedSentences(t *testing.T) {
	t.Parallel()
	generated := []string{
		contractsv1.ContextFabricMemberStatusNoneReachedLimitation("incident", "team"),
		contractsv1.ContextFabricMemberStatusNoneReachedLimitation("project", "project"),
		contractsv1.ContextFabricMemberStatusNotComputedLimitation("incident", contractsv1.ContextFabricNoCoverageCauseReason),
		contractsv1.ContextFabricDegradedWithoutLimitation("degraded", contractsv1.ContextFabricNoCoverageCauseReason),
	}
	for _, sentence := range generated {
		if sentence == "" || !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Errorf("generated sentence %q is not recognised", sentence)
		}
	}
	lookalikes := []string{
		"No incidents were reached from the team through the repositories of its linked pull requests, so the status was matched against none.",
		"The incidents were not read by status, so whether any holds it is unknown: the model felt unsure.",
		"This answer is degraded and the service recorded its cause: the model felt unsure.",
	}
	for _, sentence := range lookalikes {
		if contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Errorf("lookalike %q is recognised as service-authored", sentence)
		}
	}
	if contractsv1.ContextFabricMemberStatusNotComputedLimitation("incident", "any text") != "" || contractsv1.ContextFabricDegradedWithoutLimitation("degraded", "any text") != "" {
		t.Error("a reason outside the closed set generated a sentence")
	}
}

func TestNoneReachedIsNotClaimedForACutReachProbe(t *testing.T) {
	t.Parallel()
	result := withMemberStatusNoneReached(InvestigationResult{Limitations: []string{}}, MemberStatusOutsideSet{Kind: SubjectProject, Filtered: true, ReachCut: true}, MemberStatusPoolCut{}, nil, SubjectTeam)
	if len(result.Limitations) != 0 {
		t.Fatalf("limitations = %q, want none claimed for a cut reach probe", result.Limitations)
	}
}
