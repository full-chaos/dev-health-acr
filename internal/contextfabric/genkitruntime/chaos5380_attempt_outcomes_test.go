package genkitruntime

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/firebase/genkit/go/core"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5380. The decision event carried `attempts` (a COUNT) and `outcome`
// (the TERMINAL result) and nothing else about the passes in between, so
// `attempts=2 outcome=success` could not say what the failed attempt WAS.
// That is the engine-side instance of the regression-diagnosis doc's O4
// ("Retries made successful eventual outcomes erase failed attempts from that
// statistic", :172) and of §5's "Attempt/eventual separation | Preserve all
// failed attempts" (:202).
//
// These pins assert the EMITTED LINE, never withRetry's return value: the
// merge gate's own words are "a test asserts the actual emitted line, not a
// recording-tracer call" (cf-standing-rules 2026-09-06T19:20Z).

// sequencedGenerator returns a DIFFERENT result per call, so one runtime can
// express "attempt 1 failed, attempt 2 served". generatorStub cannot: its
// error field is fixed for the life of the stub, which is why every existing
// retry test measures a uniform sequence only.
type sequencedGenerator struct {
	interpretation interpretationOutput
	synthesis      synthesisOutput
	phrasing       phrasingOutput
	// errs is consumed one per call. A call past the end returns nil (served).
	errs  []error
	calls int
}

func (g *sequencedGenerator) next() error {
	g.calls++
	if g.calls-1 < len(g.errs) {
		return g.errs[g.calls-1]
	}
	return nil
}

func (g *sequencedGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	if err := g.next(); err != nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, err
	}
	return g.interpretation, contextfabric.ModelUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}, nil
}

func (g *sequencedGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	if err := g.next(); err != nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, err
	}
	return g.synthesis, contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, nil
}

func (g *sequencedGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	if err := g.next(); err != nil {
		return phrasingOutput{}, contextfabric.ModelUsage{}, err
	}
	return g.phrasing, contextfabric.ModelUsage{InputTokens: 6, OutputTokens: 2, TotalTokens: 8}, nil
}

// retryableUnavailable is a STRUCTURED transport failure: retryable() only
// trusts *core.GenkitError statuses, never message text (CHAOS-3770 F2), so a
// plain errors.New would not retry at all and the pin would measure nothing.
func retryableUnavailable() error {
	return &core.GenkitError{Status: core.UNAVAILABLE, Message: "provider unavailable"}
}

func retryableRateLimited() error {
	return &core.GenkitError{Status: core.RESOURCE_EXHAUSTED, Message: "rate limited"}
}

func attrInt(t *testing.T, attrs map[string]any, key string) int {
	t.Helper()
	v, ok := attrs[key]
	if !ok {
		t.Fatalf("decision event missing attribute %q: %#v", key, attrs)
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	default:
		t.Fatalf("decision event attribute %q = %#v, want int", key, v)
		return 0
	}
}

func onlyDecisionEvent(t *testing.T, h *captureLogger) decisionRecord {
	t.Helper()
	events := h.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want exactly 1: %#v", len(events), events)
	}
	return events[0]
}

// TestDecisionEventCarriesEveryAttemptOnARetriedThenServedInterpretation is
// the ticket's headline acceptance: the retried-then-served request. The
// terminal outcome alone reports a clean success; the line must also say that
// attempt 1 was `unavailable`.
func TestDecisionEventCarriesEveryAttemptOnARetriedThenServedInterpretation(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput(), errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if generator.calls != 2 {
		t.Fatalf("generator calls = %d, want 2 (the retry must actually have happened)", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrInt(t, attrs, "attempts_total"); got != 2 {
		t.Fatalf("attempts_total = %d, want 2", got)
	}
	if got := attrInt(t, attrs, "attempts_retried"); got != 1 {
		t.Fatalf("attempts_retried = %d, want 1", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:success" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:unavailable,2:success")
	}
	// The terminal result stays exactly what it was: preserving attempts must
	// never restate the eventual outcome (§5 :202, "report ... eventual
	// outcome ... separately").
	if got := attrString(t, attrs, "outcome"); got != "success" {
		t.Fatalf("outcome = %q, want %q", got, "success")
	}
	// The per-attempt elapsed list is the "stage/answer latency separately"
	// half of :202: one entry per attempt, aligned with attempt_outcomes.
	elapsed := attrString(t, attrs, "attempt_elapsed_ms")
	if n := len(strings.Split(elapsed, ",")); n != 2 {
		t.Fatalf("attempt_elapsed_ms = %q, want 2 entries", elapsed)
	}
	for i, entry := range strings.Split(elapsed, ",") {
		if !strings.HasPrefix(entry, string(rune('1'+i))+":") {
			t.Fatalf("attempt_elapsed_ms entry %d = %q, want it indexed %d:", i, entry, i+1)
		}
	}
}

// TestDecisionEventCarriesExplicitZeroOnAFirstTryServe is the explicit-zero
// control. §5 :200 forbids reporting an absent measurement as a measured
// zero; the converse is that a REAL zero must be present rather than omitted,
// so a missing attempts_retried has exactly one meaning: the site was never
// reached (cf-standing-rules dictation 378).
func TestDecisionEventCarriesExplicitZeroOnAFirstTryServe(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if generator.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if _, ok := attrs["attempts_retried"]; !ok {
		t.Fatalf("attempts_retried OMITTED on an unretried serve; an explicit zero is required: %#v", attrs)
	}
	if got := attrInt(t, attrs, "attempts_retried"); got != 0 {
		t.Fatalf("attempts_retried = %d, want 0", got)
	}
	if got := attrInt(t, attrs, "attempts_total"); got != 1 {
		t.Fatalf("attempts_total = %d, want 1", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:success" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:success")
	}
	if got := attrInt(t, attrs, "fallback_attempts_total"); got != 0 {
		t.Fatalf("fallback_attempts_total = %d, want an explicit 0", got)
	}
}

// TestDecisionEventCarriesBothAttemptsWhenBothFail is the doubly-failed
// terminal: the corpus's 504-then-504 shape. Both attempts must survive even
// though the terminal outcome already says `unavailable` -- a reader must be
// able to tell one failed call from two.
func TestDecisionEventCarriesBothAttemptsWhenBothFail(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{errs: []error{retryableUnavailable(), retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the terminal failure")
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrInt(t, attrs, "attempts_total"); got != 2 {
		t.Fatalf("attempts_total = %d, want 2", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:unavailable" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:unavailable,2:unavailable")
	}
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q", got, "unavailable")
	}
}

// TestDecisionEventDistinguishesRateLimitFromTransportAcrossAttempts is the
// discrimination control. A count cannot tell a provider rate-limiting us
// apart from a provider being down; the class per attempt can. Without this
// the two shapes are byte-identical at Info.
func TestDecisionEventDistinguishesRateLimitFromTransportAcrossAttempts(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput(), errs: []error{retryableRateLimited()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:rate_limited,2:success" {
		t.Fatalf("attempt_outcomes = %q, want %q -- a rate limit must not read as a transport fault", got, "1:rate_limited,2:success")
	}
	// NEGATIVE CONTROL for this pin: the unavailable shape above produces a
	// DIFFERENT string on the same code path, so the assertion is not
	// satisfied by any constant.
	otherHandler, otherLogger := newCaptureLogger()
	otherRuntime := mustRuntime(t, &sequencedGenerator{interpretation: validInterpretationOutput(), errs: []error{retryableUnavailable()}}, Config{Logger: otherLogger, MaxAttempts: 2})
	if _, _, err := otherRuntime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("control InterpretQuestion() error = %v", err)
	}
	if got := attrString(t, onlyDecisionEvent(t, otherHandler).Attrs, "attempt_outcomes"); got == "1:rate_limited,2:success" {
		t.Fatal("control produced the rate-limited string for an unavailable failure; the field is not discriminating")
	}
}

// TestDecisionEventReportsTheFallbackLegsOwnAttempts closes the fallback
// hole. mergeFallbackReceipt keeps the PRIMARY's Attempts, so a fallback that
// itself retried left no trace anywhere: the line said attempts=1
// outcome=fallback for two model calls.
func TestDecisionEventReportsTheFallbackLegsOwnAttempts(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{errs: []error{retryableUnavailable(), retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{
		Logger: logger, MaxAttempts: 2,
		Fallback: fallbackRuntime{interpreted: validInterpretedQuestion(), draft: validDraft()},
	})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "fallback" {
		t.Fatalf("outcome = %q, want %q", got, "fallback")
	}
	// The PRIMARY's attempts stay the primary's: a fallback must not overwrite
	// the record of what the primary did.
	if got := attrInt(t, attrs, "attempts_total"); got != 2 {
		t.Fatalf("attempts_total = %d, want the primary's 2", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:unavailable" {
		t.Fatalf("attempt_outcomes = %q, want the primary's failures", got)
	}
	// validReceipt's Attempts is 1, so the fallback leg reports its own count.
	if got := attrInt(t, attrs, "fallback_attempts_total"); got != 1 {
		t.Fatalf("fallback_attempts_total = %d, want 1", got)
	}
}

// TestSynthesizeDecisionEventCarriesEveryAttempt pins the SECOND emitter.
// Round after round on this seam closed a finding at its one named example
// instead of at its class; the fields are asserted on both decision emitters,
// not just interpret's.
func TestSynthesizeDecisionEventCarriesEveryAttempt(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{synthesis: validSynthesisOutput(), errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrInt(t, attrs, "attempts_total"); got != 2 {
		t.Fatalf("attempts_total = %d, want 2", got)
	}
	if got := attrInt(t, attrs, "attempts_retried"); got != 1 {
		t.Fatalf("attempts_retried = %d, want 1", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:success" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:unavailable,2:success")
	}
}

// TestPhraseOffersEmitsADecisionEventWithItsAttempts closes the THIRD
// withRetry site. PhraseOffers retried through the same loop and emitted no
// decision line at all, so a phrasing model degrading was invisible at Info
// however many times it retried. "Every observable fires on EVERY pass
// through its site" (cf-standing-rules dictation 378).
func TestPhraseOffersEmitsADecisionEventWithItsAttempts(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{phrasing: phrasingOutput{}, errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2, PhrasingModel: "test/phrasing"})
	if _, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput()); err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrInt(t, attrs, "attempts_total"); got != 2 {
		t.Fatalf("attempts_total = %d, want 2", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:success" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:unavailable,2:success")
	}
}

// levelledCapture is captureLogger with a real level gate. captureLogger's
// Enabled() returns true unconditionally, which is right for the field
// assertions above but cannot express the ONE control §5 of the
// regression-diagnosis doc names by hand: "lower its level below collection
// ... diagnostic acceptance must fail".
type levelledCapture struct {
	inner *captureLogger
	level slog.Level
}

func (h *levelledCapture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *levelledCapture) Handle(ctx context.Context, record slog.Record) error {
	return h.inner.Handle(ctx, record)
}

func (h *levelledCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelledCapture) WithGroup(string) slog.Handler      { return h }

// TestAttemptSequenceIsCollectedAtInfoAndLostAboveIt is the discriminating
// control REQUIRED by §5 (:204). The positive half proves the stage is
// reachable at the production level; the negative half proves the assertion
// is not satisfiable by a handler that is not collecting Info -- if the
// engine had put this line at Debug, or behind its own config knob, the
// positive half would fail here rather than in production six weeks later.
func TestAttemptSequenceIsCollectedAtInfoAndLostAboveIt(t *testing.T) {
	t.Parallel()
	run := func(level slog.Level) []decisionRecord {
		inner := &captureLogger{}
		logger := slog.New(&levelledCapture{inner: inner, level: level})
		generator := &sequencedGenerator{interpretation: validInterpretationOutput(), errs: []error{retryableUnavailable()}}
		runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
		if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
			t.Fatalf("InterpretQuestion() error = %v", err)
		}
		return inner.decisionEvents()
	}

	// PRODUCTION LEVEL. slog's own zero-value handler level is Info, and
	// acr's service logger collects at Info, so this is the deployed case.
	atInfo := run(slog.LevelInfo)
	if len(atInfo) != 1 {
		t.Fatalf("decision events at LevelInfo = %d, want 1 -- the attempt sequence must survive the PRODUCTION level", len(atInfo))
	}
	if got := attrString(t, atInfo[0].Attrs, "attempt_outcomes"); got != "1:unavailable,2:success" {
		t.Fatalf("attempt_outcomes at LevelInfo = %q, want %q", got, "1:unavailable,2:success")
	}

	// ABOVE COLLECTION. Same code path, same retry, nothing recorded: the
	// pin is measuring the emission, not restating its own expectation.
	if aboveInfo := run(slog.LevelWarn); len(aboveInfo) != 0 {
		t.Fatalf("decision events at LevelWarn = %d, want 0 -- the control does not discriminate", len(aboveInfo))
	}
}

// TestPhraseDecisionEventNeverCarriesCorpusText closes the corpus-safety
// guard at its CLASS rather than at its two named examples. A third emitter
// with no allowlist is exactly how a field that should never have been added
// gets added; this is TestDecisionEventNeverCarriesCorpusText's exact-field
// bijection, applied to phrase_offers.
func TestPhraseDecisionEventNeverCarriesCorpusText(t *testing.T) {
	t.Parallel()
	const sensitiveLabel = "MARKER_LABEL_4c81de the confidential initiative"
	const sensitivePhrasing = "MARKER_PHRASING_2b70af leaked offer wording"

	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{phrasing: phrasingOutput{
		Phrasings: []phrasingEntryOutput{{OptionID: "opt_pr", Phrasing: sensitivePhrasing}},
	}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, PhrasingModel: "test/phrasing"})
	input := validPhrasingInput()
	input.Options[0].Label = sensitiveLabel
	if _, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}

	allowed := map[string]bool{
		"request_id": true, "org_id_hash": true, "operation": true, "outcome": true,
		"attempts": true, "attempts_total": true, "attempts_retried": true,
		"attempt_outcomes": true, "attempt_elapsed_ms": true,
		"model_id": true, "model_version": true, "prompt_version": true,
	}
	event := onlyDecisionEvent(t, handler)
	for key := range allowed {
		if _, ok := event.Attrs[key]; !ok {
			t.Fatalf("phrase decision event missing expected field %q: %#v", key, event.Attrs)
		}
	}
	if len(event.Attrs) != len(allowed) {
		t.Fatalf("phrase decision event carries %d fields, want exactly %d: %#v", len(event.Attrs), len(allowed), event.Attrs)
	}
	for key, value := range event.Attrs {
		if !allowed[key] {
			t.Fatalf("phrase decision event carries unexpected field %q = %#v", key, value)
		}
		s, ok := value.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, "MARKER_LABEL") || strings.Contains(s, "MARKER_PHRASING") {
			t.Fatalf("phrase decision event field %q = %q leaks offer label/phrasing text", key, s)
		}
	}
	if got := attrString(t, event.Attrs, "org_id_hash"); got == "org_1" || len(got) != 12 {
		t.Fatalf("org_id_hash = %q, want a 12-hex-character irreversible digest", got)
	}
}

// TestPhraseDecisionEventFiresOnARejectedCall pins the UNGUARDED emission.
// The other two emitters install their defer after their validation returns,
// so a rejected call logs nothing; this one installs it first, because an
// observable that fires only on the calls that got far enough cannot
// distinguish "the site was never reached" from "reached and fine".
func TestPhraseDecisionEventFiresOnARejectedCall(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	runtime := mustRuntime(t, &sequencedGenerator{}, Config{Logger: logger, PhrasingModel: "test/phrasing"})
	if _, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.StructureOfferPhrasingInput{}); err == nil {
		t.Fatal("PhraseStructureOffers() error = nil, want the empty-offer-set rejection")
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "operation"); got != "" {
		t.Fatalf("operation = %q, want the empty string a pre-receipt return produces", got)
	}
	if got := attrInt(t, attrs, "attempts_total"); got != 0 {
		t.Fatalf("attempts_total = %d, want 0 -- no model call was made", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "" {
		t.Fatalf("attempt_outcomes = %q, want empty", got)
	}
}

// TestPhraseDecisionIsEmittedAtInfoNotDebug is the level-gated control for the PHRASE
// model-decision event (codex r4 P3). An overlay mutation moving this event to Debug
// survived the package suite because `captureLogger.Enabled` accepts every level, so the
// existing assertions prove the line's CONTENT and say nothing about whether a collector
// would ever see it. Interpretation already has such a control and synthesis is covered by
// an existing test; these two NEW events were the gap.
func TestPhraseDecisionIsEmittedAtInfoNotDebug(t *testing.T) {
	t.Parallel()
	run := func(collectAt slog.Level) int {
		inner := &captureLogger{}
		logger := slog.New(&levelledCapture{inner: inner, level: collectAt})
		runtime := mustRuntime(t, &sequencedGenerator{phrasing: phrasingOutput{}},
			Config{Logger: logger, MaxAttempts: 2, PhrasingModel: "test/phrasing"})
		if _, _, err := runtime.PhraseStructureOffers(context.Background(),
			storage.Principal{OrgID: "org_1"}, validPhrasingInput()); err != nil {
			t.Fatalf("PhraseStructureOffers() error = %v", err)
		}
		return len(inner.decisionEvents())
	}

	if n := run(slog.LevelInfo); n != 1 {
		t.Fatalf("phrase decisions collected at LevelInfo = %d, want 1", n)
	}
	if n := run(slog.LevelWarn); n != 0 {
		t.Fatalf("phrase decisions collected at LevelWarn = %d, want 0 -- not discriminating", n)
	}
}
