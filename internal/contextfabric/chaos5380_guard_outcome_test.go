package contextfabric

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5380, review round 2. The model-call decision line one layer down records
// `outcome=success` as soon as the provider answers; the guard that decides whether the
// answer is USABLE runs afterwards, up here. So a guard that started rejecting every
// generated phrase was invisible at Info — the collected logs went on showing successful
// model calls while every caller silently fell back to structural.
//
// These pins hold the second fact: one guard-decision line per Phrase call, on EVERY
// path, carrying explicit values.

const guardDecisionMessage = "context fabric offer phrasing guard decision"

type guardCapture struct {
	mu      sync.Mutex
	records []map[string]any
}

// Enabled GATES ON LEVEL. codex r4 P3: an overlay mutation moving this event to Debug
// SURVIVED the package suite, because the capture handler accepted every level -- so the
// test proved the line's CONTENT and said nothing about whether a collector would ever
// see it. A handler that accepts everything cannot notice a demotion, and a demotion is
// exactly how an observable dies quietly. This one collects at Info, like the deployed
// sink, so a Debug-level emission records nothing and every assertion below fails.
func (h *guardCapture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *guardCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Message != guardDecisionMessage {
		return nil
	}
	attrs := make(map[string]any, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, attrs)
	return nil
}

func (h *guardCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *guardCapture) WithGroup(string) slog.Handler      { return h }

func (h *guardCapture) only(t *testing.T) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 1 {
		t.Fatalf("guard decision events = %d, want exactly 1: %#v", len(h.records), h.records)
	}
	return h.records[0]
}

// guardInt coerces slog's numeric storage. slog.Value.Any() hands an int back as int64,
// so a bare `got != 0` compares an int64 against an int and is ALWAYS unequal -- a
// comparison that passes for the wrong reason in one direction and fails for the wrong
// reason in the other. Coerce once, here.
func guardInt(t *testing.T, attrs map[string]any, key string) int {
	t.Helper()
	switch n := guardAttr(t, attrs, key).(type) {
	case int:
		return n
	case int64:
		return int(n)
	default:
		t.Fatalf("guard decision attribute %q = %#v, want an integer", key, n)
		return 0
	}
}

func guardLogger() (*guardCapture, *slog.Logger) {
	h := &guardCapture{}
	return h, slog.New(h)
}

func guardAttr(t *testing.T, attrs map[string]any, key string) any {
	t.Helper()
	v, ok := attrs[key]
	if !ok {
		t.Fatalf("guard decision missing attribute %q: %#v", key, attrs)
	}
	return v
}

// TestGuardDecisionExposesARejectingGuardBesideASucceedingModelCall is the regression
// itself: the model call succeeded, the guard rejected every phrasing, and Info must say
// so. Before this line existed the only Info evidence was a successful model call.
func TestGuardDecisionExposesARejectingGuardBesideASucceedingModelCall(t *testing.T) {
	t.Parallel()
	handler, logger := guardLogger()
	// A draft naming an option that was never offered — classifyOfferPhrasingDraft's
	// own rejection shape — while the model call itself reports success.
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_never_offered", Phrasing: "something the guard must refuse"},
	}}
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
		Sink:    &fakeReceiptSink{},
		Logger:  logger,
	}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
		StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})
	if result.Outcome == OfferPhrasingGenerated {
		t.Fatalf("fixture did not trip the guard: %q", result.Outcome)
	}

	attrs := handler.only(t)
	if got := guardAttr(t, attrs, "guard_outcome"); got != string(result.Outcome) {
		t.Fatalf("guard_outcome = %v, want %q", got, result.Outcome)
	}
	// THE POINT: the model call's own outcome says success on the very same line.
	if got := guardAttr(t, attrs, "model_outcome"); got != "success" {
		t.Fatalf("model_outcome = %v, want %q — the line must show both facts", got, "success")
	}
	if got := guardInt(t, attrs, "phrasings_applied"); got != 0 {
		t.Fatalf("phrasings_applied = %v, want an explicit 0", got)
	}
	if got := guardInt(t, attrs, "offers_in"); got != len(offerOptions()) {
		t.Fatalf("offers_in = %v, want %d", got, len(offerOptions()))
	}
}

// TestGuardDecisionFiresOnEveryPathWithExplicitValues: an observable that only fires on
// the interesting arm cannot distinguish "not reached" from "reached and fine".
func TestGuardDecisionFiresOnEveryPathWithExplicitValues(t *testing.T) {
	t.Parallel()

	t.Run("generated", func(t *testing.T) {
		handler, logger := guardLogger()
		draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
			{OptionID: "opt_pr", Phrasing: "an open pull request"},
		}}
		phraser := RuntimeOfferPhraser{
			Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
			Sink:    &fakeReceiptSink{}, Logger: logger,
		}
		result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
			StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})
		attrs := handler.only(t)
		if result.Outcome != OfferPhrasingGenerated {
			t.Fatalf("Outcome = %q", result.Outcome)
		}
		if got := guardAttr(t, attrs, "guard_outcome"); got != string(OfferPhrasingGenerated) {
			t.Fatalf("guard_outcome = %v", got)
		}
		if got := guardInt(t, attrs, "phrasings_applied"); got != len(result.Phrasings) {
			t.Fatalf("phrasings_applied = %v, want %d", got, len(result.Phrasings))
		}
	})

	t.Run("no runtime configured", func(t *testing.T) {
		handler, logger := guardLogger()
		phraser := RuntimeOfferPhraser{Logger: logger}
		phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
			StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})
		attrs := handler.only(t)
		if got := guardAttr(t, attrs, "guard_outcome"); got != string(OfferPhrasingFellBackStructural) {
			t.Fatalf("guard_outcome = %v", got)
		}
		// operation is empty because no receipt was ever produced — a shape no real
		// outcome yields, which is exactly how "never reached the model" reads.
		if got := guardAttr(t, attrs, "operation"); got != "" {
			t.Fatalf("operation = %v, want empty on the no-runtime arm", got)
		}
		if got := guardInt(t, attrs, "phrasings_applied"); got != 0 {
			t.Fatalf("phrasings_applied = %v, want an explicit 0", got)
		}
	})

	t.Run("call failed", func(t *testing.T) {
		handler, logger := guardLogger()
		phraser := RuntimeOfferPhraser{
			Runtime: fakeOfferPhrasingModelRuntime{err: ErrModelUnavailable, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
			Sink:    &fakeReceiptSink{}, Logger: logger,
		}
		phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
			StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})
		attrs := handler.only(t)
		if got := guardAttr(t, attrs, "guard_outcome"); got != string(OfferPhrasingCallFailed) {
			t.Fatalf("guard_outcome = %v, want %q", got, OfferPhrasingCallFailed)
		}
	})
}

// TestGuardDecisionNeverCarriesPhrasingText is the corpus-safety bijection for the third
// place this seam now logs from: counts, closed vocabularies and correlation ids only.
func TestGuardDecisionNeverCarriesPhrasingText(t *testing.T) {
	t.Parallel()
	const marker = "MARKER_PHRASING_0f2a71 confidential wording"
	handler, logger := guardLogger()
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_pr", Phrasing: marker},
	}}
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
		Sink:    &fakeReceiptSink{}, Logger: logger,
	}
	phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
		StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})

	allowed := map[string]bool{
		"request_id": true, "org_id": true, "operation": true,
		"model_outcome": true, "guard_outcome": true,
		"phrasings_applied": true, "offers_in": true,
	}
	attrs := handler.only(t)
	for key := range allowed {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("guard decision missing expected field %q: %#v", key, attrs)
		}
	}
	if len(attrs) != len(allowed) {
		t.Fatalf("guard decision carries %d fields, want exactly %d: %#v", len(attrs), len(allowed), attrs)
	}
	for key, value := range attrs {
		if !allowed[key] {
			t.Fatalf("guard decision carries unexpected field %q = %#v", key, value)
		}
		if s, ok := value.(string); ok && strings.Contains(s, "MARKER_PHRASING") {
			t.Fatalf("guard decision field %q leaked phrasing text: %q", key, s)
		}
	}
}

// TestGuardDecisionIsEmittedAtInfoNotDebug is the level-gated control codex r4 asked for.
// Content assertions elsewhere in this file cannot see a demotion; this one can, and it
// discriminates in both directions so it cannot pass by accident.
func TestGuardDecisionIsEmittedAtInfoNotDebug(t *testing.T) {
	t.Parallel()
	run := func(collectAt slog.Level) int {
		inner := &guardCapture{}
		logger := slog.New(&levelGate{inner: inner, level: collectAt})
		phraser := RuntimeOfferPhraser{
			Runtime: fakeOfferPhrasingModelRuntime{
				draft:   StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "an open pull request"}}},
				receipt: validModelReceiptFixture(ModelOperationPhraseOffers),
			},
			Sink: &fakeReceiptSink{}, Logger: logger,
		}
		phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"},
			StructureOfferPhrasingInput{RequestID: "request_00000001", Options: offerOptions()})
		inner.mu.Lock()
		defer inner.mu.Unlock()
		return len(inner.records)
	}

	// A collector at Info -- the deployed level -- must see it.
	if n := run(slog.LevelInfo); n != 1 {
		t.Fatalf("guard decisions collected at LevelInfo = %d, want 1", n)
	}
	// And a collector ABOVE Info must not, or the control cannot discriminate and a
	// demotion to Debug would be invisible to this test too.
	if n := run(slog.LevelWarn); n != 0 {
		t.Fatalf("guard decisions collected at LevelWarn = %d, want 0 -- not discriminating", n)
	}
}

// levelGate is a real level filter around guardCapture, so "would a collector see this"
// is a measured question rather than an assumed one.
type levelGate struct {
	inner *guardCapture
	level slog.Level
}

func (h *levelGate) Enabled(_ context.Context, level slog.Level) bool { return level >= h.level }
func (h *levelGate) Handle(ctx context.Context, record slog.Record) error {
	return h.inner.Handle(ctx, record)
}
func (h *levelGate) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelGate) WithGroup(string) slog.Handler      { return h }
