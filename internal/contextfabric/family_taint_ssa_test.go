package contextfabric

// Value-flow taint of the question family over golang.org/x/tools/go/ssa.
//
// WHY THIS IS AN IR ANALYSIS AND NOT A SYNTAX WALK. The predecessor gate
// walked go/ast with go/types on the side. Four adversarial review rounds
// each defeated it with a genuinely new SYNTACTIC site the walk did not
// reach -- a composite literal wrapping the family as a map key, then a
// tagless switch whose comparison hid inside strings.EqualFold, then an
// ordinary call (fmt.Sprintf) whose RESULT the walk declined to taint.
// Four holes, one class: a syntax walker is never closed under an
// arbitrary call boundary, because a call can do anything with its
// argument and hand back something derived from it, and "how" has no
// bounded enumeration. Closures, method values, channel sends and
// interface dispatch are the same class, unprobed only because no round
// reached for them.
//
// SSA removes the enumeration. Calls, closures, method values, phi nodes,
// field and element flow, and channels are all edges in ONE value graph
// built by the IR, so "this value was computed FROM the family" is a
// single uniform relation instead of a growing list of shapes. The rules
// below are stated once and apply everywhere.
//
// THE LATTICE IS THREE-LEVEL, AND THAT IS THE LOAD-BEARING DESIGN CHOICE.
//
//	none < value < derived
//
// `value` is the family value itself, unmodified -- still typed
// ContextFabricQuestionFamily (or its genuine Go alias QuestionFamily),
// including when boxed into an interface. `derived` is text or an ordinal
// computed FROM the family.
//
// The family IS a legitimate served closed token: it is a declared member
// of the wire contract, it appears on AnswerPlan, and
// ContextFabricQuestionFamilyVocabulary() serves the whole vocabulary on
// purpose. A two-level tainted/untainted lattice would flag the wire
// contract itself and the gate would be switched off inside a day (the
// lesson of the rule lane-e3-floor tried and removed: "any index by a
// family-typed expression" fired on a vote tally). Only `derived` values
// are defects. Everything else in this file follows from that split.
//
// WHAT THIS ANALYSIS STILL DOES NOT PROVE, stated so green is not
// mistaken for a nonexistence proof:
//   - Memory is modelled per ORIGIN (an Alloc, a Global, a MakeMap, a
//     parameter) and per struct FIELD program-wide, flow-insensitively.
//     Two distinct objects of the same type share a field's taint, so the
//     model over-approximates aliasing rather than under-approximating it
//     -- false positives are possible, false negatives through aliasing
//     are not the expected failure.
//   - Implicit (control-dependence) flow is modelled through the
//     post-dominance frontier, in two strengths. Under a branch whose
//     condition is DIRECTLY family-derived, any text-typed value served
//     inside the guarded region counts. Under a branch that is merely
//     tainted, only a non-empty text CONSTANT counts. The weaker rule
//     exists because an ungated implicit-flow rule creeps over every
//     branch downstream of a family test; the stronger one exists because
//     the constant-only version was defeated (fixture R10: prose returned
//     by a no-argument helper, selected by `if family == X`). Where the
//     next evasion goes is now the DIRECTNESS test, not the constant test.
//   - Dynamic dispatch is resolved with CHA, which is sound but
//     imprecise: every method with a matching signature on a type in the
//     program is a candidate.
//   - Reflection and runtime struct-tag interpretation are not modelled.
//   - Coverage is exactly the four swept roots. A callee outside them is
//     opaque: a tainted argument yields a derived result and its body is
//     not searched for sinks (this is what keeps log/telemetry calls that
//     merely OBSERVE a family from being reported).

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// familyTaint is the three-level lattice described in the file comment.
type familyTaint uint8

const (
	familyTaintNone familyTaint = iota
	// familyTaintValue: the family value itself, unmodified. Legitimate on
	// the wire -- it is a declared closed token.
	familyTaintValue
	// familyTaintDerived: text or an ordinal computed FROM the family.
	// This is the defect class.
	familyTaintDerived
)

func (t familyTaint) join(o familyTaint) familyTaint {
	if o > t {
		return o
	}
	return t
}

func (t familyTaint) tainted() bool { return t != familyTaintNone }

// familyTaintFinding is one sink: a derived value that escapes into text.
type familyTaintFinding struct {
	pos    token.Position
	rule   string
	detail string
	// served records whether the sink is additionally reachable from an
	// encoding/json serialization boundary (see familySSAComputeServed).
	// Enforcement does NOT depend on it -- the escape rule is a strict
	// superset -- but a reviewer reads severity from it.
	served bool
	// enforced is what turns a finding into a FAILURE rather than a note.
	//
	// A gate that fails on everything it can see gets switched off; a gate
	// that fails on what actually reaches the wire is one people keep. The
	// enforced tier is the claim CHAOS-4782 makes -- no family-derived
	// text crosses the serialization boundary -- and it is exactly the
	// orders' sink definition: a store into a field of a type reachable
	// from the encoder, or a derived text value handed to the encoder
	// itself.
	//
	// Everything else the analysis finds -- derived text returned from a
	// helper, stored into an internal struct, put on a channel -- is
	// REPORTED, with its provenance, and does not fail the build. Those
	// are intermediates: if one of them ever reaches the wire, the store
	// that puts it there is itself an enforced finding, at the place a
	// reader can act on.
	enforced bool
	// path is the seed -> ... -> sink provenance chain, so a reviewer can
	// read WHY a value is considered derived instead of re-deriving it.
	path []string
}

func (f familyTaintFinding) String() string {
	reach := "not served-reachable"
	if f.served {
		reach = "SERVED-REACHABLE"
	}
	if f.enforced {
		reach = "SERVED-REACHABLE, ENFORCED"
	}
	s := fmt.Sprintf("%s [%s] %s (%s)", f.pos, f.rule, f.detail, reach)
	if len(f.path) > 0 {
		s += "\n      path: " + strings.Join(f.path, "\n         -> ")
	}
	return s
}

// familySSA holds the whole-program taint state. One instance per loaded
// program: go/types object identity and ssa.Value identity are only
// meaningful inside a single packages.Load / ssautil.Packages session.
type familySSA struct {
	t     *testing.T
	facts familyGateFacts
	// familyObj is the family type's declaring *types.TypeName, the
	// identity every seed decision is made against.
	familyObj *types.TypeName
	prog      *ssa.Program
	fset      *token.FileSet
	cg        *callgraph.Graph

	// funcs is every function whose body this analysis owns, in a
	// deterministic order so the fixed point and the report are stable.
	funcs []*ssa.Function
	owned map[*ssa.Function]bool

	// val is the taint of a VALUE.
	val map[ssa.Value]familyTaint
	// mem is the taint of the CONTENTS of a memory origin (an Alloc, a
	// Global, a MakeMap/MakeSlice/MakeChan, a pointer parameter).
	mem map[ssa.Value]familyTaint
	// memDirect marks an object whose CONTENTS were written directly from
	// the family (or from text already derived from it) rather than from
	// something that merely contains one. Its only job is to see through
	// varargs packing -- see isDirect.
	memDirect map[ssa.Value]bool
	// objField is the taint contributed to ONE object by stores into its
	// own fields. Keyed by allocation site, not by type: it is what makes
	// a whole-object load carry what was put into that object, without
	// the program-wide blowup that tainting the object type would cause.
	objField map[ssa.Value]familyTaint
	// field is the taint of a struct FIELD, program-wide. Coarse on
	// purpose: it is what carries taint across a function boundary
	// through a struct, which is the shape the syntax walker needed a
	// bespoke two-pass mechanism for.
	field map[*types.Var]familyTaint
	// ctrl marks blocks that execute only under a family-dependent
	// branch (implicit flow).
	ctrl map[*ssa.BasicBlock]familyTaint
	// writerShape memoizes familySSAIsWriterShaped. Method-set
	// construction is not cheap and the boundary question is asked for
	// every call, on every fixed-point round; uncached it cost more wall
	// time than the entire rest of the analysis.
	writerShape map[string]bool
	// ctrlDirect marks the stricter case: the guarding condition was
	// DIRECTLY family-derived (the family value itself, or text derived
	// from it), not merely tainted. See sinkValueTaint.
	ctrlDirect map[*ssa.BasicBlock]bool

	// why records, for each value whose taint was raised, the value it was
	// raised FROM, plus a one-line description of the rule that did it.
	why map[ssa.Value]familyTaintEdge

	changed bool

	// records are raw sinks, resolved into findings after the fixed point.
	records []familySSARecord

	// served state, computed once after the fixed point.
	servingValues map[ssa.Value]bool
	// egressValues are the strict subset of serving values at which bytes
	// actually LEAVE. See computeServed.
	egressValues map[ssa.Value]bool
	servedTypes  map[string]bool
}

type familyTaintEdge struct {
	from ssa.Value
	rule string
}

// familySSAAnalyze is the entry point: build SSA for an already-loaded and
// fully type-checked program, run the taint fixed point, and return every
// sink. Callers supply facts resolved from THEIR OWN load.
// requireServedAnchors is passed true by the production-roots gate, whose
// load contains internal/api and internal/mcp and therefore contains the
// encoder boundary the served set is derived from. The historical-
// construction fixtures load a single standalone package plus
// contracts/v1 and serve nothing at all, so there is no served set to
// check there; they assert against the reported tier instead, which is
// what a stand-in served field is for.
func familySSAAnalyze(t *testing.T, pkgs []*packages.Package, facts familyGateFacts, requireServedAnchors bool) []familyTaintFinding {
	t.Helper()

	// ssa.InstantiateGenerics: without it, a generic function's body is
	// only present in an uninstantiated form whose types are type
	// parameters, and a family value flowing through a generic helper
	// would be invisible to types.Identical.
	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	if len(ssaPkgs) == 0 {
		t.Fatalf("ssautil.Packages built zero packages -- the analysis would pass vacuously")
	}
	prog.Build()

	a := &familySSA{
		t:           t,
		facts:       facts,
		prog:        prog,
		fset:        prog.Fset,
		owned:       map[*ssa.Function]bool{},
		val:         map[ssa.Value]familyTaint{},
		mem:         map[ssa.Value]familyTaint{},
		memDirect:   map[ssa.Value]bool{},
		objField:    map[ssa.Value]familyTaint{},
		field:       map[*types.Var]familyTaint{},
		ctrl:        map[*ssa.BasicBlock]familyTaint{},
		ctrlDirect:  map[*ssa.BasicBlock]bool{},
		writerShape: map[string]bool{},
		why:         map[ssa.Value]familyTaintEdge{},
	}

	if named, ok := types.Unalias(facts.familyType).(*types.Named); ok {
		a.familyObj = named.Obj()
	}
	if a.familyObj == nil {
		t.Fatalf("the family type %s is not a defined type -- the seed rule has nothing to resolve identity against", facts.familyType)
	}

	for fn := range ssautil.AllFunctions(prog) {
		if fn.Blocks == nil {
			continue // externally implemented: opaque, handled at call sites
		}
		a.owned[fn] = true
		a.funcs = append(a.funcs, fn)
	}
	if len(a.funcs) == 0 {
		t.Fatalf("SSA build produced zero function bodies -- the analysis would pass vacuously")
	}
	sort.Slice(a.funcs, func(i, j int) bool {
		return a.funcs[i].String() < a.funcs[j].String()
	})

	a.cg = cha.CallGraph(prog)

	a.fixedPoint()

	a.computeServed()
	a.checkServingPositionSinks()
	if requireServedAnchors {
		a.assertServedSetIsRealistic()
	}

	return a.collectFindings()
}

// fixedPoint iterates the transfer functions until nothing changes.
// Control dependence is recomputed each round because it depends on which
// branch conditions are tainted, which the previous round may have grown.
func (a *familySSA) fixedPoint() {
	const maxRounds = 200
	for round := 0; ; round++ {
		a.changed = false
		a.computeControl()
		for _, fn := range a.funcs {
			a.transferFunc(fn)
		}
		if !a.changed {
			return
		}
		if round >= maxRounds {
			// Asserting convergence rather than silently stopping after a
			// fixed budget: a monotone analysis over a finite lattice MUST
			// converge, so hitting this means a transfer rule is not
			// monotone, which would make every green result meaningless.
			a.t.Fatalf("family taint analysis did not converge in %d rounds -- a transfer rule is non-monotone; the result cannot be trusted", maxRounds)
		}
	}
}

// ---------------------------------------------------------------------------
// lattice bookkeeping
// ---------------------------------------------------------------------------

func (a *familySSA) setVal(v ssa.Value, t familyTaint, from ssa.Value, rule string) {
	if v == nil || t == familyTaintNone {
		return
	}
	if a.val[v] >= t {
		return
	}
	a.val[v] = t
	a.why[v] = familyTaintEdge{from: from, rule: rule}
	a.changed = true
}

func (a *familySSA) setMem(origin ssa.Value, t familyTaint) {
	if origin == nil || t == familyTaintNone || a.mem[origin] >= t {
		return
	}
	a.mem[origin] = t
	a.changed = true
}

func (a *familySSA) setField(f *types.Var, t familyTaint) {
	if f == nil || t == familyTaintNone || a.field[f] >= t {
		return
	}
	a.field[f] = t
	a.changed = true
}

// isFamilyType answers the SEED question by go/types OBJECT identity: the
// family is a defined type, so every spelling of it -- including the
// package-local `QuestionFamily`, a genuine Go type alias and therefore
// literally the same *types.Named -- resolves to one *types.TypeName, and
// a future third spelling would too. No name is compared.
//
// Object identity rather than types.Identical is deliberate and not a
// micro-optimisation: an SSA program contains values whose types are not
// ordinary Go types (ssa.Builtin's signature, the tuple types of
// multi-result calls, the *ssa.Function values behind method
// expressions), and types.Identical panics "unreachable" on some of them.
// Reaching into the *types.Named is total where Identical is partial.
func (a *familySSA) isFamilyType(t types.Type) bool {
	if t == nil || a.familyObj == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() == a.familyObj
}

// seed gives every family-typed value the `value` level. Constants of the
// family type are values too, so the closed vocabulary's own constants
// seed exactly like a runtime-resolved family does.
func (a *familySSA) seed(v ssa.Value) {
	if v == nil {
		return
	}
	if a.isFamilyType(v.Type()) {
		a.setVal(v, familyTaintValue, nil, "seed: value is of the family type")
	}
}

// ---------------------------------------------------------------------------
// memory model
// ---------------------------------------------------------------------------

// familySSAOrigin resolves an address (or a container value) back to the
// object it lives in. Field and element selection collapse to the same
// origin: memory is modelled per object, with struct fields additionally
// tracked program-wide by field object.
func familySSAOrigin(v ssa.Value) ssa.Value {
	for i := 0; i < 64; i++ { // bounded: SSA address chains are finite
		switch x := v.(type) {
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.IndexAddr:
			v = x.X
		case *ssa.Field:
			v = x.X
		case *ssa.Index:
			v = x.X
		case *ssa.Slice:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			v = x.X
		default:
			return v
		}
	}
	return v
}

// fieldOf returns the struct field object an address or value selects, if
// any. This is what lets taint cross a function boundary through a struct
// without the caller and callee sharing a local.
func familySSAFieldOf(v ssa.Value) *types.Var {
	switch x := v.(type) {
	case *ssa.FieldAddr:
		if st, ok := familySSADerefStruct(x.X.Type()); ok && x.Field < st.NumFields() {
			return st.Field(x.Field)
		}
	case *ssa.Field:
		if st, ok := familySSADerefStruct(x.X.Type()); ok && x.Field < st.NumFields() {
			return st.Field(x.Field)
		}
	}
	return nil
}

func familySSADerefStruct(t types.Type) (*types.Struct, bool) {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	return st, ok
}

// readMem is the load rule, and it is FIELD-PRECISE on purpose.
//
// A load through a FieldAddr reads that field's taint ALONE -- never the
// enclosing object's. The first draft of this analysis joined the whole
// object's taint into every field load, and it lit up the two known-clean
// vote-tally files immediately: one derived field anywhere in a struct
// made every other field of that struct read as derived, including fields
// that hold the family value itself and are legitimate. Object-level taint
// still exists, but it answers a different question (what does this WHOLE
// value carry when it is passed or returned), and mixing the two is what
// turns a taint analysis into noise.
func (a *familySSA) readMem(addr ssa.Value) familyTaint {
	if f := familySSAFieldOf(addr); f != nil {
		return a.field[f]
	}
	o := familySSAOrigin(addr)
	return a.mem[o].join(a.val[o]).join(a.objField[o])
}

// writeMem is the store rule, and it is field-granular for the same
// reason readMem is.
//
// A store into obj.Field taints THE FIELD, program-wide, and NOT the
// enclosing object. Tainting the object as well is what made this
// analysis unusable in its first draft: this codebase legitimately hangs
// the family off long-lived structs (InvestigationResult, the graphrank
// trace events, the telemetry rows), so one family-typed field made the
// whole struct a tainted value, and from there every fmt.Sprintf,
// json.Marshal, append and database call that took the STRUCT reported
// derived text. A struct that merely CONTAINS a closed token is not the
// token, and it is certainly not text derived from it.
//
// Whole-object taint survives for genuinely whole-object writes -- a
// store through a bare pointer, an element written into a varargs array
// -- which is where it means what it says.
func (a *familySSA) writeMem(addr ssa.Value, t familyTaint) {
	if t == familyTaintNone {
		return
	}
	if f := familySSAFieldOf(addr); f != nil {
		a.setField(f, t)
		// The object that field belongs to carries it too -- but only
		// THIS object, at THIS allocation site. That is what keeps
		// `wrappedKey{Family: family}` (fixture R5b: a struct wrapping the
		// family, used as a map key) a tainted value without making every
		// struct that has a family field one.
		// CAPPED AT `value` DELIBERATELY. An object that contains
		// family-derived TEXT is not itself family-derived text -- that
		// text is caught at its own store or return, where it actually
		// is. Letting `derived` propagate up to the enclosing object made
		// a nested store poison the whole root object, whose every
		// subsequent load then reported another finding, and another
		// object after that: 1,107 findings from one real value. What a
		// containing object legitimately carries is the TOKEN, and that
		// is what fixture R5b's `wrappedKey{Family: family}` needs in
		// order to be a tainted map key.
		o := familySSAOrigin(addr)
		capped := t
		if capped > familyTaintValue {
			capped = familyTaintValue
		}
		if a.objField[o] < capped {
			a.objField[o] = capped
			a.changed = true
		}
		return
	}
	a.setMem(familySSAOrigin(addr), t)
}

// ---------------------------------------------------------------------------
// control dependence (implicit flow)
// ---------------------------------------------------------------------------

// computeControl marks every block that executes only under a
// family-dependent branch. Four of the seven inherited fixtures carry NO
// data dependence at all from the family to the served text -- the
// dependence runs through a branch condition -- so this is not an
// enhancement, it is a requirement.
//
// Post-dominance is computed on the reverse CFG by iterative intersection
// (O(n^2) with plain sets; SSA function CFGs are small), and control
// dependence is read off it by the textbook definition: B is control
// dependent on A when A has a successor S that B post-dominates, and B
// does not strictly post-dominate A.
func (a *familySSA) computeControl() {
	for _, fn := range a.funcs {
		a.computeControlFunc(fn)
	}
}

func (a *familySSA) computeControlFunc(fn *ssa.Function) {
	blocks := fn.Blocks
	n := len(blocks)
	if n == 0 {
		return
	}
	// Only branch blocks can induce control dependence, and only tainted
	// ones matter. Skip the whole computation when none is tainted -- the
	// common case by far, and this runs every fixed-point round.
	var branchers []*ssa.BasicBlock
	for _, b := range blocks {
		if familySSAIsTaintedIf(a, b) {
			branchers = append(branchers, b)
		}
	}
	if len(branchers) == 0 {
		return
	}

	// postdom[i] = set of block indices that post-dominate block i.
	postdom := make([]map[int]bool, n)
	full := map[int]bool{}
	for i := range blocks {
		full[i] = true
	}
	for i, b := range blocks {
		if len(b.Succs) == 0 {
			postdom[i] = map[int]bool{i: true}
		} else {
			postdom[i] = familySSACopySet(full)
		}
	}
	for changed := true; changed; {
		changed = false
		for i := n - 1; i >= 0; i-- {
			b := blocks[i]
			if len(b.Succs) == 0 {
				continue
			}
			var acc map[int]bool
			for _, s := range b.Succs {
				if acc == nil {
					acc = familySSACopySet(postdom[s.Index])
					continue
				}
				for k := range acc {
					if !postdom[s.Index][k] {
						delete(acc, k)
					}
				}
			}
			if acc == nil {
				acc = map[int]bool{}
			}
			acc[i] = true
			if !familySSASetEqual(acc, postdom[i]) {
				postdom[i] = acc
				changed = true
			}
		}
	}

	for _, aBlk := range branchers {
		ai := aBlk.Index
		for _, s := range aBlk.Succs {
			for bi := 0; bi < n; bi++ {
				// B post-dominates S ...
				if !postdom[s.Index][bi] {
					continue
				}
				// ... and does not STRICTLY post-dominate A.
				if bi != ai && postdom[ai][bi] {
					continue
				}
				a.markCtrl(blocks[bi], a.branchIsDirect(aBlk))
			}
		}
	}
}

func (a *familySSA) markCtrl(b *ssa.BasicBlock, direct bool) {
	if a.ctrl[b] < familyTaintDerived {
		a.ctrl[b] = familyTaintDerived
		a.changed = true
	}
	if direct && !a.ctrlDirect[b] {
		a.ctrlDirect[b] = true
		a.changed = true
	}
}

// branchIsDirect reports whether the block's terminating If tests
// something DIRECTLY family-derived, as opposed to a value that merely
// carries the taint.
func (a *familySSA) branchIsDirect(b *ssa.BasicBlock) bool {
	if len(b.Instrs) == 0 {
		return false
	}
	iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
	if !ok {
		return false
	}
	if a.isDirect(iff.Cond) {
		return true
	}
	// `family == X` is a BinOp: the comparison result is derived, but
	// directness lives in its operands.
	if bin, ok := iff.Cond.(*ssa.BinOp); ok {
		return a.isDirect(bin.X) || a.isDirect(bin.Y)
	}
	return false
}

func familySSAIsTaintedIf(a *familySSA, b *ssa.BasicBlock) bool {
	if len(b.Instrs) == 0 {
		return false
	}
	iff, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
	if !ok {
		return false
	}
	return a.val[iff.Cond].tainted()
}

func familySSACopySet(s map[int]bool) map[int]bool {
	out := make(map[int]bool, len(s))
	for k := range s {
		out[k] = true
	}
	return out
}

func familySSASetEqual(x, y map[int]bool) bool {
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// transfer functions
// ---------------------------------------------------------------------------

func (a *familySSA) transferFunc(fn *ssa.Function) {
	for _, p := range fn.Params {
		a.seed(p)
	}
	for _, fv := range fn.FreeVars {
		a.seed(fv)
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if v, ok := instr.(ssa.Value); ok {
				a.seed(v)
			}
			for _, opp := range instr.Operands(nil) {
				if opp != nil && *opp != nil {
					a.seed(*opp)
				}
			}
			a.transfer(fn, b, instr)
		}
	}
}

func (a *familySSA) transfer(fn *ssa.Function, b *ssa.BasicBlock, instr ssa.Instruction) {
	switch x := instr.(type) {

	// --- type operations -------------------------------------------------
	//
	// Boxing into an interface does not DERIVE anything: the family value
	// inside an `any` is still the family value, and serving it is the
	// wire contract. Any other type change does derive -- `string(family)`
	// compiles to ChangeType (identical underlying types), which is
	// exactly the R1 evasion, so ChangeType must promote whenever the
	// result stops being the family type.
	case *ssa.MakeInterface:
		a.setVal(x, a.val[x.X], x.X, "boxed into an interface (still the family value)")
	case *ssa.ChangeInterface:
		a.setVal(x, a.val[x.X], x.X, "interface-to-interface change")
	case *ssa.ChangeType:
		a.setVal(x, a.promoteUnless(x.Type(), a.val[x.X]), x.X, "type change")
	case *ssa.Convert:
		a.setVal(x, a.promoteUnless(x.Type(), a.val[x.X]), x.X, "conversion")
	case *ssa.MultiConvert:
		a.setVal(x, a.promoteUnless(x.Type(), a.val[x.X]), x.X, "generic conversion")
	case *ssa.SliceToArrayPointer:
		a.setVal(x, a.promoteUnless(x.Type(), a.val[x.X]), x.X, "slice-to-array-pointer")
	case *ssa.TypeAssert:
		a.setVal(x, a.promoteUnless(x.Type(), a.val[x.X]), x.X, "type assertion")

	// --- arithmetic and logic --------------------------------------------
	//
	// This is the rule that makes `family == X` and
	// `strings.EqualFold(string(family), "x")` the same thing to the
	// analysis: both end in a boolean derived from the family.
	case *ssa.BinOp:
		t := a.val[x.X].join(a.val[x.Y])
		if t.tainted() {
			src := x.X
			if !a.val[x.X].tainted() {
				src = x.Y
			}
			a.setVal(x, familyTaintDerived, src, "binary operation on a family-derived operand")
		}
	case *ssa.UnOp:
		if x.Op == token.MUL {
			a.setVal(x, a.readMem(x.X), x.X, "load from family-derived memory")
		} else if a.val[x.X].tainted() {
			a.setVal(x, familyTaintDerived, x.X, "unary operation on a family-derived operand")
		}

	// --- merges: data AND control ---------------------------------------
	//
	// The control half is what closes R1/R3/R6: a phi whose incoming edge
	// leaves a block that only executes under a family-dependent branch
	// carries family information even though no assignment copied it.
	case *ssa.Phi:
		for i, edge := range x.Edges {
			if t := a.val[edge]; t.tainted() {
				a.setVal(x, t, edge, "phi merge of a family-derived value")
			}
			if i < len(b.Preds) && a.ctrl[b.Preds[i]].tainted() {
				a.setVal(x, familyTaintDerived, edge, "phi edge from a block guarded by a family-dependent branch")
			}
		}

	// --- aggregates ------------------------------------------------------
	case *ssa.Field:
		// Field-precise, for the reason readMem documents: the taint of a
		// field is the field's, not its neighbours'.
		var t familyTaint
		if f := familySSAFieldOf(x); f != nil {
			t = a.field[f]
		}
		a.setVal(x, t, x.X, "field of a family-derived aggregate")
	case *ssa.FieldAddr:
		if t := a.val[x.X].join(a.mem[familySSAOrigin(x)]); t.tainted() {
			a.setMem(familySSAOrigin(x), t)
		}
	case *ssa.Index:
		t := a.val[x.X].join(a.mem[familySSAOrigin(x)])
		if a.val[x.Index].tainted() {
			a.setVal(x, familyTaintDerived, x.Index, "element selected by a family-derived index")
		}
		a.setVal(x, t, x.X, "element of a family-derived aggregate")
	case *ssa.IndexAddr:
		if a.val[x.Index].tainted() {
			// The ADDRESS is selected by a family-derived index, so a load
			// from it is family-derived regardless of what was stored.
			a.setMem(familySSAOrigin(x), familyTaintDerived)
		}
	case *ssa.Lookup:
		// Map lookup. R2 (map[Family]phrase) and P1 (a struct wrapping the
		// family used as the key) are both exactly this: the KEY is
		// tainted, so the value that comes back is family-derived.
		t := a.val[x.X].join(a.mem[familySSAOrigin(x.X)])
		if a.val[x.Index].tainted() {
			a.setVal(x, familyTaintDerived, x.Index, "map value selected by a family-derived key")
		}
		a.setVal(x, t, x.X, "value from a family-derived map")
	case *ssa.Slice:
		a.setVal(x, a.val[x.X].join(a.mem[familySSAOrigin(x.X)]), x.X, "slice of a family-derived aggregate")
	case *ssa.Next:
		// range: the tuple carries (ok, key, value) from the iterator.
		if t := a.val[x.Iter].join(a.mem[familySSAOrigin(x.Iter)]); t.tainted() {
			a.setVal(x, t, x.Iter, "range over a family-derived container")
		}
	case *ssa.Extract:
		a.setVal(x, a.tupleTaint(x.Tuple, x.Index), x.Tuple, "tuple component")

	// --- memory writes ---------------------------------------------------
	// Memory effects use the value's OWN taint (a.val), never the
	// implicit-flow reading. See sinkValueTaint: control dependence is
	// deliberately sink-local and does not feed back into the value graph.
	case *ssa.Store:
		a.writeMem(x.Addr, a.val[x.Val])
		if familySSAFieldOf(x.Addr) == nil && a.isDirect(x.Val) {
			o := familySSAOrigin(x.Addr)
			if !a.memDirect[o] {
				a.memDirect[o] = true
				a.changed = true
			}
		}
		a.checkStoreSink(fn, b, x)
	case *ssa.MapUpdate:
		a.setMem(familySSAOrigin(x.Map), a.val[x.Value].join(a.val[x.Key]))
		a.checkMapUpdateSink(fn, b, x)
	case *ssa.Send:
		a.setMem(familySSAOrigin(x.Chan), a.val[x.X])
		a.checkSendSink(fn, b, x)

	// --- closures --------------------------------------------------------
	//
	// A closure capturing a tainted local is not a special case here: the
	// binding is an edge from the captured value to the callee's FreeVar,
	// and everything downstream is the ordinary rules.
	case *ssa.MakeClosure:
		if inner, ok := x.Fn.(*ssa.Function); ok {
			for i, bound := range x.Bindings {
				if i < len(inner.FreeVars) {
					a.setVal(inner.FreeVars[i], a.val[bound], bound, "captured by a closure")
					if t := a.val[bound].join(a.mem[familySSAOrigin(bound)]); t.tainted() {
						a.setMem(inner.FreeVars[i], t)
					}
				}
			}
		}
		for _, bound := range x.Bindings {
			if a.val[bound].tainted() {
				a.setVal(x, familyTaintDerived, bound, "closure capturing a family-derived value")
			}
		}

	// --- calls -----------------------------------------------------------
	case *ssa.Call:
		a.transferCall(fn, b, x, x.Call, x)
	case *ssa.Go:
		a.transferCall(fn, b, x, x.Call, nil)
	case *ssa.Defer:
		a.transferCall(fn, b, x, x.Call, nil)

	// --- returns ---------------------------------------------------------
	case *ssa.Return:
		a.checkReturnSink(fn, b, x)
	}
}

// promoteUnless keeps the `value` level only while the result is still the
// family type; anything else is derived. This one helper is the lattice's
// whole discriminator between "the closed token, served on purpose" and
// "text computed from it".
//
// It applies to TYPE-CHANGING operations only -- a conversion, a type
// assertion -- where the family genuinely stopped being the family and
// became something computed from it. It deliberately does NOT apply to
// reading a field or an element out of a container. Selecting a member of
// a container that merely CONTAINS a family somewhere is not a derivation
// of that member from the family; a container's own taint says what it
// holds, and a member read inherits that level unchanged. When the
// container really does hold derived text, the taint stored into it was
// already `derived` and comes back out that way.
//
// Promotion at container reads is reserved for the case that IS a
// derivation: a tainted INDEX or KEY, which is fixtures R2, R3 and R5b --
// "which slot did the family choose" is family-derived information no
// matter what the slot holds.
func (a *familySSA) promoteUnless(result types.Type, t familyTaint) familyTaint {
	if t == familyTaintNone {
		return familyTaintNone
	}
	if a.isFamilyType(result) {
		return t
	}
	return familyTaintDerived
}

// tupleTaint reads one component of a multi-result call. With the uniform
// per-call-site rule there is no per-result precision to recover: if the
// call derived from the family, every result it handed back did.
func (a *familySSA) tupleTaint(tuple ssa.Value, i int) familyTaint {
	_ = i
	return a.val[tuple]
}

// calleesForSite resolves a call site to the function bodies this analysis
// owns. Static calls resolve directly; dynamic dispatch -- interface
// methods, method values, function values -- goes through the CHA call
// graph edges that originate at this exact instruction. CHA is sound: it
// over-approximates the candidate set rather than missing an
// implementation, which is the right direction of error for a gate.
func (a *familySSA) calleesForSite(caller *ssa.Function, site ssa.CallInstruction) []*ssa.Function {
	if fn := site.Common().StaticCallee(); fn != nil {
		if a.owned[fn] {
			return []*ssa.Function{fn}
		}
		return nil
	}
	node := a.cg.Nodes[caller]
	if node == nil {
		return nil
	}
	var out []*ssa.Function
	for _, e := range node.Out {
		if e.Site == site && e.Callee != nil && a.owned[e.Callee.Func] {
			out = append(out, e.Callee.Func)
		}
	}
	return out
}

// transferCall implements the rule the whole re-shape exists for.
//
//	ANY call with a tainted argument or receiver yields a tainted RESULT,
//	unless the callee resolves BY OBJECT IDENTITY to a sanctioned reader.
//
// There is no enumeration of call shapes: fmt.Sprintf, strings.EqualFold,
// a hand-written String() method, a regexp match and whatever is invented
// next are the same edge. When the callee's body is inside the swept
// roots, arguments flow into its parameters and its return taint flows
// back, so the result is precise rather than blanket-derived; when the
// callee is opaque (stdlib, or outside the roots) a tainted argument
// yields a derived result and nothing is claimed about its body.
func (a *familySSA) transferCall(caller *ssa.Function, b *ssa.BasicBlock, site ssa.CallInstruction, common ssa.CallCommon, result ssa.Value) {
	// An argument contributes its OWN taint. It does not contribute the
	// taint of whatever object it was loaded out of -- see writeMem.
	var argTaint familyTaint
	args := common.Args
	for _, arg := range args {
		argTaint = argTaint.join(a.val[arg])
	}
	if common.Value != nil {
		argTaint = argTaint.join(a.val[common.Value])
	}

	promotable := false
	for _, arg := range args {
		if a.isDirect(arg) {
			promotable = true
			break
		}
	}
	if !promotable && common.Value != nil {
		promotable = a.isDirect(common.Value)
	}

	callees := a.calleesForSite(caller, site)

	// SANCTIONED READERS. Identity, never name or file: a call whose
	// callee IS one of the four declared purposes returns clean and its
	// parameters are not seeded from here. This is the mechanism the
	// predecessor lane arrived at after codex R1-P2 showed that
	// "everything declared in a purpose FILE" is the same granularity as
	// a path allowlist -- an unrelated function added to a sanctioned
	// file is NOT exempt, because the exemption is on the declaration.
	for _, fn := range callees {
		if a.isSanctionedFunc(fn) {
			return
		}
	}
	if fn := common.StaticCallee(); fn != nil && a.isSanctionedFunc(fn) {
		return
	}

	if !argTaint.tainted() {
		return
	}

	var src ssa.Value
	for _, arg := range args {
		if a.val[arg].tainted() {
			src = arg
			break
		}
	}
	if src == nil && common.Value != nil {
		src = common.Value
	}

	// THE UNIFORM CALL RULE, applied PER CALL SITE.
	//
	// A call with a directly family-derived argument or receiver yields
	// derived results, whatever the callee is: fmt.Sprintf, a method
	// value, an interface dispatch, a closure, a function in this very
	// package. There is no enumeration of call shapes, which is the whole
	// reason this analysis is an IR analysis and not a syntax walk.
	//
	// WHY PER CALL SITE, AND NOT BY BINDING ARGUMENTS INTO PARAMETERS.
	// Binding arguments to the callee's parameters is more precise in
	// principle, but a summary analysis has no calling context, so ONE
	// caller passing a derived string to a general-purpose helper marks
	// that helper's parameter derived for EVERY caller, and the helper's
	// results come back derived everywhere. Measured on this tree, a
	// single family-dependent phi in graphrank reached a census-adapter
	// callback parameter that way and flooded the ClickHouse binding
	// layer: 990 findings, none of them a defect, the provenance chain
	// reading `argument bound to parameter` three times in a row through
	// functions that never see a family.
	//
	// Nothing is lost for functions that genuinely handle the family:
	// seeding is BY TYPE, so a parameter of family type is tainted in
	// every function that declares one, no matter who calls it -- which is
	// exactly how fixture R4's cross-function relay is found. What is
	// given up is a plain `string` parameter that is family-derived at
	// some call sites and not others; that value is reported at the CALL
	// SITE, where the derivation is visible, rather than inside a callee
	// that has no idea. Stated in the file header as a limit of the model.
	if result != nil && promotable {
		a.setVal(result, familyTaintDerived, src, "result of a call whose argument is family-derived: "+familySSACalleeName(common))
	}
}

func familySSACalleeName(common ssa.CallCommon) string {
	if fn := common.StaticCallee(); fn != nil {
		return fn.String()
	}
	if common.Method != nil {
		return common.Method.FullName()
	}
	if common.Value != nil {
		return common.Value.Name()
	}
	return "<dynamic>"
}

// ---------------------------------------------------------------------------
// sanctioning, by object identity
// ---------------------------------------------------------------------------

func (a *familySSA) isSanctionedFunc(fn *ssa.Function) bool {
	for f := fn; f != nil; f = f.Parent() {
		if obj := f.Object(); obj != nil && a.facts.sanctioned[obj] {
			return true
		}
	}
	return false
}

// isSanctionedTarget covers the one thing SSA changes about sanctioning:
// package-level var initializers do not live in a named function, they
// live in `init`. A store whose target global IS a sanctioned declaration
// (the registry table, the published-order vocabulary array) is therefore
// exempt on the target, not on the enclosing function. Granularity is
// unchanged -- still one named declaration at a time, resolved through
// go/types -- only the attribution is new.
func (a *familySSA) isSanctionedTarget(addr ssa.Value) bool {
	if g, ok := familySSAOrigin(addr).(*ssa.Global); ok {
		if obj := g.Object(); obj != nil && a.facts.sanctioned[obj] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// sinks
// ---------------------------------------------------------------------------

// sinkValueTaint is the value's own taint, plus the implicit-flow rule.
//
// The implicit-flow half is deliberately narrow in TWO independent ways,
// and both are calibration a future round should aim at first.
//
//  1. It fires only on a NON-EMPTY TEXT CONSTANT inside a block that runs
//     only under a family-dependent branch. That is exactly what R1 and R6
//     do -- test the family, then serve one of several hand-authored
//     sentences.
//  2. It is SINK-LOCAL: this reading is used to decide whether a store or
//     a return is a finding, and is never written back into the value
//     graph. Injecting it into memory instead (the first draft did) makes
//     implicit flow self-amplifying -- a constant served under a
//     family-dependent branch becomes a derived VALUE, which taints the
//     next call, which taints the next branch condition, which makes more
//     blocks control-dependent, monotonically, until most of the program
//     is derived. 2,662 findings on a tree with no defect in it. The one
//     implicit-flow rule that DOES propagate is the phi rule, and it is
//     bounded: it taints one merged value per join, not a region.
func (a *familySSA) sinkValueTaint(b *ssa.BasicBlock, v ssa.Value) familyTaint {
	if t := a.val[v]; t.tainted() {
		return t
	}
	if !a.ctrl[b].tainted() {
		return familyTaintNone
	}
	// CODEX ROUND 1, P1 (FIRST FINDING), EXECUTED AND RE-EXECUTED BY THE
	// LANE. The rule below used to require an ssa.Const, and this was
	// documented in the file header as the calibration a future evasion
	// would aim at. It was aimed at:
	//
	//	if family == SubjectInvestigation { d.NarrowerHint = hint() }
	//
	// where hint() takes NO arguments. Nothing carries a data dependence
	// on the family; the prose is selected purely by the branch; and the
	// selected value is not a constant, so the constant restriction saw
	// nothing at all.
	//
	// The widening is gated on ctrlDirect rather than ctrl: the guarding
	// condition must test the family ITSELF (or text directly derived
	// from it), not merely something that carries the taint. Under that
	// stricter guard ANY text-typed value counts, constant or not.
	// Ungated -- "everything under any tainted branch is derived" -- is
	// the label creep that took an earlier draft of this analysis to
	// thousands of findings on a tree with no defect in it.
	if a.ctrlDirect[b] && familySSAIsServableText(v.Type()) {
		if c, ok := v.(*ssa.Const); ok {
			if familySSANonEmptyText(c) {
				return familyTaintDerived
			}
			return familyTaintNone
		}
		return familyTaintDerived
	}
	if c, ok := v.(*ssa.Const); ok && familySSANonEmptyText(c) {
		return familyTaintDerived
	}
	return familyTaintNone
}

func familySSANonEmptyText(c *ssa.Const) bool {
	if c == nil || c.Value == nil {
		return false
	}
	if !familyGateMayHoldText(c.Type()) {
		return false
	}
	s := c.Value.ExactString()
	// ExactString on a string constant includes the quotes.
	return len(s) > 2 && s != `""`
}

// isDirect distinguishes "the family itself was handed over" from "a
// container that happens to hold one was handed over". It is the whole
// difference between fixture R7 and the wire contract working correctly.
//
//	fmt.Sprintf("The family is %s.", family)   -> direct   (R7: a defect)
//	json.Marshal(investigationResult)          -> indirect (the contract)
//
// Both reach an opaque callee with a tainted argument. Only the first is
// a derivation of TEXT FROM the family; the second serializes a declared
// closed token, which is what the token is for. Without this distinction
// the opaque-call rule reported 1,106 findings on a clean tree, because
// this codebase legitimately hangs the family off long-lived structs.
//
// The MakeInterface and Slice cases are not special-pleading: they are
// how Go compiles a variadic call. `Sprintf("%s", family)` packs the
// family into an `[1]any` through MakeInterface and passes a slice of it,
// so an analysis that only looked at the argument LIST would see a
// []any and never the family. memDirect carries the answer across that
// packing.
func (a *familySSA) isDirect(v ssa.Value) bool {
	return a.isDirectAt(v, 0)
}

func (a *familySSA) isDirectAt(v ssa.Value, depth int) bool {
	if v == nil || depth > 8 {
		return false
	}
	if a.val[v] == familyTaintDerived {
		return true
	}
	if !a.val[v].tainted() {
		return false
	}
	if a.isFamilyType(v.Type()) {
		return true
	}
	switch x := v.(type) {
	case *ssa.MakeInterface:
		return a.isDirectAt(x.X, depth+1)
	case *ssa.ChangeType:
		return a.isDirectAt(x.X, depth+1)
	case *ssa.Slice:
		return a.memDirect[familySSAOrigin(x.X)]
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return a.memDirect[familySSAOrigin(x.X)]
		}
	}
	return false
}

// familySSAIsError reports whether a type IS the error interface.
//
// Errors are excluded from the SINK rule (never from propagation). The
// served error body is a declared contract -- contractsv1.ErrorEnvelope,
// built from a closed code, a message and a details map -- and any
// family-derived text that reaches it does so through writeError's
// message/details ARGUMENTS, which are string and map[string]any and
// remain sinks. A Go `error` value itself is plumbing: it is not written
// to the wire, and treating it as served text made every
// `fmt.Errorf("...%v", err)` on a path that had ever seen a family into a
// finding, which is precisely the "gate that fires on correct code" that
// gets a gate switched off.
func familySSAIsError(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() == nil && obj.Name() == "error"
}

// familySSAIsServableText is the sink-side text predicate: is THIS value
// text, structurally?
//
// It is a positive, structural test -- a string underlying type (so a
// named `type phrase string`, fixture R2's evasion, is text without its
// name being consulted), a []byte, an interface that is not `error`, or a
// slice/array/map OF those. It is deliberately NOT the inherited
// fail-closed familyGateMayHoldText, which answers a different question
// ("could this possibly carry text?") and answers `true` for every struct
// and pointer.
//
// Using fail-closed here reported `func(...) QuestionFamilyOutcome` as
// "returns family-derived text" because a struct is not provably
// non-textual. That is not a missed defect, it is a misplaced one: if a
// struct carries family-derived text, the STORE into the text-typed field
// is the sink, and that store is checked. Narrowing the predicate moves
// the report to where the text actually is; it does not lose coverage,
// and all nine fixtures stay red under it.
func familySSAIsServableText(t types.Type) bool {
	return familySSAIsTextType(t, 0)
}

func familySSAIsTextType(t types.Type, depth int) bool {
	if t == nil || depth > 4 || familySSAIsError(t) {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Info()&types.IsString != 0
	case *types.Interface:
		// any / fmt.Stringer / a domain interface: an interface value can
		// carry a string, and `details map[string]any` is a real wire
		// surface on the error envelope.
		return true
	case *types.Slice:
		// []byte IS text: it is what every byte-writing boundary takes,
		// and `w.Write([]byte(prose))` is the most direct way prose
		// reaches the wire. Missing this made the R11 fix a no-op on its
		// first attempt -- the boundary was recognised and the value
		// arriving at it was then judged non-textual.
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return true
		}
		return familySSAIsTextType(u.Elem(), depth+1)
	case *types.Array:
		return familySSAIsTextType(u.Elem(), depth+1)
	case *types.Map:
		return familySSAIsTextType(u.Elem(), depth+1)
	default:
		return false
	}
}

func (a *familySSA) checkStoreSink(fn *ssa.Function, b *ssa.BasicBlock, x *ssa.Store) {
	if a.isSanctionedFunc(fn) || a.isSanctionedTarget(x.Addr) {
		return
	}
	t := a.sinkValueTaint(b, x.Val)
	if t != familyTaintDerived {
		return
	}
	if !familySSAIsServableText(x.Val.Type()) {
		return
	}
	field := familySSAFieldOf(x.Addr)
	switch {
	case field != nil:
		a.record(x.Pos(), fn, x.Val, "store-into-field",
			fmt.Sprintf("family-derived text stored into field %s of %s", field.Name(), familySSAOwnerName(x.Addr)),
			field, x.Addr.Type())
	case familySSAIsGlobal(x.Addr):
		a.record(x.Pos(), fn, x.Val, "store-into-global",
			fmt.Sprintf("family-derived text stored into package-level %s", familySSAOrigin(x.Addr).Name()),
			nil, x.Val.Type())
	}
}

func (a *familySSA) checkMapUpdateSink(fn *ssa.Function, b *ssa.BasicBlock, x *ssa.MapUpdate) {
	if a.isSanctionedFunc(fn) || a.isSanctionedTarget(x.Map) {
		return
	}
	t := a.sinkValueTaint(b, x.Value)
	if t != familyTaintDerived || !familySSAIsServableText(x.Value.Type()) {
		return
	}
	if !familySSAIsGlobal(x.Map) {
		return
	}
	a.record(x.Pos(), fn, x.Value, "store-into-global-map",
		fmt.Sprintf("family-derived text stored into package-level map %s", familySSAOrigin(x.Map).Name()),
		nil, x.Value.Type())
}

func (a *familySSA) checkSendSink(fn *ssa.Function, b *ssa.BasicBlock, x *ssa.Send) {
	if a.isSanctionedFunc(fn) {
		return
	}
	t := a.sinkValueTaint(b, x.X)
	if t != familyTaintDerived || !familySSAIsServableText(x.X.Type()) {
		return
	}
	a.record(x.Pos(), fn, x.X, "channel-send",
		"family-derived text sent on a channel", nil, x.X.Type())
}

func (a *familySSA) checkReturnSink(fn *ssa.Function, b *ssa.BasicBlock, x *ssa.Return) {
	if a.isSanctionedFunc(fn) {
		return
	}
	for _, r := range x.Results {
		t := a.sinkValueTaint(b, r)
		if t != familyTaintDerived || !familySSAIsServableText(r.Type()) {
			continue
		}
		a.record(x.Pos(), fn, r, "return-text",
			fmt.Sprintf("%s returns family-derived text", fn.String()),
			nil, r.Type())
	}
}

// familySSAValuePos resolves a source position for a value, falling back
// through the value's own operands to its enclosing function.
//
// NOT COSMETIC. Several SSA instructions carry no position at all --
// MakeInterface, in particular, which is what a variadic or interface-typed
// argument compiles to, and therefore what sits at a boundary in half the
// byte-egress fixtures. A finding built on token.NoPos formats as "-",
// which means: it cannot be attributed to a file, and TWO such findings
// from different packages are byte-identical and collapse into one in
// collectFindings' dedupe.
//
// That is exactly what happened the first time the sixteen fixtures were
// analysed as ONE program: R12a's io.Copy finding and R12c's
// template.Execute finding both had no position, deduped into a single
// unattributable finding, and both fixtures read as producing ZERO
// findings in their own package. The old harness could not see it --
// each fixture was its own program, so there was never a second
// position-less finding to collide with. One program made a latent bug
// in this analysis observable, which is the better reason for the change
// than the wall time was.
func familySSAValuePos(v ssa.Value, fn *ssa.Function) token.Pos {
	if v != nil && v.Pos().IsValid() {
		return v.Pos()
	}
	// Boxing and conversions are the usual position-less wrappers; their
	// operand is the expression a reader actually wrote.
	switch x := v.(type) {
	case *ssa.MakeInterface:
		if x.X.Pos().IsValid() {
			return x.X.Pos()
		}
	case *ssa.ChangeType:
		if x.X.Pos().IsValid() {
			return x.X.Pos()
		}
	case *ssa.Convert:
		if x.X.Pos().IsValid() {
			return x.X.Pos()
		}
	case *ssa.Slice:
		if x.X.Pos().IsValid() {
			return x.X.Pos()
		}
	}
	if fn != nil {
		return fn.Pos()
	}
	return token.NoPos
}

func familySSAIsGlobal(v ssa.Value) bool {
	_, ok := familySSAOrigin(v).(*ssa.Global)
	return ok
}

func familySSAOwnerName(addr ssa.Value) string {
	if fa, ok := addr.(*ssa.FieldAddr); ok {
		return fa.X.Type().String()
	}
	return addr.Type().String()
}

type familySSARecord struct {
	pos    token.Pos
	fn     *ssa.Function
	val    ssa.Value
	rule   string
	detail string
	// field and typ are what served-reachability is decided FROM. They are
	// kept raw and resolved in collectFindings, never at record time: the
	// served set is computed after the taint fixed point has converged, so
	// a record that decided its own served flag while the fixed point was
	// still running always decided `false`.
	field *types.Var
	typ   types.Type
}

func (a *familySSA) record(pos token.Pos, fn *ssa.Function, v ssa.Value, rule, detail string, field *types.Var, typ types.Type) {
	a.records = append(a.records, familySSARecord{pos: pos, fn: fn, val: v, rule: rule, detail: detail, field: field, typ: typ})
}

// checkServingPositionSinks catches derived text handed straight to the
// serialization boundary, with no struct field in between --
// json.Marshal(phrase) rather than json.Marshal(struct{F: phrase}).
func (a *familySSA) checkServingPositionSinks() {
	for v := range a.egressValues {
		// NO TEXT-TYPE TEST. Anything family-derived that arrives at a
		// boundary is a defect whatever its Go type -- the boundary exists
		// to turn it into bytes.
		//
		// A correction worth keeping, because the first version of this
		// comment was wrong about its own example. It claimed the text
		// test was "the only thing discarding" R12a's
		// `io.Copy(w, strings.NewReader(prose))`. It was not: io.Copy's
		// parameter is io.Reader, so the value arrives interface-typed and
		// the text predicate accepts every interface. A mutation that
		// restored the text test turned NO fixture green -- which is how
		// the bad reasoning was caught. Fixture R12e now pins the rule
		// properly, with the payload arriving as a concrete
		// *strings.Reader that no structural text test can see.
		if a.val[v] != familyTaintDerived {
			continue
		}
		instr, ok := v.(ssa.Instruction)
		if !ok {
			continue
		}
		fn := instr.Parent()
		if fn == nil || a.isSanctionedFunc(fn) {
			continue
		}
		a.record(familySSAValuePos(v, fn), fn, v, "serving-position",
			"family-derived text handed directly to the serialization boundary", nil, v.Type())
	}
}

// recordServed decides served-reachability from the OWNER of the sink --
// the struct type the field belongs to, or the type of the value returned
// or stored -- against the set derived from the encoder boundary.
func (a *familySSA) recordServed(r familySSARecord) bool {
	if r.rule == "serving-position" {
		// By construction: this value WAS handed to a boundary.
		return true
	}
	if r.field != nil {
		if st, ok := familySSADerefStructType(r.typ); ok {
			return a.servedTypes[st]
		}
		return false
	}
	return a.typeServed(r.typ)
}

func familySSADerefStructType(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	if _, ok := t.Underlying().(*types.Struct); !ok {
		return "", false
	}
	return t.String(), true
}

func (a *familySSA) collectFindings() []familyTaintFinding {
	seen := map[string]bool{}
	var out []familyTaintFinding
	for _, r := range a.records {
		pos := a.fset.Position(r.pos)
		f := familyTaintFinding{
			pos:      pos,
			rule:     r.rule,
			detail:   r.detail,
			served:   a.recordServed(r),
			enforced: a.recordServed(r) && (r.rule == "store-into-field" || r.rule == "serving-position"),
			path:     a.provenance(r.val),
		}
		key := f.pos.String() + "|" + f.rule + "|" + f.detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].pos.Filename != out[j].pos.Filename {
			return out[i].pos.Filename < out[j].pos.Filename
		}
		if out[i].pos.Line != out[j].pos.Line {
			return out[i].pos.Line < out[j].pos.Line
		}
		return out[i].rule < out[j].rule
	})
	return out
}

// provenance walks the why-chain back to the seed so a reviewer reads the
// derivation instead of re-deriving it. Reporting the path is not a
// nicety: a taint result nobody can check is a taint result nobody will
// act on.
func (a *familySSA) provenance(v ssa.Value) []string {
	var out []string
	seen := map[ssa.Value]bool{}
	for cur := v; cur != nil && !seen[cur]; {
		seen[cur] = true
		edge, ok := a.why[cur]
		if !ok {
			break
		}
		line := fmt.Sprintf("%s: %s", familySSAValueLabel(a, cur), edge.rule)
		out = append(out, line)
		if edge.from == nil {
			break
		}
		cur = edge.from
	}
	// seed -> ... -> sink reads better than sink -> ... -> seed.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func familySSAValueLabel(a *familySSA, v ssa.Value) string {
	pos := a.fset.Position(v.Pos())
	name := v.Name()
	if pos.IsValid() {
		return fmt.Sprintf("%s (%s:%d)", name, pos.Filename, pos.Line)
	}
	return name
}

// ---------------------------------------------------------------------------
// served reachability -- COMPUTED, never a type-name list
// ---------------------------------------------------------------------------

// computeServed derives which types actually reach the wire, rather than
// naming them. The roots are the argument positions of the encoding/json
// serialization boundary; a parameter that flows into such a position is
// itself a serving position, closed to a fixed point over call sites. That
// derivation picks up internal/api.writeJSON's `value any` and
// writeError's `details map[string]any`, and every json.Marshal site in
// internal/mcp, with no name written down anywhere.
func (a *familySSA) computeServed() {
	a.servedTypes = map[string]bool{}
	a.servingValues = map[ssa.Value]bool{}
	a.egressValues = map[ssa.Value]bool{}

	// The backward walk is deliberately SHORT: through interface boxing,
	// and through a parameter to its call sites. It does NOT chase a
	// call's result back into every possible callee's return statements.
	// Doing that (the first draft did) made every value returned by any
	// function that a serving position could ever reach a served type --
	// 429 types and 1,060 fields, which is most of the tree, and a served
	// set that large answers "is this served?" with yes for everything and
	// so answers nothing.
	work := map[ssa.Value]bool{}
	var order []ssa.Value
	push := func(v ssa.Value) {
		if v == nil || work[v] {
			return
		}
		work[v] = true
		order = append(order, v)
	}

	for _, fn := range a.funcs {
		marshaller := familySSAIsMarshalMethod(fn)
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if marshaller {
					if ret, ok := instr.(*ssa.Return); ok && len(ret.Results) > 0 {
						push(ret.Results[0])
						a.egressValues[ret.Results[0]] = true
					}
				}
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				common := call.Common()
				for _, arg := range a.servingArgs(common) {
					push(arg)
				}
				// EGRESS is the strict subset where bytes LEAVE.
				//
				// encoding/json.Marshal PRODUCES bytes; it does not send
				// them anywhere. Treating it as egress reported
				// `canonicalSampleKey`, which json.Marshals a sample to
				// build an internal sort key, as text reaching the wire --
				// a false positive on one of the two files the acceptance
				// criteria name as known-clean. A writer is where bytes
				// leave; a marshaller's results leave by contract, since
				// encoding/json emits them wherever the type is encoded.
				// The encoding family still seeds the served TYPE set: it
				// says what SHAPE is served, which is a different question
				// from where bytes go.
				for _, arg := range a.egressArgs(common) {
					a.egressValues[arg] = true
				}
			}
		}
	}

	for i := 0; i < len(order); i++ {
		v := order[i]
		switch x := v.(type) {
		case *ssa.MakeInterface:
			a.markServedType(x.X.Type())
			push(x.X)
			continue
		case *ssa.Parameter:
			// A parameter at a serving position makes every argument
			// passed to it a serving value too -- this is what turns
			// writeJSON/writeError into boundaries without naming them.
			a.markServedType(x.Type())
			idx := -1
			for j, p := range x.Parent().Params {
				if p == x {
					idx = j
					break
				}
			}
			if idx >= 0 {
				if node := a.cg.Nodes[x.Parent()]; node != nil {
					for _, e := range node.In {
						if e.Site == nil {
							continue
						}
						args := e.Site.Common().Args
						off := 0
						if e.Site.Common().IsInvoke() {
							off = 1
						}
						if k := idx - off; k >= 0 && k < len(args) {
							push(args[k])
						}
					}
				}
			}
			continue
		}
		a.markServedType(v.Type())
	}

	a.servingValues = work

	// Close the served type set over structure: a field of a served type
	// is served, and so is everything reachable through pointers, slices,
	// arrays and maps.
	a.closeServedTypes()
}

// assertServedSetIsRealistic is the non-vacuity check on the ENFORCED
// tier. The served set is derived, not written down, so a change to how
// the API serializes could silently empty it -- and an empty served set
// makes the enforced tier pass on everything, which would read as
// "green" while proving nothing. These three types are the answer
// projection, the error envelope and the canonical result: if the
// derivation cannot find them, it has stopped working and the gate says
// so instead of passing.
func (a *familySSA) assertServedSetIsRealistic() {
	const contracts = "github.com/full-chaos/dev-health-acr/internal/contracts/v1."
	anchors := []string{
		contracts + "ContextFabricAnswerProjection",
		contracts + "ErrorEnvelope",
		contracts + "ContextFabricInvestigationResult",
	}
	var missing []string
	for _, anchor := range anchors {
		if !a.servedTypes[anchor] {
			missing = append(missing, anchor)
		}
	}
	if len(missing) > 0 {
		a.t.Fatalf("the served-type derivation reached %d types but NOT %v -- it walks from the encoding/json boundary back through serving parameters, so this means that walk broke, and the enforced tier would pass vacuously",
			len(a.servedTypes), missing)
	}
}

// servingArgs returns the arguments of a call that CROSS THE WIRE, or nil
// if the call is not a boundary.
//
// A boundary is a PROPERTY, not a list. Two families:
//
//  1. ENCODING — encoding/json's Marshal, MarshalIndent and Encoder.Encode,
//     plus any MarshalJSON/MarshalText method, whose RESULTS are what the
//     encoder puts on the wire even though our own code never calls it.
//  2. BYTE WRITING — ANY method call whose receiver is writer-shaped, and
//     any function whose first parameter is writer-shaped. Writer-shape is
//     structural: a Write([]byte) (int, error) method.
//
// THE HISTORY HERE IS THE POINT, AND IT IS THIS LANE'S OWN MISTAKE.
// Round 1 found that this function knew only `encoding/json`, so a plain
// `w.Write([]byte(prose))` was invisible. It was fixed by adding a byte
// family that matched a method named exactly `Write`. Round 2 then found
// TWO more members of that same family: `io.Copy(w, strings.NewReader(p))`
// and `bufio.Writer.WriteString(p)`. Same class, consecutive rounds, the
// previous fix being where the next defect landed.
//
// The diagnosis was not "two cases missed". The propagation half of this
// analysis was moved off per-shape enumeration and onto a uniform IR rule
// -- that was the whole ticket -- but the SINK SURFACE was left as an
// enumeration, and "every way bytes leave a Go process" is not a closed
// set: Write, WriteString, ReadFrom, io.Copy, Fprintf, template.Execute,
// ServeContent, a custom marshaller. That is the walker's failure mode
// reproduced one level up, inside the fix for it.
//
// So the rules below were written by DELETING conditions, not adding
// cases: no method-name test (any method on a writer writes), and no
// text-type test at the sink (see checkServingPositionSinks). Both
// deletions make the surface a property. If a future round still finds a
// byte-egress path, the surface is genuinely unbounded and the enforced
// claim should be narrowed to encoder-reachable field stores rather than
// patched a third time.
func (a *familySSA) servingArgs(common *ssa.CallCommon) []ssa.Value {
	args := common.Args

	// Family 1: encoding.
	if fn := common.StaticCallee(); fn != nil {
		if obj := fn.Object(); obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "encoding/json" {
			switch obj.Name() {
			case "Marshal", "MarshalIndent", "Encode":
				if len(args) > 0 {
					return args[:1]
				}
			}
		}
	}
	if common.Method != nil && common.Method.Pkg() != nil &&
		common.Method.Pkg().Path() == "encoding/json" && common.Method.Name() == "Encode" {
		if len(args) > 0 {
			return args[:1]
		}
	}

	// Family 2: byte writing. ANY method on a writer-shaped receiver.
	if common.Method != nil && a.isWriterShaped(common.Value.Type()) {
		return args
	}
	fn := common.StaticCallee()
	if fn == nil || fn.Signature == nil {
		return nil
	}
	// A method on a CONCRETE writer (bufio.Writer.WriteString, and every
	// other write-ish method on it) arrives as a static call whose args[0]
	// is the receiver.
	recvOffset := 0
	if fn.Signature.Recv() != nil {
		recvOffset = 1
		if len(args) > 0 && a.isWriterShaped(args[0].Type()) {
			return args[1:]
		}
	}
	// A function whose FIRST PARAMETER is a writer: io.Copy, io.WriteString,
	// fmt.Fprintf, template.Execute, and any helper of that shape.
	if params := fn.Signature.Params(); params.Len() > 0 &&
		a.isWriterShaped(params.At(0).Type()) {
		if len(args) > recvOffset+1 {
			return args[recvOffset+1:]
		}
	}
	return nil
}

// egressArgs returns the arguments at which bytes actually LEAVE: the
// byte-writing family only. It is deliberately the writer half of
// servingArgs and nothing else.
func (a *familySSA) egressArgs(common *ssa.CallCommon) []ssa.Value {
	args := common.Args
	if common.Method != nil {
		if a.isWriterShaped(common.Value.Type()) {
			return args
		}
		if common.Method.Pkg() != nil && common.Method.Pkg().Path() == "encoding/json" &&
			common.Method.Name() == "Encode" && len(args) > 0 {
			// Encoder.Encode writes to the writer it was built with.
			return args[:1]
		}
		return nil
	}
	fn := common.StaticCallee()
	if fn == nil || fn.Signature == nil {
		return nil
	}
	recvOffset := 0
	if fn.Signature.Recv() != nil {
		recvOffset = 1
		if len(args) > 0 && a.isWriterShaped(args[0].Type()) {
			return args[1:]
		}
	}
	if params := fn.Signature.Params(); params.Len() > 0 &&
		a.isWriterShaped(params.At(0).Type()) {
		if len(args) > recvOffset+1 {
			return args[recvOffset+1:]
		}
	}
	return nil
}

// familySSAIsMarshalMethod reports whether fn is a custom marshaller whose
// RESULTS go to the wire. Our code never calls it -- encoding/json does --
// so its returns are a boundary in their own right.
func familySSAIsMarshalMethod(fn *ssa.Function) bool {
	obj := fn.Object()
	if obj == nil || fn.Signature == nil || fn.Signature.Recv() == nil {
		return false
	}
	switch obj.Name() {
	case "MarshalJSON", "MarshalText":
	default:
		return false
	}
	return fn.Signature.Params().Len() == 0 && fn.Signature.Results().Len() == 2
}

// familySSAIsWriterShaped reports whether a type has a Write([]byte)
// (int, error) method -- io.Writer's shape, decided by structure so that
// any writer, standard or domain-specific, is a boundary.
func (a *familySSA) isWriterShaped(t types.Type) bool {
	if t == nil {
		return false
	}
	key := t.String()
	if cached, ok := a.writerShape[key]; ok {
		return cached
	}
	shaped := familySSAIsWriterShaped(t)
	a.writerShape[key] = shaped
	return shaped
}

func familySSAIsWriterShaped(t types.Type) bool {
	if t == nil {
		return false
	}
	ms := types.NewMethodSet(t)
	if m := familySSALookupWrite(ms); m != nil {
		return true
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		return familySSALookupWrite(types.NewMethodSet(p.Elem())) != nil
	}
	return false
}

func familySSALookupWrite(ms *types.MethodSet) *types.Func {
	for i := 0; i < ms.Len(); i++ {
		sel := ms.At(i)
		fn, ok := sel.Obj().(*types.Func)
		if !ok || fn.Name() != "Write" {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Params().Len() != 1 || sig.Results().Len() != 2 {
			continue
		}
		sl, ok := sig.Params().At(0).Type().Underlying().(*types.Slice)
		if !ok {
			continue
		}
		b, ok := sl.Elem().Underlying().(*types.Basic)
		if !ok || b.Kind() != types.Byte {
			continue
		}
		return fn
	}
	return nil
}

func (a *familySSA) markServedType(t types.Type) {
	if t == nil {
		return
	}
	a.servedTypes[t.String()] = true
}

func (a *familySSA) closeServedTypes() {
	// Re-walk every named struct type in the program; a struct whose name
	// is already served contributes its fields.
	for round := 0; round < 8; round++ {
		grew := false
		for _, fn := range a.funcs {
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					v, ok := instr.(ssa.Value)
					if !ok {
						continue
					}
					if a.expandServed(v.Type()) {
						grew = true
					}
				}
			}
		}
		if !grew {
			return
		}
	}
}

func (a *familySSA) expandServed(t types.Type) bool {
	if t == nil || !a.servedTypes[t.String()] {
		return false
	}
	grew := false
	var walk func(types.Type, int)
	walk = func(tt types.Type, depth int) {
		if tt == nil || depth > 6 {
			return
		}
		switch u := tt.Underlying().(type) {
		case *types.Pointer:
			if !a.servedTypes[u.Elem().String()] {
				a.servedTypes[u.Elem().String()] = true
				grew = true
			}
			walk(u.Elem(), depth+1)
		case *types.Slice:
			if !a.servedTypes[u.Elem().String()] {
				a.servedTypes[u.Elem().String()] = true
				grew = true
			}
			walk(u.Elem(), depth+1)
		case *types.Array:
			walk(u.Elem(), depth+1)
		case *types.Map:
			if !a.servedTypes[u.Elem().String()] {
				a.servedTypes[u.Elem().String()] = true
				grew = true
			}
			walk(u.Elem(), depth+1)
		case *types.Struct:
			for i := 0; i < u.NumFields(); i++ {
				f := u.Field(i)
				if !a.servedTypes[f.Type().String()] {
					a.servedTypes[f.Type().String()] = true
					grew = true
				}
				walk(f.Type(), depth+1)
			}
		}
	}
	walk(t, 0)
	return grew
}

func (a *familySSA) typeServed(t types.Type) bool {
	if t == nil {
		return false
	}
	return a.servedTypes[t.String()]
}
