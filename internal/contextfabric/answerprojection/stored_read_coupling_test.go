package answerprojection

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file states the coupling invariant for the STORED-READ boundary
// (codex round-14 F1).
//
// It is the same invariant TestQuestionEchoCannotComposeThenReject states one
// layer down, applied to the layer it was not extended to: anything
// ValidateStored ACCEPTS must survive Project plus projection validation.
//
// The gap it closes: canonical stored reads measure bounded text on the
// TRIMMED value, deliberately, because padded rows were legally writable and
// are immutable (CHAOS-3746 round 13, world (b)). The projection then copied
// those values verbatim and validated them RAW, so a legacy row that reads
// back perfectly well produced a projection its own validator rejected --
// HTTP 500 from the hosted route, an internal error from the MCP tool. A
// readable answer became unservable, which is worse than either layer's
// behaviour taken alone.
//
// Testing the two layers separately would keep both green while they
// disagreed. The coupling is the invariant, so the coupling is asserted.

// paddedLegacyResult returns a result that is VALID to read from storage and
// carries whitespace padding in every bounded text field a legacy row could
// legitimately hold it in.
//
// The padding is what a pre-enforcement writer could produce: raw length past
// the maximum, trimming back to within it.
func paddedLegacyResult(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	// Padded to EXACTLY the maximum after trimming: that is the only shape
	// that is readable canonically and oversized to the projection. Padding a
	// short value proves nothing, because it stays inside the bound either
	// way -- the failure needs a value sitting on the boundary, which is
	// precisely the legacy row codex described.
	atMax := func(seed string, maximum int) string {
		body := seed + strings.Repeat("x", maximum-len(seed))
		return "  " + body + "  "
	}

	result := richResult()
	result.Question = atMax("legacy question ", 8000)
	for i := range result.Drivers {
		result.Drivers[i].Title = atMax("legacy title ", contractsv1.ContextFabricDriverTitleMaxLength)
		result.Drivers[i].Summary = atMax("legacy summary ", contractsv1.ContextFabricDriverSummaryMaxLength)
		// Qualification is deliberately NOT padded: canonical validation
		// measures it RAW on both paths, so a padded qualification was never
		// writable and no legacy row can carry one. Padding it here would
		// build a fixture that ValidateStored rejects, testing nothing.
		// Established empirically -- the first version of this fixture did
		// pad it, and the withheld driver's stored read refused it.
	}
	if result.Cohort != nil {
		result.Cohort.Rationale = atMax("legacy rationale ", 4000)
	}
	// List-valued bounded text (strongest_pressures, limitations, warnings,
	// inclusion reasons) is deliberately NOT padded, for the same reason as
	// Qualification: uniqueTrimmedStrings requires TrimSpace(value) == value
	// on BOTH paths (validate_context_fabric_helpers.go:275), so a padded
	// list item was never writable and no legacy row can carry one.
	//
	// Established empirically -- padding them made ValidateStored refuse the
	// fixture. The blast radius of the round-13 leniency is therefore exactly
	// the SCALAR bounded-text fields; the list fields were already immune.

	// The premise: this row is legitimately readable. If it is not, the test
	// is not exercising the boundary it claims to.
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("the padded fixture is not a readable stored row, so it cannot test the stored-read boundary: %v", err)
	}
	return result
}

// TestAnythingReadableProjectsValidly is the coupling assertion.
func TestAnythingReadableProjectsValidly(t *testing.T) {
	result := paddedLegacyResult(t)

	projection := Project(result, Budget{MaxDrivers: 50, MaxCohortMembers: 100, MaxEvidenceRefs: 500})
	if err := projection.Validate(); err != nil {
		t.Errorf("a stored result that ValidateStored ACCEPTS produced a projection its own validator rejects (%v).\nThe row is readable canonically but unservable through the answer surface: the hosted route answers 500 and the MCP tool reports an internal error, for data the service itself says is valid.", err)
	}
}

// TestProjectionTrimsRatherThanTruncatesLegacyPadding pins HOW the boundary
// is crossed, not merely that it is.
//
// Trimming is normalization of legal legacy data, not repair of model output:
// whitespace padding carries no content, so removing it changes nothing a
// reader could observe except the length. Truncating instead would cut real
// characters off the end of a value that was never too long, and would have
// to be declared in the budget as a clamp -- a false statement, since nothing
// the caller cares about was dropped.
func TestProjectionTrimsRatherThanTruncatesLegacyPadding(t *testing.T) {
	result := paddedLegacyResult(t)
	projection := Project(result, Budget{MaxDrivers: 50, MaxCohortMembers: 100, MaxEvidenceRefs: 500})

	if got, want := projection.Question, strings.TrimSpace(result.Question); got != want {
		t.Errorf("projected question = %q, want the trimmed stored value %q", got, want)
	}
	for i, driver := range projection.PrincipalDrivers {
		if strings.TrimSpace(driver.Title) != driver.Title {
			t.Errorf("driver %d title still carries padding: %q", i, driver.Title)
		}
		if strings.TrimSpace(driver.Summary) != driver.Summary {
			t.Errorf("driver %d summary still carries padding: %q", i, driver.Summary)
		}
		if strings.TrimSpace(driver.Qualification) != driver.Qualification {
			t.Errorf("driver %d qualification still carries padding: %q", i, driver.Qualification)
		}
	}

	// Trimming padding is not clamping. A caller reading ValuesClamped must
	// not be told content was shortened when only whitespace was removed.
	if projection.ProjectionBudget.ValuesClamped != 0 {
		t.Errorf("ValuesClamped = %d after trimming padding only; removing whitespace is normalization, not a clamp, and must not be reported as one",
			projection.ProjectionBudget.ValuesClamped)
	}
}

// TestPaddedAndOversizedStillReportsGenuineClamping closes the one-sided
// assertion in TestProjectionTrimsRatherThanTruncatesLegacyPadding
// (self-found before the round-15 verdict).
//
// That test asserts ValuesClamped == 0 for padding-only input, which is the
// right assertion for the defect it guards -- but it only ever checks the
// ZERO side. If trimming ever started swallowing real truncation, the count
// would drop to zero for genuinely shortened values too and that test would
// stay green. A test that can only fail in one direction is the vacuity
// family with a time delay.
//
// The composite case is reachable, not hypothetical: legacy judgment text is
// readable up to 8000 runes while the projection bounds it at 4000, so a
// stored row can carry a judgment that is BOTH padded and genuinely
// oversized. Trimming must remove the padding and clamping must still report
// the real loss.
func TestPaddedAndOversizedStillReportsGenuineClamping(t *testing.T) {
	const projected = contractsv1.ContextFabricProjectedJudgmentMaxLength
	const stored = 6000 // within the legacy allowance, past the projection bound

	result := richResult()
	result.DirectJudgment = "  " + strings.Repeat("j", stored) + "  "

	if err := result.ValidateStored(); err != nil {
		t.Fatalf("the fixture is not a readable stored row: %v", err)
	}
	if len([]rune(strings.TrimSpace(result.DirectJudgment))) <= projected {
		t.Fatal("the fixture is not oversized after trimming, so it cannot exercise genuine clamping")
	}

	projection := Project(result, Budget{MaxDrivers: 50, MaxCohortMembers: 100, MaxEvidenceRefs: 500})

	if got := len([]rune(projection.DirectJudgment)); got != projected {
		t.Errorf("projected judgment is %d runes, want it clamped to %d", got, projected)
	}
	if strings.TrimSpace(projection.DirectJudgment) != projection.DirectJudgment {
		t.Errorf("the clamped judgment still carries padding: %q", projection.DirectJudgment[:20])
	}
	// Exactly one value was genuinely shortened, and it must be reported.
	// Nothing else in richResult clamps -- the padding-only case proves that
	// by asserting zero -- so this isolates the composite field.
	if got := projection.ProjectionBudget.ValuesClamped; got != 1 {
		t.Errorf("ValuesClamped = %d, want 1: the judgment lost real characters, not just padding, and a consumer must be told",
			got)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("truncated is false while a value was clamped")
	}
}
