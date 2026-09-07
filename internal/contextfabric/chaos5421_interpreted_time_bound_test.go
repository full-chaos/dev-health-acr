package contextfabric

import (
	"context"
	"errors"
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

	for _, testCase := range []struct {
		name         string
		time         TimeContext
		wantOutcome  InterpretedTimeBoundOutcome
		wantClamp    bool
		wantAnswered bool
	}{
		{"ordinary current axis", TimeContext{Axis: TemporalCurrent}, InterpretedTimeBoundOK, false, true},
		{"ordinary historical", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, InterpretedTimeBoundOK, false, true},
		{"clamped", TimeContext{Axis: TemporalValidTime, AsOf: &futureAsOf}, InterpretedTimeBoundFutureEnd, true, true},
		{"refused", TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}, InterpretedTimeBoundRangeTooWide, false, false},
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
