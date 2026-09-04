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

// A STRUCTURAL AID, NOT THE GUARANTEE. Read this before trusting it.
//
// Stage three emits its assembled-result event from five arms -- the measured
// fit, the retry-synthesis failure, the retry that did not fit, the planned
// refusal, and the outcome layer's served narrowing. Every one used to write
// MeasuredItems and MeasuredBytes as two free-standing assignments, and this
// seam's whole review history is a dimension present on some arms and absent
// from others. Routing them through one `recordMeasurement` removes the easy
// version of that mistake, and this walk keeps the routing in place.
//
// WHAT IT ENFORCES: no function in this package's non-test files ASSIGNS to
// MeasuredItems, MeasuredBytes or Attribution outside `recordMeasurement`, and
// none takes the ADDRESS of one of those fields. Both are checked on the
// syntax tree rather than by text search, because a substring pin passes on a
// commented-out assignment and fails on the word appearing in a comment, and
// both mistakes have been made here.
//
// WHAT IT CANNOT ENFORCE, stated because a guard that overclaims is worse than
// one that admits its edge. The set of ways to write to a struct field is
// open: an alias to the whole event, a slice or map of events, reflection, a
// helper in another package handed a pointer. An adversarial review defeated
// the FIRST version of this walk in two lines --
//
//	attribution := &event.Attribution
//	*attribution = contractsv1.ContextFabricItemAttribution{}
//
// -- because the left-hand side of that assignment is a star expression, not a
// selector. The address-taking check below closes exactly that shape and makes
// no claim beyond it.
//
// SO THIS IS NOT WHAT PROVES THE VALUES ARE RIGHT.
// TestEveryAssembledResultArmEmitsASplitThatDescribesIt drives each arm through
// the public entry point and reads what the engine emitted; it fails whatever
// route was used to corrupt a stamp, and it is the load-bearing test. The real
// defect that review found was not the walk's blind spot -- it was that three
// of the five arms had no behavioural case at all, which is what made a static
// check load-bearing in the first place.

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
	// via names HOW the field was reached, so the failure message tells a
	// reader which of the two shapes fired rather than leaving them to
	// guess from a line number.
	via string
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
		// Address-taking, checked separately because it is not an
		// assignment: `&event.Attribution` hands a writer a route the
		// assignment walk above cannot see. Anywhere outside
		// recordMeasurement it is treated as a stamp.
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			unary, isUnary := node.(*ast.UnaryExpr)
			if !isUnary || unary.Op != token.AND {
				return true
			}
			selector, isSelector := unary.X.(*ast.SelectorExpr)
			if !isSelector || !measurementFieldNames[selector.Sel.Name] {
				return true
			}
			stamps = append(stamps, measurementStamp{
				file:     name,
				function: fn.Name.Name,
				field:    selector.Sel.Name,
				line:     fset.Position(selector.Pos()).Line,
				via:      "address-of",
			})
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
		how := offender.via
		if how == "" {
			how = "assigns"
		}
		t.Errorf("%s:%d in %s %s %s outside recordMeasurement -- the three numbers that describe one "+
			"document must be written together, or this arm can carry two of them and a zero for the third",
			offender.file, offender.line, offender.function, how, offender.field)
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

	// THE SHAPE AN ADVERSARIAL REVIEW ACTUALLY USED. The first version of
	// this walk only matched assignments whose left-hand side is a selector,
	// so taking the address of a guarded field and writing through the alias
	// was invisible to it and to every behavioural test then present. This
	// control is the reason the address-of arm exists, and it fails if that
	// arm is ever removed.
	const aliasing = `package contextfabric

func anotherNewArm(event *PlanNarrowingEvent, measurement ResponseMeasurement) {
	event.recordMeasurement(measurement)
	attribution := &event.Attribution
	*attribution = contractsv1.ContextFabricItemAttribution{}
}
`
	parsedAliasing, err := parser.ParseFile(fset, "aliasing.go", aliasing, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse aliasing source: %v", err)
	}
	viaAddress := false
	for _, stamp := range findMeasurementStamps(fset, parsedAliasing, "aliasing.go") {
		if stamp.via == "address-of" && stamp.field == "Attribution" {
			viaAddress = true
		}
	}
	if !viaAddress {
		t.Fatal("the detector did not flag `&event.Attribution`: the aliasing route a review used to " +
			"zero an arm's split is invisible to it again")
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
