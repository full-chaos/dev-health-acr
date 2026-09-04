package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// The three numbers that describe one measured document are stamped in ONE
// place, and this test is what keeps it that way.
//
// WHY A STRUCTURAL PIN AND NOT A BEHAVIOURAL ONE. Stage three emits its
// assembled-result event from five distinct arms -- the measured fit, the
// retry-synthesis failure, the retry that did not fit, the planned refusal,
// and the outcome layer's served narrowing. Every one of them used to write
// MeasuredItems and MeasuredBytes as two free-standing assignments. This
// seam's entire review history is a decision dimension present on some arms
// and absent from others: written at three sites and read at none, then
// reaching the served emitter and dropped on both refusal arms, then dropped
// on the retry-fit path. Each fix was correct for the arm it addressed and the
// omission moved one branch over.
//
// A behavioural test can only cover the arms someone thought to drive. This
// one quantifies over ALL of them, from the syntax tree, so a sixth arm added
// next year cannot stamp two of the three numbers and leave the third at zero.
//
// It is an AST walk rather than a text search on purpose: a substring pin
// passes on a commented-out assignment and fails on the word appearing in a
// comment, and both mistakes have been made in this package.

// measurementFieldNames are the fields that must move together, because they
// describe one document and a reader given two of them and a stale third
// cannot tell which to believe.
var measurementFieldNames = map[string]bool{
	"MeasuredItems": true,
	"MeasuredBytes": true,
	"Attribution":   true,
}

// measurementStamp is one assignment to one of those fields.
type measurementStamp struct {
	file     string
	function string
	field    string
	line     int
}

// findMeasurementStamps walks a parsed file and reports every assignment to a
// measurement field, with the function it sits in.
func findMeasurementStamps(fset *token.FileSet, file *ast.File, name string) []measurementStamp {
	stamps := []measurementStamp{}
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			assign, isAssign := node.(*ast.AssignStmt)
			if !isAssign {
				return true
			}
			for _, target := range assign.Lhs {
				selector, isSelector := target.(*ast.SelectorExpr)
				if !isSelector || !measurementFieldNames[selector.Sel.Name] {
					continue
				}
				stamps = append(stamps, measurementStamp{
					file:     name,
					function: fn.Name.Name,
					field:    selector.Sel.Name,
					line:     fset.Position(selector.Pos()).Line,
				})
			}
			return true
		})
	}
	return stamps
}

// countRecordMeasurementCalls counts calls to the one stamping method.
func countRecordMeasurementCalls(file *ast.File) int {
	calls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		if selector, isSelector := call.Fun.(*ast.SelectorExpr); isSelector && selector.Sel.Name == "recordMeasurement" {
			calls++
		}
		return true
	})
	return calls
}

// packageProductionFiles is every non-test Go file of this package.
func packageProductionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	// A walk that found no files would report "no violations" and mean
	// "the measurement never happened" -- the shape this repository's own
	// conventions call a measurement that must fail loudly.
	if len(names) < 20 {
		t.Fatalf("walked only %d production files in this package; the enumeration is broken, "+
			"and an empty walk reports a clean result for a check that never ran", len(names))
	}
	return names
}

func TestEveryAssembledResultArmStampsItsMeasurementThroughOnePath(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()

	offenders := []measurementStamp{}
	stampsInHelper := map[string]bool{}
	calls := 0
	for _, name := range packageProductionFiles(t) {
		parsed, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		calls += countRecordMeasurementCalls(parsed)
		for _, stamp := range findMeasurementStamps(fset, parsed, name) {
			if stamp.function == "recordMeasurement" {
				stampsInHelper[stamp.field] = true
				continue
			}
			offenders = append(offenders, stamp)
		}
	}

	// The helper must actually stamp all three, or "one path" is one path
	// to a partial write.
	for field := range measurementFieldNames {
		if !stampsInHelper[field] {
			t.Errorf("recordMeasurement does not assign %s: the single stamping path writes only part "+
				"of the group it exists to keep together", field)
		}
	}

	// The arms. Five is the number that exist today; a floor rather than an
	// equality so that adding an arm is not a test failure, but losing one
	// silently is.
	const knownArms = 5
	if calls < knownArms {
		t.Errorf("found %d recordMeasurement call sites, want at least the %d assembled-result arms: "+
			"an arm has stopped stamping its measurement", calls, knownArms)
	}

	for _, offender := range offenders {
		t.Errorf("%s:%d in %s assigns %s directly instead of through recordMeasurement -- "+
			"the three numbers that describe one document must be written together, or this arm can "+
			"carry two of them and a zero for the third",
			offender.file, offender.line, offender.function, offender.field)
	}
}

// TestTheOneStampPinCanActuallyFail is the positive control.
//
// A structural check that cannot detect the thing it forbids reports every
// tree clean, and this package has shipped exactly that: a source pin that
// passed on a commented-out assignment because the literal text was still in
// the file. So the detector is run against a source that DOES contain the
// violation, and must find it.
func TestTheOneStampPinCanActuallyFail(t *testing.T) {
	t.Parallel()
	const violating = `package contextfabric

func someNewArm(event *PlanNarrowingEvent, measurement ResponseMeasurement) {
	event.MeasuredItems = measurement.Items.Budgeted()
	event.MeasuredBytes = measurement.Bytes
	// the third one is exactly what an arm forgets
}
`
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "violating.go", violating, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse control source: %v", err)
	}
	stamps := findMeasurementStamps(fset, parsed, "violating.go")
	found := map[string]bool{}
	for _, stamp := range stamps {
		if stamp.function == "recordMeasurement" {
			t.Fatalf("the control source has no recordMeasurement; the detector attributed %+v to it", stamp)
		}
		found[stamp.field] = true
	}
	if !found["MeasuredItems"] || !found["MeasuredBytes"] {
		t.Fatalf("the detector missed a direct assignment it must catch; it found %v", found)
	}

	// And the converse: an assignment INSIDE recordMeasurement is not an
	// offence, or the check would flag its own single stamping path.
	const permitted = `package contextfabric

func (e *PlanNarrowingEvent) recordMeasurement(measurement ResponseMeasurement) {
	e.MeasuredItems = measurement.Items.Budgeted()
}
`
	parsedPermitted, err := parser.ParseFile(fset, "permitted.go", permitted, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse permitted source: %v", err)
	}
	for _, stamp := range findMeasurementStamps(fset, parsedPermitted, "permitted.go") {
		if stamp.function != "recordMeasurement" {
			t.Fatalf("an assignment inside recordMeasurement was attributed to %q", stamp.function)
		}
	}
}
