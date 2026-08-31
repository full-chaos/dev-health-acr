package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CHAOS-4637 (S6): a render shape must trace to a plan item. A shape whose
// KIND the answer plan does not authorize is refused by the document's own
// validator, not merely declined by the selector.
//
// The selector already consults the plan. This is deliberately a SECOND,
// independent gate: selection is one code path, and a result also arrives
// here from storage, from a replay, and from any future producer. North Star
// check 10 -- "rich views are conditional on intent, never default" -- is
// worth stating as a property of the DOCUMENT rather than as a property of
// one function that happened to check.

// publishedRenderShapeResult is the golden example -- a COMPLETE, valid
// document rather than a minimal struct, so a refusal below can only be
// the plan gate and never a field the fixture forgot.
func publishedRenderShapeResult(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_render_shapes.v1.json"))
	if err != nil {
		t.Fatalf("read published example: %v", err)
	}
	var result ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode published example: %v", err)
	}
	return result
}

func planWithRenderKinds(kinds ...ContextFabricRenderKind) *ContextFabricAnswerPlan {
	return &ContextFabricAnswerPlan{
		Family:        ContextFabricQuestionFamilyGroupedCohortStatus,
		FamilySource:  ContextFabricQuestionFamilySourceFallback,
		FamilyVersion: "v1",
		RenderKinds:   kinds,
	}
}

func TestValidateRefusesARenderShapeThePlanDoesNotAuthorize(t *testing.T) {
	t.Parallel()
	result := publishedRenderShapeResult(t)
	// NON-VACUITY FIRST. The fixture must be a document that validates
	// CLEAN without a plan, and must actually carry a shape -- otherwise
	// the refusal below could come from anything.
	if len(result.RenderShapes) == 0 {
		t.Fatal("fixture carries no render shape; the plan gate cannot be shown to be what refuses it")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("fixture does not validate before the plan is attached: %v", err)
	}
	kind := result.RenderShapes[0].Kind
	if kind == ContextFabricRenderKindTable {
		t.Fatalf("fixture's shape kind is already %q, so a plan authorizing only `table` would not exclude it", kind)
	}

	// A plan that authorizes a DIFFERENT kind than the shape carries.
	result.AnswerPlan = planWithRenderKinds(ContextFabricRenderKindTable)
	err := result.Validate()
	if err == nil {
		t.Fatalf("a %q shape validated against a plan that authorizes only `table`", kind)
	}
	if !strings.Contains(err.Error(), "does not authorize") {
		t.Errorf("the error does not say the plan is what refused it: %v", err)
	}
}

func TestValidateAcceptsARenderShapeThePlanAuthorizes(t *testing.T) {
	t.Parallel()
	result := publishedRenderShapeResult(t)
	if len(result.RenderShapes) == 0 {
		t.Fatal("fixture carries no render shape")
	}
	result.AnswerPlan = planWithRenderKinds(result.RenderShapes[0].Kind)
	if err := result.Validate(); err != nil {
		t.Fatalf("a shape the plan explicitly authorizes was refused: %v", err)
	}
}

// TestAnAbsentOrSilentPlanAuthorizesEveryShape is the non-regression half,
// and it is not a formality: every result written before the planning stage
// existed carries no plan, and a plan that declared no render kinds has
// declared no restriction. Inferring a restriction from silence is how a
// chart quietly disappears.
func TestAnAbsentOrSilentPlanAuthorizesEveryShape(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		plan *ContextFabricAnswerPlan
	}{
		{"no plan at all", nil},
		{"a plan that declares no render kinds", planWithRenderKinds()},
	} {
		result := publishedRenderShapeResult(t)
		result.AnswerPlan = testCase.plan
		if err := result.Validate(); err != nil {
			t.Errorf("%s: a shape was refused: %v", testCase.name, err)
		}
	}
}

// TestADeclaredTableMustDescribeTheRowsBesideIt. The declaration is only
// worth carrying if it is checkable: a key column a consumer reads by
// declaration has to resolve to a cell on every row, or the axis it names is
// a hole.
func TestADeclaredTableMustDescribeTheRowsBesideIt(t *testing.T) {
	t.Parallel()
	day := "2026-08-03"
	ratio := 0.41
	base := func() ContextFabricClaimedFact {
		return ContextFabricClaimedFact{
			ClaimID: "claim_declared_ok",
			Kind:    "readiness",
			Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:x", Label: "x"},
			Field:   "coverage_ratio",
			Value:   ContextFabricScalarValue{Number: &ratio},
			Rows: []ContextFabricClaimedFactRow{
				{Fields: map[string]ContextFabricScalarValue{"day": {String: &day}, "coverage_ratio": {Number: &ratio}}},
			},
			Table: &ContextFabricClaimedFactTable{
				Field: "daily_readiness", Shape: ContextFabricFactTableShapeTimeSeries,
				Key: []string{"day"}, Measures: []string{"coverage_ratio"},
			},
		}
	}
	// Non-vacuity: the honest declaration validates.
	if err := base().Validate(); err != nil {
		t.Fatalf("the honest declaration was refused: %v", err)
	}

	for _, testCase := range []struct {
		name   string
		break_ func(*ContextFabricClaimedFact)
		want   string
	}{
		{"key column absent from the rows", func(c *ContextFabricClaimedFact) {
			c.Table.Key = []string{"observed_at"}
		}, "absent from row"},
		{"a time_series with two key columns", func(c *ContextFabricClaimedFact) {
			c.Table.Key = []string{"day", "provider"}
		}, "exactly one key column"},
		{"a column that is both key and measure", func(c *ContextFabricClaimedFact) {
			c.Table.Measures = []string{"coverage_ratio", "day"}
		}, "more than once"},
		{"a ranking whose order_by names no measure", func(c *ContextFabricClaimedFact) {
			c.Table.Shape = ContextFabricFactTableShapeRanking
			c.Table.OrderBy = "not_a_measure"
		}, "order_by must name"},
		{"order_by on a shape that has no order", func(c *ContextFabricClaimedFact) {
			c.Table.OrderBy = "coverage_ratio"
		}, "only for a ranking"},
		{"a shape outside the closed vocabulary", func(c *ContextFabricClaimedFact) {
			c.Table.Shape = "line_chart"
		}, "closed vocabulary"},
		{"a declaration with no rows to describe", func(c *ContextFabricClaimedFact) {
			c.Rows = nil
		}, "rows the fact does not carry"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			claim := base()
			testCase.break_(&claim)
			err := claim.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted %s", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error %q does not name the rule it broke (want it to contain %q)", err, testCase.want)
			}
		})
	}
}

// TestAStoredResultIsNotRefusedByARuleThatDidNotExistWhenItWasWritten is
// codex round 3 finding 1 (P1, re-run here before it was ledgered).
//
// The plan gate was first written into the SHARED validate(), which
// ValidateStored() also calls — and both investigation result stores
// validate on read. Investigation results are IMMUTABLE, so a rule
// introduced after a row was written can never be satisfied by that row:
// enforcing it on read turns a previously valid answer into an API 500 and
// an MCP retrieval failure. This file's own ValidateStored doc comment
// states that invariant in as many words, and the first version of this
// check broke it.
//
// Writes stay strict, which is where the rule earns its keep: no NEW result
// can be persisted carrying a shape its plan does not authorize.
func TestAStoredResultIsNotRefusedByARuleThatDidNotExistWhenItWasWritten(t *testing.T) {
	t.Parallel()
	result := publishedRenderShapeResult(t)
	if len(result.RenderShapes) == 0 {
		t.Fatal("fixture carries no render shape")
	}
	kind := result.RenderShapes[0].Kind
	if kind == ContextFabricRenderKindTable {
		t.Fatalf("fixture's shape kind is already %q; a plan authorizing only `table` would not exclude it", kind)
	}
	result.AnswerPlan = planWithRenderKinds(ContextFabricRenderKindTable)

	// NON-VACUITY: the WRITE path must still refuse it, or this test would
	// pass against a gate that had simply been deleted.
	if err := result.Validate(); err == nil {
		t.Fatal("the write path accepted an unauthorized shape; the rule has been lost, not scoped")
	}

	// And the READ path must accept it, because the row is immutable.
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("a stored result was refused by a rule that post-dates it: %v", err)
	}
}
