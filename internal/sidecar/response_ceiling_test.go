package sidecar

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// requiredCeilingMargin is how many times the sidecar's hard ceiling must
// clear the largest single response the hosted API will serve.
//
// It is a margin, not a tolerance. Equality would make the two values one
// change apart from a truncation that shows up only under maximum load,
// and the failure mode is silent: a truncated JSON body is a decode error
// at the client, blamed on the network rather than on a limit.
const requiredCeilingMargin = 8

// TestSidecarCeilingClearsTheServingBudget is CHAOS-3795, option (a).
//
// The sidecar refuses to read more than maxResponseBytesCeil bytes of any
// hosted response body. That is only safe if no legitimate response can be
// larger, and the largest one the service will serve is bounded by
// ContextFabricInvestigationOptions.MaxSerializedBytes, whose own maximum
// is ContextFabricSerializedBytesMax.
//
// Both sides are read from constants here rather than restated, which is
// the whole point: this asserts a RELATION, and it stays true only while
// both numbers are named. Raising the serving budget past an eighth of the
// ceiling fails this test at the moment of the change, not in production.
//
// Scope: one response, deliberately. The contract's aggregate maximum
// across a full expansion runs to hundreds of MiB, and no ceiling that
// cleared THAT would bound anything useful. The sidecar reads one response
// at a time (see TestEveryHostedResponseBodyIsReadThroughTheCeiling), so
// one response is the right quantity to compare.
func TestSidecarCeilingClearsTheServingBudget(t *testing.T) {
	serving := int64(contractsv1.ContextFabricSerializedBytesMax)

	if got := int64(maxResponseBytesCeil); got < serving*requiredCeilingMargin {
		t.Errorf("the sidecar ceiling is %d bytes; the serving budget is %d, so the ceiling must be at least %d (%dx)",
			got, serving, serving*requiredCeilingMargin, requiredCeilingMargin)
	}
	// The DEFAULT matters too, and separately: an operator who never sets
	// ACR_API_MAX_RESPONSE_BYTES runs on it, so a default below the
	// serving budget would truncate a legitimate maximal answer on a
	// stock deployment even though the ceiling above is generous.
	if got := int64(defaultMaxResponseBytes); got < serving {
		t.Errorf("the default response limit is %d bytes, below the %d-byte serving budget: a stock sidecar truncates a maximal answer", got, serving)
	}
	// And the configurable FLOOR is deliberately below the serving budget
	// -- an operator may choose a tight limit -- so this states that as an
	// intended asymmetry rather than leaving a reader to wonder whether it
	// is the same defect.
	if int64(minResponseBytes) >= serving {
		t.Errorf("minResponseBytes (%d) is no longer below the serving budget; the operator-tightening case this comment describes has changed", minResponseBytes)
	}
}

// auditedBoundedBodyReads are the hosted response body consumptions that
// are deliberately bounded by something other than the configured ceiling.
//
// Each entry is a CLAIM, keyed by the function it lives in and the bound it
// applies, and the guard below is two-sided: a body consumption that is not
// the ceiling and not listed here fails, and an entry here that no longer
// matches a real site fails too. An audited exemption that quietly stopped
// describing the code would be worse than no list at all.
var auditedBoundedBodyReads = map[string]string{
	"callPublic#redirectDrainBytes":          "unexpected-redirect drain: the body is discarded into io.Discard, never parsed and never returned, and the request fails immediately afterwards. Bounded rather than read through the operator ceiling because nothing is kept. Best-effort reuse: a body within the bound is read to EOF and the connection survives; a larger one forfeits the connection rather than being read unbounded, which costs a reconnect and nothing else.",
	"callWithHeaders#redirectDrainBytes":     "the same drain on the transport path, for the same reason.",
	"exchange#maxTokenExchangeResponseBytes": "CHAOS-4013 RFC 8693 token exchange response, read before a Client (and its configured MaxResponseBytes) exists -- this call resolves the bearer credential a Client will use. A real response is well under 1 KiB; fixed and small for the same reason redirectDrainBytes is.",
}

// auditedBoundValues pins the VALUE behind each audited bound, not only
// the site that uses it (round-17 finding 3).
//
// Keying the audit on the expression alone meant redirectDrainBytes could
// grow to any size and still pass: the exemption said "an audited bound",
// and any number satisfied that. The exemption is only justified while the
// bound stays negligible against the ceiling it sidesteps, so the value is
// asserted too.
var auditedBoundValues = map[string]int{"redirectDrainBytes": redirectDrainBytes, "maxTokenExchangeResponseBytes": maxTokenExchangeResponseBytes}

// maxAuditedBoundBytes is how large an audited non-ceiling bound may be
// before it stops being negligible and has to be justified again on its
// own terms. Set well above today's drain and far below the ceiling: this
// is a tripwire against silent growth, not a second tuning knob.
const maxAuditedBoundBytes = 64 << 10

// bodyConsumption is one place a hosted response body is used as a value.
type bodyConsumption struct {
	function string
	// bound is "ceiling" when the read goes through readLimited with the
	// configured limit, "bounded:<expr>" for an explicit io.LimitReader,
	// and "" when nothing bounds it at all.
	bound    string
	position string
}

// TestEveryHostedResponseBodyFlowsThroughABound asserts the PREMISE the
// margin test rests on, and asserts it as a CLASS rather than by naming
// verbs (round-16 finding 2).
//
// The margin proves the ceiling is large enough. It proves nothing about
// whether the ceiling is applied, and the previous version of this guard
// checked only for readLimited call sites and bare io.ReadAll -- so
// io.Copy, io.ReadFull, bufio.NewReader, or anything else reading a body
// would have walked straight past it. Two io.Copy sites already existed in
// this package while that guard reported everything was fine.
//
// So this works on the BODY HANDLE instead of on the reading verb: every
// use of a `*.Body` selector as a value must either flow into readLimited
// (the configured ceiling) or into an explicit io.LimitReader that is
// listed in auditedBoundedBodyReads with a reason. Closing a body is not a
// consumption and is excluded. Whatever new verb someone reaches for, the
// body still has to reach it through one of those two paths.
func TestEveryHostedResponseBodyFlowsThroughABound(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	var consumptions []bodyConsumption
	matched := map[string]bool{}

	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			// bounded/closed record the Body selectors already accounted
			// for, keyed by source position so a second use of the same
			// expression elsewhere is still its own consumption.
			bounded := map[token.Pos]string{}
			closed := map[token.Pos]bool{}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "readLimited" && len(call.Args) == 2 {
						if pos, ok := bodySelectorPos(call.Args[0]); ok {
							bounded[pos] = "ceiling:" + exprText(fileSet, call.Args[1])
						}
					}
				case *ast.SelectorExpr:
					if isPackageCall(fun, "io", "LimitReader") && len(call.Args) == 2 {
						if pos, ok := bodySelectorPos(call.Args[0]); ok {
							bounded[pos] = "bounded:" + exprText(fileSet, call.Args[1])
						}
					}
					// A Close() on the body is not a consumption.
					if fun.Sel.Name == "Close" {
						if pos, ok := bodySelectorPos(fun.X); ok {
							closed[pos] = true
						}
					}
				}
				return true
			})

			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Body" {
					return true
				}
				if closed[selector.Pos()] {
					return true
				}
				consumptions = append(consumptions, bodyConsumption{
					function: enclosingFunction(file, selector.Pos()),
					bound:    bounded[selector.Pos()],
					position: fileSet.Position(selector.Pos()).String(),
				})
				return true
			})
		}
	}

	if len(consumptions) == 0 {
		t.Fatal("found no hosted response body consumption at all; the walker is not reaching the client code and would pass over any evasion")
	}

	for _, consumption := range consumptions {
		switch {
		case strings.HasPrefix(consumption.bound, "ceiling:"):
			if limit := strings.TrimPrefix(consumption.bound, "ceiling:"); limit != "c.cfg.MaxResponseBytes" {
				t.Errorf("%s reads the body through readLimited with %q rather than the configured ceiling: a local number cannot be tuned by an operator and is invisible to the margin check", consumption.position, limit)
			}
		case strings.HasPrefix(consumption.bound, "bounded:"):
			key := consumption.function + "#" + strings.TrimPrefix(consumption.bound, "bounded:")
			if _, audited := auditedBoundedBodyReads[key]; !audited {
				t.Errorf("%s bounds a response body by %q, which is not the configured ceiling and is not audited; add %q to auditedBoundedBodyReads with the reason its own bound is adequate", consumption.position, consumption.bound, key)
				continue
			}
			matched[key] = true
			expression := strings.TrimPrefix(consumption.bound, "bounded:")
			value, known := auditedBoundValues[expression]
			if !known {
				t.Errorf("%s is audited but its bound %q has no pinned value; add it to auditedBoundValues so growth cannot pass the audit unnoticed", consumption.position, expression)
				continue
			}
			if value <= 0 || value > maxAuditedBoundBytes {
				t.Errorf("%s bounds a response body at %d bytes, outside the negligible range this exemption rests on (1..%d): an audited bound that grew this far needs justifying against the ceiling on its own terms, not by inheriting an old audit",
					consumption.position, value, maxAuditedBoundBytes)
			}
		default:
			t.Errorf("%s consumes a hosted response body without any bound; every read must flow through readLimited or an audited io.LimitReader, whatever verb does the reading", consumption.position)
		}
	}

	// The other side: a stale exemption is a lie about audited code.
	for key := range auditedBoundedBodyReads {
		if !matched[key] {
			t.Errorf("auditedBoundedBodyReads lists %q, which matches no bounded body read; remove it rather than leaving an exemption that describes nothing", key)
		}
	}
}

// bodySelectorPos reports the position of expression when it is a `*.Body`
// selector, which is how a hosted response body is always reached here.
func bodySelectorPos(expression ast.Expr) (token.Pos, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Body" {
		return token.NoPos, false
	}
	return selector.Pos(), true
}

func isPackageCall(selector *ast.SelectorExpr, pkg, name string) bool {
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == pkg && selector.Sel.Name == name
}

// enclosingFunction names the function a position sits in, so an audited
// exemption is keyed by where it lives rather than by a line number that
// moves whenever anything above it changes.
func enclosingFunction(file *ast.File, pos token.Pos) string {
	name := "?"
	ast.Inspect(file, func(node ast.Node) bool {
		function, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if function.Pos() <= pos && pos <= function.End() {
			name = function.Name.Name
		}
		return true
	})
	return name
}

// exprText renders an expression back to source text for the assertions
// above. Only the selector and identifier shapes this test compares need
// to render exactly; anything else renders well enough to name in a
// failure message.
func exprText(fileSet *token.FileSet, expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return exprText(fileSet, value.X) + "." + value.Sel.Name
	case *ast.Ident:
		return value.Name
	case *ast.BasicLit:
		return value.Value
	default:
		return fileSet.Position(expression.Pos()).String()
	}
}
