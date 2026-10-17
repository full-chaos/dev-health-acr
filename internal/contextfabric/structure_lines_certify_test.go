package contextfabric_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
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
