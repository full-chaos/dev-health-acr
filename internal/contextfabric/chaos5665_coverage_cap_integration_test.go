package contextfabric

import (
	"context"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// syntheticCappableDetail mints one schema-legal, non-degrading coverage
// detail for the integration fixtures below -- see
// chaos5612_coverage_entries_cap_test.go's own synthetic builders for the
// same reasoning: FactKind and ContextFabricCoverageDetailCode are both
// closed vocabularies that cannot grow past today's real ceiling (93,
// TestTheOriginRowsFitTheCoverageBoundAtTheVocabularyMaximum) inside a
// test, so a fixture that must land EXACTLY at or past the 100-entry bound
// stands in with synthetic rows. PopulationTruncated's field rule
// (coverageDetailFieldRules) is `{}` -- no FactKind, no count, nothing --
// so a bare Source/Raw pair is the whole legal shape.
func syntheticCappableDetail(i int) CoverageDetail {
	d := CoverageDetail{
		DetailID: fmt.Sprintf("cov-synth-nd-%03d", i),
		Source:   fmt.Sprintf("synthetic:integration-kind-%03d", i),
		Code:     contractsv1.ContextFabricCoverageDetailPopulationTruncated,
		Raw:      fmt.Sprintf("synthetic integration disclosure %03d", i),
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d
}

// syntheticCappableDegradingDetail stands in for a real read failure: a row
// that must survive the cap intact, because dropping it would erase the
// disclosure of an actual problem. Returns the detail and its Raw string,
// so the caller can build the paired DegradedReasons entry the write-path
// contract's dual-write derivation requires.
func syntheticCappableDegradingDetail(i int) (CoverageDetail, string) {
	count := 1
	raw := fmt.Sprintf("synthetic integration failure %03d", i)
	d := CoverageDetail{
		DetailID:  fmt.Sprintf("cov-synth-deg-%03d", i),
		Source:    fmt.Sprintf("synthetic:integration-failed-kind-%03d", i),
		Code:      contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed,
		Degrading: true,
		Raw:       raw,
		Count:     &count,
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d, raw
}

// buildCoverageCapIntegrationEngine wires a minimal, real *Engine -- the
// same shape TestEngineInvestigatesNovelQuestionThroughComposableCapabilities
// uses -- whose Synthesizer stub returns the given Coverage verbatim, the
// ONE thing these tests control. Everything AFTER synthesis (finalize,
// applyCoverageDisplayLabels, capCoverageEntriesToWriteBound, Validate) then
// runs for REAL, through the engine's own Investigate call -- proving the
// cap through the actual production call site (engine.go's "single stamp
// point for the decisive path"), not a direct unit call to the cap function.
func buildCoverageCapIntegrationEngine(t *testing.T, telemetry EngineTelemetry, coverage Coverage) (*Engine, InvestigationRequest) {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_cap_integration", Label: "Cap Integration"}
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: resolution,
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
				EvidenceRefIDs:   []string{},
				Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "status ok", CurrentState: "steady",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage:            coverage,
				DeterministicAnswer: "status ok",
				Warnings:            []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewResultID: func() string { return "result_cap_integration" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequest()
	request.Question = "What is the status?"
	return engine, request
}

// buildCoverageCapFixture builds a Coverage with degradingCount degrading
// rows followed by nonDegradingCount non-degrading rows -- degrading first,
// so the fixture's own DegradedReasons order (positional) matches the
// degrading rows' order by construction, the same "sort/dedupe never
// reorders within a partition" invariant capCoverageEntriesToWriteBound
// itself relies on.
func buildCoverageCapFixture(degradingCount, nonDegradingCount int) Coverage {
	var details []CoverageDetail
	var reasons []string
	for i := 0; i < degradingCount; i++ {
		d, raw := syntheticCappableDegradingDetail(i)
		details = append(details, d)
		reasons = append(reasons, raw)
	}
	for i := 0; i < nonDegradingCount; i++ {
		details = append(details, syntheticCappableDetail(i))
	}
	return Coverage{
		Sources:         []SourceObservation{{Source: "synthetic:root", State: SourceAvailable}},
		DegradedReasons: reasons,
		Details:         details,
	}
}

// TestInvestigateServesExactlyAtTheCoverageBoundWithoutCapping is the
// boundary control: a served turn whose coverage lands EXACTLY at the
// 100-entry bound through a real Investigate call must reach the caller
// unchanged, and the cap's own disclosure line must never fire when there
// is nothing to disclose.
func TestInvestigateServesExactlyAtTheCoverageBoundWithoutCapping(t *testing.T) {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	const degradingCount = 10
	coverage := buildCoverageCapFixture(degradingCount, bound-degradingCount)
	logs := captureEngineLogger(t)
	engine, request := buildCoverageCapIntegrationEngine(t, logs.telemetry, coverage)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if got := len(result.Coverage.Details); got != bound {
		t.Fatalf("served Coverage.Details = %d, want exactly the bound %d (no cap needed)", got, bound)
	}
	degradingServed := 0
	for _, d := range result.Coverage.Details {
		if d.Degrading {
			degradingServed++
		}
	}
	if degradingServed != degradingCount {
		t.Fatalf("degrading rows served = %d, want all %d preserved", degradingServed, degradingCount)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result fails the write-path contract: %v", err)
	}
	if lines := linesWithMessage(t, logs.configured.String(), "context fabric coverage entries capped"); len(lines) != 0 {
		t.Fatalf("cap disclosure line(s) fired at exactly the bound, want none: %v", lines)
	}
}

// TestInvestigateCapsCoverageEntriesOneOverTheBoundThroughARealInvestigateCall
// is CHAOS-5665's own deliverable: the ceiling-past-the-ceiling proof driven
// through a REAL engine.Investigate call (not a direct unit call to
// capCoverageEntriesToWriteBound), one entry over the bound -- the smallest
// overflow the cap must still catch, served through the engine's actual
// happy-path composition, telemetry sink, and write-path Validate().
func TestInvestigateCapsCoverageEntriesOneOverTheBoundThroughARealInvestigateCall(t *testing.T) {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	const degradingCount = 10
	nonDegradingCount := bound - degradingCount + 1 // one entry over the bound
	coverage := buildCoverageCapFixture(degradingCount, nonDegradingCount)
	logs := captureEngineLogger(t)
	engine, request := buildCoverageCapIntegrationEngine(t, logs.telemetry, coverage)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// BOUNDED: the served document never exceeds the bound, even though the
	// synthesized draft did.
	if got := len(result.Coverage.Details); got != bound {
		t.Fatalf("served Coverage.Details = %d, want capped to exactly the bound %d", got, bound)
	}
	// DEGRADING NEVER YIELDS, end to end: every real-failure row survives
	// the round trip through the whole engine, not just the unit-level cap
	// function.
	degradingServed := 0
	for _, d := range result.Coverage.Details {
		if d.Degrading {
			degradingServed++
		}
	}
	if degradingServed != degradingCount {
		t.Fatalf("degrading rows served = %d, want all %d preserved -- the cap must never trim a real failure to make room", degradingServed, degradingCount)
	}
	// SERVABLE: the capped document is a LEGAL write -- the whole point of
	// capping before emit rather than letting the write validator refuse
	// the turn (validateCoverageDetails' own entry-bound refusal).
	if err := result.Validate(); err != nil {
		t.Fatalf("capped served result fails the write-path contract: %v", err)
	}

	// DISCLOSED ON THE TRACE, on the engine's configured logger, with real
	// values, through the real Slog sink -- never slog.Default(), which
	// acr-api's JSON handler never reads.
	lines := linesWithMessage(t, logs.configured.String(), "context fabric coverage entries capped")
	if len(lines) != 1 {
		t.Fatalf("cap disclosure line(s) on the configured logger = %d, want 1", len(lines))
	}
	line := lines[0]
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
	for field, want := range map[string]float64{
		"coverage_entries_served":  float64(bound),
		"coverage_entries_omitted": 1,
		"coverage_entries_bound":   float64(bound),
	} {
		if got, ok := line[field].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v", field, line[field], want)
		}
	}
	if stray := linesWithMessage(t, logs.fallback.String(), "context fabric coverage entries capped"); len(stray) != 0 {
		t.Errorf("%d cap disclosure line(s) reached the PROCESS DEFAULT logger, which acr-api's JSON handler never reads", len(stray))
	}
}
