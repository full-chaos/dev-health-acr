package contextfabric

import (
	"bytes"
	"context"
	"go/ast"
	"go/printer"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The FIFTH site of this branch's recurring defect class -- one decision
// described by two documents -- and the second one in the allocator itself.
//
// Keystone review #3 found that stage three VALIDATED an allocation it had
// re-derived rather than the one synthesis CONSUMED. The fix (91408cc1)
// derived the first-pass allocation once, at params construction, and carried
// it on `synthesisAssemblyParams.Allocation`.
//
// That fix was INCOMPLETE, and keystone review #4 found the half it missed.
// `forRetry` copies the graph, the facts, the resolution and the cohort -- and
// says nothing about `Allocation`, so the first pass's allocation rides through
// unchanged on `retry := p`. The retry's synthesis therefore SPENDS the
// allocation written for the UN-NARROWED cohort, while stage three derives the
// narrowed cohort's allocation AFTER synthesis and MEASURES against that one.
// Consumed and validated are two different documents again, one level down.
//
// It needs no fault injection to matter: the retried answer is produced under
// grants for a member and group population it no longer has, so its narration
// budget is wrong and the served content changes.
//
// WHY THIS TEST TAKES THE SHAPE IT DOES. A test that corrupts `input.Allocation`
// inside a synthesizer wrapper cannot pin anything here -- `SynthesisInput` is
// passed BY VALUE, so the corruption lands on the synthesizer's own copy and
// fails identically before and after a fix. That dead end is already recorded
// on the first-pass pin. This test therefore only OBSERVES what each synthesis
// call was handed, which a by-value copy carries faithfully, and asserts a
// relation between the two calls.
//
// THE PROPERTY, and it needs no plan of its own: `AllocateItems` is a function
// of the member and group counts, so a cohort that genuinely narrowed MUST
// yield a different allocation. If the two synthesis calls were handed equal
// allocations across a narrowing, the second one was not derived for the cohort
// it was given.

// recordedSynthesis is what one synthesis call was handed. The Allocation and
// the cohort sizes are read off the input rather than reconstructed, because
// the question is what the producer actually spent, not what it should have.
type recordedSynthesis struct {
	allocation ItemAllocation
	members    int
	groups     int
}

// recordingSynthesizerCalls wraps a synthesizer and records every call's
// allocation and cohort shape, passing the call through untouched.
func recordingSynthesizerCalls(inner AnswerSynthesizer, into *[]recordedSynthesis) AnswerSynthesizer {
	return synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		*into = append(*into, recordedSynthesis{
			allocation: input.Allocation,
			members:    cohortMemberCount(input.Graph.Cohort),
			groups:     groupCountOf(input.Graph.Cohort),
		})
		return inner.Synthesize(ctx, principal, input)
	})
}

// TestTheRetryIsSynthesizedUnderItsOwnCohortsAllocation is the behavioural half.
func TestTheRetryIsSynthesizedUnderItsOwnCohortsAllocation(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// The same fixture path 3 uses to reach a retry that actually RUNS: an
	// overlapping grouped cohort that overruns at maxItems=20, with a reserve
	// large enough that the retry is admitted rather than declined.
	engine := budgetStageEngine(t, chaos4809OverlappingGroupedCohort(), 20, budgetStageOptions(20, time.Second), &calls, telemetry)
	var seen []recordedSynthesis
	engine.synthesizer = recordingSynthesizerCalls(engine.synthesizer, &seen)

	// The error is not the subject here: this path may serve or refuse
	// depending on whether the narrowed answer fits. What matters is what the
	// SECOND synthesis was handed.
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if len(seen) != 2 {
		t.Fatalf("synthesis ran %d time(s), want 2 -- the retry must actually RUN or this test asserts nothing", len(seen))
	}
	first, retry := seen[0], seen[1]

	// CONTROL. If the cohort did not narrow, the two allocations SHOULD be
	// equal and the assertion below would be vacuous -- it would pass on the
	// broken code for the wrong reason. Fail loudly instead of asserting
	// nothing.
	if retry.members == first.members && retry.groups == first.groups {
		t.Fatalf("the cohort did not narrow (members %d->%d, groups %d->%d): this fixture cannot "+
			"distinguish a retry allocated for its own cohort from one that inherited the first "+
			"pass's, so the assertion below would be vacuous",
			first.members, retry.members, first.groups, retry.groups)
	}

	// THE DEFECT. AllocateItems is a function of the group and member counts,
	// so across a genuine narrowing the retry's allocation cannot legitimately
	// equal the first pass's.
	if retry.allocation == first.allocation {
		t.Errorf("the retry was synthesized under the FIRST pass's allocation.\n"+
			"  first pass: %d member(s), %d group(s), grants %v\n"+
			"  retry     : %d member(s), %d group(s), grants %v\n"+
			"The retried document is produced under grants written for a member and group "+
			"population it no longer has, and stage three then measures it against a THIRD "+
			"allocation derived after synthesis. Consumed and validated must be one document.",
			first.members, first.groups, first.allocation.Grants,
			retry.members, retry.groups, retry.allocation.Grants)
	}

	// The retry's own allocation must still be internally coherent -- a
	// different allocation that does not agree with its own formula would be a
	// different defect wearing this one's clothes.
	if got := retry.allocation.Agreement(); got != AllocationAgrees {
		t.Errorf("the retry's allocation does not agree with its own formula: %q", got)
	}
}

// TestTheConsumedAllocationIsTheReturnedOne is the structural half, and it is
// read off the AST rather than the file text.
//
// THE PREVIOUS VERSION OF THIS PIN WAS VACUOUS, and it is worth saying how so
// nobody rebuilds it that way. It asserted `strings.Contains(src,
// "retry.Allocation = allocation")`. Comment that assignment out and the literal
// SURVIVES IN THE COMMENT: the tree is broken, the pin passes. Proven on a clean
// tree at 542e6acb. That is rule :481's trap -- an asserted literal living in
// prose next to the thing that checks for it -- with the prose being the
// disabled code itself. A text search cannot tell live code from a comment, and
// only a parse can.
//
// THE INVARIANT (team-lead, 2026-09-06): the allocation a pass CONSUMED is the
// value the producer RETURNS; the guard measures the returned value; no consumer
// holds a private copy the guard cannot see. Three previous fixes each closed an
// instance and left the class open by stopping at derive-once -- which still
// permits a producer-local alias. `ItemAllocation` is a value type and `params`
// is by-value, so the return is the ONLY formulation that survives a function
// boundary.
func TestTheConsumedAllocationIsTheReturnedOne(t *testing.T) {
	t.Parallel()
	_, files := parsePackageForQuantifier(t)

	producer := findFuncDecl(t, files, "synthesizeAndAssemble")
	results := producer.Type.Results
	if results == nil {
		t.Fatal("synthesizeAndAssemble returns nothing; this pin is stale")
	}
	var returnsAllocation bool
	for _, field := range results.List {
		if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "ItemAllocation" {
			returnsAllocation = true
		}
	}
	if !returnsAllocation {
		t.Error("synthesizeAndAssemble does not return an ItemAllocation: the allocation it consumed " +
			"is invisible to its callers, so no guard can measure what was actually spent")
	}

	// forRetry must ASSIGN its parameter -- a real assignment node, so a
	// commented-out one is simply absent from the tree.
	forRetry := findFuncDecl(t, files, "forRetry")
	if !hasParam(forRetry, "allocation", "ItemAllocation") {
		t.Error("forRetry does not take the allocation as a parameter: a retry can inherit the first " +
			"pass's grants by silent struct copy")
	}
	if !assignsField(forRetry, "retry", "Allocation") {
		t.Error("forRetry contains no assignment to retry.Allocation: the parameter is decorative and " +
			"the first pass's allocation still rides through on the struct copy")
	}

	// THE THIRD LEG: what the producer RETURNS must be what its consumers SPENT.
	//
	// Without this, `return result, params.Allocation, pending, nil` compiles,
	// reads as correct, and silently restores the whole defect: narration and
	// synthesis spend `synthesisAllocation` while the guard measures a pristine
	// copy that no producer-local fault can ever touch. The invariant needs all
	// three legs -- one binding, every consumer reads it, and the return is that
	// same identifier -- and only the third one makes the other two observable
	// outside the function.
	// QUANTIFIER, and the first version of this block got it backwards.
	//
	// It asked "is each RETURNED identifier present among the consumers?" —
	// an EXISTENTIAL — while the comment above it stated the universal the
	// invariant actually needs. Those differ exactly when the consumers
	// DISAGREE WITH EACH OTHER: point narration at `params.Allocation` while
	// synthesis keeps `synthesisAllocation`, and the returned identifier is
	// still findable among the consumers, so the check passed on a tree where
	// the two consumers spend different objects. Keystone #6 found it by
	// executing exactly that mutation.
	//
	// A comment stating the property while the code asserts something weaker is
	// this branch's defect class living in its own pin, which is why the fix is
	// the quantifier and not another special case.
	//
	// Both halves are required. Collapsing the RETURNED set to one identifier
	// alone would still permit two return paths handing back different locals;
	// requiring every consumer to match without it would not notice that.
	consumers, returned := producerAllocationIdents(t, files)
	distinctReturned := distinctIdents(returned)
	if len(distinctReturned) != 1 {
		t.Fatalf("synthesizeAndAssemble returns %d distinct allocation identifiers (%v), want exactly 1: "+
			"two return paths handing back different objects means the caller's guard measures whichever "+
			"path happened to run", len(distinctReturned), distinctReturned)
	}
	consumed := distinctReturned[0]
	for _, c := range consumers {
		if c != consumed {
			t.Errorf("a consumer inside synthesizeAndAssemble spends %q while the producer returns %q "+
				"(consumers seen: %v). EVERY consumer must spend the returned identifier: if one of them "+
				"spends a different object, a fault applied to it is invisible to the guard, which is the "+
				"whole defect this seam exists to prevent.", c, consumed, consumers)
		}
	}

	// THE FIRST PASS's binding, which the checks below do not reach.
	//
	// fitAssembledResult receives the first pass's consumed allocation as a
	// PARAMETER, so it is not bound from a producer call inside this function
	// and the producer-binding walk cannot see it. Assert directly that the
	// value the first-pass guard measures comes from that parameter and not
	// from params: `allocation := params.Allocation` is exactly the pre-#5
	// shape, it compiles, and nothing else here would catch it.
	if src := firstPassAllocationSource(t, files); src != "consumed" {
		t.Errorf("the first-pass guard binds its allocation from %q, want the `consumed` parameter -- "+
			"binding it from params reads the caller's own copy, which no producer-local fault can "+
			"ever reach", src)
	}

	// A GUARD ARGUMENT IS ONLY AS GOOD AS WHAT THE IDENTIFIER STILL HOLDS.
	//
	// Asserting the argument's spelling is not enough, and a negative control
	// proved it: rebinding `consumedRetryAllocation = retryParams.Allocation`
	// immediately after the producer call leaves every call site reading the
	// same name while silently restoring the defect, and the shape check below
	// passed on that tree. So the binding must also never be REASSIGNED --
	// a value returned as consumed and then overwritten is not the consumed
	// value any more.
	for _, fn := range []string{"fitAssembledResult"} {
		for _, name := range allocationsBoundFromProducer(t, files, fn) {
			if reassigns(t, files, fn, name) {
				t.Errorf("%s reassigns %q after binding it from synthesizeAndAssemble: the guard would "+
					"measure whatever it was overwritten with, not what the producer consumed", fn, name)
			}
		}
	}

	// Both guards must measure a RETURNED value, never a params field. The
	// argument is checked by identifier, so `retryParams.Allocation` (a
	// selector on the caller's own copy) fails while a plain identifier bound
	// from the producer's return passes.
	for _, site := range []struct{ fn, stage string }{
		{"fitAssembledResult", `"assembled_result"`},
		{"fitAssembledResult", `"re_synthesized_result"`},
	} {
		if arg := measureArgFor(t, files, site.fn, site.stage); arg == "" {
			t.Errorf("no measureAssembledAttempt call for stage %s in %s; this pin is stale", site.stage, site.fn)
		} else if strings.Contains(arg, ".Allocation") {
			t.Errorf("the %s guard measures %s -- a field of the caller's own copy. `params` is passed "+
				"BY VALUE, so a fault applied inside the producer can never reach it; the guard must "+
				"measure the value the producer RETURNED as consumed.", site.stage, arg)
		}
	}
}

// findFuncDecl returns the named function or method, failing if it is absent or
// ambiguous -- a pin that silently found nothing would assert nothing.
func findFuncDecl(t *testing.T, files []*ast.File, name string) *ast.FuncDecl {
	t.Helper()
	var found []*ast.FuncDecl
	for _, f := range files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
				found = append(found, fn)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d declarations of %q, want exactly 1", len(found), name)
	}
	return found[0]
}

func hasParam(fn *ast.FuncDecl, name, typeName string) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		ident, ok := field.Type.(*ast.Ident)
		if !ok || ident.Name != typeName {
			continue
		}
		for _, n := range field.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// assignsField reports whether the body contains a real assignment to
// <receiver>.<field>. An assignment inside a comment is not in the AST at all,
// which is the entire reason this is a parse and not a grep.
// assignsField reports whether the body contains a REACHABLE assignment to
// <receiver>.<field>.
//
// PRESENCE IS NOT REACHABILITY, and this pin asserted only presence. A keystone
// review disabled the assignment by burying it in a statically dead branch:
//
//	if false { retry.Allocation = allocation }
//
// That compiles, the assignment node is still in the tree, and the pin passed.
// The mutation is caught overall — the behavioural pin
// TestTheRetryIsSynthesizedUnderItsOwnCohortsAllocation fails on it, so no tree
// could ship with the assignment disabled — but the STRUCTURAL pin is the one
// that names the property, and a pin that passes on a tree it should fail is
// worth nothing as documentation of that property.
//
// Statically-dead detection is deliberately narrow: a constant `false` (or a
// parenthesised one) as an `if` condition. That is what a disabling mutation
// looks like, and it is decidable by inspection. Anything requiring real
// constant folding is out of scope — a pin that pretends to evaluate arbitrary
// conditions would make a promise it cannot keep, which is the failure this
// whole family of fixes exists to remove.
func assignsField(fn *ast.FuncDecl, receiver, field string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if ifStmt, ok := n.(*ast.IfStmt); ok && isStaticallyFalse(ifStmt.Cond) {
			// Walk the else branch (still reachable), skip the dead body.
			if ifStmt.Else != nil {
				ast.Inspect(ifStmt.Else, func(inner ast.Node) bool {
					if assign, ok := inner.(*ast.AssignStmt); ok && assignsTo(assign, receiver, field) {
						found = true
					}
					return true
				})
			}
			return false
		}
		// No depth counter is needed: returning false above PRUNES the dead
		// branch's subtree, so nothing inside it reaches this line. A
		// `deadDepth == 0` guard here would be a condition that can never be
		// false — a guard that cannot fire, which is the defect this pin's own
		// PR spent seven rounds removing.
		if assign, ok := n.(*ast.AssignStmt); ok && assignsTo(assign, receiver, field) {
			found = true
		}
		return true
	})
	return found
}

// isStaticallyFalse reports whether an expression is the literal `false`,
// possibly parenthesised.
func isStaticallyFalse(expr ast.Expr) bool {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "false"
}

// assignsTo reports whether an assignment writes <receiver>.<field>.
func assignsTo(assign *ast.AssignStmt, receiver, field string) bool {
	for _, lhs := range assign.Lhs {
		sel, ok := lhs.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != field {
			continue
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == receiver {
			return true
		}
	}
	return false
}

// measureArgFor returns the source form of the allocation argument passed to
// measureAssembledAttempt for the given stage literal.
func measureArgFor(t *testing.T, files []*ast.File, fnName, stageLit string) string {
	t.Helper()
	fn := findFuncDecl(t, files, fnName)
	out := ""
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "measureAssembledAttempt" || len(call.Args) < 5 {
			return true
		}
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Value != stageLit {
			return true
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), call.Args[3]); err != nil {
			t.Fatalf("print allocation argument: %v", err)
		}
		out = buf.String()
		return false
	})
	return out
}

// allocationsBoundFromProducer returns the identifiers bound to
// synthesizeAndAssemble's ItemAllocation result inside fnName. It fails if it
// finds none: a pin that quantifies over an empty set asserts nothing.
func allocationsBoundFromProducer(t *testing.T, files []*ast.File, fnName string) []string {
	t.Helper()
	fn := findFuncDecl(t, files, fnName)
	var names []string
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "synthesizeAndAssemble" {
			return true
		}
		// The allocation is the SECOND result, by the signature this file pins.
		if len(assign.Lhs) < 2 {
			return true
		}
		if ident, ok := assign.Lhs[1].(*ast.Ident); ok && ident.Name != "_" {
			names = append(names, ident.Name)
		}
		return true
	})
	if len(names) == 0 {
		t.Fatalf("%s binds no allocation from synthesizeAndAssemble; this pin is stale and would "+
			"quantify over an empty set", fnName)
	}
	return names
}

// reassigns reports whether name appears on the left of any assignment that is
// not the binding itself.
func reassigns(t *testing.T, files []*ast.File, fnName, name string) bool {
	t.Helper()
	fn := findFuncDecl(t, files, fnName)
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		// The := that binds it from the producer is the binding, not a rebind.
		if len(assign.Rhs) == 1 {
			if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "synthesizeAndAssemble" {
					return true
				}
			}
		}
		for _, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok && ident.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// firstPassAllocationSource returns the source expression the first-pass guard's
// `allocation` is bound from inside fitAssembledResult.
func firstPassAllocationSource(t *testing.T, files []*ast.File) string {
	t.Helper()
	fn := findFuncDecl(t, files, "fitAssembledResult")
	out := ""
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != "allocation" {
			return true
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), assign.Rhs[0]); err != nil {
			t.Fatalf("print allocation source: %v", err)
		}
		out = buf.String()
		return false
	})
	if out == "" {
		t.Fatal("fitAssembledResult binds no `allocation`; this pin is stale and asserts nothing")
	}
	return out
}

// producerAllocationIdents returns (identifiers passed as the allocation to
// synthesis and narration, identifiers returned in the ItemAllocation position).
func producerAllocationIdents(t *testing.T, files []*ast.File) ([]string, []string) {
	t.Helper()
	fn := findFuncDecl(t, files, "synthesizeAndAssemble")
	var consumers, returned []string
	ast.Inspect(fn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			// SynthesisInput{Allocation: X, ...}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Allocation" {
					consumers = append(consumers, exprSource(t, kv.Value))
				}
			}
		case *ast.CallExpr:
			if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "narrateCohortDriverJudgments" && len(node.Args) > 0 {
				consumers = append(consumers, exprSource(t, node.Args[len(node.Args)-1]))
			}
		case *ast.ReturnStmt:
			// The allocation is the SECOND result.
			if len(node.Results) >= 2 {
				if ident, ok := node.Results[1].(*ast.Ident); ok {
					returned = append(returned, ident.Name)
				} else {
					var buf bytes.Buffer
					if err := printer.Fprint(&buf, token.NewFileSet(), node.Results[1]); err == nil {
						returned = append(returned, buf.String())
					}
				}
			}
		}
		return true
	})
	if len(consumers) == 0 {
		t.Fatal("found no allocation consumer inside synthesizeAndAssemble; this pin is stale")
	}
	if len(returned) == 0 {
		t.Fatal("found no allocation return inside synthesizeAndAssemble; this pin is stale")
	}
	return consumers, returned
}

// exprSource renders any expression as its source text.
//
// EVERY consumer is recorded, whatever its shape — and that is the point.
// The first version of this walk only recorded a consumer when the argument was
// a bare `*ast.Ident`, so when keystone #6 pointed narration at
// `params.Allocation` — a SelectorExpr — the mutated consumer was SILENTLY
// DROPPED from the list rather than recorded as a mismatch. The check then saw
// one consumer, agreed with itself, and passed. A checker that skips what it
// cannot parse reports "all consumers agree" when it has simply not looked at
// the disagreeing one, which is the same silent-skip defect as a battery that
// quietly drops an arm.
func exprSource(t *testing.T, expr ast.Expr) string {
	t.Helper()
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		t.Fatalf("print consumer expression: %v", err)
	}
	return buf.String()
}

// distinctIdents collapses the identifier list, preserving first-seen order so
// the failure message reads in source order.
func distinctIdents(idents []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range idents {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// containsIdent is deliberately GONE. It existed only for the existential form
// of the check above ("is the returned identifier somewhere among the
// consumers"), which is the weaker property that let a divergent consumer pass.
// The universal form needs no membership helper, and leaving the helper behind
// would invite the weaker check back.
