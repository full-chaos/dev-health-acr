package genkitruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5380, PR-A follow-up pins (lane-thread-b-corpus). These drive the
// three items the recut handoff's PR-A axes table left open after 27d733ca's
// reuse: A3 (pre-call cancellation's own class), A5 (elapsed-by-VALUE), B4
// (primary-vs-answer identity), B6 (synthesize's missing level-gate pin), and
// C3 (the defer-hoist ruling, option (a)). Each asserts the EMITTED line
// through the real handler, never withRetry's return value alone.

// --- A3: pre-call cancellation is its own class, not "unavailable" ---

// TestPreCallCancellationIsItsOwnClass is the RED case: a context already
// cancelled BEFORE the first attempt must record a class that is NOT
// "unavailable" -- today it is, because receiptOutcomeForError defaults every
// non-rate-limited/non-invalid-output error (context.Canceled included) to
// "unavailable", identically to a provider actually being down.
func TestPreCallCancellationIsItsOwnClass(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the pre-call cancellation surfaced")
	}
	// PROVENANCE CONTROL: the provider was never reached. If this ever reads
	// nonzero, the "own class" assertion below is measuring the wrong arm.
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0 -- a pre-call cancellation must never reach the provider", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	got := attrString(t, attrs, "attempt_outcomes")
	if strings.Contains(got, "unavailable") {
		t.Fatalf("attempt_outcomes = %q -- a pre-call cancellation must not read identically to the provider being down", got)
	}
	if got != "1:cancelled" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:cancelled")
	}
}

// TestMidCallFailureStillClassifiesUnavailable is the PROVIDER-REACHED
// control for A3: a real mid-call failure (the provider answered, or the
// transport genuinely failed reaching it) must still classify "unavailable".
// Without this control, a fix that made EVERY failure read "cancelled" would
// also pass the RED test above for the wrong reason.
func TestMidCallFailureStillClassifiesUnavailable(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{errs: []error{retryableUnavailable(), retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the terminal failure")
	}
	if generator.calls != 2 {
		t.Fatalf("generator.calls = %d, want 2 -- the provider WAS reached both times", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:unavailable" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:unavailable,2:unavailable")
	}
}

// TestPreCallCancellationTerminalReceiptOutcomeIsCancelled pins the CHAOS-5577
// resolution (chris, dictation 969, "Recommends accepted", D2 = A): the
// terminal receipt.Outcome vocabulary is now widened with "cancelled" for
// exactly this case. This SUPERSEDES the earlier A3 scope-limit (team-lead,
// option (b), 2026-09-10), which deliberately left the terminal vocabulary
// unchanged pending this exact CHRIS-PENDING decision (see ADR 0008's dated
// CHAOS-5577 section). The per-attempt attempt_outcomes class is UNCHANGED by
// this ticket -- "1:cancelled" already existed before CHAOS-5577 and still
// reads that way; only the TERMINAL field's vocabulary widened.
func TestPreCallCancellationTerminalReceiptOutcomeIsCancelled(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	// MaxAttempts=1 so the single pre-call cancellation IS the terminal result.
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the pre-call cancellation surfaced")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0 -- a pre-call cancellation must never reach the provider", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "cancelled" {
		t.Fatalf("outcome = %q, want %q (CHAOS-5577: the terminal vocabulary now distinguishes a "+
			"pre-call cancellation from a genuine provider outage)", got, "cancelled")
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:cancelled" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:cancelled")
	}
}

// inCallCancelGenerator lets a test cancel a context while a call is
// genuinely IN FLIGHT, as opposed to before the call is even placed --
// closing started is itself the happens-before edge the test goroutine
// synchronizes on before calling cancel(), so there is no data race waiting
// for the call to begin. sequencedGenerator cannot express this: none of its
// three methods ever blocks.
type inCallCancelGenerator struct {
	started chan struct{}
}

func (g *inCallCancelGenerator) Interpret(ctx context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	close(g.started)
	<-ctx.Done()
	return interpretationOutput{}, contextfabric.ModelUsage{}, ctx.Err()
}

func (g *inCallCancelGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

func (g *inCallCancelGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

// TestInCallCancellationTerminalReceiptOutcomeStaysUnavailable is the
// IN-CALL control CHAOS-5577's own lane brief requires pinned alongside the
// pre-call cell above: the generator IS invoked -- the call was actually
// placed and is genuinely in flight -- when its context is canceled. This
// must keep reporting the pre-existing "unavailable" terminal outcome, never
// "cancelled". Without this control, a fix that made every context
// cancellation read "cancelled" terminally (not just the pre-call one) would
// pass TestPreCallCancellationTerminalReceiptOutcomeIsCancelled above for the
// wrong reason.
func TestInCallCancellationTerminalReceiptOutcomeStaysUnavailable(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &inCallCancelGenerator{started: make(chan struct{})}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Timeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
		close(done)
	}()
	select {
	case <-generator.started:
		// The call is genuinely in flight -- cancel now, which is what makes
		// this case IN-CALL rather than pre-call.
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("generator was never invoked before the timeout -- cannot exercise the in-call case")
	}
	<-done

	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q -- an IN-CALL cancellation must keep the pre-CHAOS-5577 outcome, unlike the pre-call case", got, "unavailable")
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:cancelled" {
		t.Fatalf("attempt_outcomes = %q, want %q", got, "1:cancelled")
	}
}

// --- A5: elapsed is asserted by VALUE under an advancing clock ---

// TestAttemptElapsedIsNonZeroAndDistinctUnderAnAdvancingClock replaces the
// count/index-only pin: with r.now stubbed to ADVANCE a fixed step on every
// call, each attempt's ElapsedMS must be non-zero and the two entries must
// differ (attempt 2's window starts later). An `ElapsedMS: 0` mutant must
// die here -- the FROZEN clock every other pin uses cannot kill it, since a
// frozen clock produces zero regardless of whether the value is even read.
func TestAttemptElapsedIsNonZeroAndDistinctUnderAnAdvancingClock(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput(), errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	base := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	step := 0
	runtime.now = func() time.Time {
		step++
		// Each call advances 137ms -- an odd, unmistakable step so a
		// hard-coded constant in the pin cannot coincidentally match a
		// mutant's own hard-coded replacement value.
		return base.Add(time.Duration(step) * 137 * time.Millisecond)
	}
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	elapsed := attrString(t, attrs, "attempt_elapsed_ms")
	parts := strings.Split(elapsed, ",")
	if len(parts) != 2 {
		t.Fatalf("attempt_elapsed_ms = %q, want 2 entries", elapsed)
	}
	for i, p := range parts {
		kv := strings.SplitN(p, ":", 2)
		if len(kv) != 2 {
			t.Fatalf("attempt_elapsed_ms entry %d = %q, malformed", i, p)
		}
		if kv[1] == "0" {
			t.Fatalf("attempt_elapsed_ms entry %d = %q, want a NON-ZERO value under an advancing clock", i, p)
		}
	}
	if parts[0] == parts[1] {
		t.Fatalf("attempt_elapsed_ms = %q, want the two attempts' elapsed VALUES to differ", elapsed)
	}
}

// --- B4: primary identity, captured BEFORE mergeFallbackReceipt, separate from the answer identity ---

// TestDecisionLineCarriesPrimaryIdentitySeparateFromAnswerIdentity is r5
// P1-1: on a fallback success, model_id/model_version must stay the ANSWER
// identity (CHAOS-4631 r3 -- do not revert), and the PRIMARY's identity
// (which attempt_outcomes actually describes) must be on the line as its own
// fields. Changing the PRIMARY's model must change the decision record --
// r5 got byte-identical records on this exact shape, which is the red.
func TestDecisionLineCarriesPrimaryIdentitySeparateFromAnswerIdentity(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	// The primary's own generation fails (retryable, then exhausted) so the
	// fallback leg serves -- mustRuntime's fixed identity
	// (Provider="test-provider" Model="test/model" ModelVersion="test-model-v1")
	// is the PRIMARY's. fallbackRuntime returns validReceipt(), whose identity
	// (Provider="fallback" Model="deterministic" ModelVersion="v1") is
	// deliberately DIFFERENT -- indistinguishable identities would make this
	// pin pass vacuously regardless of which one the line actually carries.
	generator := &sequencedGenerator{errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{
		Logger: logger, MaxAttempts: 1,
		Fallback: fallbackRuntime{interpreted: validInterpretedQuestion()},
	})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v, want the fallback to serve", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "fallback" {
		t.Fatalf("outcome = %q, want %q", got, "fallback")
	}
	// ANSWER identity -- unchanged direction, CHAOS-4631 r3.
	if got := attrString(t, attrs, "model_id"); got != "deterministic" {
		t.Fatalf("model_id = %q, want the ANSWER (fallback) identity %q", got, "deterministic")
	}
	if got := attrString(t, attrs, "model_version"); got != "v1" {
		t.Fatalf("model_version = %q, want the ANSWER (fallback) identity %q", got, "v1")
	}
	// PRIMARY identity -- the NEW field pair, r5 P1-1: the identity the
	// attempt_outcomes sequence actually describes, captured BEFORE
	// mergeFallbackReceipt overwrites Provider/Model/ModelVersion.
	if got := attrString(t, attrs, "primary_model_id"); got != "test/model" {
		t.Fatalf("primary_model_id = %q, want the PRIMARY identity %q (the identity attempt_outcomes describes)", got, "test/model")
	}
	if got := attrString(t, attrs, "primary_model_version"); got != "test-model-v1" {
		t.Fatalf("primary_model_version = %q, want %q", got, "test-model-v1")
	}
	if got := attrString(t, attrs, "primary_provider"); got != "test-provider" {
		t.Fatalf("primary_provider = %q, want %q", got, "test-provider")
	}
}

// TestSynthesizeDecisionLineCarriesPrimaryIdentitySeparateFromAnswerIdentity
// is the SYNTHESIZE sibling of the interpret pin above -- a mutation-battery
// survivor caught this gap: a synthesize-only regression that zeroed the
// primary identity fields (present, but empty) satisfied every OTHER
// existing pin (field presence/count, corpus-safety) without this one, since
// none of them asserted the VALUES on synthesize's own line.
func TestSynthesizeDecisionLineCarriesPrimaryIdentitySeparateFromAnswerIdentity(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{errs: []error{retryableUnavailable()}}
	runtime := mustRuntime(t, generator, Config{
		Logger: logger, MaxAttempts: 1,
		Fallback: fallbackRuntime{draft: validDraft()},
	})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the fallback to serve", err)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "fallback" {
		t.Fatalf("outcome = %q, want %q", got, "fallback")
	}
	if got := attrString(t, attrs, "model_id"); got != "deterministic" {
		t.Fatalf("model_id = %q, want the ANSWER (fallback) identity %q", got, "deterministic")
	}
	if got := attrString(t, attrs, "primary_model_id"); got != "test/model" {
		t.Fatalf("primary_model_id = %q, want the PRIMARY identity %q", got, "test/model")
	}
	if got := attrString(t, attrs, "primary_model_version"); got != "test-model-v1" {
		t.Fatalf("primary_model_version = %q, want %q", got, "test-model-v1")
	}
	if got := attrString(t, attrs, "primary_provider"); got != "test-provider" {
		t.Fatalf("primary_provider = %q, want %q", got, "test-provider")
	}
}

// --- B6: the level-gate on SYNTHESIZE, the gap the axes-table enumeration found ---

// TestSynthesizeAttemptSequenceIsCollectedAtInfoAndLostAboveIt mirrors
// TestAttemptSequenceIsCollectedAtInfoAndLostAboveIt (interpret's pin) for
// SynthesizeAnswer. Enumerating B1 x B6 found interpret, phrase and guard
// each had a level-gated pin and synthesize did not -- so a Debug demotion of
// the synthesize decision line could survive its package suite undetected.
func TestSynthesizeAttemptSequenceIsCollectedAtInfoAndLostAboveIt(t *testing.T) {
	t.Parallel()
	run := func(collectAt slog.Level) []decisionRecord {
		inner, logger := newCaptureLogger()
		logger = slog.New(&levelledCapture{inner: inner, level: collectAt})
		generator := &sequencedGenerator{synthesis: validSynthesisOutput(), errs: []error{retryableUnavailable()}}
		runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
		if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", err)
		}
		return inner.decisionEvents()
	}
	atInfo := run(slog.LevelInfo)
	if len(atInfo) != 1 {
		t.Fatalf("decision events at LevelInfo = %d, want 1 -- the synthesize attempt sequence must survive the PRODUCTION level", len(atInfo))
	}
	if got := attrString(t, atInfo[0].Attrs, "attempt_outcomes"); got != "1:unavailable,2:success" {
		t.Fatalf("attempt_outcomes at LevelInfo = %q, want %q", got, "1:unavailable,2:success")
	}
	if aboveInfo := run(slog.LevelWarn); len(aboveInfo) != 0 {
		t.Fatalf("decision events at LevelWarn = %d, want 0 -- the control does not discriminate", len(aboveInfo))
	}
}

// --- B2: the shared renderer's field SET is identical across all three emitters ---

// TestAttemptLogFieldsKeySetIsIdenticalAcrossEveryEmitter asserts the shared
// renderer's own four keys -- attempts_total, attempts_retried,
// attempt_outcomes, attempt_elapsed_ms -- appear, same spelling, on all three
// decision lines. A fourth emitter that hand-rolled its own field list
// instead of calling attemptLogFields would drift silently.
//
// r1 (codex) P3, fixed TWICE. First fix (rejected on r2 grant): a
// hand-authored literal in the pin -- catches a dropped field, but is a
// second copy of the vocabulary, exactly what "never a hand list" rules out.
// Fixed properly: `attemptLogFieldKeys` (runtime.go) is the ONE declaration
// attemptLogFields itself is now built from (see its own doc comment), so
// this pin references THAT var directly -- never a hand-typed set, never a
// call to attemptLogFields. The count (4) is pinned as a bare literal
// alongside it for one reason only: a mutation that shrinks
// attemptLogFieldKeys ITSELF would move the "expected" set and the emitted
// line together, and nothing else here would notice.
func TestAttemptLogFieldsKeySetIsIdenticalAcrossEveryEmitter(t *testing.T) {
	t.Parallel()
	if len(attemptLogFieldKeys) != 4 {
		t.Fatalf("attemptLogFieldKeys = %v, want exactly 4 declared keys", attemptLogFieldKeys)
	}
	wantKeys := map[string]bool{}
	for _, key := range attemptLogFieldKeys {
		wantKeys[key] = true
	}

	interpretInner, interpretLogger := newCaptureLogger()
	interpretGen := &sequencedGenerator{interpretation: validInterpretationOutput()}
	interpretRT := mustRuntime(t, interpretGen, Config{Logger: interpretLogger, MaxAttempts: 1})
	if _, _, err := interpretRT.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	synthInner, synthLogger := newCaptureLogger()
	synthGen := &sequencedGenerator{synthesis: validSynthesisOutput()}
	synthRT := mustRuntime(t, synthGen, Config{Logger: synthLogger, MaxAttempts: 1})
	if _, _, err := synthRT.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}

	phraseInner, phraseLogger := newCaptureLogger()
	phraseGen := &sequencedGenerator{phrasing: phrasingOutput{}}
	phraseRT := mustRuntime(t, phraseGen, Config{Logger: phraseLogger, MaxAttempts: 1, PhrasingModel: "test/phrasing"})
	if _, _, err := phraseRT.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput()); err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}

	for name, events := range map[string][]decisionRecord{
		"interpret":  interpretInner.decisionEvents(),
		"synthesize": synthInner.decisionEvents(),
		"phrase":     phraseInner.decisionEvents(),
	} {
		if len(events) != 1 {
			t.Fatalf("%s: decision events = %d, want 1", name, len(events))
		}
		for key := range wantKeys {
			if _, ok := events[0].Attrs[key]; !ok {
				t.Fatalf("%s: missing shared-renderer key %q; emitter has drifted from attemptLogFields", name, key)
			}
		}
	}
}

// --- C3: the decision line must fire on returns strictly before today's defer ---
//
// C3 RULING (team-lead, 2026-09-09T14:4xZ), option (a): hoist BOTH the
// interpret and synthesize decision defers to the FIRST statement of their
// bodies, the one-owner pattern PhraseStructureOffers already uses. A
// rejected call must still emit, with operation="" and an empty attempt
// list -- a shape no real outcome produces (dictation 378: a line that only
// fires when things went well cannot tell "not reached" from "reached and
// fine"). Measured by go/ast at 27d733ca: interpretQuestionWithSample had 3
// uncovered early returns (request-validation failure, unauthenticated
// principal, oversized payload); SynthesizeAnswer had 2 (no separate
// request-validation step, so no first shape).

// assertDecisionLineNotReached is the zero-value caveat, PINNED rather than
// assumed: operation="" and an EMPTY attempt list together are a shape no
// real (reached) outcome can produce -- attemptLogFields always renders
// attempt_outcomes for a non-empty sequence, and a reached call always sets
// receipt.Operation. If either check here ever fires ambiguously against a
// legitimate reached-and-empty outcome, that is exactly the case the C3
// ruling says earns an explicit reached=false field -- decided against the
// enumerated arms, not assumed away.
func assertDecisionLineNotReached(t *testing.T, attrs map[string]any) {
	t.Helper()
	if got := attrString(t, attrs, "operation"); got != "" {
		t.Fatalf("operation = %q, want \"\" -- a rejected call must read as NOT REACHED, never a real operation", got)
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "" {
		t.Fatalf("attempt_outcomes = %q, want an EMPTY list -- a rejected call made no attempts", got)
	}
}

func TestInterpretDecisionLineFiresOnRequestValidationFailure(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	invalid := validRequest()
	now := time.Now().UTC()
	// Same shape TestInvestigationRequestValidateRejectsInvalidTemporalShape
	// (internal/contextfabric/model_test.go) proves Validate() rejects:
	// TemporalCurrent with AsOf set.
	invalid.TimeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, AsOf: &now}
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, invalid); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the request-validation rejection")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0 -- request validation must reject before any model call", generator.calls)
	}
	assertDecisionLineNotReached(t, onlyDecisionEvent(t, handler).Attrs)
}

func TestInterpretDecisionLineFiresOnUnauthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "  "}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the unauthenticated rejection")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}
	assertDecisionLineNotReached(t, onlyDecisionEvent(t, handler).Attrs)
}

func TestInterpretDecisionLineFiresOnOversizedPayload(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	runtime.config.MaxInputBytes = 1
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the oversized-payload rejection")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}
	assertDecisionLineNotReached(t, onlyDecisionEvent(t, handler).Attrs)
}

func TestSynthesizeDecisionLineFiresOnUnauthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{synthesis: validSynthesisOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "  "}, validSynthesisInput()); err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the unauthenticated rejection")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}
	assertDecisionLineNotReached(t, onlyDecisionEvent(t, handler).Attrs)
}

func TestSynthesizeDecisionLineFiresOnOversizedPayload(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &sequencedGenerator{synthesis: validSynthesisOutput()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1})
	runtime.config.MaxInputBytes = 1
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the oversized-payload rejection")
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}
	assertDecisionLineNotReached(t, onlyDecisionEvent(t, handler).Attrs)
}

// --- round 2 P1: a pre-call cancellation on a LATER attempt, after an
// earlier attempt already contacted the provider, must NOT read
// "cancelled" terminally ---

// cancelAfterFirstAttemptGenerator lets a test cancel the caller's own ctx
// from INSIDE the generator's first call -- deterministic and race-free
// (single goroutine, no synchronization needed): attempt 1 genuinely
// contacts the provider and returns a retryable failure, then cancels ctx
// before returning, so attempt 2's pre-call ctx.Err() check in withRetry
// fires with the caller's context already done. If attempt 2's own
// Interpret is ever invoked, that is itself the failure this generator
// exists to catch (attempt 2 must never reach the provider once ctx is
// already cancelled) -- it returns a distinguishable error rather than
// panicking, so a regression reads as a normal test failure.
type cancelAfterFirstAttemptGenerator struct {
	calls  int
	cancel context.CancelFunc
}

func (g *cancelAfterFirstAttemptGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	g.calls++
	if g.calls == 1 {
		g.cancel()
		return interpretationOutput{}, contextfabric.ModelUsage{}, retryableUnavailable()
	}
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("attempt 2 must never reach the provider once ctx is already cancelled")
}

func (g *cancelAfterFirstAttemptGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

func (g *cancelAfterFirstAttemptGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

// TestPreCallCancellationOnALaterAttemptStaysUnavailable pins the round 2
// P1 fix: attempt 1 actually contacts the provider (a real retryable
// failure), the caller's context is cancelled before attempt 2 is placed,
// and attempt 2's own pre-call ctx.Err() check fires -- but because the
// provider WAS already contacted earlier in this same operation, the
// terminal outcome must read "unavailable", not "cancelled". Only a
// cancellation before the operation's very FIRST attempt (no prior
// provider contact at all) reads "cancelled" -- see
// TestPreCallCancellationTerminalReceiptOutcomeIsCancelled for that cell.
func TestPreCallCancellationOnALaterAttemptStaysUnavailable(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	ctx, cancel := context.WithCancel(context.Background())
	generator := &cancelAfterFirstAttemptGenerator{cancel: cancel}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 2})
	if _, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the terminal failure surfaced")
	}
	if generator.calls != 1 {
		t.Fatalf("generator.calls = %d, want 1 -- attempt 2 must never reach the provider once ctx is already cancelled", generator.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q -- attempt 1 already contacted the provider, so a later pre-call cancellation is not \"the provider never saw a request\"", got, "unavailable")
	}
	if got := attrString(t, attrs, "attempt_outcomes"); got != "1:unavailable,2:cancelled" {
		t.Fatalf("attempt_outcomes = %q, want %q (per-attempt class is unaffected by this fix)", got, "1:unavailable,2:cancelled")
	}
}

// --- round 3 (CHAOS-5577 D15=A): the terminal outcome is an OPERATION-WIDE
// question -- "did ANY leg ever contact a provider" -- not a per-leg one.
// Round 2's attempt==1 fix only proved a LATER attempt in the SAME leg's own
// retry sequence never contacted a provider; it said nothing about a
// DIFFERENT leg (primary vs. fallback). A primary that genuinely contacted a
// provider and was then canceled IN FLIGHT correctly reads "unavailable" for
// its own leg (round 2's fix is right there) -- but with a fallback
// configured, the fallback inherits the SAME already-canceled ctx, sees it
// on ITS OWN attempt 1, tags ErrModelCancelled correctly for ITS leg, and
// the pre-existing "both legs failed -> report the fallback's own outcome"
// composition rule then overwrote the WHOLE OPERATION's outcome to
// "cancelled" -- even though a provider genuinely was contacted. The fix
// (fallbackWouldSeeADeadContext, runtime.go) skips the doomed fallback call
// entirely in that exact situation, letting each of these four call sites
// fall through to the same code path it already runs when no fallback is
// configured at all -- see the pin group below, one per site, plus a
// regression pin proving the fallback is STILL invoked, and can still
// compose a genuine "cancelled" outcome, when neither leg has contacted a
// provider.

// neverCalledFallback is a fallback ModelRuntime that must NEVER be invoked
// in the tests below -- proving the fix skips the call outright, rather than
// invoking it and merely discarding/overriding its result. Tracking a call
// counter (checked only AFTER the primary call's goroutine has finished, via
// a happens-before `<-done` channel receive) follows this file's own
// established pattern (cancelAfterFirstAttemptGenerator.calls,
// inCallCancelGenerator) rather than calling t.Fatal from a background
// goroutine, which the testing package does not support.
type neverCalledFallback struct {
	calls       int
	interpreted contextfabric.InterpretedQuestion
	draft       contextfabric.SynthesisDraft
}

func (f *neverCalledFallback) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	f.calls++
	return f.interpreted, validReceipt(contextfabric.ModelOperationInterpret), nil
}

func (f *neverCalledFallback) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	f.calls++
	return f.draft, validReceipt(contextfabric.ModelOperationSynthesize), nil
}

// inCallCancelSynthesizeGenerator is inCallCancelGenerator's Synthesize
// counterpart -- the existing type's Synthesize method is a fixed "not used
// by this test" stub, so a distinct type is needed to block on ctx.Done()
// from inside Synthesize specifically.
type inCallCancelSynthesizeGenerator struct {
	started chan struct{}
}

func (g *inCallCancelSynthesizeGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

func (g *inCallCancelSynthesizeGenerator) Synthesize(ctx context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	close(g.started)
	<-ctx.Done()
	return synthesisOutput{}, contextfabric.ModelUsage{}, ctx.Err()
}

func (g *inCallCancelSynthesizeGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

// TestFallbackSkippedWhenPrimaryInterpretationContactedProviderThenCanceled
// is the r3 repro itself, over InterpretQuestion's generation-error fallback
// site (runtime.go:918): the primary's call is genuinely IN FLIGHT --
// inCallCancelGenerator blocks until the caller cancels ctx -- so the
// primary's own leg correctly classifies "unavailable" (round 2's fix), and
// the fix under test must now skip the fallback call outright rather than
// let it inherit the same dead ctx and overwrite that with "cancelled".
func TestFallbackSkippedWhenPrimaryInterpretationContactedProviderThenCanceled(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &inCallCancelGenerator{started: make(chan struct{})}
	fallback := &neverCalledFallback{interpreted: validInterpretedQuestion(), draft: validDraft()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Timeout: time.Minute, Fallback: fallback})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
		close(done)
	}()
	select {
	case <-generator.started:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("generator was never invoked before the timeout -- cannot exercise the in-call case")
	}
	<-done

	if err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the primary's own cancellation error surfaced")
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback.calls = %d, want 0 -- the primary already contacted the provider, so the fallback must never be invoked on the same already-canceled ctx", fallback.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q -- the primary genuinely contacted the provider before the in-flight cancellation, so the operation must not read cancelled", got, "unavailable")
	}
	if got := attrString(t, attrs, "primary_failure_classification"); got != "unavailable" {
		t.Fatalf("primary_failure_classification = %q, want %q", got, "unavailable")
	}
	if got, ok := attrs["fallback_used"].(bool); !ok || got {
		t.Fatalf("fallback_used = %#v, want false", attrs["fallback_used"])
	}
}

// cancelThenInvalidInterpretGenerator returns a genuinely-generated (no
// error) but semantically invalid interpretation, canceling the caller's own
// ctx as a side effect of the call -- so by the time the domain-validation
// fallback guard (runtime.go:987) runs, ctx is already done, exactly as it
// could be if the caller's deadline landed between the provider's response
// and that check.
type cancelThenInvalidInterpretGenerator struct {
	cancel context.CancelFunc
	output interpretationOutput
}

func (g *cancelThenInvalidInterpretGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	g.cancel()
	return g.output, contextfabric.ModelUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}, nil
}

func (g *cancelThenInvalidInterpretGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

func (g *cancelThenInvalidInterpretGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

// TestFallbackSkippedWhenPrimaryInterpretationInvalidAfterContextCanceled
// covers InterpretQuestion's SECOND fallback site (runtime.go:987, the
// domain-validation-failure branch): the primary DID produce output (a
// provider was genuinely contacted -- generationErr is nil), the output is
// semantically invalid, and ctx is already canceled by the time the fallback
// guard runs. The fix must skip the fallback call here too.
func TestFallbackSkippedWhenPrimaryInterpretationInvalidAfterContextCanceled(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	ctx, cancel := context.WithCancel(context.Background())
	invalid := validInterpretationOutput()
	invalid.RequestedJudgment = strings.Repeat("a", 259)
	generator := &cancelThenInvalidInterpretGenerator{cancel: cancel, output: invalid}
	fallback := &neverCalledFallback{interpreted: validInterpretedQuestion(), draft: validDraft()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Fallback: fallback})

	_, receipt, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || !errors.Is(err, contextfabric.ErrInterpretationRejected) {
		t.Fatalf("InterpretQuestion() error = %v, want ErrInterpretationRejected surfaced from the primary's own rejection", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback.calls = %d, want 0 -- the primary already contacted the provider, so the fallback must never be invoked on the same already-canceled ctx", fallback.calls)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want %q", receipt.Outcome, "invalid_output")
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "primary_failure_classification"); got != "invalid_output" {
		t.Fatalf("primary_failure_classification = %q, want %q", got, "invalid_output")
	}
}

// cancelThenInvalidSynthesizeGenerator is
// cancelThenInvalidInterpretGenerator's Synthesize counterpart.
type cancelThenInvalidSynthesizeGenerator struct {
	cancel context.CancelFunc
	output synthesisOutput
}

func (g *cancelThenInvalidSynthesizeGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

func (g *cancelThenInvalidSynthesizeGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	g.cancel()
	return g.output, contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, nil
}

func (g *cancelThenInvalidSynthesizeGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("not used by this test")
}

// TestFallbackSkippedWhenPrimarySynthesisContactedProviderThenCanceled is the
// r3 repro over SynthesizeAnswer's generation-error fallback site
// (runtime.go:1479) -- the SynthesizeAnswer sibling of
// TestFallbackSkippedWhenPrimaryInterpretationContactedProviderThenCanceled.
func TestFallbackSkippedWhenPrimarySynthesisContactedProviderThenCanceled(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	generator := &inCallCancelSynthesizeGenerator{started: make(chan struct{})}
	fallback := &neverCalledFallback{interpreted: validInterpretedQuestion(), draft: validDraft()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Timeout: time.Minute, Fallback: fallback})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
		close(done)
	}()
	select {
	case <-generator.started:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("generator was never invoked before the timeout -- cannot exercise the in-call case")
	}
	<-done

	if err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the primary's own cancellation error surfaced")
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback.calls = %d, want 0 -- the primary already contacted the provider, so the fallback must never be invoked on the same already-canceled ctx", fallback.calls)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q -- the primary genuinely contacted the provider before the in-flight cancellation, so the operation must not read cancelled", got, "unavailable")
	}
	if got := attrString(t, attrs, "primary_failure_classification"); got != "unavailable" {
		t.Fatalf("primary_failure_classification = %q, want %q", got, "unavailable")
	}
}

// TestFallbackSkippedWhenPrimarySynthesisInvalidAfterContextCanceled covers
// SynthesizeAnswer's SECOND fallback site (runtime.go:1557, the
// domain-validation-failure branch) -- the SynthesizeAnswer sibling of
// TestFallbackSkippedWhenPrimaryInterpretationInvalidAfterContextCanceled.
func TestFallbackSkippedWhenPrimarySynthesisInvalidAfterContextCanceled(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	ctx, cancel := context.WithCancel(context.Background())
	invalid := validSynthesisOutput()
	invalid.EvidenceRefIDs = []string{"evidence_not_in_input"}
	generator := &cancelThenInvalidSynthesizeGenerator{cancel: cancel, output: invalid}
	fallback := &neverCalledFallback{interpreted: validInterpretedQuestion(), draft: validDraft()}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Fallback: fallback})

	_, receipt, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil || !errors.Is(err, contextfabric.ErrSynthesisRejected) {
		t.Fatalf("SynthesizeAnswer() error = %v, want ErrSynthesisRejected surfaced from the primary's own rejection", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback.calls = %d, want 0 -- the primary already contacted the provider, so the fallback must never be invoked on the same already-canceled ctx", fallback.calls)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want %q", receipt.Outcome, "invalid_output")
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "primary_failure_classification"); got != "invalid_output" {
		t.Fatalf("primary_failure_classification = %q, want %q", got, "invalid_output")
	}
}

// trackedErroringFallback is erroringFallbackRuntime plus a call counter --
// needed by the regression pin below to prove the fallback WAS invoked (not
// merely that its result surfaced), since with this fix's guard in place a
// SKIPPED fallback and an INVOKED-then-both-legs-failed fallback can produce
// indistinguishable errors/outcomes when both legs report "cancelled" (see
// the test's own comment). erroringFallbackRuntime itself is left untouched
// -- every OTHER test using it purely for its returned result, never for
// whether it was actually invoked.
type trackedErroringFallback struct {
	calls   int
	err     error
	receipt contextfabric.ModelExecutionReceipt
}

func (f *trackedErroringFallback) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	f.calls++
	return contextfabric.InterpretedQuestion{}, f.receipt, f.err
}

func (f *trackedErroringFallback) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	f.calls++
	return contextfabric.SynthesisDraft{}, f.receipt, f.err
}

// TestFallbackStillInvokedAndComposesCancelledWhenNeitherLegContactsProvider
// is the regression pin chris's ruling also required: "the existing
// first-attempt cell stays cancelled". A primary that is ITSELF pre-call
// canceled (ctx already done before its very first attempt --
// primaryContactedProvider false, receipt.Outcome already "cancelled") must
// still invoke the fallback exactly as before this fix -- the guard must
// fire ONLY when a provider was genuinely contacted somewhere in the
// operation, never here. When that fallback ALSO never reaches a provider
// (the same dead ctx) and fails, the pre-existing "both legs failed -> use
// the fallback's own outcome" composition rule correctly still yields
// "cancelled" for the operation as a whole -- the genuine double-miss case
// this ticket's whole vocabulary exists to distinguish from a real outage.
// Asserting fallback.calls == 1 (not just the error/outcome shape) is what
// proves the fallback was actually invoked here, rather than skipped for the
// wrong reason and coincidentally producing the same "cancelled" reading.
func TestFallbackStillInvokedAndComposesCancelledWhenNeitherLegContactsProvider(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	fbReceipt := validReceipt(contextfabric.ModelOperationInterpret)
	fbReceipt.Outcome = "cancelled"
	fallback := &trackedErroringFallback{
		err:     fmt.Errorf("%w: %w", contextfabric.ErrModelCancelled, context.Canceled),
		receipt: fbReceipt,
	}
	runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Fallback: fallback})

	_, receipt, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || !errors.Is(err, contextfabric.ErrModelCancelled) {
		t.Fatalf("InterpretQuestion() error = %v, want ErrModelCancelled surfaced from the fallback leg", err)
	}
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0 -- the primary itself must never reach the provider once ctx is already canceled before the first attempt", generator.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback.calls = %d, want 1 -- neither leg has contacted a provider yet, so the fix must NOT have skipped the fallback call", fallback.calls)
	}
	if receipt.Outcome != "cancelled" {
		t.Fatalf("receipt.Outcome = %q, want %q -- neither leg ever contacted a provider", receipt.Outcome, "cancelled")
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "primary_failure_classification"); got != "cancelled" {
		t.Fatalf("primary_failure_classification = %q, want %q", got, "cancelled")
	}
	if got := attrString(t, attrs, "outcome"); got != "cancelled" {
		t.Fatalf("outcome = %q, want %q", got, "cancelled")
	}
}
