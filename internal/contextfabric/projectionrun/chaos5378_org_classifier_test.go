package projectionrun

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestEveryOrgOutcomeHasARecorder makes the classifier's vocabulary and its
// mapping onto the tick counters impossible to leave partial.
//
// recorderFor is an ALLOW-LIST with no default arm, deliberately: a default
// would admit the next member added to orgOutcome -- and the zero value with
// it -- by silently bucketing it as something it is not. The cost of that
// choice is that a new member with no case returns nil, so this test derives
// the member list from the source's OWN const block and asserts the map is
// total. Adding a member without a recorder fails here rather than in
// production, where it would surface as an organization that reached no
// verdict.
func TestEveryOrgOutcomeHasARecorder(t *testing.T) {
	t.Parallel()
	declared := declaredOrgOutcomes(t, "coordinator.go")
	if len(declared) == 0 {
		t.Fatal("no orgOutcome constants found in coordinator.go -- the walk matched nothing, so its result proves nothing")
	}

	stats := &tickFreshnessStats{}
	for _, name := range declared {
		outcome, ok := orgOutcomeByConstName[name]
		if !ok {
			t.Errorf("orgOutcome constant %s is declared in coordinator.go but this test does not know it -- a member added to the vocabulary must be classified here too, not silently skipped", name)
			continue
		}
		if stats.recorderFor(outcome) == nil {
			t.Errorf("recorderFor(%q) is nil -- every member of the closed vocabulary must name the counter it commits to, or an organization in that bucket reaches no verdict at all", outcome)
		}
	}

	// The value that is NOT a member: the zero value. A default arm would
	// have given it a bucket; the allow-list must refuse it.
	if stats.recorderFor(orgOutcome("")) != nil {
		t.Error("recorderFor(\"\") returned a recorder -- the zero value is not a member of the vocabulary and must not be bucketed as one")
	}
	if stats.recorderFor(orgOutcome("not_a_member")) != nil {
		t.Error("recorderFor of an unknown value returned a recorder -- the switch has grown a default arm, which is what lets the next member be misfiled silently")
	}
}

// orgOutcomeByConstName ties the source's const NAMES to their values so the
// walk above cannot pass by simply not knowing about a member.
var orgOutcomeByConstName = map[string]orgOutcome{
	"orgOutcomeOK":              orgOutcomeOK,
	"orgOutcomeRebuildRequired": orgOutcomeRebuildRequired,
	"orgOutcomeBackoff":         orgOutcomeBackoff,
	"orgOutcomeSourceFailed":    orgOutcomeSourceFailed,
	"orgOutcomePairFailed":      orgOutcomePairFailed,
	"orgOutcomeUnevaluated":     orgOutcomeUnevaluated,
}

func declaredOrgOutcomes(t *testing.T, filename string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typeName, ok := spec.Type.(*ast.Ident)
		if !ok || typeName.Name != "orgOutcome" {
			return true
		}
		for _, name := range spec.Names {
			names = append(names, name.Name)
		}
		return true
	})
	return names
}

// TestTheOrgBucketIsChosenInExactlyOnePlace is the structural half of the
// shared-classifier fix.
//
// The bucket decision used to be carried by hand in runOrgLegacy,
// runOrgLifecycle and runBuildTick. Three copies of one ladder lost
// sourceFailed once and cost three review findings after it -- the last being
// that the build copy could not reach orgs_source_failed at all, so a required
// source failing through a live build reported orgs_backoff:1 forever.
//
// Fixing the build copy would have left three copies to drift again. This pin
// asserts the property the fix actually established: the bucket recorders are
// named in exactly ONE function, recorderFor, so a fourth path cannot grow a
// fourth ladder.
func TestTheOrgBucketIsChosenInExactlyOnePlace(t *testing.T) {
	t.Parallel()
	buckets := map[string]bool{
		"recordOK": true, "recordRebuildRequired": true, "recordBackoff": true,
		"recordSourceFailedOrg": true, "recordPairFailedOrg": true,
		"recordUnevaluated": true,
	}
	inside, outside := bucketReferencesByFunc(t, "coordinator.go", buckets)
	if inside == 0 {
		t.Fatal("recorderFor names no bucket recorder at all -- the walk matched nothing, so its zero proves nothing")
	}
	if len(outside) != 0 {
		t.Errorf("bucket recorders are named outside recorderFor, in %v -- every path must hand orgOutcomeOf what it OBSERVED and let one function choose the bucket, or the ladder drifts per path again", outside)
	}

	// The three paths must hand their observations to the scope. A path that
	// stopped doing so would satisfy the assertion above by recording
	// nothing at all.
	for _, fn := range []string{"runOrgLegacy", "runOrgLifecycle", "runBuildTick"} {
		if !callsNamed(t, "coordinator.go", fn, "recordOutcome") && !callsNamedOn(t, "coordinator.go", fn, "recordOutcome") {
			t.Errorf("%s does not call scope.recordOutcome -- it is deciding its own bucket, or recording none", fn)
		}
	}

	// And the ladder itself runs in exactly ONE place: finish(). A path that
	// resolved it at its own last line would be answering "was the tick
	// cancelled" at a moment that is not the moment the verdict is
	// committed.
	ladderCallers := functionsCalling(t, "coordinator.go", "orgOutcomeOf")
	if len(ladderCallers) != 1 || ladderCallers[0] != "finish" {
		t.Errorf("orgOutcomeOf is called from %v, want exactly [finish] -- truncation is a member of the vocabulary now, so the ladder has to run at commit time", ladderCallers)
	}

	// Negative controls over the AST.
	for _, tc := range []struct {
		name          string
		src           string
		wantIn        int
		wantOutsideNo int
	}{
		{
			name:   "a reference inside recorderFor is the allowed shape",
			src:    "package projectionrun\n\nfunc (s *tickFreshnessStats) recorderFor(o orgOutcome) func() {\n\treturn s.recordOK\n}\n",
			wantIn: 1, wantOutsideNo: 0,
		},
		{
			name:   "a reference anywhere else is a violation",
			src:    "package projectionrun\n\nfunc (s *tickFreshnessStats) recorderFor(o orgOutcome) func() {\n\treturn s.recordOK\n}\n\nfunc other(s *tickFreshnessStats) {\n\t_ = s.recordBackoff\n}\n",
			wantIn: 1, wantOutsideNo: 1,
		},
		{
			name:   "a commented-out reference is not a reference",
			src:    "package projectionrun\n\nfunc (s *tickFreshnessStats) recorderFor(o orgOutcome) func() {\n\treturn s.recordOK\n}\n\nfunc other(s *tickFreshnessStats) {\n\t// _ = s.recordBackoff\n\t_ = s\n}\n",
			wantIn: 1, wantOutsideNo: 0,
		},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "control.go", tc.src, 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		gotIn, gotOut := bucketReferencesIn(file, buckets)
		if gotIn != tc.wantIn || len(gotOut) != tc.wantOutsideNo {
			t.Errorf("control %q: inside=%d outside=%d, want inside=%d outside=%d -- the walk is matching text, not the AST", tc.name, gotIn, len(gotOut), tc.wantIn, tc.wantOutsideNo)
		}
	}
}

// bucketReferencesByFunc counts every mention of a bucket recorder -- call or
// bare reference alike -- and reports which functions other than recorderFor
// name one.
//
// A BARE reference counts here, unlike in the finalizer pin next door: handing
// scope.record a recorder value is exactly how a path would smuggle its own
// bucket choice back in.
func bucketReferencesByFunc(t *testing.T, filename string, buckets map[string]bool) (inside int, outside []string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return bucketReferencesIn(file, buckets)
}

func bucketReferencesIn(file *ast.File, buckets map[string]bool) (inside int, outside []string) {
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		hits := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !buckets[sel.Sel.Name] {
				return true
			}
			// Coordinator has its OWN recordBackoff(key, err) -- the per-pair
			// retry scheduler, a different method that merely shares a name.
			// Discriminated by receiver, exactly as the finalizer pin does.
			recv, ok := sel.X.(*ast.Ident)
			if !ok || (recv.Name != "s" && recv.Name != "stats") {
				return true
			}
			hits++
			return true
		})
		if hits == 0 {
			continue
		}
		if fn.Name.Name == "recorderFor" {
			inside += hits
			continue
		}
		if !seen[fn.Name.Name] {
			seen[fn.Name.Name] = true
			outside = append(outside, fn.Name.Name)
		}
	}
	return inside, outside
}

func callsNamed(t *testing.T, filename, fnName, callee string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == callee {
				found = true
			}
			return true
		})
	}
	return found
}

// TestOrgOutcomeOfPrecedence pins the ladder itself, as a table over every
// combination of the four signals for BOTH healthy buckets.
//
// The expected values come from an independent table written here, never from
// orgOutcomeOf itself: an expectation computed by the function under test is
// true of any mapping and cannot fail.
func TestOrgOutcomeOfPrecedence(t *testing.T) {
	t.Parallel()
	for _, healthy := range []orgOutcome{orgOutcomeOK, orgOutcomeBackoff} {
		for _, evaluated := range []bool{false, true} {
			for _, stale := range []bool{false, true} {
				for _, sourceFailed := range []bool{false, true} {
					for _, pairBroke := range []bool{false, true} {
						for _, truncated := range []bool{false, true} {
							signals := orgSignals{
								evaluated: evaluated, stale: stale,
								sourceFailed: sourceFailed, pairBroke: pairBroke,
								truncated: truncated, healthy: healthy,
							}
							want := expectedOutcome(signals)
							if got := orgOutcomeOf(signals); got != want {
								t.Errorf("orgOutcomeOf(%+v) = %q, want %q", signals, got, want)
							}
						}
					}
				}
			}
		}
	}

	// The two amendment properties, stated on their own so they cannot be
	// lost inside the table.
	//
	// An established fact outranks truncation: a source that failed before
	// the cancellation arrived keeps its bucket.
	established := orgSignals{evaluated: true, sourceFailed: true, truncated: true, healthy: orgOutcomeOK}
	if got := orgOutcomeOf(established); got != orgOutcomeSourceFailed {
		t.Errorf("a source failure under a truncated tick = %q, want %q -- a cancellation arriving afterwards does not un-observe it", got, orgOutcomeSourceFailed)
	}
	// Truncation outranks the readings: a cancelled build is unevaluated,
	// never backoff, which an operator reads as "building, nothing wrong".
	cutShort := orgSignals{truncated: true, stale: true, healthy: orgOutcomeBackoff}
	if got := orgOutcomeOf(cutShort); got != orgOutcomeUnevaluated {
		t.Errorf("a truncated evaluation = %q, want %q -- backoff and stale are claims about a tick that finished looking", got, orgOutcomeUnevaluated)
	}

	// The property the build path depends on, stated on its own so it cannot
	// be lost in the table: a failing source reaches the source bucket even
	// when the healthy bucket is backoff. That combination is exactly what
	// the build path reports, and what read orgs_backoff:1 orgs_source_failed:0
	// for as long as the three ladders were maintained by hand.
	building := orgSignals{evaluated: true, sourceFailed: true, healthy: orgOutcomeBackoff}
	if got := orgOutcomeOf(building); got != orgOutcomeSourceFailed {
		t.Errorf("a building organization with a failing required source = %q, want %q -- \"still building\" must not absorb \"a required source is down\"", got, orgOutcomeSourceFailed)
	}
}

// expectedOutcome is the ladder written out independently of the code under
// test, so a change to the precedence has to be made in two places on purpose
// rather than in one by accident.
func expectedOutcome(s orgSignals) orgOutcome {
	if s.sourceFailed {
		return orgOutcomeSourceFailed
	}
	if s.pairBroke {
		return orgOutcomePairFailed
	}
	if s.truncated {
		return orgOutcomeUnevaluated
	}
	if !s.evaluated {
		return orgOutcomeBackoff
	}
	if s.stale {
		return orgOutcomeRebuildRequired
	}
	return s.healthy
}

// callsNamedOn is callsNamed for a method call on a receiver
// (scope.recordOutcome), which callsNamed's bare-identifier match cannot see.
func callsNamedOn(t *testing.T, filename, fnName, callee string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == callee {
				found = true
			}
			return true
		})
	}
	return found
}

// functionsCalling names every function in a file that calls callee, so "in
// exactly one place" is an assertion rather than a convention.
func functionsCalling(t *testing.T, filename, callee string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var callers []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		hit := false
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == callee {
				hit = true
			}
			return true
		})
		if hit {
			callers = append(callers, fn.Name.Name)
		}
	}
	return callers
}
