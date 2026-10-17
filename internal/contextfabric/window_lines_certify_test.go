package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	windowBinderKeys    = []string{"time", "level", "msg", "org_id", "surface", "reason", "relative_id", "grammar", "spans_bound", "request_id"}
	statedWindowKeys    = []string{"time", "level", "msg", "org_id", "surface", "origin", "interpreted_axis", "executed_axis", "outcome", "request_id"}
	windowGatedKeys     = []string{"time", "level", "msg", "org_id", "surface", "origin", "relative_id", "provenance", "window_class", "request_id"}
	windowCanonKeys     = []string{"time", "level", "msg", "org_id", "outcome", "request_id"}
	windowCarryKeys     = []string{"time", "level", "msg", "org_id", "outcome", "chain_depth", "seed_source", "via_stored_ancestry", "request_id"}
	kindCarryKeys       = []string{"time", "level", "msg", "org_id", "outcome", "chain_depth", "carried_kind", "redeemed_kind", "seed_source", "via_stored_ancestry", "request_id"}
	ledgerWindowKeys    = []string{"time", "level", "msg", "org_id", "decision", "source_result_id", "applied_window", "request_id"}
	windowBinderSurface = "mcp"
)

func TestWindowBinderLineCertifiesWhatTheBinderProposes(t *testing.T) {
	t.Parallel()
	questions := []string{
		"how is the team doing?",
		"Last month versus last quarter, how did we do?",
		"Which repository carried the most work last month and why?",
		"Last month, how is the team doing?", "Last quarter, how is the team doing?", "Last year, how is the team doing?",
		"Last 30 days, how is the team doing?", "Past 90 days, how is the team doing?", "Last 3 months, how is the team doing?",
		"Last 365 days, how is the team doing?", "Last 12 months, how is the team doing?",
	}
	seenReason, seenGrammar := map[string]bool{}, map[string]bool{}
	for _, question := range questions {
		outcome := contextfabric.ProposeWindowFromSpans(question)
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordWindowBinderOutcome(ctx, principalOrg1, windowBinderSurface, outcome)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowBinderKeys, 1)
		line := certifyRequestLine(t, buf.Bytes(), eventspec.WindowBinderOutcome, map[string]any{"org_id": "org_1", "surface": windowBinderSurface})
		seenReason[line["reason"].(string)] = true
		seenGrammar[line["grammar"].(string)] = true
	}
	sameMembers(t, "binder reasons", keysOf(seenReason), contextfabric.WindowBindReasonVocabulary())
	sameMembers(t, "binder grammars", keysOf(seenGrammar), contextfabric.WindowBindGrammarVocabulary())
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordWindowBinderOutcome(ctx, principalOrg1, windowBinderSurface, contextfabric.WindowBindOutcome{Reason: "future_reason"})
	certifyRefused(t, buf.Bytes(), eventspec.WindowBinderOutcome, "an unknown binder reason")
	buf.Reset()
	sink.RecordWindowBinderOutcome(ctx, principalOrg1, windowBinderSurface, contextfabric.WindowBindOutcome{Reason: contextfabric.WindowBindRoutedInferred, Grammar: "future_grammar"})
	certifyRefused(t, buf.Bytes(), eventspec.WindowBinderOutcome, "an unknown grammar")
	buf.Reset()
	sink.RecordWindowBinderOutcome(context.Background(), principalOrg1, windowBinderSurface, contextfabric.ProposeWindowFromSpans("how is the team doing?"))
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), without(windowBinderKeys, "request_id"), 1)
}

func TestStatedWindowAxisLineCertifiesEveryProducerValueAndRefusesAnyOther(t *testing.T) {
	t.Parallel()
	axes := contextfabric.TemporalAxisLogVocabulary()
	for _, origin := range contextfabric.StatedWindowOriginVocabulary() {
		for _, outcome := range contextfabric.StatedWindowAxisOutcomeVocabulary() {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordStatedWindowAxis(ctx, principalOrg1, windowBinderSurface, origin, contextfabric.TemporalAxis(axes[1]), contextfabric.TemporalAxis(axes[len(axes)-1]), contextfabric.StatedWindowAxisOutcome(outcome))
			pinKeys(t, bytes.NewBuffer(buf.Bytes()), statedWindowKeys, 1)
			certifyRequestLine(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, map[string]any{"origin": origin, "outcome": outcome})
		}
	}
	for _, axis := range axes {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordStatedWindowAxis(ctx, principalOrg1, windowBinderSurface, contextfabric.StatedWindowOriginField, contextfabric.TemporalAxis(axis), contextfabric.TemporalAxis(axis), contextfabric.StatedWindowAxisAgreed)
		certifyRequestLine(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, map[string]any{"interpreted_axis": axis, "executed_axis": axis})
	}
	long := strings.Repeat("s", 200)
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordStatedWindowAxis(ctx, principalOrg1, long, contextfabric.StatedWindowOriginField, "current", "current", contextfabric.StatedWindowAxisAgreed)
	certifyRequestLine(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, map[string]any{"surface": long})
	buf.Reset()
	sink.RecordStatedWindowAxis(ctx, principalOrg1, windowBinderSurface, "future_origin", "current", "current", contextfabric.StatedWindowAxisAgreed)
	certifyRefused(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, "an unknown origin")
	buf.Reset()
	sink.RecordStatedWindowAxis(ctx, principalOrg1, windowBinderSurface, contextfabric.StatedWindowOriginField, "future_axis", "current", contextfabric.StatedWindowAxisAgreed)
	certifyRefused(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, "an unknown axis")
	buf.Reset()
	sink.RecordStatedWindowAxis(ctx, principalOrg1, windowBinderSurface, contextfabric.StatedWindowOriginField, "current", "current", "future_outcome")
	certifyRefused(t, buf.Bytes(), eventspec.StatedWindowAxisDecision, "an unknown outcome")
}

func TestWindowGatedAndCanonicalizationLinesCertifyEveryProducerValue(t *testing.T) {
	t.Parallel()
	for _, origin := range contextfabric.WindowGatedOriginVocabulary() {
		for _, provenance := range contextfabric.WindowProvenanceLogVocabulary() {
			for _, class := range contextfabric.WindowClassLogVocabulary() {
				for _, relative := range contextfabric.RelativeWindowIDLogVocabulary() {
					var buf bytes.Buffer
					sink, ctx := answerLineSink(&buf, slog.LevelInfo)
					sink.RecordWindowGatedForConfirmation(ctx, principalOrg1, windowBinderSurface, contextfabric.WindowCanonicalizationOutcome(origin), contractsv1.ContextFabricEffectiveEvidenceWindow{
						RelativeID: contractsv1.ContextFabricRelativeWindowID(relative), Provenance: contractsv1.ContextFabricWindowProvenance(provenance), WindowClass: contractsv1.ContextFabricWindowClass(class),
					})
					pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowGatedKeys, 1)
					certifyRequestLine(t, buf.Bytes(), eventspec.WindowGatedForConfirmation, map[string]any{
						"origin": origin, "provenance": provenance, "window_class": class, "relative_id": relative,
					})
				}
			}
		}
	}
	for _, outcome := range contextfabric.WindowCanonicalizationOutcomeVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordWindowCanonicalization(ctx, principalOrg1, contextfabric.WindowCanonicalizationOutcome(outcome))
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowCanonKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.WindowCanonicalizationOutcome, map[string]any{"outcome": outcome})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordWindowGatedForConfirmation(ctx, principalOrg1, windowBinderSurface, contextfabric.WindowCanonicalizationCarried, contractsv1.ContextFabricEffectiveEvidenceWindow{})
	certifyRefused(t, buf.Bytes(), eventspec.WindowGatedForConfirmation, "an origin the gate never writes")
	buf.Reset()
	sink.RecordWindowCanonicalization(ctx, principalOrg1, "future_outcome")
	certifyRefused(t, buf.Bytes(), eventspec.WindowCanonicalizationOutcome, "an unknown canonicalization outcome")
}

func TestCarryAndLedgerWindowLinesCertifyEveryProducerValue(t *testing.T) {
	t.Parallel()
	for _, seed := range contextfabric.CarrySeedSourceVocabulary() {
		for _, outcome := range contextfabric.WindowCarryOutcomeVocabulary() {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordWindowCarry(ctx, principalOrg1, contextfabric.WindowCarryOutcome(outcome), 2, contextfabric.CarrySeedSource(seed), true)
			pinKeys(t, bytes.NewBuffer(buf.Bytes()), windowCarryKeys, 1)
			certifyRequestLine(t, buf.Bytes(), eventspec.WindowCarry, map[string]any{"outcome": outcome, "seed_source": seed, "chain_depth": 2, "via_stored_ancestry": true})
		}
		for _, outcome := range contextfabric.KindCarryOutcomeVocabulary() {
			for _, kind := range append([]contractsv1.ContextFabricSubjectKind{""}, kindsOf()...) {
				var buf bytes.Buffer
				sink, ctx := answerLineSink(&buf, slog.LevelInfo)
				sink.RecordKindCarry(ctx, principalOrg1, contextfabric.KindCarryOutcome(outcome), 1, kind, kind, contextfabric.CarrySeedSource(seed), false)
				pinKeys(t, bytes.NewBuffer(buf.Bytes()), kindCarryKeys, 1)
				certifyRequestLine(t, buf.Bytes(), eventspec.KindCarry, map[string]any{"outcome": outcome, "seed_source": seed, "carried_kind": string(kind), "redeemed_kind": string(kind)})
			}
		}
	}
	for _, decision := range contextfabric.ConfirmedNeedLedgerWindowDecisionVocabulary() {
		for _, applied := range []string{contextfabric.LedgerWindowObservedForTest(contextfabric.LedgerWindowAbsoluteValueForTest()), string(contractsv1.ContextFabricRelativeWindowTrailing30D), ""} {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordConfirmedNeedLedgerWindow(ctx, principalOrg1, contextfabric.ConfirmedNeedLedgerWindowDecision(decision), "result_parent_1", applied)
			pinKeys(t, bytes.NewBuffer(buf.Bytes()), ledgerWindowKeys, 1)
			certifyRequestLine(t, buf.Bytes(), eventspec.ConfirmedNeedLedgerWindow, map[string]any{"decision": decision, "applied_window": applied})
		}
	}
	if got := contextfabric.LedgerWindowObservedForTest(contextfabric.LedgerWindowAbsoluteValueForTest()); got != "absolute" {
		t.Errorf("an absolute window renders as %q, want the word absolute and no bounds", got)
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordWindowCarry(ctx, principalOrg1, "future_outcome", 1, contextfabric.CarrySeedNone, false)
	certifyRefused(t, buf.Bytes(), eventspec.WindowCarry, "an unknown window carry outcome")
	buf.Reset()
	sink.RecordKindCarry(ctx, principalOrg1, contextfabric.KindCarryHit, 1, "future_kind", "", contextfabric.CarrySeedNone, false)
	certifyRefused(t, buf.Bytes(), eventspec.KindCarry, "an unknown carried kind")
	buf.Reset()
	sink.RecordConfirmedNeedLedgerWindow(ctx, principalOrg1, contextfabric.ConfirmedNeedLedgerWindowApplied, "result_parent_1", "window:1:2")
	certifyRefused(t, buf.Bytes(), eventspec.ConfirmedNeedLedgerWindow, "an applied window that carries bounds")
}

func kindsOf() []contractsv1.ContextFabricSubjectKind {
	all := contractsv1.ContextFabricSubjectKindVocabulary()
	return all[:]
}
