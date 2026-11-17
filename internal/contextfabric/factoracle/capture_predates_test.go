package factoracle

import (
	"slices"
	"strings"
	"testing"
)

const sizedUnitCount = "analytics.evidenceQualityStats.sizedUnitCount"

func shapesOf(shapes []Shape, operation string) []Shape {
	var out []Shape
	for _, s := range shapes {
		if s.Operation == operation {
			out = append(out, s)
		}
	}
	return out
}

// A listed path leaves every shape of its operation, and only of it.
func TestCapturePredatesDropsThePathFromEveryShapeOfTheOperationOnly(t *testing.T) {
	shapes, err := Shapes(mustPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := withoutCapturePredates(shapes, map[string][]string{"investmentBreakdownTeam": {sizedUnitCount}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(shapes) {
		t.Fatalf("%d shapes, want %d", len(got), len(shapes))
	}
	held := 0
	for i := range shapes {
		before, after := shapes[i], got[i]
		if before.ID() != after.ID() {
			t.Fatalf("shape %d is %s, want %s", i, after.ID(), before.ID())
		}
		if before.Operation != "investmentBreakdownTeam" {
			if !slices.Equal(before.Paths, after.Paths) {
				t.Errorf("%s: paths changed", after.ID())
			}
			continue
		}
		if slices.Contains(after.Paths, sizedUnitCount) {
			t.Errorf("%s still selects %s", after.ID(), sizedUnitCount)
		}
		if slices.Contains(before.Paths, sizedUnitCount) {
			held++
			if len(after.Paths) != len(before.Paths)-1 {
				t.Errorf("%s: %d paths, want %d", after.ID(), len(after.Paths), len(before.Paths)-1)
			}
		}
	}
	if held < 2 {
		t.Fatalf("%d investmentBreakdownTeam shapes selected %s, want the full selection and its branch", held, sizedUnitCount)
	}
	for _, s := range shapesOf(got, "investmentBreakdownRepository") {
		if s.Name == "all" && !slices.Contains(s.Paths, sizedUnitCount) {
			t.Fatalf("an unlisted operation lost %s", sizedUnitCount)
		}
	}
}

func TestCapturePredatesRefusesAListThatNamesNothingReal(t *testing.T) {
	shapes, err := Shapes(mustPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		list map[string][]string
		want string
	}{
		"operation not a candidate": {map[string][]string{"noSuchOperation": {sizedUnitCount}}, "no root has it as a candidate"},
		"path not an output":        {map[string][]string{"investmentBreakdown": {sizedUnitCount}}, "not an output path"},
		"no path":                   {map[string][]string{"investmentBreakdownTeam": nil}, "with no path"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := withoutCapturePredates(shapes, tc.list); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// An answer that holds a listed path is a finding of the trimmed shape, so the
// list cannot outlive a capture that records the field.
func TestCapturePredatesAnswerHoldingTheListedPathIsAProblem(t *testing.T) {
	shapes, err := Shapes(mustPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	trimmed, err := withoutCapturePredates(shapes, map[string][]string{"investmentBreakdownTeam": {sizedUnitCount}})
	if err != nil {
		t.Fatal(err)
	}
	all, ok := ShapeByID(trimmed, "analytics/investmentBreakdownTeam/all")
	if !ok {
		t.Fatal("no full selection of investmentBreakdownTeam")
	}
	answer := func(withSized bool) any {
		sized := ""
		if withSized {
			sized = `,"sizedUnitCount":2`
		}
		data, err := decodeJSON([]byte(`{"analytics":{"breakdowns":[],"evidenceQualityDistribution":{},"evidenceQualityStats":{"mean":0.5,"stddev":0.1,"total":3,"bandCounts":{},"uncategorizedCount":1` + sized + `,"__typename":"EvidenceQualityStats"},"__typename":"AnalyticsResult"}}`))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	_, problems := all.shapeLeaves(answer(false))
	if len(problems) != 0 {
		t.Fatalf("an answer without the listed path: %v", problems)
	}
	_, problems = all.shapeLeaves(answer(true))
	if len(problems) != 1 || !strings.Contains(problems[0], sizedUnitCount+" is in the answer but not in the selection") {
		t.Fatalf("an answer holding the listed path: %v", problems)
	}
}
