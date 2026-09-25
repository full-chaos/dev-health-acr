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

// CHAOS-6558 (second byte-axis lever): the prod non-row overrun, replayed.
//
// PROD (2026-09-25, acr 163629d2, api req_99c4ea66949f4fe30dff1018cae7d6ca,
// raw acc-c13-g1.json): "Which teams need attention over the last 30 days?"
// with evidence_window trailing_30d. discovered_cohort_ranking, a 2-team
// cohort, 25 relationship paths, 4 drivers. Pass 1: 79,376 bytes = 11,132 of
// fact-table rows + 68,244 non-row (> 65,536 alone); the row lever at one row
// per table measured 69,712 (insufficient, rows_dominate=false), the cohort
// retry ran (2 -> 1), pass 2 measured 77,690 (rows 9,431), the row lever
// declined again at 69,555, and the caller got a 413.
//
// THE FIXTURE keeps that structure: a 2-team cohort, facts about subjects
// outside the cohort (so the retry keeps them, as prod did), claims carrying
// row tables, 25 graph paths making the non-row bytes alone exceed the
// ceiling, and 4 drivers citing some of the paths. Ceilings are prod's.
const (
	chaos6558PathCount     = 25
	chaos6558PathFacts     = 8
	chaos6558PathRows      = 10
	chaos6558PathMaxBytes  = 65536
	chaos6558PathMaxItems  = 30
	chaos6558PathWhyLength = 1900
)

type chaos6558PathShape struct {
	maxBytes int64
	// driversWithoutEvidence makes every driver cite its paths with no
	// evidence ref, so no cited path is droppable.
	driversWithoutEvidence bool
	// noRows makes the claims carry no row tables, so the row lever has
	// nothing to cut and the path drop alone must disclose and fit.
	noRows bool
}

func chaos6558PathTeam(index int) SubjectRef {
	id := []string{"team_platform", "team_payments"}[index]
	return SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id}
}

func chaos6558PathID(index int) string { return fmt.Sprintf("path_g1_%04d", index) }

func chaos6558Paths() []RelationshipPath {
	paths := make([]RelationshipPath, 0, chaos6558PathCount)
	for index := 0; index < chaos6558PathCount; index++ {
		team := chaos6558PathTeam(index % 2)
		repo := SubjectRef{Kind: SubjectRepository, CanonicalID: fmt.Sprintf("repo_%02d", index), Label: fmt.Sprintf("full-chaos/repo-%02d", index)}
		paths = append(paths, RelationshipPath{
			PathID: chaos6558PathID(index), Nodes: []SubjectRef{team, repo},
			WhyRelevant: fmt.Sprintf("path %02d: ", index) + strings.Repeat("the team owns this repository and its recent work ", chaos6558PathWhyLength/52),
			Edges: []RelationshipEdge{{
				Type: contractsv1.ContextFabricRelationshipRelatedTo, From: team, To: repo,
				Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
				EvidenceRefIDs: []string{fmt.Sprintf("evidence_path_%02d", index)},
			}},
			EvidenceRefIDs: []string{fmt.Sprintf("evidence_path_%02d", index)},
		})
	}
	return paths
}

func chaos6558PathEngine(t *testing.T, calls *int, telemetry *recordingTelemetry, shape chaos6558PathShape) *Engine {
	t.Helper()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "chaos-6558 g1 prod shape", Complete: true, Members: []CohortMember{
		{Subject: chaos6558PathTeam(0), Rank: 1, InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."}},
		{Subject: chaos6558PathTeam(1), Rank: 2, InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."}},
	}}
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
				Cohort: cohort, Paths: chaos6558Paths(), DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			observed := time.Unix(100, 0).UTC()
			facts := make([]CanonicalFact, 0, chaos6558PathFacts)
			for index := 0; index < chaos6558PathFacts; index++ {
				status := "green"
				facts = append(facts, CanonicalFact{
					Kind: FactStatus, Subject: chaos6558Repository(index),
					Fields:         map[string]FactValue{"status": {String: &status}},
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
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := make([]ClaimedFact, 0, len(input.Facts.Facts))
			for index, fact := range input.Facts.Facts {
				claim := ClaimedFact{
					ClaimID: fmt.Sprintf("claim_%02d", index), Kind: fact.Kind, Subject: fact.Subject, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				}
				if !shape.noRows {
					claim.Rows, claim.Table = chaos6558Rows(index, chaos6558Shape{rowPadding: 80})[:chaos6558PathRows], chaos6558SeriesTable()
				}
				claims = append(claims, claim)
			}
			drivers := make([]DriverJudgment, 0, 4)
			for index := 0; index < 4; index++ {
				evidence := []string{fmt.Sprintf("evidence_driver_%02d", index)}
				if shape.driversWithoutEvidence {
					evidence = []string{}
				}
				drivers = append(drivers, DriverJudgment{
					DriverID: fmt.Sprintf("driver_g1_%02d", index), Standing: DriverPrincipal, Category: "relationship",
					Title: "Ownership relationship", Summary: "The team appears to own active repositories.",
					AffectedSubjects: []SubjectRef{chaos6558PathTeam(index % 2)},
					PathIDs:          []string{chaos6558PathID(index * 2), chaos6558PathID(index*2 + 1)},
					EvidenceRefIDs:   evidence,
					Derivation:       DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.8, Current: true,
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Two teams lean toward needing attention.", CurrentState: "Review queues appear to be growing.",
				StrongestPressures: []string{}, Drivers: drivers, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: cloneSlice(input.Graph.Paths), Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Two teams appear to need attention, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: chaos6558PathMaxItems, MaxSerializedBytes: shape.maxBytes,
		SynthesisDeadlineReserve: time.Second,
		Now:                      func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:              func() string { return "result_65580000" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestCHAOS6558NonRowOverrunIsServedByDroppingPaths is the red/green pin.
// BASELINE (2fb2dc72, == prod's stage 3): a 413 after two syntheses with the
// row lever insufficient twice. FIX: one synthesis, served partial, paths
// dropped uncited-first, the drop disclosed, under the ceiling.
func TestCHAOS6558NonRowOverrunIsServedByDroppingPaths(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: chaos6558PathMaxBytes})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused after %d syntheses: overrun=%s %d bytes / %d items, retry_attempted=%v, axis=%s -- the prod 413; non-row bytes over the budget must be served by dropping paths",
				calls, refusal.Overrun, refusal.MeasuredBytes, refusal.MeasuredItems, refusal.RetryAttempted, refusal.NarrowerContinuationAxis)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1: the path drop runs before any cohort retry", calls)
	}
	if result.Status != InvestigationPartial || result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial || !result.Coverage.Partial {
		t.Fatalf("status %q completeness %q partial=%v: a cut answer is partial", result.Status, result.Completeness.State, result.Coverage.Partial)
	}
	measured, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil || measured.Bytes > chaos6558PathMaxBytes {
		t.Fatalf("served %d bytes (err %v) against %d", measured.Bytes, err, chaos6558PathMaxBytes)
	}
	if len(result.Paths) >= chaos6558PathCount || len(result.Paths) == 0 {
		t.Fatalf("served %d of %d paths: want some dropped, the fewest that fit", len(result.Paths), chaos6558PathCount)
	}
	if len(result.Drivers) != 4 || len(result.ClaimedFacts) != chaos6558PathFacts || result.Cohort == nil || len(result.Cohort.Members) != 2 {
		t.Fatalf("drivers %d claims %d: drivers, claims and members are never dropped", len(result.Drivers), len(result.ClaimedFacts))
	}
	for _, claim := range result.ClaimedFacts {
		if len(claim.Rows) != 1 {
			t.Fatalf("claim %s carries %d rows, want 1: the row lever runs on the reduced document", claim.ClaimID, len(claim.Rows))
		}
	}
	kept := map[string]bool{}
	for _, path := range result.Paths {
		kept[path.PathID] = true
	}
	// Uncited paths (8..24) go first, from the end: every cited path (0..7)
	// survives unless all 17 uncited ones are gone.
	uncitedLeft := 0
	for index := 8; index < chaos6558PathCount; index++ {
		if kept[chaos6558PathID(index)] {
			uncitedLeft++
		}
	}
	for index := 0; index < 8; index++ {
		if !kept[chaos6558PathID(index)] && uncitedLeft > 0 {
			t.Fatalf("cited path %d dropped while %d uncited paths remain", index, uncitedLeft)
		}
	}
	for index := 8; index < chaos6558PathCount-1; index++ {
		if !kept[chaos6558PathID(index)] && kept[chaos6558PathID(index+1)] {
			t.Fatalf("path %d dropped before path %d: each group is dropped from the end of the list", index, index+1)
		}
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.PathIDs {
			if !kept[id] {
				t.Fatalf("driver %s still cites dropped path %s", driver.DriverID, id)
			}
		}
	}
	want := fmt.Sprintf("This answer shows %d of the %d relationship paths", len(result.Paths), chaos6558PathCount)
	disclosed := false
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, want) && contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("no service-authored limitation starts %q; limitations = %q", want, result.Limitations)
	}
	var row *RequirementOutcomeRow
	for index := range result.Completeness.Outcomes {
		candidate := &result.Completeness.Outcomes[index]
		if candidate.Impact == contractsv1.ContextFabricAnswerImpactDepth && candidate.CauseOverrun == contractsv1.ContextFabricBudgetOverrunBytes && candidate.Declared == chaos6558PathCount {
			row = candidate
		}
	}
	if row == nil || row.Served != len(result.Paths) || row.Outcome != contractsv1.ContextFabricRequirementNarrowed || !row.CauseObserved {
		t.Fatalf("path outcome row = %+v, want narrowed/depth/bytes %d of %d", row, len(result.Paths), chaos6558PathCount)
	}
	if len(telemetry.pathDrops) != 1 {
		t.Fatalf("path drop lines = %d, want 1", len(telemetry.pathDrops))
	}
	line := telemetry.pathDrops[0]
	rowLine := telemetry.factRowTruncations[0]
	t.Logf("pass 1: %d bytes = %d row bytes + %d non-row; row floor %d; paths %d -> %d (cited dropped %d), per-table cap %d, served %d bytes, minimum answer %d bytes",
		rowLine.BytesBefore, rowLine.RowBytes, rowLine.BytesBefore-rowLine.RowBytes, rowLine.BytesAfter, line.PathsBefore, line.PathsAfter, line.CitedDropped, line.PerTable, line.BytesAfter, line.MinimumBytes)
	if want := []string{chaos6558PathID(chaos6558PathCount - 1), chaos6558PathID(chaos6558PathCount - 2)}; strings.Join(line.DroppedPathIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("dropped_path_ids = %v, want %v (uncited, tail-first)", line.DroppedPathIDs, want)
	}
	if !line.Served || line.PathsBefore != chaos6558PathCount || line.PathsAfter != len(result.Paths) || line.MinimumBytes <= 0 || line.MinimumBytes > line.BytesAfter || line.BytesAfter != measured.Bytes && line.BytesAfter > chaos6558PathMaxBytes {
		t.Fatalf("path drop line = %+v", line)
	}
	if len(telemetry.factRowTruncations) == 0 || telemetry.factRowTruncations[0].Declined != FactRowTruncationInsufficient || telemetry.factRowTruncations[0].RowsDominate {
		t.Fatalf("first row-lever line = %+v, want insufficient with rows_dominate=false (the prod shape)", telemetry.factRowTruncations)
	}
}

// TestCHAOS6558OnlyTheMinimumAnswerOverBudgetStillRefuses: when every
// droppable path is gone and every table is at one row, and the document
// still does not fit, the 413 stands -- and the lever's line names the
// minimum answer's bytes.
func TestCHAOS6558OnlyTheMinimumAnswerOverBudgetStillRefuses(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: 9000})
	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("error = %v, want the byte refusal: the minimum answer exceeds a 9,000-byte budget", err)
	}
	if len(telemetry.pathDrops) == 0 {
		t.Fatalf("no path drop line before the refusal")
	}
	line := telemetry.pathDrops[0]
	if line.Served || line.Declined != PathDropInsufficient || line.MinimumBytes <= 9000 {
		t.Fatalf("path drop line = %+v, want insufficient with minimum_bytes above the budget", line)
	}
}

// TestCHAOS6558CitedPathsWithoutOtherSupportAreNeverDropped: a driver whose
// only support is its paths keeps them; only the 17 uncited paths can go.
func TestCHAOS6558CitedPathsWithoutOtherSupportAreNeverDropped(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: chaos6558PathMaxBytes, driversWithoutEvidence: true})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	kept := map[string]bool{}
	for _, path := range result.Paths {
		kept[path.PathID] = true
	}
	for index := 0; index < 8; index++ {
		if !kept[chaos6558PathID(index)] {
			t.Fatalf("cited path %d dropped although its driver has no other support", index)
		}
	}
}

// TestCHAOS6558PathDropAloneServesAndDisclosesPartial: no claim carries rows,
// so the row lever has nothing to cut; the path drop alone must make the
// answer partial, set coverage.partial and add its outcome row.
func TestCHAOS6558PathDropAloneServesAndDisclosesPartial(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: 45000, noRows: true})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 || len(telemetry.pathDrops) != 1 || !telemetry.pathDrops[0].Served || telemetry.pathDrops[0].PerTable != 0 {
		t.Fatalf("calls=%d path drop lines=%+v, want one served drop with no row cut", calls, telemetry.pathDrops)
	}
	if result.Status != InvestigationPartial || !result.Coverage.Partial || result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("status %q coverage.partial=%v completeness %q: a path drop alone makes the answer partial", result.Status, result.Coverage.Partial, result.Completeness.State)
	}
	found := false
	for _, row := range result.Completeness.Outcomes {
		if row.Impact == contractsv1.ContextFabricAnswerImpactDepth && row.CauseOverrun == contractsv1.ContextFabricBudgetOverrunBytes && row.Declared == chaos6558PathCount && row.Served == len(result.Paths) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no depth/bytes path outcome row; outcomes = %+v", result.Completeness.Outcomes)
	}
}

func TestCHAOS6558PathDropLeverWithNoOutcomeRoomNeverServesAnInvalidAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: chaos6558PathMaxBytes}))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if len(telemetry.pathDrops) == 0 {
		t.Fatalf("path-drop lever never ran; the shape no longer reaches the seam under test")
	}
	assertLeverNeverServesAnInvalidAnswer(t, result, err, telemetry)
	for _, event := range telemetry.pathDrops {
		if event.Served {
			t.Fatalf("path-drop event says served with no outcome room: %+v", event)
		}
		if event.Declined == PathDropInvalidResult {
			return
		}
	}
	t.Fatalf("no path-drop event names %q; events=%+v", PathDropInvalidResult, telemetry.pathDrops)
}
