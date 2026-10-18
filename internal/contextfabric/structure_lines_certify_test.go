package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	structureNeedsKeys        = []string{"time", "level", "msg", "org_id", "member", "request_id"}
	gatedOfferResolutionKeys  = []string{"time", "level", "msg", "org_id", "outcome", "request_id"}
	cohortStructureGateKeys   = []string{"time", "level", "msg", "org_id", "outcome", "shape", "request_id"}
	windowGateOfferKeys       = []string{"time", "level", "msg", "org_id", "offered", "request_id"}
	windowExpandRedeemedKeys  = []string{"time", "level", "msg", "org_id", "request_id"}
	interpretedTimeBoundKeys  = []string{"time", "level", "msg", "org_id", "axis", "outcome", "clamp_applied", "range_days", "request_id"}
	structureOfferCountKeys   = []string{"time", "level", "msg", "org_id", "member", "source", "count", "request_id"}
	structureReceiptKeys      = []string{"time", "level", "msg", "org_id", "member", "outcome", "request_id"}
	structureReceiptVetoKeys  = []string{"time", "level", "msg", "org_id", "detail", "request_id"}
	structureExplicitKeys     = []string{"time", "level", "msg", "org_id", "member", "outcome", "request_id"}
	structureLineOrgAndTarget = map[string]any{"org_id": "org_1"}
)

func structureNeedKinds() []contractsv1.ContextFabricStructureNeedKind {
	all := contractsv1.ContextFabricStructureNeedKindVocabulary()
	return all[:]
}

func structureNeedsFixture() *contractsv1.ContextFabricStructureNeeds {
	engine, prior := contractsv1.ContextFabricStructureOfferEngine, contractsv1.ContextFabricStructureOfferPrior
	return &contractsv1.ContextFabricStructureNeeds{
		Missing:          structureNeedKinds(),
		KindOptions:      []contractsv1.ContextFabricKindOption{{OfferSource: engine}, {OfferSource: engine}, {OfferSource: prior}},
		AnchorOptions:    []contractsv1.ContextFabricAnchorOption{{OfferSource: engine}},
		HandleOptions:    []contractsv1.ContextFabricHandleOption{{OfferSource: prior}},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{{OfferSource: engine}},
		WindowOptions:    []contractsv1.ContextFabricWindowOption{{}, {}},
	}
}

func TestStructureNeedsAndOfferCountLinesCertifyWhatTheEngineWrites(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	contextfabric.RecordStructureNeedsForTest(ctx, sink, principalOrg1, structureNeedsFixture())
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	needs, offers := 0, 0
	for _, line := range lines {
		switch {
		case bytes.Contains(line, []byte(eventspec.StructureNeedsDisclosed.Msg)):
			needs++
			if got := jsonKeys(t, line); strings.Join(got, ",") != strings.Join(structureNeedsKeys, ",") {
				t.Fatalf("needs disclosed key sequence = %v, want %v", got, structureNeedsKeys)
			}
		case bytes.Contains(line, []byte(eventspec.StructureOfferCount.Msg)):
			offers++
			if got := jsonKeys(t, line); strings.Join(got, ",") != strings.Join(structureOfferCountKeys, ",") {
				t.Fatalf("offer count key sequence = %v, want %v", got, structureOfferCountKeys)
			}
		}
	}
	if needs != 5 || offers != 6 {
		t.Fatalf("a needs block naming every member wrote %d needs lines and %d offer count lines, want 5 and 6: %s", needs, offers, buf.String())
	}
	for _, member := range contextfabric.StructureNeedKindLogVocabulary() {
		certifyRequestLine(t, buf.Bytes(), eventspec.StructureNeedsDisclosed, map[string]any{"org_id": "org_1", "member": member})
	}
	for _, want := range []struct {
		member, source string
		count          int
	}{
		{"expected_kind", "engine", 2}, {"expected_kind", "prior", 1}, {"subject_anchor", "engine", 1}, {"subject_handle", "prior", 1},
		{"window", "engine", 2}, {"subject_candidate", "engine", 1},
	} {
		certifyRequestLine(t, buf.Bytes(), eventspec.StructureOfferCount, map[string]any{"org_id": "org_1", "member": want.member, "source": want.source, "count": want.count})
	}

	buf.Reset()
	sink.RecordStructureNeedsDisclosed(ctx, principalOrg1, "future_member")
	certifyRefused(t, buf.Bytes(), eventspec.StructureNeedsDisclosed, "an unknown structure member")
	buf.Reset()
	sink.RecordStructureOfferCount(ctx, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, "future_source", 1)
	certifyRefused(t, buf.Bytes(), eventspec.StructureOfferCount, "an unknown offer source")
	buf.Reset()
	for range 6 {
		sink.RecordStructureNeedsDisclosed(ctx, principalOrg1, contractsv1.ContextFabricStructureNeedWindow)
	}
	certifyRefused(t, buf.Bytes(), eventspec.StructureNeedsDisclosed, "more needs lines than the structure has members in one request", map[string]any{"member": "window"})
}

func TestStructureReceiptAndExplicitLinesCertifyEveryOutcomeTheEngineWrites(t *testing.T) {
	t.Parallel()
	seenReceipt, seenExplicit := map[string]bool{}, map[string]bool{}
	for outcomeName := range contextfabric.StructureVetoKindsForTest() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		contextfabric.RecordStructureReceiptsForTest(ctx, sink, principalOrg1, contextfabric.StructureVetoKindsForTest()[outcomeName])
		receipts, explicit := 0, 0
		for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			switch {
			case bytes.Contains(line, []byte(eventspec.StructureReceipt.Msg)):
				receipts++
				if got := jsonKeys(t, line); strings.Join(got, ",") != strings.Join(structureReceiptKeys, ",") {
					t.Fatalf("receipt key sequence = %v, want %v", got, structureReceiptKeys)
				}
			case bytes.Contains(line, []byte(eventspec.StructureExplicit.Msg)):
				explicit++
				if got := jsonKeys(t, line); strings.Join(got, ",") != strings.Join(structureExplicitKeys, ",") {
					t.Fatalf("explicit key sequence = %v, want %v", got, structureExplicitKeys)
				}
			}
		}
		if receipts != 4 || explicit != 2 {
			t.Fatalf("%s: a request bearing every member wrote %d receipt and %d explicit lines, want 4 and 2", outcomeName, receipts, explicit)
		}
		for _, member := range []string{"expected_kind", "subject_anchor", "subject_handle", "subject_candidate"} {
			line := certifyRequestLine(t, buf.Bytes(), eventspec.StructureReceipt, map[string]any{"org_id": "org_1", "member": member, "outcome": outcomeName})
			seenReceipt[line["outcome"].(string)] = true
		}
		explicitOutcome := "conflict"
		if outcomeName == "applied" {
			explicitOutcome = "applied"
		}
		for _, member := range []string{"expected_kind", "subject_handle"} {
			line := certifyRequestLine(t, buf.Bytes(), eventspec.StructureExplicit, map[string]any{"org_id": "org_1", "member": member, "outcome": explicitOutcome})
			seenExplicit[line["outcome"].(string)] = true
		}
		doubled := bytes.Repeat(buf.Bytes(), 2)
		certifyRefused(t, doubled, eventspec.StructureReceipt, "more receipt lines than the receipt bearing members in one request", map[string]any{"member": "subject_anchor", "outcome": outcomeName})
	}
	sameMembers(t, "receipt outcomes", keysOf(seenReceipt), contextfabric.StructureReceiptOutcomeVocabulary())
	sameMembers(t, "explicit outcomes", keysOf(seenExplicit), contextfabric.StructureExplicitOutcomeVocabulary())

	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordStructureReceipt(ctx, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, "future_outcome")
	certifyRefused(t, buf.Bytes(), eventspec.StructureReceipt, "an unknown receipt outcome")
	buf.Reset()
	sink.RecordStructureExplicit(ctx, principalOrg1, "future_member", contextfabric.StructureExplicitApplied)
	certifyRefused(t, buf.Bytes(), eventspec.StructureExplicit, "an unknown explicit member")
}

func TestCohortStructureGateLineCertifiesEveryOutcomeAndShapeAndLogsAnUnknownShapeAsAClass(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, outcome := range contextfabric.CohortStructureGateOutcomesForTest() {
		for _, shape := range contractsv1.ContextFabricInvestigationShapeVocabulary() {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			contextfabric.RecordCohortStructureGateForTest(ctx, sink, principalOrg1, outcome, shape)
			pinKeys(t, bytes.NewBuffer(buf.Bytes()), cohortStructureGateKeys, 1)
			certifyRequestLine(t, buf.Bytes(), eventspec.CohortStructureGate, map[string]any{"outcome": string(outcome), "shape": string(shape)})
			seen[string(outcome)] = true
		}
	}
	sameMembers(t, "cohort gate outcomes", keysOf(seen), contextfabric.CohortStructureGateOutcomeVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordCohortStructureGate(ctx, principalOrg1, contextfabric.CohortStructureGateApplied, "Which team owns payments-api?")
	if strings.Contains(buf.String(), "payments-api") {
		t.Fatalf("an unknown shape reached the log: %s", buf.String())
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.CohortStructureGate, map[string]any{"shape": "unrecognised"})
	buf.Reset()
	sink.RecordCohortStructureGate(ctx, principalOrg1, "future_outcome", contractsv1.ContextFabricShapeOpen)
	certifyRefused(t, buf.Bytes(), eventspec.CohortStructureGate, "an unknown gate outcome")
	buf.Reset()
	sink.RecordCohortStructureGate(ctx, principalOrg1, contextfabric.CohortStructureGateApplied, contractsv1.ContextFabricShapeOpen)
	sink.RecordCohortStructureGate(ctx, principalOrg1, contextfabric.CohortStructureGateApplied, contractsv1.ContextFabricShapeOpen)
	certifyRequestLine(t, buf.Bytes(), eventspec.CohortStructureGate, map[string]any{"outcome": "applied"})
	sink.RecordCohortStructureGate(ctx, principalOrg1, contextfabric.CohortStructureGateApplied, contractsv1.ContextFabricShapeOpen)
	certifyRefused(t, buf.Bytes(), eventspec.CohortStructureGate, "a third gate line in one request", map[string]any{"outcome": "applied"})
}

func TestGatedOfferAndWindowOfferLinesCertifyEveryProducerValue(t *testing.T) {
	t.Parallel()
	for _, outcome := range contextfabric.GatedOfferResolutionOutcomeVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordGatedOfferResolution(ctx, principalOrg1, contextfabric.GatedOfferResolutionOutcome(outcome))
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), gatedOfferResolutionKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.GatedOfferResolution, map[string]any{"outcome": outcome})
	}
	for _, offered := range []bool{true, false} {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordWindowGateOfferDisclosure(ctx, principalOrg1, offered)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowGateOfferKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.WindowGateOfferDisclosure, map[string]any{"offered": offered})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordWindowExpandOfferRedeemed(ctx, principalOrg1)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowExpandRedeemedKeys, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.WindowExpandOfferRedeemed, structureLineOrgAndTarget)
	buf.Reset()
	sink.RecordGatedOfferResolution(ctx, principalOrg1, "future_outcome")
	certifyRefused(t, buf.Bytes(), eventspec.GatedOfferResolution, "an unknown offers only outcome")
	buf.Reset()
	sink.RecordGatedOfferResolution(ctx, principalOrg1, contextfabric.GatedOfferResolutionComposed)
	sink.RecordGatedOfferResolution(ctx, principalOrg1, contextfabric.GatedOfferResolutionComposed)
	certifyRefused(t, buf.Bytes(), eventspec.GatedOfferResolution, "two offers only lines in one request")
}

func TestInterpretedTimeBoundLineCertifiesEveryVerdictAndLogsAnInterpreterAxisAsAClass(t *testing.T) {
	t.Parallel()
	const free = "Which team owns payments-api?"
	seenOutcome, seenAxis := map[string]bool{}, map[string]bool{}
	for _, decision := range contextfabric.InterpretedTimeBoundDecisionsForTest(free) {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordInterpretedTimeBound(ctx, principalOrg1, decision)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), interpretedTimeBoundKeys, 1)
		if strings.Contains(buf.String(), "payments-api") {
			t.Fatalf("interpreter output reached the log: %s", buf.String())
		}
		line := certifyRequestLine(t, buf.Bytes(), eventspec.InterpretedTimeBound, map[string]any{"outcome": string(decision.Outcome), "clamp_applied": decision.ClampApplied, "range_days": decision.RangeDays})
		seenOutcome[line["outcome"].(string)] = true
		seenAxis[line["axis"].(string)] = true
	}
	sameMembers(t, "interpreted time bound outcomes", keysOf(seenOutcome), contextfabric.InterpretedTimeBoundOutcomeLogVocabulary())
	for _, axis := range []string{"current", "valid_time", "observed_time", "range", "unrecognised"} {
		if !seenAxis[axis] {
			t.Errorf("the verdicts of the engine never logged the axis %q: %v", axis, keysOf(seenAxis))
		}
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordInterpretedTimeBound(ctx, principalOrg1, contextfabric.InterpretedTimeBoundDecision{Axis: "", Outcome: contextfabric.InterpretedTimeBoundAbsentOrZero})
	certifyRequestLine(t, buf.Bytes(), eventspec.InterpretedTimeBound, map[string]any{"axis": ""})
	buf.Reset()
	sink.RecordInterpretedTimeBound(ctx, principalOrg1, contextfabric.InterpretedTimeBoundDecision{Axis: "current", Outcome: "future_outcome"})
	certifyRefused(t, buf.Bytes(), eventspec.InterpretedTimeBound, "an unknown verdict")
	buf.Reset()
	sink.RecordInterpretedTimeBound(ctx, principalOrg1, contextfabric.InterpretedTimeBoundDecision{Axis: "current", Outcome: contextfabric.InterpretedTimeBoundOK})
	certifyRefused(t, []byte(strings.Replace(buf.String(), `"axis":"current"`, `"axis":"future_axis"`, 1)), eventspec.InterpretedTimeBound, "an axis outside the contract and the class")
}

func TestStructureReceiptVetoLineCertifiesEveryExitTheEngineWrites(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, detail := range contextfabric.StructureVetoDetailsForTest() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		contextfabric.RecordStructureVetoDetailForTest(ctx, sink, principalOrg1, detail)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), structureReceiptVetoKeys, 1)
		line := certifyRequestLine(t, buf.Bytes(), eventspec.StructureReceiptVeto, nil)
		seen[line["detail"].(string)] = true
	}
	sameMembers(t, "veto details", keysOf(seen), contextfabric.StructureVetoDetailVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordStructureVetoDetail(ctx, principalOrg1, "stored_result_denied_future_reason")
	certifyRefused(t, buf.Bytes(), eventspec.StructureReceiptVeto, "an unknown denial reason")
	buf.Reset()
	sink, ctx = answerLineSink(&buf, slog.LevelWarn)
	contextfabric.RecordStructureVetoDetailForTest(ctx, sink, principalOrg1, contextfabric.StructureVetoDetailsForTest()[0])
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("the veto line is not a Warn line: %s", buf.String())
	}
}

// TestEveryStructureLineCertifiesWithoutARequestID emits each declared line
// through the real sink under a context that carries no request id, and
// requires the line to certify when the assertion states the id is absent,
// and to be unmatched by the same assertion when the line carries an id.
func TestEveryStructureLineCertifiesWithoutARequestID(t *testing.T) {
	t.Parallel()
	engine := contractsv1.ContextFabricStructureOfferEngine
	cases := []struct {
		event eventspec.Event
		emit  func(contextfabric.SlogEngineTelemetry, context.Context)
	}{
		{eventspec.StructureNeedsDisclosed, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordStructureNeedsDisclosed(c, principalOrg1, contractsv1.ContextFabricStructureNeedWindow)
		}},
		{eventspec.GatedOfferResolution, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordGatedOfferResolution(c, principalOrg1, contextfabric.GatedOfferResolutionComposed)
		}},
		{eventspec.CohortStructureGate, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordCohortStructureGate(c, principalOrg1, contextfabric.CohortStructureGateApplied, contractsv1.ContextFabricShapeOpen)
		}},
		{eventspec.WindowGateOfferDisclosure, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordWindowGateOfferDisclosure(c, principalOrg1, true)
		}},
		{eventspec.WindowExpandOfferRedeemed, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordWindowExpandOfferRedeemed(c, principalOrg1)
		}},
		{eventspec.InterpretedTimeBound, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordInterpretedTimeBound(c, principalOrg1, contextfabric.InterpretedTimeBoundDecision{Axis: "current", Outcome: contextfabric.InterpretedTimeBoundOK})
		}},
		{eventspec.StructureOfferCount, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordStructureOfferCount(c, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, engine, 2)
		}},
		{eventspec.StructureReceipt, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordStructureReceipt(c, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, contextfabric.StructureReceiptApplied)
		}},
		{eventspec.StructureReceiptVeto, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			contextfabric.RecordStructureVetoDetailForTest(c, s, principalOrg1, contextfabric.StructureVetoDetailsForTest()[0])
		}},
		{eventspec.StructureExplicit, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordStructureExplicit(c, principalOrg1, contractsv1.ContextFabricStructureNeedExpectedKind, contextfabric.StructureExplicitApplied)
		}},
	}
	cases = append(cases, []struct {
		event eventspec.Event
		emit  func(contextfabric.SlogEngineTelemetry, context.Context)
	}{
		{eventspec.PriorConsulted, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordPriorConsulted(c, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, contextfabric.PriorConsultedOffered)
		}},
		{eventspec.PriorDegradationPointerDangling, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordPriorDegradation(c, principalOrg1, contextfabric.PriorDegradationPointerDangling)
		}},
		{eventspec.PriorDegradation, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordPriorDegradation(c, principalOrg1, contextfabric.PriorDegradationStoreUnavailable)
		}},
		{eventspec.ProjectedRowsCount, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordProjectedRowsCount(c, principalOrg1, 4, false)
		}},
		{eventspec.ProjectedRowsByFactKind, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordProjectedRowsByFactKind(c, principalOrg1, map[contextfabric.FactKind]int{contractsv1.ContextFabricFactMetrics: 2})
		}},
	}...)
	for _, extra := range []struct {
		event eventspec.Event
		emit  func(contextfabric.SlogEngineTelemetry, context.Context)
	}{
		{eventspec.DualTableFacts, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordDualTableFacts(c, principalOrg1, 1, 2)
		}},
		{eventspec.ModelRowsStripped, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordModelRowsStripped(c, principalOrg1, 1)
		}},
		{eventspec.DriverSourceStateCorrected, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordDriverSourceStateCorrected(c, principalOrg1, 1)
		}},
		{eventspec.ServiceLimitationLookalikesDropped, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordServiceLimitationLookalikesDropped(c, principalOrg1, 1, []string{"fixed"})
		}},
		{eventspec.DriverIdentityCollisions, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordDriverIdentityCollisions(c, principalOrg1, contextfabric.DriverIdentityCollisions{Restated: 1})
		}},
	} {
		cases = append(cases, extra)
	}
	cases = append(cases, []struct {
		event eventspec.Event
		emit  func(contextfabric.SlogEngineTelemetry, context.Context)
	}{
		{eventspec.EvidenceLabelFallback, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordEvidenceLabelFallback(c, principalOrg1, 2)
		}},
		{eventspec.CoverageEntriesCapped, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordCoverageEntriesCapped(c, principalOrg1, 105, 3)
		}},
		{eventspec.CoverageDisclosurePhrasing, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordCoverageDisclosurePhrasing(c, principalOrg1, contextfabric.CoverageDisclosureAbsent, "", 0, 2)
		}},
		{eventspec.CategoryFactComposition, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordCategoryFactComposition(c, principalOrg1, contextfabric.CategoryFactCompositionEvent{RequirementKind: contextfabric.FactStatus, SubjectKind: contextfabric.SubjectTeam, ComposedKinds: []contextfabric.FactKind{contextfabric.FactHealth}})
		}},
		{eventspec.PeriodDeltaComposition, func(s contextfabric.SlogEngineTelemetry, c context.Context) {
			s.RecordPeriodDeltaComposition(c, principalOrg1, contextfabric.PeriodDeltaCompositionEvent{
				RequirementKind: contextfabric.FactStatus, SubjectKind: contextfabric.SubjectTeam, Grain: contextfabric.SubjectTeam,
				ComposedKinds: []contextfabric.FactKind{contextfabric.FactHealth}, FailureReason: contextfabric.PeriodDeltaFailureAnchorUnresolved,
				TransitionCounts: map[contextfabric.PeriodDeltaTransition]int{contextfabric.PeriodDeltaTransitionAnchorUnresolved: 1},
			})
		}},
	}...)
	if len(cases) != 25 {
		t.Fatalf("want one case per declared line, have %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.event.ID, func(t *testing.T) {
			var without, with bytes.Buffer
			sink, _ := answerLineSink(&without, slog.LevelInfo)
			tc.emit(sink, context.Background())
			if strings.Contains(without.String(), "request_id") {
				t.Fatalf("a context without a request id wrote one: %s", without.String())
			}
			log, err := certify.Parse(without.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: tc.event, NoAttribution: []string{"request_id"}}); err != nil {
				t.Fatalf("the line written without a request id was refused: %v\n%s", err, without.String())
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: tc.event, Want: map[string]any{"request_id": factScopeCertifyRequestID}}); err == nil {
				t.Fatalf("a line without a request id certified as the line of request %s", factScopeCertifyRequestID)
			}
			sinkWith, ctx := answerLineSink(&with, slog.LevelInfo)
			tc.emit(sinkWith, ctx)
			logWith, err := certify.Parse(with.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(logWith, certify.Assertion{Event: tc.event, NoAttribution: []string{"request_id"}}); err == nil {
				t.Fatalf("a line carrying a request id certified as the line without one: %s", with.String())
			}
			if _, err := certify.Certify(logWith, certify.Assertion{Event: tc.event, Want: map[string]any{"request_id": factScopeCertifyRequestID}, NoAttribution: []string{"request_id"}}); err == nil {
				t.Fatal("an assertion naming the request id both present and absent was accepted")
			}
			if _, err := certify.Certify(logWith, certify.Assertion{Event: tc.event, Want: map[string]any{"request_id": factScopeCertifyRequestID}}); err != nil {
				t.Fatalf("the line carrying a request id was refused: %v", err)
			}
		})
	}
}

func TestNoAttributionRefusesAKeyThatIsNotAConditionalAttributionField(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordStructureReceipt(ctx, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, contextfabric.StructureReceiptApplied)
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"org_id", "member", "undeclared"} {
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.StructureReceipt, Want: map[string]any{"request_id": factScopeCertifyRequestID}, NoAttribution: []string{key}}); err == nil {
			t.Errorf("NoAttribution %q was accepted", key)
		}
	}
}

func TestPriorConsultedAndDegradationLinesCertifyWhatTheEngineWrites(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, tc := range []struct {
		saw, offered, revoked bool
		want                  string
	}{{true, true, false, "offered"}, {true, false, false, "suppressed_verification"}, {true, false, true, "suppressed_revoked"}} {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		for _, member := range structureNeedKinds() {
			contextfabric.RecordPriorConsultOutcomeForTest(ctx, sink, principalOrg1, member, tc.saw, tc.offered, tc.revoked)
		}
		for _, member := range structureNeedKinds() {
			line := certifyRequestLine(t, buf.Bytes(), eventspec.PriorConsulted, map[string]any{"org_id": "org_1", "member": string(member), "outcome": tc.want})
			seen[line["outcome"].(string)] = true
		}
	}
	seen["degraded"] = true // named by the vocabulary, never written by the engine (priors.go)
	sameMembers(t, "prior consulted outcomes", keysOf(seen), contextfabric.PriorConsultedOutcomeVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	contextfabric.RecordPriorConsultOutcomeForTest(ctx, sink, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, false, false, false)
	if buf.Len() != 0 {
		t.Fatalf("a member with no candidate wrote a line: %s", buf.String())
	}
	sink.RecordPriorConsulted(ctx, principalOrg1, contractsv1.ContextFabricStructureNeedWindow, "future_outcome")
	certifyRefused(t, buf.Bytes(), eventspec.PriorConsulted, "an unknown prior outcome")

	infoSeen := map[string]bool{}
	for _, state := range contextfabric.PriorDegradationInfoVocabulary() {
		buf.Reset()
		contextfabric.RecordPriorDegradationForTest(ctx, sink, principalOrg1, contextfabric.PriorDegradationState(state))
		certifyRequestLine(t, buf.Bytes(), eventspec.PriorDegradation, map[string]any{"state": state})
		infoSeen[state] = true
	}
	buf.Reset()
	contextfabric.RecordPriorDegradationForTest(ctx, sink, principalOrg1, contextfabric.PriorDegradationPointerDangling)
	certifyRequestLine(t, buf.Bytes(), eventspec.PriorDegradationPointerDangling, map[string]any{"state": "pointer_dangling"})
	certifyRefused(t, buf.Bytes(), eventspec.PriorDegradation, "a dangling pointer on the Info line")
	buf.Reset()
	contextfabric.RecordPriorDegradationForTest(ctx, sink, principalOrg1, contextfabric.PriorDegradationNone)
	if buf.Len() != 0 {
		t.Fatalf("no degradation wrote a line: %s", buf.String())
	}
	sink.RecordPriorDegradation(ctx, principalOrg1, "future_state")
	certifyRefused(t, buf.Bytes(), eventspec.PriorDegradation, "an unknown degradation state")
}

func TestProjectedRowsLinesCertifyWhatTheSynthesizerWrites(t *testing.T) {
	t.Parallel()
	kinds := contextfabric.ProjectedRowsFactKindLogVocabulary()
	claimed := append([]string(nil), kinds[:len(kinds)-1]...)
	claimed = append(claimed, "Which team owns payments-api?")
	rows, byKind, truncated := contextfabric.ProjectedRowsOfClaimsForTest(claimed)
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordProjectedRowsCount(ctx, principalOrg1, rows, truncated)
	sink.RecordProjectedRowsByFactKind(ctx, principalOrg1, byKind)
	if strings.Contains(buf.String(), "payments-api") {
		t.Fatalf("a claim kind from the model reached the log: %s", buf.String())
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.ProjectedRowsCount, map[string]any{"org_id": "org_1", "rows_count": 0, "truncated": false})
	seen := map[string]bool{}
	for _, kind := range kinds {
		line := certifyRequestLine(t, buf.Bytes(), eventspec.ProjectedRowsByFactKind, map[string]any{"fact_kind": kind, "rows_projected_by_fact_kind": 0})
		seen[line["fact_kind"].(string)] = true
	}
	sameMembers(t, "projected rows fact kinds", keysOf(seen), kinds)
	if got := strings.Count(buf.String(), "projected rows count by fact kind"); got != len(kinds) {
		t.Fatalf("%d by kind lines for %d claimed kinds, want one each with the foreign kind as one class line", got, len(kinds))
	}
	buf.Reset()
	sink.RecordProjectedRowsCount(ctx, principalOrg1, 7, true)
	sink.RecordProjectedRowsCount(ctx, principalOrg1, 3, false)
	certifyRequestLine(t, buf.Bytes(), eventspec.ProjectedRowsCount, map[string]any{"rows_count": 7, "truncated": true})
	certifyRequestLine(t, buf.Bytes(), eventspec.ProjectedRowsCount, map[string]any{"rows_count": 3, "truncated": false})
	sink.RecordProjectedRowsCount(ctx, principalOrg1, 1, false)
	certifyRefused(t, buf.Bytes(), eventspec.ProjectedRowsCount, "a third projected rows line in one request", map[string]any{"rows_count": 1})
	buf.Reset()
	sink.RecordProjectedRowsByFactKind(ctx, principalOrg1, map[contextfabric.FactKind]int{"future_a": 2, "future_b": 3})
	if strings.Count(buf.String(), "by fact kind") != 1 || !strings.Contains(buf.String(), `"rows_projected_by_fact_kind":5`) {
		t.Fatalf("kinds outside the contract were not summed into one class line: %s", buf.String())
	}
}

func TestSynthesisCountLinesCertifyWhatTheSynthesizerWrites(t *testing.T) {
	t.Parallel()
	run := func(variant string) []byte {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		if err := contextfabric.SynthesizeWithTelemetryForTest(ctx, sink, variant); err != nil {
			t.Fatalf("%s: %v", variant, err)
		}
		return buf.Bytes()
	}
	count := func(raw []byte, event eventspec.Event) int { return bytes.Count(raw, []byte(`"msg":"`+event.Msg+`"`)) }

	clean := run("clean")
	certifyRequestLine(t, clean, eventspec.DriverIdentityCollisions, map[string]any{"org_id": "org_1", "cf_driver_identity_collisions": 0, "cf_driver_identity_restated": 0, "cf_driver_identity_reidentified": 0})
	certifyRequestLine(t, clean, eventspec.DualTableFacts, map[string]any{"org_id": "org_1", "dual_table_claims": 0, "secondary_rows_bytes": 0})
	for _, event := range []eventspec.Event{eventspec.ModelRowsStripped, eventspec.DriverSourceStateCorrected} {
		if count(clean, event) != 0 {
			t.Errorf("%s was written for a clean draft", event.ID)
		}
		log, _ := certify.Parse(clean)
		if err := certify.CertifyAbsent(log, event, map[string]any{"request_id": factScopeCertifyRequestID}); err != nil {
			t.Errorf("%s: %v", event.ID, err)
		}
	}

	collision := run("collision")
	certifyRequestLine(t, collision, eventspec.DriverIdentityCollisions, map[string]any{
		"cf_driver_identity_collisions": 1, "cf_driver_identity_reidentified": 1, "cf_driver_identity_restated": 0,
	})

	stripped := run("stripped")
	certifyRequestLine(t, stripped, eventspec.ModelRowsStripped, map[string]any{"org_id": "org_1", "cf_model_rows_stripped": 1})

	corrected := run("source_state")
	certifyRequestLine(t, corrected, eventspec.DriverSourceStateCorrected, map[string]any{"org_id": "org_1", "cf_driver_source_state_corrected": 1})

	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	producedClaims, producedBytes := contextfabric.DualTableCountsForTest()
	if producedClaims != 1 || producedBytes == 0 {
		t.Fatalf("the row attachment over a dual table fact reported %d claims and %d bytes, want 1 claim and a nonzero cost", producedClaims, producedBytes)
	}
	sink.RecordDualTableFacts(ctx, principalOrg1, producedClaims, producedBytes)
	certifyRequestLine(t, buf.Bytes(), eventspec.DualTableFacts, map[string]any{"dual_table_claims": producedClaims, "secondary_rows_bytes": producedBytes})
	buf.Reset()
	sink.RecordDualTableFacts(ctx, principalOrg1, 3, 1024)
	certifyRequestLine(t, buf.Bytes(), eventspec.DualTableFacts, map[string]any{"dual_table_claims": 3, "secondary_rows_bytes": 1024})
	buf.Reset()
	sink.RecordDualTableFacts(ctx, principalOrg1, 3, 1024)
	sink.RecordDualTableFacts(ctx, principalOrg1, 1, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.DualTableFacts, map[string]any{"dual_table_claims": 3, "secondary_rows_bytes": 1024})
	certifyRequestLine(t, buf.Bytes(), eventspec.DualTableFacts, map[string]any{"dual_table_claims": 1, "secondary_rows_bytes": 1})
	sink.RecordDualTableFacts(ctx, principalOrg1, 2, 2)
	certifyRefused(t, buf.Bytes(), eventspec.DualTableFacts, "a third dual table line in one request (two synthesis calls)", map[string]any{"dual_table_claims": 2})
}

func TestStatusCategoryCompositionLinesCertifyWhatTheEngineWrites(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	contextfabric.E6StatusCategoryCompositionForTest(ctx, sink)
	if got := bytes.Count(buf.Bytes(), []byte(eventspec.CategoryFactComposition.Msg)); got != 3 {
		t.Fatalf("a status requirement over repository, team, project and work item subjects wrote %d composition lines, want 3 (the work item composes nothing)", got)
	}
	seen := map[string]bool{}
	for _, kind := range contextfabric.StatusCategoryComposedSubjectKindVocabulary() {
		line := certifyRequestLine(t, buf.Bytes(), eventspec.CategoryFactComposition, map[string]any{"org_id": "org_1", "requirement_kind": "status", "subject_kind": kind})
		if len(line["composed_kinds"].([]any)) == 0 {
			t.Errorf("%s: no composed kinds", kind)
		}
		seen[kind] = true
	}
	sameMembers(t, "composed subject kinds", keysOf(seen), contextfabric.StatusCategoryComposedSubjectKindVocabulary())
	buf.Reset()
	sink.RecordCategoryFactComposition(ctx, principalOrg1, contextfabric.CategoryFactCompositionEvent{RequirementKind: contextfabric.FactStatus, SubjectKind: "future_kind"})
	certifyRefused(t, buf.Bytes(), eventspec.CategoryFactComposition, "an unknown subject kind")
	buf.Reset()
	sink.RecordCategoryFactComposition(ctx, principalOrg1, contextfabric.CategoryFactCompositionEvent{RequirementKind: contextfabric.FactStatus, SubjectKind: contextfabric.SubjectTeam, ComposedKinds: []contextfabric.FactKind{"future_fact"}})
	certifyRefused(t, buf.Bytes(), eventspec.CategoryFactComposition, "an unknown composed fact kind")
}

func TestPeriodDeltaLinesCertifyEveryOutcomeTheEngineWrites(t *testing.T) {
	t.Parallel()
	failures := map[string]bool{}
	for scenario, want := range map[string]struct {
		failure string
		issued  bool
	}{
		"compared": {"", true}, "deadline": {"deadline_exceeded", false}, "canceled": {"canceled", false},
		"read_failed": {"read_failed", false}, "anchor": {"anchor_unresolved", false},
	} {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		contextfabric.PeriodDeltaForTest(ctx, sink, scenario)
		if got := bytes.Count(buf.Bytes(), []byte(eventspec.PeriodDeltaComposition.Msg)); got != 2 {
			t.Fatalf("%s: %d period delta lines for a team and a project, want 2", scenario, got)
		}
		for _, kind := range contextfabric.PeriodDeltaSubjectKindVocabulary() {
			line := certifyRequestLine(t, buf.Bytes(), eventspec.PeriodDeltaComposition, map[string]any{
				"org_id": "org_1", "requirement_kind": "status", "subject_kind": kind, "grain": kind,
			})
			counts := line["transition_counts"].(map[string]any)
			keys := map[string]bool{}
			total := 0.0
			for transition, count := range counts {
				keys[transition] = true
				total += count.(float64)
			}
			sameMembers(t, scenario+" transition keys (zeros included)", keysOf(keys), contextfabric.PeriodDeltaTransitionLogVocabulary())
			if total != 1 {
				t.Errorf("%s/%s: transitions counted %v subjects, want 1", scenario, kind, total)
			}
			if kind == "team" && scenario != "compared" && line["failure_reason"] != want.failure {
				t.Errorf("%s: failure_reason = %v, want %q", scenario, line["failure_reason"], want.failure)
			}
			failures[line["failure_reason"].(string)] = true
		}
	}
	sameMembers(t, "period delta failure reasons", keysOf(failures), contextfabric.PeriodDeltaFailureReasonVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordPeriodDeltaComposition(ctx, principalOrg1, contextfabric.PeriodDeltaCompositionEvent{
		RequirementKind: contextfabric.FactStatus, SubjectKind: contextfabric.SubjectTeam, Grain: contextfabric.SubjectTeam,
		TransitionCounts: map[contextfabric.PeriodDeltaTransition]int{"future_transition": 1},
	})
	certifyRefused(t, buf.Bytes(), eventspec.PeriodDeltaComposition, "a transition outside the vocabulary")
}

func TestCoverageLinesCertifyWhatTheEngineWrites(t *testing.T) {
	t.Parallel()
	served, omitted, fallbacks := contextfabric.CoverageCapAndLabelsForTest()
	if served != 105 || omitted != 5 || fallbacks == 0 {
		t.Fatalf("cap and labels reported served=%d omitted=%d fallbacks=%d, want 105, 5 and a nonzero fallback count", served, omitted, fallbacks)
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordCoverageEntriesCapped(ctx, principalOrg1, served, omitted)
	sink.RecordEvidenceLabelFallback(ctx, principalOrg1, fallbacks)
	certifyRequestLine(t, buf.Bytes(), eventspec.CoverageEntriesCapped, map[string]any{"org_id": "org_1", "coverage_entries_served": served, "coverage_entries_omitted": omitted, "coverage_entries_bound": 105})
	certifyRequestLine(t, buf.Bytes(), eventspec.EvidenceLabelFallback, map[string]any{"org_id": "org_1", "cf_evidence_label_fallback": fallbacks})
	sink.RecordCoverageEntriesCapped(ctx, principalOrg1, served, omitted)
	certifyRequestLine(t, buf.Bytes(), eventspec.CoverageEntriesCapped, map[string]any{"coverage_entries_omitted": omitted})
	sink.RecordCoverageEntriesCapped(ctx, principalOrg1, served, omitted)
	certifyRefused(t, buf.Bytes(), eventspec.CoverageEntriesCapped, "a third capped line in one request", map[string]any{"coverage_entries_omitted": omitted})
	sink.RecordEvidenceLabelFallback(ctx, principalOrg1, 1)
	certifyRefused(t, buf.Bytes(), eventspec.EvidenceLabelFallback, "a second label fallback line in one request")

	outcomes, violations := map[string]bool{}, map[string]bool{}
	for _, verdict := range contextfabric.CoverageDisclosureVerdictsForTest() {
		buf.Reset()
		sink.RecordCoverageDisclosurePhrasing(ctx, principalOrg1, verdict.Outcome, verdict.Violation, verdict.Phrased, verdict.Total)
		certifyRequestLine(t, buf.Bytes(), eventspec.CoverageDisclosurePhrasing, map[string]any{
			"org_id": "org_1", "outcome": string(verdict.Outcome), "violation": string(verdict.Violation), "phrased": verdict.Phrased, "total": verdict.Total,
		})
		outcomes[string(verdict.Outcome)] = true
		violations[string(verdict.Violation)] = true
	}
	sameMembers(t, "disclosure outcomes", keysOf(outcomes), contextfabric.CoverageDisclosureOutcomeVocabulary())
	sameMembers(t, "disclosure violations", keysOf(violations), contextfabric.CoverageDisclosureViolationVocabulary())
	buf.Reset()
	sink.RecordCoverageDisclosurePhrasing(ctx, principalOrg1, "future_outcome", "", 0, 0)
	certifyRefused(t, buf.Bytes(), eventspec.CoverageDisclosurePhrasing, "an unknown disclosure outcome")
	buf.Reset()
	sink.RecordCoverageDisclosurePhrasing(ctx, principalOrg1, contextfabric.CoverageDisclosureRejectedByGuard, "future_violation", 0, 0)
	certifyRefused(t, buf.Bytes(), eventspec.CoverageDisclosurePhrasing, "an unknown disclosure violation")
}
