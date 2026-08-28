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
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/api"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
// summarizeTwoTurnTiming's own WallP50MS precedent), and the count of
// rows whose measured bytes exceed configuredMax (the live
// ACR_MAX_SERIALIZED_BYTES gate this run actually loaded via config.Load)
// and chaos4386LegacyResponseByteCap (the retired 16,000-byte effective
// cap, see that constant's own doc comment).
//
// resultBytes<=0 entries (an OfferMiss/ArmInvalid row this harness never
// even reached Investigate() far enough to produce a result for) are
// excluded from every statistic: a 0 there is an absence, not a
// legitimately tiny result, and letting it into the distribution would
// understate p50/max for no reason while never itself being "over
// budget" either way.
func chaos4386ResultByteStats(resultBytes []int64, configuredMax int64) (max, p50, p99 int64, overConfiguredMax, overLegacy16K int) {
	values := make([]int64, 0, len(resultBytes))
	for _, b := range resultBytes {
		if b <= 0 {
			continue
		}
		values = append(values, b)
		if b > max {
			max = b
		}
		if b > configuredMax {
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
