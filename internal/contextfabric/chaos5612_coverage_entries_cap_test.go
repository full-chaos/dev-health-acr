package contextfabric

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// syntheticNonDegradingDetail stands in for a FUTURE fact kind or disclosure
// code without touching either closed vocabulary (FactKind, CoverageDetailCode
// are both closed and cannot grow inside a test). GraphExactNameCandidatesTruncated
// carries no code-conditioned fields at all (coverageDetailFieldRules' empty
// rule) -- a bare Source/Raw pair is the whole legal shape a synthetic
// "the vocabulary grew by one more kind" row needs.
func syntheticNonDegradingDetail(i int) CoverageDetail {
	d := CoverageDetail{
		DetailID: fmt.Sprintf("cov-synthetic-nd-%04d", i),
		Source:   fmt.Sprintf("synthetic:kind-%04d", i),
		Code:     contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		Raw:      fmt.Sprintf("synthetic disclosure %04d", i),
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d
}

// syntheticDegradingDetail stands in for a real read failure: a row that
// MUST survive any cap, because dropping it would erase the disclosure of an
// actual problem and (via validateCoverageDetails' dual-write derivation)
// desync Details from DegradedReasons.
func syntheticDegradingDetail(i int) CoverageDetail {
	count := 1
	d := CoverageDetail{
		DetailID:  fmt.Sprintf("cov-synthetic-deg-%04d", i),
		Source:    fmt.Sprintf("synthetic:failed-kind-%04d", i),
		Code:      contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed,
		Degrading: true,
		Raw:       fmt.Sprintf("synthetic failure %04d", i),
		Count:     &count,
	}
	d.Label = contractsv1.ComposeCoverageDetailLabel(d)
	return d
}

// TestCapCoverageEntriesToWriteBoundIsANoopUnderTheBound is the
// DISCRIMINATING CONTROL: a result already inside the bound must reach the
// composer unchanged, so the tests below that DO cap prove the cap fires
// because of the count, not on every call.
func TestCapCoverageEntriesToWriteBoundIsANoopUnderTheBound(t *testing.T) {
	var details []CoverageDetail
	for i := 0; i < 50; i++ {
		details = append(details, syntheticDegradingDetail(i))
	}
	for i := 0; i < 40; i++ {
		details = append(details, syntheticNonDegradingDetail(i))
	}
	result := &InvestigationResult{Coverage: Coverage{Details: append([]CoverageDetail(nil), details...)}}

	omitted := capCoverageEntriesToWriteBound(result)

	if omitted != 0 {
		t.Fatalf("CONTROL BROKEN: omitted = %d, want 0 -- 90 details is under the 100 bound", omitted)
	}
	if len(result.Coverage.Details) != len(details) {
		t.Fatalf("CONTROL BROKEN: details = %d, want unchanged %d", len(result.Coverage.Details), len(details))
	}
	for i, d := range result.Coverage.Details {
		if d.DetailID != details[i].DetailID {
			t.Fatalf("CONTROL BROKEN: detail %d id = %q, want unchanged %q -- a no-op cap must not re-mint ids", i, d.DetailID, details[i].DetailID)
		}
	}
}

// TestCapCoverageEntriesToWriteBoundBoundaryCells sweeps the boundary±1 and
// empty cells the domain table owes a guard's own input: nil/empty Details,
// a total exactly AT the bound (no capacity consumed, no trim), and one
// entry OVER the bound (the smallest overflow the cap must still catch).
func TestCapCoverageEntriesToWriteBoundBoundaryCells(t *testing.T) {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	cells := []struct {
		name             string
		degrading        int
		nonDegrading     int
		wantOmitted      int
		wantDetailsAfter int
		// wantIDsUnchanged: an omitted==0 cell must leave every detail's own
		// id exactly as the producer minted it -- NOT re-minted to "cov-NN"
		// -- because nothing was capped. This is what an off-by-one on
		// either entry guard (`len(details) <= bound`, or
		// `len(nonDegrading) <= remaining`) would flip at the exact
		// boundary: both guards would still land on omitted=0 by
		// arithmetic, but only through the mint loop, silently rewriting
		// every id a caller (and any downstream consumer keying on them)
		// would have kept.
		wantIDsUnchanged bool
	}{
		{"nil details", 0, 0, 0, 0, true},
		{"exactly at the bound", 60, bound - 60, 0, bound, true},
		{"one over the bound", 60, bound - 60 + 1, 1, bound, false},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			var details []CoverageDetail
			for i := 0; i < c.degrading; i++ {
				details = append(details, syntheticDegradingDetail(i))
			}
			for i := 0; i < c.nonDegrading; i++ {
				details = append(details, syntheticNonDegradingDetail(i))
			}
			originalIDs := make([]string, len(details))
			for i, d := range details {
				originalIDs[i] = d.DetailID
			}
			result := &InvestigationResult{Coverage: Coverage{Details: details}}

			omitted := capCoverageEntriesToWriteBound(result)

			t.Logf("CELL %s: degrading=%d non_degrading=%d -> omitted=%d details=%d",
				c.name, c.degrading, c.nonDegrading, omitted, len(result.Coverage.Details))
			if omitted != c.wantOmitted {
				t.Errorf("omitted = %d, want %d", omitted, c.wantOmitted)
			}
			if got := len(result.Coverage.Details); got != c.wantDetailsAfter {
				t.Errorf("details after cap = %d, want %d", got, c.wantDetailsAfter)
			}
			if c.wantIDsUnchanged {
				for i, d := range result.Coverage.Details {
					if d.DetailID != originalIDs[i] {
						t.Errorf("detail %d id = %q, want unchanged %q -- omitted=0 must never re-mint ids", i, d.DetailID, originalIDs[i])
					}
				}
			}
		})
	}
}

// TestCapCoverageEntriesToWriteBoundDropsSyntheticKindsPastTheCapDeterministically
// is CHAOS-5612's own ceiling-past-the-ceiling pin: a fixture that adds
// synthetic kinds past the 100-entry coverageEntries bound must be served
// with a deterministic, disclosed cap -- never a silent drop, and never the
// write refusal an uncapped overflow would produce.
func TestCapCoverageEntriesToWriteBoundDropsSyntheticKindsPastTheCapDeterministically(t *testing.T) {
	build := func() []CoverageDetail {
		var details []CoverageDetail
		for i := 0; i < 70; i++ {
			details = append(details, syntheticDegradingDetail(i))
		}
		for i := 0; i < 40; i++ {
			details = append(details, syntheticNonDegradingDetail(i))
		}
		return details
	}
	original := build()
	t.Logf("fixture: %d degrading + %d non-degrading = %d details, bound %d",
		70, 40, len(original), contractsv1.ContextFabricCoverageEntriesMaxCount)

	// DISCRIMINATING CONTROL, on the fixture itself: uncapped, this shape is
	// exactly the failure CHAOS-5612 exists to prevent -- a legal turn that
	// the write validator refuses outright rather than serving narrower.
	uncapped := Coverage{
		Sources:         []SourceObservation{{Source: "synthetic:root", State: SourceAvailable}},
		DegradedReasons: rawsOf(original[:70]),
		Details:         append([]CoverageDetail(nil), original...),
	}
	if err := uncapped.Validate(); err == nil {
		t.Fatalf("CONTROL BROKEN: %d details validated under the %d bound -- the fixture must genuinely overflow it", len(original), contractsv1.ContextFabricCoverageEntriesMaxCount)
	}

	result := &InvestigationResult{Coverage: Coverage{
		Sources:         []SourceObservation{{Source: "synthetic:root", State: SourceAvailable}},
		DegradedReasons: rawsOf(original[:70]),
		Details:         append([]CoverageDetail(nil), original...),
	}}
	omitted := capCoverageEntriesToWriteBound(result)

	// BOUNDED.
	if got := len(result.Coverage.Details); got != contractsv1.ContextFabricCoverageEntriesMaxCount {
		t.Fatalf("details after cap = %d, want exactly the bound %d", got, contractsv1.ContextFabricCoverageEntriesMaxCount)
	}
	if omitted != 10 {
		t.Fatalf("omitted = %d, want 10 (110 total - 100 bound)", omitted)
	}

	// DEGRADING NEVER YIELDS: all 70 degrading rows survive, in their
	// original relative order, at the front.
	for i := 0; i < 70; i++ {
		if result.Coverage.Details[i].Source != original[i].Source || !result.Coverage.Details[i].Degrading {
			t.Fatalf("degrading row %d = %+v, want the untouched original %+v -- a real failure must never be the one that yields",
				i, result.Coverage.Details[i], original[i])
		}
	}

	// DETERMINISTIC: the kept non-degrading rows are the PREFIX of the
	// original non-degrading order (the first 30 of the 40), never a
	// random or arrival-order subset.
	for i := 0; i < 30; i++ {
		got := result.Coverage.Details[70+i]
		want := original[70+i]
		if got.Source != want.Source || got.Degrading {
			t.Fatalf("kept non-degrading row %d = %+v, want the prefix row %+v", i, got, want)
		}
	}

	// UNIQUE, CONTIGUOUS ids over the kept set -- the reconciled order the
	// write path expects (mergeCoverageDetails' own "cov-NN" convention).
	seen := map[string]bool{}
	for i, d := range result.Coverage.Details {
		want := fmt.Sprintf("cov-%02d", i+1)
		if d.DetailID != want {
			t.Errorf("detail %d id = %q, want %q", i, d.DetailID, want)
		}
		if seen[d.DetailID] {
			t.Fatalf("duplicate detail id %q after cap", d.DetailID)
		}
		seen[d.DetailID] = true
	}

	// SERVABLE: the capped Coverage is now a LEGAL write -- the whole point
	// of capping before emit rather than letting the write validator refuse
	// the turn.
	if err := result.Coverage.Validate(); err != nil {
		t.Fatalf("capped coverage still fails the write-path contract: %v", err)
	}

	// DETERMINISTIC ACROSS RUNS: rebuilding the identical fixture from
	// scratch and capping it again produces the identical kept set and
	// identical omitted count -- not merely a stable prefix by construction
	// of this one slice.
	rebuilt := &InvestigationResult{Coverage: Coverage{Details: append([]CoverageDetail(nil), build()...)}}
	omitted2 := capCoverageEntriesToWriteBound(rebuilt)
	if omitted2 != omitted {
		t.Fatalf("second run omitted = %d, want %d (same fixture, same answer)", omitted2, omitted)
	}
	for i := range result.Coverage.Details {
		if result.Coverage.Details[i].DetailID != rebuilt.Coverage.Details[i].DetailID ||
			result.Coverage.Details[i].Source != rebuilt.Coverage.Details[i].Source {
			t.Fatalf("detail %d differs between two runs of the identical fixture: %+v vs %+v", i, result.Coverage.Details[i], rebuilt.Coverage.Details[i])
		}
	}
}

// TestCapCoverageEntriesToWriteBoundNeverDropsADegradingRowEvenAtTheBoundary
// is the boundary cell the domain table above owes: degrading ALONE already
// fills the bound, so the cap has zero remaining capacity for any
// non-degrading row -- every one of them is omitted, never a partial mix
// that could look like an off-by-one instead of the "degrading never
// yields" rule.
func TestCapCoverageEntriesToWriteBoundNeverDropsADegradingRowEvenAtTheBoundary(t *testing.T) {
	var details []CoverageDetail
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount; i++ {
		details = append(details, syntheticDegradingDetail(i))
	}
	for i := 0; i < 20; i++ {
		details = append(details, syntheticNonDegradingDetail(i))
	}
	result := &InvestigationResult{Coverage: Coverage{Details: details}}

	omitted := capCoverageEntriesToWriteBound(result)

	if omitted != 20 {
		t.Fatalf("omitted = %d, want 20 -- degrading alone already fills the bound, so every non-degrading row yields", omitted)
	}
	if got := len(result.Coverage.Details); got != contractsv1.ContextFabricCoverageEntriesMaxCount {
		t.Fatalf("details after cap = %d, want exactly the bound %d", got, contractsv1.ContextFabricCoverageEntriesMaxCount)
	}
	for i, d := range result.Coverage.Details {
		if !d.Degrading {
			t.Fatalf("detail %d is non-degrading -- want every surviving row to be one of the %d degrading rows", i, contractsv1.ContextFabricCoverageEntriesMaxCount)
		}
	}
}

// TestCapCoverageEntriesToWriteBoundClampsRemainingWhenDegradingAloneExceedsTheBound
// pins the remaining-capacity clamp (`remaining < 0` -> 0) for the
// pathological cell past the boundary test above: degrading ALONE already
// exceeds the bound before any non-degrading row is even considered.
// Without the clamp, `remaining` goes negative and the subsequent
// `nonDegrading[:remaining]` slice expression panics -- a mutant that
// removes or inverts the clamp is caught here, not by any cell where
// degrading merely fills the bound exactly (remaining lands on 0 either
// way there, so that boundary alone cannot distinguish the two).
//
// This is also the one shape this cap cannot fully repair: degrading rows
// never yield, so the served total can still exceed the bound when
// degrading alone does. That is an accepted, documented limitation (real
// vocabularies cannot reach it -- the maximal fixture tops out at 66
// degrading rows) and this test pins the CURRENT behavior precisely so a
// future change to it is a deliberate, reviewed decision, not a silent
// side effect.
func TestCapCoverageEntriesToWriteBoundClampsRemainingWhenDegradingAloneExceedsTheBound(t *testing.T) {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	var details []CoverageDetail
	for i := 0; i < bound+10; i++ {
		details = append(details, syntheticDegradingDetail(i))
	}
	for i := 0; i < 5; i++ {
		details = append(details, syntheticNonDegradingDetail(i))
	}
	result := &InvestigationResult{Coverage: Coverage{Details: details}}

	omitted := capCoverageEntriesToWriteBound(result)

	if omitted != 5 {
		t.Fatalf("omitted = %d, want 5 -- every non-degrading row yields once remaining capacity clamps to 0", omitted)
	}
	if got := len(result.Coverage.Details); got != bound+10 {
		t.Fatalf("details after cap = %d, want %d -- all %d degrading rows survive uncapped (documented limitation)", got, bound+10, bound+10)
	}
	for i, d := range result.Coverage.Details {
		if !d.Degrading {
			t.Fatalf("detail %d is non-degrading -- want only the %d degrading rows to survive", i, bound+10)
		}
	}
}

// TestCapCoverageEntriesToWriteBoundIsANoopWhenDegradingAloneExceedsTheBoundWithNoNonDegradingRows
// is the second half of the clamp pin above, for the cell that test cannot
// reach: nonDegrading EMPTY. `remaining` still clamps to 0 (degrading alone
// is already over the bound), so `len(nonDegrading) <= remaining` reads
// `0 <= 0` -- true, same as the ordinary no-op path -- and the whole
// function must return before ever reaching the mint loop, leaving every
// producer-minted id untouched.
//
// This is the one cell that can tell "the second guard's `<=` is really
// `<=`" apart from a `<` weakening: at every OTHER shape this test file
// exercises, `len(nonDegrading) == remaining` forces `len(details) ==
// bound` exactly (remaining is only ever `bound - degrading` when it is not
// clamped), so the FIRST guard already returns before the second is ever
// evaluated -- a `<=`-to-`<` weakening on the second guard is invisible
// everywhere except here, where the clamp decouples `remaining` from that
// identity.
func TestCapCoverageEntriesToWriteBoundIsANoopWhenDegradingAloneExceedsTheBoundWithNoNonDegradingRows(t *testing.T) {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	var details []CoverageDetail
	for i := 0; i < bound+10; i++ {
		details = append(details, syntheticDegradingDetail(i))
	}
	originalIDs := make([]string, len(details))
	for i, d := range details {
		originalIDs[i] = d.DetailID
	}
	result := &InvestigationResult{Coverage: Coverage{Details: append([]CoverageDetail(nil), details...)}}

	omitted := capCoverageEntriesToWriteBound(result)

	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0 -- there are no non-degrading rows to yield", omitted)
	}
	if got := len(result.Coverage.Details); got != bound+10 {
		t.Fatalf("details after cap = %d, want unchanged %d", got, bound+10)
	}
	for i, d := range result.Coverage.Details {
		if d.DetailID != originalIDs[i] {
			t.Errorf("detail %d id = %q, want unchanged %q -- omitted=0 must never re-mint ids", i, d.DetailID, originalIDs[i])
		}
	}
}

func rawsOf(details []CoverageDetail) []string {
	raws := make([]string, len(details))
	for i, d := range details {
		raws[i] = d.Raw
	}
	return raws
}

// TestSlogEngineTelemetry_RecordCoverageEntriesCappedLogsCounts pins the
// CERTIFIED half of CHAOS-5612's bar: the disclosure reaches the real
// EngineTelemetry implementation, at Info, with the served/omitted/bound
// counts a reader needs to rebuild the decision -- through the SAME
// NewSlogEngineTelemetry constructor production wires to its configured
// logger (chaos5285_retention_trace_test.go's captureEngineLogger uses this
// identical constructor for the "configured" side of its split).
func TestSlogEngineTelemetry_RecordCoverageEntriesCappedLogsCounts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordCoverageEntriesCapped(context.Background(), storage.Principal{OrgID: "org_telemetry_cap"}, 100, 10)

	line := buf.String()
	for _, want := range []string{
		"level=INFO",
		"msg=\"context fabric coverage entries capped\"",
		"org_id=org_telemetry_cap",
		"coverage_entries_served=100",
		"coverage_entries_omitted=10",
		fmt.Sprintf("coverage_entries_bound=%d", contractsv1.ContextFabricCoverageEntriesMaxCount),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line = %q, want it to contain %q", line, want)
		}
	}
	// Content-safety: never a detail_id, fact kind, or raw/label string --
	// the same discipline every sibling counter on EngineTelemetry follows.
	if strings.Contains(line, "detail_id") || strings.Contains(line, "fact_kind") || strings.Contains(line, "raw=") {
		t.Errorf("log line = %q, unexpectedly contains content-bearing keys", line)
	}
}
