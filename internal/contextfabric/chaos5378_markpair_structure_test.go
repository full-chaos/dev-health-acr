package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTheCauseHandedToMarkPairIsNeverAlreadyWrapped is the half of the
// markPair fix that a behavioural test cannot reach.
//
// markPair now builds the wrapper itself, so a caller CANNOT wrap before the
// identity check runs -- unless it wraps the argument it hands over as the
// cause, which puts the defect back exactly where it was: the check would run
// against a *fmt.wrapError and report PropagatedCancellation false for a bare
// context sentinel, at every stage, silently, while passing every test that
// does not drive a cancellation through that specific stage. That is how the
// original survived eleven rounds and 58 of 58 mutants.
//
// So the cause argument is pinned structurally: it may be an identifier, a
// selector, or a locally constructed error (errors.New / a package sentinel),
// and never a call to fmt.Errorf.
func TestTheCauseHandedToMarkPairIsNeverAlreadyWrapped(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "projector.go", nil, 0)
	if err != nil {
		t.Fatalf("parse projector.go: %v", err)
	}
	calls, wrapped := markPairCauseShapes(file)

	// Vacuity guard: a walk that matches nothing passes trivially, and this
	// file is expected to carry every marking site in the worker.
	if calls == 0 {
		t.Fatal("no markPair/markPairSentinel calls found in projector.go -- the walk matched nothing, so its zero proves nothing")
	}
	if len(wrapped) != 0 {
		t.Errorf("%d markPair call(s) in projector.go hand over an ALREADY-WRAPPED cause: %v -- the identity check then runs against the wrapper and can never see a bare context sentinel, which is the defect this signature exists to make unexpressible", len(wrapped), wrapped)
	}

	// Negative controls both directions, over the AST rather than the text.
	for _, tc := range []struct {
		name  string
		src   string
		calls int
		want  int
	}{
		{
			name:  "the shape that is required",
			src:   "package contextfabric\n\nfunc f(err error) error {\n\treturn markPair(PairStageApply, err, \"apply projection batch\")\n}\n",
			calls: 1, want: 0,
		},
		{
			name:  "the shape the fix removes",
			src:   "package contextfabric\n\nfunc f(err error) error {\n\treturn markPair(PairStageApply, fmt.Errorf(\"apply projection batch: %w\", err), \"\")\n}\n",
			calls: 1, want: 1,
		},
		{
			name:  "a sentinel marking is not a wrap",
			src:   "package contextfabric\n\nfunc f() error {\n\treturn markPairSentinel(PairStageApply, ErrProjectionConflict, \"backend receipt does not match batch\")\n}\n",
			calls: 1, want: 0,
		},
		{
			name:  "a comment carrying the literal does not count",
			src:   "package contextfabric\n\nfunc f(err error) error {\n\t// markPair(PairStageApply, fmt.Errorf(\"x: %w\", err), \"\")\n\treturn markPair(PairStageApply, err, \"\")\n}\n",
			calls: 1, want: 0,
		},
	} {
		control, perr := parser.ParseFile(token.NewFileSet(), "control.go", tc.src, 0)
		if perr != nil {
			t.Fatalf("%s: parse: %v", tc.name, perr)
		}
		gotCalls, gotWrapped := markPairCauseShapes(control)
		if gotCalls != tc.calls || len(gotWrapped) != tc.want {
			t.Errorf("control %q: calls=%d wrapped=%d, want calls=%d wrapped=%d -- the walk is matching text, not the AST", tc.name, gotCalls, len(gotWrapped), tc.calls, tc.want)
		}
	}
}

// markPairCauseShapes returns how many marking calls a file makes and which of
// them pass a fmt.Errorf expression as the cause (the second argument of both
// constructors).
func markPairCauseShapes(file *ast.File) (calls int, wrapped []string) {
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || (name.Name != "markPair" && name.Name != "markPairSentinel") {
			return true
		}
		calls++
		if len(call.Args) < 2 {
			// Not a shape this pin knows how to read. An unrecognised shape
			// is a FAILURE of the pin, never a skip: it is reported as a
			// violation so it cannot pass unexamined.
			wrapped = append(wrapped, name.Name+"(<fewer than 2 arguments>)")
			return true
		}
		inner, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "fmt" && sel.Sel.Name == "Errorf" {
			wrapped = append(wrapped, name.Name)
		}
		return true
	})
	return calls, wrapped
}

// TestABareContextSentinelIsPropagatedAtEveryStage drives the classification
// itself, at every stage the worker can mark, rather than trusting that the
// signature change reached them all.
//
// The behavioural pins in projectionrun prove this through the coordinator;
// this one proves it at the unit the invariant is actually about, and covers
// the stages a coordinator fixture would have to contort to reach.
func TestABareContextSentinelIsPropagatedAtEveryStage(t *testing.T) {
	t.Parallel()
	stages := []PairStage{
		PairStageValidation, PairStageCheckpointLoad, PairStageSourceRead,
		PairStageProgressCAS, PairStageClaimCAS, PairStageApply, PairStageFinalCAS,
	}
	for _, stage := range stages {
		for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
			// Bare: the tick's cancellation passing through a stage that
			// added nothing of its own.
			marked := markPair(stage, sentinel, "")
			var pairErr *PairRunError
			if !errors.As(marked, &pairErr) {
				t.Fatalf("stage %s: markPair did not produce a *PairRunError", stage)
			}
			if !pairErr.PropagatedCancellation {
				t.Errorf("stage %s: a BARE %v marked PropagatedCancellation=false -- the tick's own cancellation reads as a failure at this stage", stage, sentinel)
			}

			// Described: markPair builds the wrapper, and the classification
			// must be unchanged by it. This is the arm that would have caught
			// the original defect: before the signature change the caller
			// built this wrapper and the answer flipped.
			described := markPair(stage, sentinel, "load projection checkpoint")
			if !errors.As(described, &pairErr) {
				t.Fatalf("stage %s: markPair with a description did not produce a *PairRunError", stage)
			}
			if !pairErr.PropagatedCancellation {
				t.Errorf("stage %s: a bare %v marked PropagatedCancellation=false once markPair wrapped it -- wrapping is supposed to happen AFTER the check", stage, sentinel)
			}
			if !errors.Is(described, sentinel) {
				t.Errorf("stage %s: errors.Is against %v broke -- the wrapper must keep %%w semantics", stage, sentinel)
			}
			if want := "load projection checkpoint: " + sentinel.Error(); described.Error() != want {
				t.Errorf("stage %s: message = %q, want %q -- the wrapper markPair builds must be byte-identical to the one the call sites used to build", stage, described.Error(), want)
			}

			// A source that WRAPPED a context error added its own account of
			// its own failure, and that is not a propagated cancellation.
			// Without this arm the fix could satisfy everything above by
			// answering true for anything that errors.Is a context sentinel,
			// which is precisely the test the original comment rejects.
			own := markPair(stage, fmt.Errorf("teams projects read: %w", sentinel), "")
			if !errors.As(own, &pairErr) {
				t.Fatalf("stage %s: markPair did not produce a *PairRunError", stage)
			}
			if pairErr.PropagatedCancellation {
				t.Errorf("stage %s: a source-WRAPPED %v marked PropagatedCancellation=true -- the source described its own failure and that description is what an operator needs", stage, sentinel)
			}
		}

		// A sentinel this package constructed is never the tick's
		// cancellation passing through.
		marked := markPairSentinel(stage, ErrProjectionConflict, "backend receipt does not match batch")
		var pairErr *PairRunError
		if !errors.As(marked, &pairErr) {
			t.Fatalf("stage %s: markPairSentinel did not produce a *PairRunError", stage)
		}
		if pairErr.PropagatedCancellation {
			t.Errorf("stage %s: a package sentinel marked PropagatedCancellation=true", stage)
		}
		if !errors.Is(marked, ErrProjectionConflict) {
			t.Errorf("stage %s: errors.Is against the sentinel broke", stage)
		}
		if want := ErrProjectionConflict.Error() + ": backend receipt does not match batch"; marked.Error() != want {
			t.Errorf("stage %s: message = %q, want %q -- the suffix shape must stay byte-identical", stage, marked.Error(), want)
		}
	}

	if markPair(PairStageApply, nil, "apply projection batch") != nil {
		t.Error("markPair(nil) returned non-nil -- a nil cause must stay nil, or every success path becomes a failure")
	}
	if markPairSentinel(PairStageApply, nil, "detail") != nil {
		t.Error("markPairSentinel(nil) returned non-nil")
	}
}
