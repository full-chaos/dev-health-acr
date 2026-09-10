package genkitruntime

import (
	"context"
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

// TestPreCallCancellationTerminalReceiptOutcomeStaysUnavailable pins the A3
// ruling's SCOPE (team-lead, option (b), 2026-09-10): the new "cancelled"
// class exists ONLY at the per-attempt level. The terminal receipt.Outcome
// -- the ADR-0008-governed field -- is deliberately left unchanged: a
// pre-call cancellation that exhausts every attempt still reads
// outcome=unavailable, exactly as it did before this lane's A3 fix. Extending
// that documented vocabulary is a contract-token decision outside this
// lane's authority (see the handoff's CHRIS-PENDING row). Without this pin,
// a future change could widen receiptOutcomeForError itself and silently
// cross that boundary.
func TestPreCallCancellationTerminalReceiptOutcomeStaysUnavailable(t *testing.T) {
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
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrString(t, attrs, "outcome"); got != "unavailable" {
		t.Fatalf("outcome = %q, want %q (the ADR-0008 terminal vocabulary is unchanged; "+
			"only attempt_outcomes' per-attempt class distinguishes cancellation)", got, "unavailable")
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
