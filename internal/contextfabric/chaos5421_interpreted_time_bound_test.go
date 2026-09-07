package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5421. resolveTimeContext is called at two sites with one sentinel
// for two different actors: engine.go's request clamp (the CALLER's own
// bounds, where a 400 invalid_request is right) and engine.go's
// post-Interpret clamp (the INTERPRETER's own output, where it is not).
// The second returns its error bare, so the route classifier cannot tell
// the sites apart and writes a model-side defect back as
// `400 invalid_request / "ACR rejected the investigation request"` on a
// request whose only content was a question -- and FailureStage reports
// "unknown", because nothing wrapped the error.
//
// The design of record for this axis is
// docs/design/context-fabric-historical-time-axis.md §1, which states the
// post-Interpret check's verdict verbatim: "only the verdict changes from
// 'refuse' to 'bind the as-of and label it'", with rule 1's own tolerance
// clause "then clamp to `now`". The shipped post-Interpret site refuses
// instead, for every one of its arms.
//
// These pins are written RED against that site. Each names one member of
// the closed interpreted-bound vocabulary, so the fix cannot satisfy them
// by refusing everything or by serving everything:
//
//	future_end             -> CLAMP to now and serve (this file's P1/P2)
//	range_too_wide         -> terminal refusal, named basis  (P3)
//	absent_or_zero_instant -> terminal refusal, named basis  (P4)
//	unknown_axis           -> terminal refusal, named basis  (P5)
//	malformed_range        -> terminal refusal, named basis  (P6)
//	ok                     -> unchanged                      (P7 control)
//
// The wire-request site is NOT in scope and keeps its 400 byte for byte;
// TestEngineRefusesUnanswerableTimeBounds (engine_test.go) is that
// negative control and must stay green throughout, which P8 asserts here
// alongside its own case so a reader sees both halves in one place.

// interpretedRangeEndingInTheFuture is the corpus shape. A model asked
// about a bounded window emits a range whose END overshoots `now` -- it is
// reasoning about a calendar period, not reading this service's clock --
// and today that overshoot is fatal beyond the one-minute skew tolerance.
// 23 days is the distance from a question asked on the 7th of a month to
// that month's end.
func TestCHAOS5421_AnInterpretedRangeEndingInTheFutureIsClampedAndServed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	start := now.Add(-30 * 24 * time.Hour)
	futureEnd := now.Add(23 * 24 * time.Hour)

	engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalRange, Start: &start, End: &futureEnd}, now)
	request := validInvestigationRequest()
	if request.TimeContext.Axis != TemporalCurrent {
		t.Fatalf("fixture axis = %q, want a current-axis wire request so ONLY the interpreter carries the future end", request.TimeContext.Axis)
	}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want nil -- a model-emitted future end is clamped to now and labeled, never refused through the caller's error channel", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want a served answer", result.Status)
	}
	// The clamp is the whole point: every layer below must bind to `now`,
	// not to the instant the model invented. Asserting only that the call
	// succeeded would pass on a fix that let the future end through.
	if probe.factContext.End == nil {
		t.Fatal("fact request carried no range end; the clamped bound never reached the fact read")
	}
	if !probe.factContext.End.Equal(now) {
		t.Fatalf("fact request end = %s, want it clamped to now (%s) -- an unclamped future end answers a question about time that has not happened", probe.factContext.End, now)
	}
	if probe.factContext.Start == nil || !probe.factContext.Start.Equal(start) {
		t.Fatalf("fact request start = %v, want the interpreted start %s untouched -- the clamp moves the end only", probe.factContext.Start, start)
	}
	// The fact read and the LABEL are two authorities, and a fix that
	// clamped the bound it queries on while labeling the instant the model
	// invented would satisfy the assertion above and still publish an
	// answer claiming to speak for a time that has not happened.
	if result.Temporal == nil {
		t.Fatal("a clamped historical answer must still carry a temporal label")
	}
	if result.Temporal.Requested.End == nil || !result.Temporal.Requested.End.Equal(now) {
		t.Fatalf("labeled requested end = %v, want the CLAMPED end %s -- the label states what the answer speaks for, not what was asked", result.Temporal.Requested.End, now)
	}
	if result.Temporal.Effective.End == nil || !result.Temporal.Effective.End.Equal(now) {
		t.Fatalf("labeled effective end = %v, want the CLAMPED end %s", result.Temporal.Effective.End, now)
	}
	if probe.graph.resolveCalls == 0 || !probe.factsRead || !probe.synthesized {
		t.Fatalf("resolve=%d factsRead=%v synthesized=%v, want the turn to do real work rather than short-circuit", probe.graph.resolveCalls, probe.factsRead, probe.synthesized)
	}
}

// The as-of arm of the same rule. Kept separate from the range arm because
// resolveTimeContext clamps them in two different switch branches, and a
// fix applied to one only would leave this green while P1 stayed red (or
// the reverse).
func TestCHAOS5421_AnInterpretedAsOfInTheFutureIsClampedAndServed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	futureAsOf := now.Add(48 * time.Hour)

	engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalValidTime, AsOf: &futureAsOf}, now)
	request := validInvestigationRequest()

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want nil -- rule 1's own clause is \"tolerance +1m for clock skew, then clamp to now\"", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want a served answer", result.Status)
	}
	if probe.factContext.AsOf == nil || !probe.factContext.AsOf.Equal(now) {
		t.Fatalf("fact request as-of = %v, want it clamped to now (%s)", probe.factContext.AsOf, now)
	}
	// The label is the second authority, for the same reason as the range
	// arm above.
	if result.Temporal == nil {
		t.Fatal("a clamped historical answer must still carry a temporal label")
	}
	if result.Temporal.Requested.AsOf == nil || !result.Temporal.Requested.AsOf.Equal(now) {
		t.Fatalf("labeled requested as-of = %v, want the CLAMPED instant %s", result.Temporal.Requested.AsOf, now)
	}
}

// The three refusal arms below share one shape: the turn must terminate as
// a RESULT carrying a named basis, with err == nil, so the caller reads a
// refusal instead of an HTTP error blaming their request. `no_match` with
// a Limitation is the shape windowVetoResult already establishes for
// "this turn cannot proceed, and here is why".
func TestCHAOS5421_AnUnanswerableInterpretedBoundRefusesWithANamedBasis(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	ancient := now.Add(-3000 * 24 * time.Hour)
	inverted := now.Add(-24 * time.Hour)
	zero := time.Time{}

	for _, testCase := range []struct {
		name string
		// arm names the vocabulary member this case is the sole
		// representative of, so a reader can see the set is covered and a
		// reviewer can see none is a deny-list "else".
		arm  string
		time TimeContext
	}{
		{"a range wider than the supported window", "range_too_wide", TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}},
		{"a present zero instant", "absent_or_zero_instant", TimeContext{Axis: TemporalValidTime, AsOf: &zero}},
		{"a point-in-time axis with no as-of", "absent_or_zero_instant", TimeContext{Axis: TemporalValidTime}},
		{"an axis outside the closed vocabulary", "unknown_axis", TimeContext{Axis: TemporalAxis("sideways")}},
		{"a range whose end precedes its start", "malformed_range", TimeContext{Axis: TemporalRange, Start: &now, End: &inverted}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			engine, probe := mustHistoricalEngine(t, testCase.time, now)
			request := validInvestigationRequest()

			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v, want a terminal refusal with err == nil -- arm %q is the interpreter's own output, and returning it through the caller's error channel reports the wrong actor", err, testCase.arm)
			}
			if result.Status != InvestigationNoMatch {
				t.Fatalf("Status = %q, want %q for arm %q", result.Status, InvestigationNoMatch, testCase.arm)
			}
			// A refusal that does not say why is the defect one layer
			// along: the corpus row's only signal today is a 400 whose
			// body names nothing.
			if len(result.Limitations) == 0 {
				t.Fatalf("arm %q refused with no stated basis; a refusal a reader cannot act on is not a named basis", testCase.arm)
			}
			// The refusal must still precede every capability call, which
			// is what the pre-existing refusal bought and must not be lost
			// in trading an error for a terminal.
			if probe.graph.resolveCalls != 0 || probe.factsRead || probe.synthesized {
				t.Fatalf("arm %q did graph/fact/synthesis work before refusing (resolve=%d facts=%v synth=%v)", testCase.arm, probe.graph.resolveCalls, probe.factsRead, probe.synthesized)
			}
		})
	}
}

// P7, the over-blocking control. Refusing or clamping everything would
// satisfy every pin above. An interpreted bound this service CAN answer
// must pass through untouched -- same instant in, same instant bound.
func TestCHAOS5421_AnAnswerableInterpretedBoundIsNeitherClampedNorRefused(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, now)
	request := validInvestigationRequest()

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want an ordinary historical question to be answered", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want a served answer", result.Status)
	}
	if probe.factContext.AsOf == nil || !probe.factContext.AsOf.Equal(asOf) {
		t.Fatalf("fact request as-of = %v, want the interpreted instant %s unchanged -- an answerable bound must not be clamped", probe.factContext.AsOf, asOf)
	}
}

// P8, the negative control stated where the change is, not only where the
// pre-existing test happens to live: the CALLER's own unanswerable bounds
// keep producing ErrInvalidTimeBound from the request site. This is the
// assertion that stops the fix from being "delete the check".
func TestCHAOS5421_TheWireRequestSiteStillRefusesTheCallersOwnBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	ancient := now.Add(-3000 * 24 * time.Hour)

	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"caller as-of in the future", TimeContext{Axis: TemporalValidTime, AsOf: &future}},
		{"caller range ending in the future", TimeContext{Axis: TemporalRange, Start: &now, End: &future}},
		{"caller range wider than the supported window", TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The INTERPRETER is answerable here; only the wire request is
			// not. That is the exact mirror of every pin above.
			engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalCurrent}, now)
			request := validInvestigationRequest()
			request.TimeContext = testCase.time

			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if !errors.Is(err, ErrInvalidTimeBound) {
				t.Fatalf("Investigate() error = %v, want ErrInvalidTimeBound -- the caller's own bounds are still a 400 invalid_request and this site is deliberately untouched", err)
			}
			if probe.graph.resolveCalls != 0 || probe.factsRead || probe.synthesized {
				t.Fatal("a rejected request must do no graph, fact, or synthesis work")
			}
		})
	}
}

// P9. The observable fires on EVERY pass through the site, exactly once,
// including the ordinary arm. An event emitted only on a refusal would have
// a numerator and no denominator: "the interpreter produced a future bound
// on 4% of turns" -- the question this whole change exists to make
// answerable -- would stay underivable from the stream.
//
// Asserted against the engine's own call, not the sink's formatting; the
// emitted LINE is asserted separately in internal/runtime/hosted, through
// the construction open.go actually makes.
func TestCHAOS5421_TheInterpretedTimeBoundVerdictIsRecordedOnEveryArm(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	futureAsOf := now.Add(48 * time.Hour)
	ancient := now.Add(-3000 * 24 * time.Hour)

	// The RANGE clamp needs its own arm, and this is the battery's finding,
	// not a hypothetical: M5421-CLAMP-NEVER-REPORTED deleted `clampApplied =
	// true` from the range branch and SURVIVED. The bound is still pulled
	// back, so the answer is still served against the right window and every
	// binding assertion stays green -- only the VERDICT lies, reporting `ok`
	// with clamp_applied=false on a turn whose evidence window this engine
	// moved. That is precisely the regression this line exists to make
	// visible, and nothing pinned it on the range axis: the clamped arm below
	// was valid_time only, and the two branches set the flag independently.
	rangeStart := now.Add(-90 * 24 * time.Hour)
	rangeFutureEnd := now.Add(23 * 24 * time.Hour)
	for _, testCase := range []struct {
		name         string
		time         TimeContext
		wantOutcome  InterpretedTimeBoundOutcome
		wantClamp    bool
		wantAnswered bool
		// wantRangeDays is asserted on every arm, so the explicit 0 off the
		// range axis is pinned too -- "not a range" and "we did not measure"
		// must never read alike.
		wantRangeDays int
	}{
		{"ordinary current axis", TimeContext{Axis: TemporalCurrent}, InterpretedTimeBoundOK, false, true, 0},
		{"ordinary historical", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, InterpretedTimeBoundOK, false, true, 0},
		{"clamped as-of", TimeContext{Axis: TemporalValidTime, AsOf: &futureAsOf}, InterpretedTimeBoundFutureEnd, true, true, 0},
		// The corpus shape, and the arm the surviving mutant exposed. The
		// reported width is the CLAMPED one (90 days), never the 113 the
		// model asked for, or the line would describe a window nothing read.
		{"clamped range", TimeContext{Axis: TemporalRange, Start: &rangeStart, End: &rangeFutureEnd}, InterpretedTimeBoundFutureEnd, true, true, 90},
		{"refused", TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}, InterpretedTimeBoundRangeTooWide, false, false, 3000},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			engine, _ := mustHistoricalEngineWithTelemetry(t, testCase.time, now, telemetry)

			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			// EXACTLY once. A recorder that appends is what makes this
			// checkable -- one keeping only the last decision could not
			// tell a single verdict from three.
			if len(telemetry.interpretedTimeBounds) != 1 {
				t.Fatalf("recorded %d interpreted-time-bound verdicts, want exactly 1 -- a site that reports twice double-counts every rate derived from it, and one that reports none is invisible", len(telemetry.interpretedTimeBounds))
			}
			decision := telemetry.interpretedTimeBounds[0]
			if decision.Outcome != testCase.wantOutcome {
				t.Errorf("outcome = %q, want %q", decision.Outcome, testCase.wantOutcome)
			}
			if !ValidInterpretedTimeBoundOutcome(decision.Outcome) {
				t.Errorf("outcome %q is not a member of the closed vocabulary", decision.Outcome)
			}
			if decision.ClampApplied != testCase.wantClamp {
				t.Errorf("clamp_applied = %v, want %v", decision.ClampApplied, testCase.wantClamp)
			}
			if decision.Axis != testCase.time.Axis {
				t.Errorf("axis = %q, want the interpreted axis %q echoed", decision.Axis, testCase.time.Axis)
			}
			if decision.RangeDays != testCase.wantRangeDays {
				t.Errorf("range_days = %d, want %d -- the width reported is the one actually read, and an explicit 0 off the range axis", decision.RangeDays, testCase.wantRangeDays)
			}
			if decision.Answerable() != testCase.wantAnswered {
				t.Errorf("Answerable() = %v, want %v for outcome %q", decision.Answerable(), testCase.wantAnswered, decision.Outcome)
			}
		})
	}
}

// P10, the boundary. A bound at exactly `now` has happened and must not be
// clamped: an off-by-one here would report clamp_applied=true on every
// historical question, which fails nothing and makes the key useless.
// Asserted on the decision function directly because that is where the
// comparison lives.
func TestCHAOS5421_ABoundExactlyAtNowIsNotTheFuture(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	start := now.Add(-30 * 24 * time.Hour)

	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"as-of exactly at now", TimeContext{Axis: TemporalValidTime, AsOf: &now}},
		{"range ending exactly at now", TimeContext{Axis: TemporalRange, Start: &start, End: &now}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := resolveInterpretedTimeContext(testCase.time, now)
			if decision.ClampApplied {
				t.Error("a bound at exactly now was clamped; now has happened, and reporting a move that did not occur makes clamp_applied unreadable")
			}
			if decision.Outcome != InterpretedTimeBoundOK {
				t.Errorf("outcome = %q, want ok", decision.Outcome)
			}
		})
	}
}

// P11. The vocabulary is an ALLOW-LIST and Answerable() classifies every
// member of it explicitly. A member added without being classified would
// otherwise fall to the fail-closed default and silently start refusing
// turns -- safe, but silent, and the wrong way to find out.
func TestCHAOS5421_EveryOutcomeIsAMemberAndIsClassified(t *testing.T) {
	t.Parallel()
	answerable := 0
	for _, outcome := range InterpretedTimeBoundOutcomeVocabulary() {
		if !ValidInterpretedTimeBoundOutcome(outcome) {
			t.Errorf("%q is in the vocabulary but not admitted by its own membership test", outcome)
		}
		decision := InterpretedTimeBoundDecision{Outcome: outcome}
		if decision.Answerable() {
			answerable++
			continue
		}
		// Every refusing member must have a stated basis, or the terminal
		// it produces would refuse with nothing a caller can read.
		if _, ok := interpretedTimeBoundLimitation(outcome); !ok {
			t.Errorf("refusing member %q has no stated basis", outcome)
		}
	}
	// Exactly two members are answerable. Pinned as a NUMBER because the
	// split is the substance of this change: a fix that made everything
	// answerable, or nothing, would satisfy every membership assertion above.
	if answerable != 2 {
		t.Errorf("%d answerable members, want exactly 2 (ok and future_end)", answerable)
	}
	// And an unclassified value fails CLOSED rather than serving.
	if (InterpretedTimeBoundDecision{Outcome: InterpretedTimeBoundOutcome("invented")}).Answerable() {
		t.Error("a value outside the vocabulary was treated as answerable; the default arm must fail closed")
	}
	if ValidInterpretedTimeBoundOutcome("") {
		t.Error("the empty value is a member; an absent outcome must never stand in for one")
	}
}

// CHAOS-5421 r1 P1-1, ANSWERED BY PROOF RATHER THAN BY A CODE CHANGE.
//
// The reviewer read design §1 item 1 correctly -- "an as_of/end after now is
// refused (ErrInvalidTimeBound, 400) ... Tolerance: +1m for clock skew, then
// clamp to now" -- and concluded that clamping every future value at the
// post-Interpret site erases the distinction between clock skew and a
// prediction request.
//
// The distinction is not erased, because a CALLER-supplied future bound can
// never reach the post-Interpret site at all. engine.go's request clamp runs
// first and its clamped value REPLACES the caller's on the request every layer
// below sees, INCLUDING the one handed to Interpret as its default time
// context (genkitruntime's toDomain falls back to it when the model emits no
// axis, which is the only way a caller's own instants can re-enter the
// interpretation). So by the time the post-Interpret evaluator runs:
//
//   - a caller bound beyond the tolerance was already REFUSED, 400, caller-side
//   - a caller bound within the tolerance was already CLAMPED to now
//
// and the only future bound left for it to see is one the INTERPRETER derived
// -- a calendar window whose end has not arrived yet, which is not a prediction
// request. These two pin exactly that, so the claim cannot silently stop being
// true: if a future caller bound ever did reach the second site, the first of
// these goes red.

// The RED half: beyond the tolerance, the caller's own bound is refused by the
// REQUEST site, with the caller-side sentinel, before Interpret ever runs.
func TestCHAOS5421_ACallerBoundBeyondTheToleranceIsRefusedBeforeInterpretRuns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	farFuture := now.Add(2 * time.Hour)

	// The interpreter is answerable; only the CALLER is out of bounds.
	engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalCurrent}, now)
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalRange, Start: &now, End: &farFuture}

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if !errors.Is(err, ErrInvalidTimeBound) {
		t.Fatalf("Investigate() error = %v, want ErrInvalidTimeBound -- a caller asking about a time 2h away is a prediction request and the design refuses it 400", err)
	}
	// Refused before ANY capability call, which is also what stops the
	// interpreter from ever seeing it.
	if probe.graph.resolveCalls != 0 || probe.factsRead || probe.synthesized {
		t.Fatal("work ran on a caller bound the request site should have refused")
	}
	if len(probe.interpretedContext.Axis) != 0 && probe.factContext.End != nil {
		t.Fatal("the interpreter was reached with a refused caller bound")
	}
}

// The GREEN half: within the tolerance, the caller's own bound is absorbed as
// clock skew by the REQUEST site and the turn is served.
//
// The interpreter here echoes the request's own context, which is exactly what
// toDomain does when a model emits no axis -- the only route by which a
// caller's instants reach the second site. What it receives is already `now`,
// so the post-Interpret verdict is `ok` with clamp_applied FALSE: the skew was
// absorbed one site earlier, by the check the design assigns it to. That is
// the point. A `future_end` here would mean the request site had not clamped.
func TestCHAOS5421_ACallerBoundWithinTheToleranceIsAbsorbedAsSkewAndServed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	start := now.Add(-30 * 24 * time.Hour)
	withinSkew := now.Add(30 * time.Second)

	telemetry := &recordingTelemetry{}
	engine, probe := mustHistoricalEngineEchoingTheRequest(t, now, telemetry)
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &withinSkew}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer -- 30s is clock skew, not a question about the future", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want a served answer", result.Status)
	}
	// The bound every layer below binds to is `now`, never the caller's
	// +30s: the request site pulled it back before Interpret ran.
	if probe.factContext.End == nil || !probe.factContext.End.Equal(now) {
		t.Fatalf("fact request end = %v, want it clamped to now (%s) by the REQUEST site", probe.factContext.End, now)
	}
	if len(telemetry.interpretedTimeBounds) != 1 {
		t.Fatalf("recorded %d interpreted-time verdicts, want exactly 1", len(telemetry.interpretedTimeBounds))
	}
	decision := telemetry.interpretedTimeBounds[0]
	if decision.Outcome != InterpretedTimeBoundOK || decision.ClampApplied {
		t.Fatalf("post-Interpret verdict = %q clamp_applied=%v, want ok/false -- a future value reaching THIS site would mean the caller-side clamp had not run, which is the invariant these two pins exist to hold",
			decision.Outcome, decision.ClampApplied)
	}
}

// CHAOS-5421 r1 P1-2. The refusal terminal persisted the REQUEST's time
// context for every member. That is right for three of them -- an absent or
// zero instant, an axis the contract does not define, and an inverted range
// are all values ContextFabricTimeContext.Validate REFUSES, so persisting one
// would fail the result's own Validate and leave the refusal unreadable.
//
// It is WRONG for range_too_wide. An ordered 3000-day range is perfectly
// representable: Validate owns shape and representability, not this service's
// 400-day read bound, so that context can round-trip -- and the time-axis
// design says Interpretation.TimeContext round-trips {axis, as_of, start, end}
// in the result. Dropping it means the persisted answer cannot say what span
// was refused, which is the one thing a reader of that refusal needs.
//
// So the rule is not "the request's, always" but "the interpreter's wherever
// the contract can carry it" -- decided by RUNNING the contract's own
// validator, never by a second hand-maintained list of which members qualify.
func TestCHAOS5421_ARefusedButRepresentableInterpretedContextIsCarriedNotDropped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	ancient := now.Add(-3000 * 24 * time.Hour)
	interpreted := TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}

	// PREMISE, asserted rather than assumed: this context is refused by the
	// engine's 400-day bound AND accepted by the wire contract. If the
	// contract ever rejected it, the finding would not exist and this test
	// would be pinning nothing.
	if err := interpreted.Validate(); err != nil {
		t.Fatalf("premise: the contract must accept this context (it is ordered and representable), got %v", err)
	}

	engine, _ := mustHistoricalEngine(t, interpreted, now)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a terminal refusal", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	got := result.Interpretation.TimeContext
	if got.Axis != TemporalRange {
		t.Fatalf("persisted interpretation axis = %q, want %q -- the refusal must say what span it refused", got.Axis, TemporalRange)
	}
	if got.Start == nil || !got.Start.Equal(ancient) || got.End == nil || !got.End.Equal(now) {
		t.Fatalf("persisted interpretation bounds = %v..%v, want %s..%s", got.Start, got.End, ancient, now)
	}
	// A non-current axis REQUIRES a temporal label, so carrying the context
	// and dropping the label would trade one unreadable refusal for another
	// -- and would fail the result's own Validate.
	if result.Temporal == nil {
		t.Fatal("a refusal carrying a historical axis must carry the temporal label that axis requires")
	}
}

// The complement, and the reason this is decided by the validator rather than
// by a list of members: a context the contract CANNOT carry must still fall
// back to the request's, or the refusal fails its own Validate and the caller
// gets an error in place of a readable no_match.
func TestCHAOS5421_AnUnrepresentableInterpretedContextStillFallsBackToTheRequests(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"an axis the contract does not define", TimeContext{Axis: TemporalAxis("sideways")}},
		{"a point-in-time axis with no as-of", TimeContext{Axis: TemporalValidTime}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := testCase.time.Validate(); err == nil {
				t.Fatal("premise: the contract must REJECT this context, or it belongs in the carried case above")
			}
			engine, _ := mustHistoricalEngine(t, testCase.time, now)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v, want a terminal refusal", err)
			}
			if result.Interpretation.TimeContext.Axis != TemporalCurrent {
				t.Fatalf("persisted axis = %q, want the REQUEST's current axis -- an unrepresentable context cannot be persisted", result.Interpretation.TimeContext.Axis)
			}
		})
	}
}

// CHAOS-5421 r1: the four mutants the reviewer predicted would survive, and
// did. Each was run and reported SURVIVED before these were written, so every
// one of them is a measured coverage gap rather than a defensive guess.

// RV1. A range whose WHOLE span is in the future. Clamping only the end would
// invert it -- the end lands on now while the start stays ahead of it -- so
// the start is pulled back too. No test constructed this shape, so the guard
// that prevents the inversion was unpinned.
func TestCHAOS5421_ARangeWhollyInTheFutureClampsBothEnds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	start := now.Add(24 * time.Hour)
	end := now.Add(48 * time.Hour)

	decision := resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange, Start: &start, End: &end}, now)
	if decision.Outcome != InterpretedTimeBoundFutureEnd || !decision.ClampApplied {
		t.Fatalf("outcome = %q clamp = %v, want future_end/true", decision.Outcome, decision.ClampApplied)
	}
	if decision.Bound.Start == nil || !decision.Bound.Start.Equal(now) {
		t.Fatalf("clamped start = %v, want now (%s) -- clamping only the end inverts the range", decision.Bound.Start, now)
	}
	if decision.Bound.End == nil || !decision.Bound.End.Equal(now) {
		t.Fatalf("clamped end = %v, want now (%s)", decision.Bound.End, now)
	}
	if decision.Bound.End.Before(*decision.Bound.Start) {
		t.Fatal("the clamped range is inverted, which is the exact defect the start clamp exists to prevent")
	}
	if decision.RangeDays != 0 {
		t.Errorf("range_days = %d, want 0 for a span collapsed onto now", decision.RangeDays)
	}
}

// RV2. A range missing an endpoint. The point-in-time axis's missing as-of and
// the present-zero instant were both covered; the range's own missing-endpoint
// guard was not, so deleting it left nil dereferences one line away.
func TestCHAOS5421_ARangeMissingAnEndpointIsRefusedNotDereferenced(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"no start", TimeContext{Axis: TemporalRange, End: &now}},
		{"no end", TimeContext{Axis: TemporalRange, Start: &now}},
		{"neither", TimeContext{Axis: TemporalRange}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := resolveInterpretedTimeContext(testCase.time, now)
			if decision.Outcome != InterpretedTimeBoundAbsentOrZero {
				t.Fatalf("outcome = %q, want %q", decision.Outcome, InterpretedTimeBoundAbsentOrZero)
			}
			if decision.Answerable() {
				t.Error("a range missing an endpoint was reported answerable")
			}
		})
	}
}

// RV3. THE POPULATION ITSELF, pinned independently of the array.
//
// Every other test here derives its iteration FROM InterpretedTimeBoundOutcomeVocabulary(),
// so deleting a member from that array deletes its own coverage along with it
// and every derived assertion still passes over the smaller set. A checker that
// takes its expectations from the thing under test cannot see that thing shrink.
// So this names the members and the count by hand -- the one place a
// hand-maintained list is the right instrument, because it is the independent
// side of the comparison.
func TestCHAOS5421_TheOutcomeVocabularyPopulationIsPinnedByName(t *testing.T) {
	t.Parallel()
	want := []InterpretedTimeBoundOutcome{
		InterpretedTimeBoundOK,
		InterpretedTimeBoundFutureEnd,
		InterpretedTimeBoundRangeTooWide,
		InterpretedTimeBoundAbsentOrZero,
		InterpretedTimeBoundUnknownAxis,
		InterpretedTimeBoundMalformedRange,
	}
	if InterpretedTimeBoundOutcomeCount != len(want) {
		t.Fatalf("vocabulary size = %d, want %d -- a member was added or removed; classify it and update this list deliberately", InterpretedTimeBoundOutcomeCount, len(want))
	}
	got := InterpretedTimeBoundOutcomeVocabulary()
	if len(got) != len(want) {
		t.Fatalf("vocabulary array length = %d, want %d", len(got), len(want))
	}
	for i, member := range want {
		if got[i] != member {
			t.Errorf("vocabulary[%d] = %q, want %q -- published order is part of the contract", i, got[i], member)
		}
		if !ValidInterpretedTimeBoundOutcome(member) {
			t.Errorf("%q is named here but not admitted by the membership test", member)
		}
	}
}

// RV4. THE BASIS TEXT, not merely its presence.
//
// Every refusal assertion so far read `len(Limitations) != 0`, which cannot
// see a basis lose the thing that makes it a basis. A refusal that says
// nothing specific is the defect one layer along from a refusal that says
// nothing at all -- the corpus row's whole problem was a 400 whose body named
// no cause.
func TestCHAOS5421_EveryRefusingMemberStatesItsOwnDistinctBasis(t *testing.T) {
	t.Parallel()
	// An INDEPENDENT expectation table: the keyword each basis must carry to
	// be about its own rule, not lifted from the strings under test.
	wantKeyword := map[InterpretedTimeBoundOutcome]string{
		InterpretedTimeBoundRangeTooWide:   "wider",
		InterpretedTimeBoundAbsentOrZero:   "could not be established",
		InterpretedTimeBoundUnknownAxis:    "kind of time",
		InterpretedTimeBoundMalformedRange: "ended before it began",
	}
	seen := map[string]InterpretedTimeBoundOutcome{}
	for _, member := range InterpretedTimeBoundOutcomeVocabulary() {
		basis, ok := interpretedTimeBoundLimitation(member)
		if (InterpretedTimeBoundDecision{Outcome: member}).Answerable() {
			if ok {
				t.Errorf("%q is answerable but carries a refusal basis", member)
			}
			continue
		}
		if !ok || strings.TrimSpace(basis) == "" {
			t.Errorf("refusing member %q states no basis", member)
			continue
		}
		keyword, named := wantKeyword[member]
		if !named {
			t.Errorf("refusing member %q has no expected keyword; the table above must name every refusing member", member)
			continue
		}
		if !strings.Contains(basis, keyword) {
			t.Errorf("basis for %q does not say what it refused (missing %q): %q", member, keyword, basis)
		}
		// Distinct, so no member can silently inherit another's sentence.
		if prior, dup := seen[basis]; dup {
			t.Errorf("members %q and %q share one basis; a caller cannot tell them apart", prior, member)
		}
		seen[basis] = member
	}
}

// RV5. The key the refusal is PERSISTED under.
//
// Nothing read it, so replacing it with "" survived. The reviewer noted the
// row is safely non-reusable today because the terminal passes nil reuse
// snapshots and the store treats nil as "never becomes reusable" -- true, and
// the reason this is a coverage gap rather than a live defect. It is still
// worth pinning: "harmless because a DIFFERENT argument happens to be nil" is
// an invariant owned by another layer, and an empty key would become wrong the
// moment that layer changed. The key is asserted where it is decided.
//
// The value is the REQUEST's, never the refused interpreted span: a span this
// service will not read must not become a lookup key, even one nothing reads.
func TestCHAOS5421_TheRefusalIsPersistedUnderTheRequestsOwnTimeAxisKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	ancient := now.Add(-3000 * 24 * time.Hour)
	interpreted := TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}

	store := &keyRecordingResultStore{}
	engine, _ := mustHistoricalEngineWithStore(t, interpreted, now, store)
	request := validInvestigationRequest()

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	want := TimeAxisKeyFor(request.TimeContext)
	if want == "" {
		t.Fatal("premise: the request's own key must be non-empty, or this test cannot tell it from the empty one")
	}
	if store.savedKey != want {
		t.Fatalf("persisted time-axis key = %q, want the REQUEST's own %q", store.savedKey, want)
	}
	// And explicitly NOT the refused span's key.
	if refused := TimeAxisKeyFor(interpreted); store.savedKey == refused {
		t.Fatalf("the refusal was keyed on the span it refused (%q); a span this service will not read must never become a lookup key", refused)
	}
}
