package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A refusal must name the axis of the document it MEASURED.
//
// This exists because of a review finding: `planRefusal` took the measurement
// from the attempt the candidate reduction returned, and the axis from a
// separate parameter carrying the caller's PRE-reduction value. After a
// reduction runs the document is a different document, so the refusal published
// post-reduction counts under a pre-reduction axis -- 30 items against a ceiling
// of 30 (not over) and 9593 bytes against 9500 (over), while naming `items`.
//
// The numbers and the axis described different documents. A reader given both
// has no way to tell which to believe, and the axis is the half that decides
// what a caller is told to do about it.
//
// The fix is the same rule the allocator's Agreement() applies to grants: if
// two things must agree, do not carry them separately and hope. One value, one
// derivation.

// refusalAxisEngine drives the SAME fixture the outcome-reduction sweep uses,
// because that is the fixture that reaches the exit where the two documents
// diverge: the reduction runs, removes items, and the result still does not fit.
func refusalAxisEngine(t *testing.T, sink *bytes.Buffer, maxItems int, maxBytes int64) *Engine {
	t.Helper()
	engine, _ := attributionEngine(t, attributionFixtureSpec{
		members: 1, globalFindings: 3, groupDrivers: 5, multiGroupDrivers: 6,
		memberDrivers: 1, candidates: 7,
	}, sink, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: maxItems, MaxSerializedBytes: maxBytes,
		Now:         budgetStageOptions(maxItems, 0).Now,
		NewResultID: budgetStageOptions(maxItems, 0).NewResultID,
	}, &[]int{})
	return engine
}

// TestEveryRefusalNamesTheAxisOfItsOwnMeasurement is the finding, pinned.
//
// A GRID over BOTH ceilings, and the second axis is the whole reason this test
// works. My first version swept byte ceilings at a fixed 30-item ceiling and
// passed against the broken code -- at 30 items this fixture never lands in the
// band where the two documents differ, so the sweep exercised only exits where
// the stale axis happened to be right. Measured under the unfixed tree, the
// contradiction lives at item ceilings of 16 and 20 with a tight byte ceiling:
//
//	maxItems=16 maxBytes=8000  axis=items  measured 16 items (NOT over 16) / 8771 bytes (over 8000)
//	maxItems=20 maxBytes=9000  axis=items  measured 20 items (NOT over 20) / 9606 bytes (over 9000)
//
// Both name `items` while their own numbers show the item axis is not breached.
// The grid straddles that band, and the non-vacuity checks below fail if a
// fixture change moves the serialized size out from under it.
func TestEveryRefusalNamesTheAxisOfItsOwnMeasurement(t *testing.T) {
	t.Parallel()
	refusals := 0
	namedItems := 0
	namedBytes := 0

	for _, maxItems := range []int{12, 16, 20, 24, 30} {
		for _, maxBytes := range []int64{8000, 9000, 9500, 11000} {
			var sink bytes.Buffer
			engine := refusalAxisEngine(t, &sink, maxItems, maxBytes)
			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
				validInvestigationRequestWithConfirmedWindow())
			if err == nil {
				continue
			}
			var refusal AnswerBudgetRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("items=%d bytes=%d: error = %v, want an AnswerBudgetRefusal", maxItems, maxBytes, err)
			}
			refusals++

			if !contractsv1.ValidContextFabricBudgetOverrun(refusal.Overrun) {
				t.Errorf("items=%d bytes=%d: overrun=%q is outside the closed vocabulary",
					maxItems, maxBytes, refusal.Overrun)
				continue
			}
			if refusal.Overrun == contractsv1.ContextFabricBudgetFits {
				t.Errorf("items=%d bytes=%d: the refusal reports overrun=fits; an answer that fits is "+
					"not refused", maxItems, maxBytes)
				continue
			}

			// THE ASSERTION: the axis must be one the refusal's OWN numbers
			// support. Stated as an implication rather than an equality
			// because a document can breach both ceilings at once and either
			// name is then honest -- what is never honest is naming an axis
			// the measurement shows is not breached.
			overItems := refusal.MeasuredItems > maxItems
			overBytes := refusal.MeasuredBytes > maxBytes
			switch refusal.Overrun {
			case contractsv1.ContextFabricBudgetOverrunItems:
				namedItems++
				if !overItems {
					t.Errorf("items=%d bytes=%d: the refusal names overrun=items, but its own "+
						"measurement is %d items against a ceiling of %d -- NOT over -- and %d bytes "+
						"against %d. The axis and the counts describe different documents.",
						maxItems, maxBytes, refusal.MeasuredItems, maxItems,
						refusal.MeasuredBytes, maxBytes)
				}
			case contractsv1.ContextFabricBudgetOverrunBytes:
				namedBytes++
				if !overBytes {
					t.Errorf("items=%d bytes=%d: the refusal names overrun=bytes, but its own "+
						"measurement is %d bytes against a ceiling of %d -- NOT over -- and %d items "+
						"against %d.",
						maxItems, maxBytes, refusal.MeasuredBytes, maxBytes,
						refusal.MeasuredItems, maxItems)
				}
			}
		}
	}

	// Non-vacuity, in three parts. A grid that refuses nowhere asserts
	// nothing; a grid that only ever names one axis is not straddling the
	// window this test exists for.
	if refusals == 0 {
		t.Fatal("the grid produced no refusal at any ceiling pair, so this test asserted nothing")
	}
	if namedItems == 0 || namedBytes == 0 {
		t.Fatalf("the grid produced %d items-refusals and %d bytes-refusals; it must straddle both "+
			"axes or it cannot see an axis reported for the wrong document", namedItems, namedBytes)
	}
	t.Logf("grid produced %d refusals (%d items, %d bytes), each checked against its own measurement",
		refusals, namedItems, namedBytes)
}

// TestAnUnmeasuredOverrunIsUnclassifiedNotEmpty covers the smaller half of the
// same defect, at the sink.
//
// The zero value of ContextFabricBudgetOverrun is "" -- not `fits`, and not a
// vocabulary member -- so an arm that published an attempt nobody measured used
// to emit `overrun=` and say nothing at all, indistinguishable from a field
// whose value happened to be blank. Every other closed value in this file
// already routes through a fail-closed helper; this one did not.
func TestAnUnmeasuredOverrunIsUnclassifiedNotEmpty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   contractsv1.ContextFabricBudgetOverrun
		want contractsv1.ContextFabricBudgetOverrun
	}{
		{"unmeasured zero value", contractsv1.ContextFabricBudgetOverrun(""), "unclassified"},
		{"a value outside the vocabulary", contractsv1.ContextFabricBudgetOverrun("sideways"), "unclassified"},
		{"fits passes through", contractsv1.ContextFabricBudgetFits, contractsv1.ContextFabricBudgetFits},
		{"items passes through", contractsv1.ContextFabricBudgetOverrunItems, contractsv1.ContextFabricBudgetOverrunItems},
		{"bytes passes through", contractsv1.ContextFabricBudgetOverrunBytes, contractsv1.ContextFabricBudgetOverrunBytes},
	}
	passthrough := 0
	for _, one := range cases {
		if got := validBudgetOverrunOrUnclassified(one.in); got != one.want {
			t.Errorf("%s: validBudgetOverrunOrUnclassified(%q) = %q, want %q", one.name, one.in, got, one.want)
		}
		if one.in == one.want {
			passthrough++
		}
	}
	// A control on the test: if every case were a passthrough, a helper that
	// simply returned its argument would satisfy all of them.
	if passthrough != 3 {
		t.Fatalf("%d passthrough cases; the table must contain both kinds or it cannot discriminate an "+
			"identity function", passthrough)
	}
}
