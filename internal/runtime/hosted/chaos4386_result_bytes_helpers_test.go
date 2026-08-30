package hosted_test

// CHAOS-4386: the trial harness (chaos3742_two_turn_confirmation_test.go,
// chaos4360_nturn_confirmation_test.go) calls investigator.Investigate() in
// process and never traverses the HTTP route's own
// limits.Claim.CompleteWithBudget gate (internal/api/context_fabric_routes.go)
// -- see this file's own PR and cf-measurement-trials.md's Run I entry
// (2026-08-27) for the finding. Every case this harness ran, across 906
// real model calls, measured only the responder's raw output_bytes per
// model completion -- an upstream proxy for, never the assembled
// InvestigationResult (Rows, evidence, drivers) the route's
// ACR_MAX_SERIALIZED_BYTES gate actually bounds.
//
// chaos4386MeasureResult closes that gap: it serializes a case's own final
// InvestigationResult with the EXACT SAME encoder the route uses
// (api.MarshalContextFabricResponse, exported for this reuse -- never a
// second, independently-drifting json.Marshal call) and derives the same
// 4-bytes-per-token estimate context_fabric_routes.go itself uses
// (estimatedTokens := (measuredBytes + 3) / 4).

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos4386LegacyResponseByteCap is the retired effective response-size
// ceiling CHAOS-4355 (acr PR #309) lifted: ACR_MAX_OUTPUT_TOKENS defaulted
// to 4000, and context_fabric_routes.go's own (bytes+3)/4 estimate made
// that a ~16,000-byte cap in practice, before the route separately gained
// its own token-unlimited override budget (see
// internal/config/config.go's defaultMaxOutputTokens doc comment and
// internal/api/context_fabric_response_bound_test.go's package doc
// comment for the full history). Comparing every case's own result_bytes
// against this retired number is what proves the old, pre-CHAOS-4355 cap
// would have rejected a shape the harness had never once measured.
const chaos4386LegacyResponseByteCap = 16_000

// chaos4386DefaultMaxSerializedBytes mirrors internal/config's
// defaultMaxSerializedBytes (unexported there) -- the production
// ACR_MAX_SERIALIZED_BYTES default. Used only as a fallback when this
// harness process never itself calls config.Load() (every live corpus
// test in this package does, and passes its own cfg.MaxSerializedBytes
// through instead -- see TestChaos3742TwoTurnConfirmationReplay/
// TestChaos4360NTurnConfirmationCarry), and by the RED/GREEN pure-logic
// tests in chaos4386_result_bytes_test.go, which have no config.Config to
// read at all.
const chaos4386DefaultMaxSerializedBytes = 262144

// chaos4386RequestMaxSerializedBytes mirrors twoTurnRequest's own literal
// options.max_serialized_bytes (262144) -- every request this harness
// builds (two-turn and N-turn both call twoTurnRequest) asks for exactly
// this ceiling. The HTTP route's effective cap is
// min(a.config.MaxSerializedBytes, request.Options.MaxSerializedBytes)
// (context_fabric_routes.go), so on a deployment configured ABOVE this
// value the request's own literal -- not the server config alone -- is
// what actually binds; see chaos4386EffectiveMaxSerializedBytes (codex
// review round 1, P2, confirmed: comparing against cfg.MaxSerializedBytes
// alone would report a result as under budget when the real route would
// still 413 it).
const chaos4386RequestMaxSerializedBytes = 262144

// chaos4386EffectiveMaxSerializedBytes returns the SAME effective ceiling
// the HTTP route would apply to a request this harness's own twoTurnRequest
// built -- min(configured, chaos4386RequestMaxSerializedBytes) -- so
// OverMaxSerializedBytesCount reflects whether the real route would have
// rejected each result, not merely whether it exceeds the server's own
// configured value in isolation.
func chaos4386EffectiveMaxSerializedBytes(configured int64) int64 {
	if configured < chaos4386RequestMaxSerializedBytes {
		return configured
	}
	return chaos4386RequestMaxSerializedBytes
}

// chaos4386RequireCompatibleConfig fails the run loudly (codex review
// round 2, P2, confirmed) rather than silently misreporting when
// configured (cfg.MaxSerializedBytes) sits BELOW chaos4386RequestMaxSerializedBytes
// -- config validation permits this (internal/config/config.go accepts
// values as low as 8192). Under that configuration, twoTurnRequest's own
// fixed options.max_serialized_bytes=262144 exceeds a.config.MaxSerializedBytes,
// so the route's request-validation step
// (request.Options.MaxSerializedBytes > a.config.MaxSerializedBytes) would
// reject EVERY request this harness sends with a 400 before the response-
// size gate ever runs at all -- there is no real "does this result fit"
// behavior left to measure, and chaos4386EffectiveMaxSerializedBytes'
// returned value would silently misrepresent an unreachable byte-gate
// verdict as a real one. This harness bypasses HTTP entirely
// (investigator.Investigate() in-process), so it has no other way to
// notice that every one of its calls would have 400'd; refusing to run is
// the only honest option under this configuration.
func chaos4386RequireCompatibleConfig(t *testing.T, configured int64) {
	t.Helper()
	if chaos4386ConfigIncompatible(configured) {
		t.Fatalf("ACR_MAX_SERIALIZED_BYTES=%d is below this harness's own fixed request option (options.max_serialized_bytes=%d, twoTurnRequest) -- every request this run sends would be rejected 400 invalid_request by the real route before the response-size gate ever ran, so result_bytes measurement would not correspond to real route behavior; raise ACR_MAX_SERIALIZED_BYTES to at least %d to run this class under this configuration", configured, chaos4386RequestMaxSerializedBytes, chaos4386RequestMaxSerializedBytes)
	}
}

// chaos4386ConfigIncompatible is chaos4386RequireCompatibleConfig's own
// condition, extracted as a pure predicate so it has a direct unit-test
// surface independent of *testing.T's own Fatalf/FailNow machinery (this
// file's own established precedent -- e.g. twoTurnPositiveFalseNoMatch,
// twoTurnMutationProbe -- of pulling a guard's condition out for testing).
func chaos4386ConfigIncompatible(configured int64) bool {
	return configured < chaos4386RequestMaxSerializedBytes
}

// chaos4386MeasureResult serializes result with the production route's own
// encoder and returns (resultBytes, estTokens). A marshal error is not
// expected for a well-formed contractsv1.ContextFabricInvestigationResult
// (every field is a plain JSON-tagged value; nothing in the type carries a
// channel, func, or cyclic pointer) -- if json.Marshal ever does fail here,
// that is itself a defect in this harness's own fixture/live-result
// shape, not an over-budget result, so this returns 0 rather than
// fabricating a byte count a caller could mistake for a real measurement.
func chaos4386MeasureResult(result contractsv1.ContextFabricInvestigationResult) (resultBytes, estTokens int64) {
	_, measuredBytes, err := api.MarshalContextFabricResponse(result)
	if err != nil {
		return 0, 0
	}
	return measuredBytes, (measuredBytes + 3) / 4
}

// chaos4386ResultByteStats computes CHAOS-4386's run-level result_bytes
// distribution: max, p50/p99 (rank-by-truncation, no interpolation --
// summarizeTwoTurnTiming's own WallP50MS precedent), and the count of rows
// whose measured bytes exceed effectiveMax (the caller's own
// chaos4386EffectiveMaxSerializedBytes(cfg.MaxSerializedBytes) -- the SAME
// effective ceiling the HTTP route would apply, never the server's
// configured value in isolation) and chaos4386LegacyResponseByteCap (the
// retired 16,000-byte effective cap, see that constant's own doc comment).
//
// resultBytes<=0 entries (an OfferMiss/ArmInvalid row this harness never
// even reached Investigate() far enough to produce a result for) are
// excluded from every statistic: a 0 there is an absence, not a
// legitimately tiny result, and letting it into the distribution would
// understate p50/max for no reason while never itself being "over
// budget" either way.
func chaos4386ResultByteStats(resultBytes []int64, effectiveMax int64) (max, p50, p99 int64, overConfiguredMax, overLegacy16K int) {
	values := make([]int64, 0, len(resultBytes))
	for _, b := range resultBytes {
		if b <= 0 {
			continue
		}
		values = append(values, b)
		if b > max {
			max = b
		}
		if b > effectiveMax {
			overConfiguredMax++
		}
		if b > chaos4386LegacyResponseByteCap {
			overLegacy16K++
		}
	}
	if len(values) == 0 {
		return 0, 0, 0, overConfiguredMax, overLegacy16K
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	p50 = values[len(values)/2]
	p99Index := len(values) * 99 / 100
	if p99Index >= len(values) {
		p99Index = len(values) - 1
	}
	p99 = values[p99Index]
	return max, p50, p99, overConfiguredMax, overLegacy16K
}

// chaos4386MeasuringInvestigator wraps a contextfabric.Investigator and
// captures EVERY successful call's own measured bytes -- not merely each
// case row's final result (codex review round 2, P1, confirmed). The HTTP
// route gates every individual investigation response, not only the last
// one a multi-turn arm/case happens to end on: a setup call
// (mintWindowPrecondition), an inferred-tier arm's baseline leg, or an
// N-turn case's turn 1 could each independently be oversized and 413 in
// production even when the row's own FINAL result is comfortably small --
// per-row-final measurement alone would never see that. Both
// TestChaos3742TwoTurnConfirmationReplay and TestChaos4360NTurnConfirmationCarry
// wrap their investigator with this BEFORE passing it to any arm/case
// function, so the run-level result_bytes distribution is built from every
// call this run actually made, while each row's own ResultBytes/EstTokens
// field (chaos4386MeasureResult, called directly by the arm/case functions)
// keeps its existing, separately useful "this row's own final answer"
// meaning.
//
// mu guards values: two-turn's own per-case arms already run purely
// sequentially within one process (no concurrent Investigate() calls), but
// a mutex costs nothing here and removes any future refactor's need to
// re-derive that this type is safe to share.
type chaos4386MeasuringInvestigator struct {
	contextfabric.Investigator
	mu     sync.Mutex
	values []int64
}

func (m *chaos4386MeasuringInvestigator) Investigate(ctx context.Context, principal storage.Principal, req contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	result, err := m.Investigator.Investigate(ctx, principal, req)
	if err == nil {
		bytes, _ := chaos4386MeasureResult(result)
		m.mu.Lock()
		m.values = append(m.values, bytes)
		m.mu.Unlock()
	}
	return result, err
}

// snapshot returns every byte measurement captured so far, safe to call
// once the run's calls are all complete (or, defensively, at any point --
// the mutex makes a concurrent call harmless even though none is expected).
func (m *chaos4386MeasuringInvestigator) snapshot() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, len(m.values))
	copy(out, m.values)
	return out
}

// --- answer-rate fields (CHAOS-4386 scope-add, team-lead ruling 2026-08-28
// 04:30 PDT): the v39/v40 report carried no per-case terminal answer
// record, so an answer-rate analysis had to proxy from arm x member
// structure rows. chaos4386TerminalFields closes that gap the same way
// chaos4386MeasureResult closes the byte gap: read once, off the SAME
// final InvestigationResult each site already has in scope, at each of
// the same measurement sites CHAOS-4386's own ResultBytes/EstTokens stamp.

// chaos4386TerminalReason returns a CLOSED-VOCABULARY class naming WHICH
// disclosure channel the engine used to explain why result did not reach
// a final "complete" answer -- NEVER the raw text itself (codex review
// round 1, P2, confirmed: acr/AGENTS.md's own CANONICAL ARCHITECTURE rule
// -- "every decision branch... emits corpus-safe, closed-vocabulary
// telemetry" -- and this repo's own standing "never the question text,
// never a label/phrasing string" corpus-safety discipline every other
// field on this row already follows). Interpretation.ClarificationReason
// in particular can be MODEL-AUTHORED prose; persisting it verbatim into a
// durable trial artifact would leak arbitrary phrasing into a
// corpus-adjacent report. Coverage.DegradedReasons/Warnings are
// engine-authored but still open-ended, dynamically-formatted strings, not
// a fixed small enum -- the same risk, lower severity.
//
// There is no single unified "reason" field on
// ContextFabricInvestigationResult. For clarification_required, the
// engine's OWN disclosure channel is SubjectResolution.ClarificationPrompt
// (internal/contextfabric/unresolved.go's composeSubjectlessTerminal:
// "if status == InvestigationClarificationRequired && resolution.ClarificationPrompt
// != \"\" { answer += \" \" + resolution.ClarificationPrompt }") -- the
// ordinary ambiguous-candidate path populates THIS field, not
// Interpretation.ClarificationReason (codex review round 4/confirmation
// pass, P2, confirmed: that field belongs to the INTERPRETATION step and
// can legitimately stay empty on a normal clarification_required result,
// which made the original check misclassify the common case as
// "undisclosed"). Interpretation.ClarificationReason is checked too, as a
// secondary, independent channel, so a future/alternate path that
// populates ONLY that field is still classified correctly. Every other
// non-complete status (partial/degraded/no_match) carries its
// explanation, when the engine gave one, in Coverage.DegradedReasons,
// Limitations, or, failing those, Warnings -- Limitations specifically
// (codex confirmation pass 3, P2, confirmed) is where a normal production
// no_match (an empty subject pool, a window/structure veto) puts its
// explanation, while DegradedReasons/Warnings both stay empty on that
// path; without checking it, the common no_match case classified as
// "undisclosed" despite a real, disclosed reason.
// CHAOS-4413: this function used to hold its own copy of the disclosure
// classification. That copy is now production logic
// (internal/contextfabric.ComputeAnswerCompleteness, called by the engine
// on every result before Validate) and this wrapper only delegates to it,
// so the harness's OWN pinning tests (chaos4386_answer_rate_test.go) keep
// exercising the real implementation rather than a second copy that could
// silently drift from it.
func chaos4386TerminalReason(result contractsv1.ContextFabricInvestigationResult) string {
	return string(contextfabric.ComputeAnswerCompleteness(result).TerminalReason)
}

// chaos4386TerminalFields returns the per-case terminal-answer fields:
// terminalStatus is the real ContextFabricInvestigationStatus wire
// literal (never the "error:<class>" strings some pre-existing fields on
// this row, e.g. Turn2Status, carry on a failed call); claimedFactsCount
// is the literal length of result.ClaimedFacts -- deliberately NOT the
// same thing as this row's own pre-existing CanonicalFactsCount field
// (twoTurnCanonicalFactsCount counts distinct canonical_fact:* COVERAGE
// SOURCES the synthesis step could have cited, not the claimed-fact array
// length); rowsCount sums every claimed fact's own Rows table length
// (mirrors nTurnRowsCount's identical logic, kept as its own copy here
// rather than an import so this shared helper has no dependency on the
// N-turn file); terminalReason is chaos4386TerminalReason's own value.
func chaos4386TerminalFields(result contractsv1.ContextFabricInvestigationResult) (terminalStatus string, claimedFactsCount, rowsCount int, terminalReason string) {
	c := contextfabric.ComputeAnswerCompleteness(result)
	return string(c.TerminalStatus), c.ClaimedFactsCount, c.RowsCount, string(c.TerminalReason)
}

// chaos4525CohortScoredMemberCount counts the members of result's delivered
// cohort that actually carry an EXPLANATION -- Outcome qualified or
// provisional, which is exactly and only when RankCohort populates Score,
// RankingBasis and Drivers (cohort_ranking.go:419).
//
// This predicate was RankingComputed until codex review R4 (P1, confirmed
// against source). That was wrong, and wrong in the direction that
// false-greens a bar: cohort_ranking.go:277 sets RankingComputed = true for
// EVERY member the moment a ranking pass runs, while lines 403-411 assign
// Outcome not_applicable when availableWeight == 0 and insufficient_evidence
// when availableWeight < 50 || availableCount < 2 -- and line 419 gives those
// two members no Score, no RankingBasis and no Drivers at all. So
// "RankingComputed" means "ranking executed", never "this member was
// explained", and a cohort of entirely insufficient_evidence members would
// have counted as a delivered answer.
//
// Not hypothetical: Run J (CHAOS-4450) observed exactly this shape on live
// data -- outcome_counts={"insufficient_evidence":2,"provisional":1}, two of
// three members carrying no score.
//
// AGENTS.md check 8 is the rule this now satisfies: "Scores help prioritize;
// drivers explain -- never a bare score." A bar that accepts a cohort with
// neither score nor driver is the degenerate case of that prohibition.
//
// len(Cohort.Members) is likewise not the count: a member can be DISCOVERED
// and listed without ever being scored. Nil Cohort (every non-cohort
// question) reads 0, which is why the field is omitempty.
//
// Corpus-safe by construction: a count, never a subject label or handle.
func chaos4525CohortScoredMemberCount(result contractsv1.ContextFabricInvestigationResult) int {
	if result.Cohort == nil {
		return 0
	}
	scored := 0
	for _, member := range result.Cohort.Members {
		switch member.Outcome {
		case contractsv1.ContextFabricCohortOutcomeQualified,
			contractsv1.ContextFabricCohortOutcomeProvisional:
			scored++
		}
	}
	return scored
}

// chaos4525StampTerminal writes EVERY terminal field of one row from one
// result, in one place.
//
// It exists because the five fields must move together and there are six call
// sites. Before CHAOS-4525 the four chaos4386TerminalFields values were
// assigned by a bare multi-assignment repeated at each of those sites, which
// is exactly the shape that silently loses a fifth field when one site is
// missed -- and a missed site here does not fail loudly: it produces a row
// with cohort_scored_member_count=0, indistinguishable from a genuine
// unranked cohort, which would then be scored as unanswered. Folding the
// assignment into one function makes that class of miss structurally
// impossible rather than merely unlikely.
func chaos4525StampTerminal(res *twoTurnCaseResult, result contractsv1.ContextFabricInvestigationResult) {
	if res == nil {
		return
	}
	// CHAOS-4413: a result that actually came from the engine (as opposed
	// to a fixture literal built directly by a fake Investigator in some of
	// this package's other tests) already carries these values, stamped,
	// on result.Completeness -- chaos4386TerminalFields now delegates to
	// the exact same production function (contextfabric.
	// ComputeAnswerCompleteness) that computed them, so calling it here
	// reads the promoted contract's own derivation rather than a
	// side-channel copy, and stays correct for both engine-produced and
	// fixture-literal results.
	res.TerminalStatus, res.ClaimedFactsCount, res.RowsCount, res.TerminalReason = chaos4386TerminalFields(result)
	res.CohortScoredMemberCount = chaos4525CohortScoredMemberCount(result)
}
