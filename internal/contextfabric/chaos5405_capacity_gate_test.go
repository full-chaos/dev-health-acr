package contextfabric

// CHAOS-5405 D-a -- the in-flight admission gate, and the driver enumeration
// that keeps every decision reason honest.
//
// WHY THE QUEUED CELL NEEDED ITS OWN PIN. Writing out the input shape space
// showed `capacity_timeout` was the one decision reason nothing exercised. That
// is the same position `authorization_error` and `axis_unsupported` were in
// earlier in this ticket -- a declared vocabulary member with no production
// driver on its real path, which is decoration rather than a signal, and which
// nothing notices until an operator alerts on it and gets silence.
//
// Three instances of that shape in one ticket is the reason for the second
// test here: rather than pin the reasons one at a time as they are noticed,
// enumerate them and require EVERY member to have a production driver.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// blockingExpander must never be reached: the gate is full and the context is
// already done, so the resolver has to refuse before calling it.
type blockingExpander struct{ calls int }

func (e *blockingExpander) ExpandFactScope(_ context.Context, _ FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	e.calls++
	return FactScopeExpansionResult{}, nil
}

// TestChaos5405_ADeadlineWhileQueuedIsACapacityTimeout is the queued cell.
//
// The slots are filled directly rather than by racing 32 goroutines: the
// property under test is what the resolver decides when it CANNOT get a slot,
// and a deterministic full gate tests exactly that without making the result
// depend on scheduler timing.
func TestChaos5405_ADeadlineWhileQueuedIsACapacityTimeout(t *testing.T) {
	t.Parallel()
	expander := &blockingExpander{}
	resolver := NewFactReadScopeResolver(expander)

	for i := 0; i < maxWorkItemScopeInFlight; i++ {
		select {
		case resolver.workItemSlots <- struct{}{}:
		default:
			t.Fatalf("could not fill slot %d of %d -- the gate is smaller than its own bound", i, maxWorkItemScopeInFlight)
		}
	}
	// One more must not fit. Without this the test would pass on a gate with
	// no bound at all.
	select {
	case resolver.workItemSlots <- struct{}{}:
		t.Fatalf("a %dth slot was admitted -- the gate is not bounded at maxWorkItemScopeInFlight", maxWorkItemScopeInFlight+1)
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scope := resolver.Resolve(ctx, storage.Principal{OrgID: "org_1"},
		newFactScopeResolveInput(scopeRequest(
			[]SubjectRef{scopeProject},
			[]FactRequirement{{Kind: FactStatus}},
			TemporalCurrent,
		)),
		map[FactKind]FactCapability{FactStatus: planCapability(FactStatus, "status", SubjectWorkItem)},
	)

	if expander.calls != 0 {
		t.Fatalf("the expander ran %d time(s) -- a caller that never got a slot must not reach it", expander.calls)
	}
	var seen bool
	for _, event := range scope.Events {
		if event.RequirementKind != FactStatus {
			continue
		}
		seen = true
		if event.Outcome != FactScopeFailed {
			t.Fatalf("outcome = %q, want %q", event.Outcome, FactScopeFailed)
		}
		if event.FailureClass != FactScopeFailureTimeout {
			t.Fatalf("failure_class = %q, want %q", event.FailureClass, FactScopeFailureTimeout)
		}
		if event.DecisionReason != FactScopeDecisionCapacityTimeout {
			t.Fatalf("decision_reason = %q, want %q -- 'we never started' and 'the query was slow' are different operational stories and must not share a reason",
				event.DecisionReason, FactScopeDecisionCapacityTimeout)
		}
		if event.AdmittedCount != 0 {
			t.Fatalf("admitted_count = %d, want 0", event.AdmittedCount)
		}
	}
	if !seen {
		t.Fatalf("no event recorded for the requirement")
	}
}

// TestChaos5405_EveryDecisionReasonHasAProductionDriver enumerates the closed
// vocabulary against the production source.
//
// A reason no production code can assign is one an operator will alert on and
// never see fire -- and this ticket produced THREE of those before anyone
// noticed, each found by hand. This closes that by construction: add a member
// without a driver and this fails.
func TestChaos5405_EveryDecisionReasonHasAProductionDriver(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse production sources: %v", err)
	}

	// COUNTING OCCURRENCES IS NOT ENOUGH, and this test learned that the same
	// way everything else in this ticket did -- by a control failing to fire.
	// A member appears at least twice in production source for free: once in
	// its const declaration, once in the factScopeDecisionReasons vocabulary
	// slice. An occurrence count of two therefore proves nothing, and the
	// first version of this test passed while capacity_timeout had no driver
	// at all.
	//
	// So walk for ASSIGNMENTS and RETURNS specifically: `x = FactScopeDecisionY`
	// or `return FactScopeDecisionY`. Those are the only shapes that put a
	// reason on an event, which is the property being asserted.
	drivers := map[string]bool{}
	record := func(expr ast.Expr) {
		if ident, ok := expr.(*ast.Ident); ok && strings.HasPrefix(ident.Name, "FactScopeDecision") {
			drivers[ident.Name] = true
		}
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, rhs := range node.Rhs {
						record(rhs)
					}
				case *ast.ReturnStmt:
					for _, res := range node.Results {
						record(res)
					}
				case *ast.KeyValueExpr:
					record(node.Value)
				}
				return true
			})
		}
	}

	for _, reason := range factScopeDecisionReasons {
		name := "FactScopeDecision" + decisionReasonGoName(string(reason))
		if !drivers[name] {
			t.Fatalf("decision reason %q (%s) is never ASSIGNED or RETURNED in production source -- declared and listed in the vocabulary, but nothing can ever emit it, so an operator alerting on it would wait forever",
				reason, name)
		}
	}
}

// decisionReasonGoName maps a wire reason onto its Go constant suffix.
func decisionReasonGoName(reason string) string {
	parts := strings.Split(reason, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// TestChaos5405_TheInFlightBoundIsTheRuledNumber closes a gap the mutation
// battery found in THIS FILE's own gate test.
//
// The test above fills the queue by READING maxWorkItemScopeInFlight, so the
// expectation is computed by the very thing under test: raise the constant and
// the test simply fills more slots and still passes. The arm that replaced 32
// with 1048576 SURVIVED — an admission gate that no longer bounds anything
// would have shipped with a green suite.
//
// So the bound is asserted against an INDEPENDENT literal. 32 is a ruled
// number, not an implementation detail: it is the ceiling on resident
// work-item expansions, and changing it is a capacity decision someone has to
// make on purpose. If this test and the constant ever disagree, that is the
// point — one of them is a change nobody argued for.
func TestChaos5405_TheInFlightBoundIsTheRuledNumber(t *testing.T) {
	t.Parallel()
	const ruled = 32
	if maxWorkItemScopeInFlight != ruled {
		t.Fatalf("maxWorkItemScopeInFlight = %d, ruled at %d -- a capacity bound moved without a decision", maxWorkItemScopeInFlight, ruled)
	}
	// And the CHANNEL the constructor actually builds carries that capacity:
	// the constant alone says nothing if the buffer is sized from something
	// else, and the channel's own length is what does the gating.
	resolver := NewFactReadScopeResolver(&reasonDriverExpander{})
	if got := cap(resolver.workItemSlots); got != ruled {
		t.Fatalf("workItemSlots capacity = %d, want %d -- the counter IS the channel, so a buffer sized from anything else is a different bound", got, ruled)
	}
}
