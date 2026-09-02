package contextfabric

import (
	"reflect"
	"strings"
	"testing"
)

// THE SWEEP IS GENERATED, NOT LISTED.
//
// Four review rounds found one defect at four depths because the bound and
// its sweep were both written per level: each level had to be
// hand-enumerated, so the next level down was always missing. A hand-listed
// axis table is banned in this lane for exactly that reason. These tests
// quantify over `FramePaths()`, which is derived by reflection over the
// frame's type tree — so a new field, or a new level of nesting, is swept
// the moment it exists, with no list to update and nobody to remember.

// TestSweepCoversEveryPathOfTheFrameTree is the generated sweep: for every
// invariant × every path, the bound's verdict must match what the
// invariant's own Constrains declaration says.
//
// This replaces the hand-written axis table. Its value is not the rows it
// checks today but the rows it will check tomorrow without being edited.
func TestSweepCoversEveryPathOfTheFrameTree(t *testing.T) {
	paths := FramePaths()
	if len(paths) < 20 {
		t.Fatalf("the derived path set has %d entries, which is too few to be the whole tree — reflection is not walking it", len(paths))
	}

	// Every path an invariant constrains must be a real path, and every
	// constrained path must be REACHABLE by the bound's own lookup. A
	// path that exists in the declaration but not in the tree constrains
	// nothing and silently widens the bound.
	declared := map[FramePath]bool{}
	for _, path := range paths {
		declared[path] = true
	}
	for _, spec := range FrameInvariantSpecs() {
		for _, constrained := range spec.Constrains {
			if !declared[constrained] {
				t.Errorf("invariant %q constrains %q, absent from the derived tree", spec.ID, constrained)
			}
		}
	}

	// The sweep proper: for each phase-A invariant, every path is either
	// constrained (a repair may change it) or not (it may not), and the
	// two relations must agree with the path grammar rather than with
	// anyone's recollection.
	for _, spec := range FrameInvariantSpecs() {
		if spec.Phase != FrameValidationPhaseA1 && spec.Phase != FrameValidationPhaseA2 {
			continue
		}
		for _, path := range paths {
			constrained := FrameInvariantConstrainsPath(spec, path)
			if !constrained {
				continue
			}
			// A constrained path must be the declared path itself or a
			// descendant of one — never a sibling reached by a sloppy
			// prefix match ("a.b" must not cover "a.bc").
			var covered bool
			for _, declaredPath := range spec.Constrains {
				if declaredPath == path || strings.HasPrefix(string(path), string(declaredPath)+".") {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("invariant %q reports it constrains %q, but no declared path covers it", spec.ID, path)
			}
		}
	}
}

// TestListPermissionNeverReachesElementContents is round 4's defect stated
// as a PROPERTY over the tree rather than as one case.
//
// For every slice-of-struct path, constraining the LIST must not constrain
// anything INSIDE an element. This is the distinction that took four
// rounds to find, and it is now a property of the path grammar — so it
// holds for a nested structure nobody has added yet.
func TestListPermissionNeverReachesElementContents(t *testing.T) {
	structures := FrameStructurePaths()
	if len(structures) == 0 {
		t.Fatal("no slice-of-struct paths found — the frozen-structure rule would be vacuous")
	}
	for _, elementPath := range structures {
		listPath := FramePath(strings.TrimSuffix(string(elementPath), framePathElementMarker))
		if PathConstrainedBy(listPath, elementPath) {
			t.Errorf("constraining the list %q also constrains its elements %q — that collapse is the round-4 defect", listPath, elementPath)
		}
		for _, path := range FramePaths() {
			if !strings.HasPrefix(string(path), string(elementPath)+".") {
				continue
			}
			if PathConstrainedBy(listPath, path) {
				t.Errorf("constraining the list %q also constrains element field %q", listPath, path)
			}
			if !PathConstrainedBy(elementPath, path) {
				t.Errorf("constraining the elements %q does NOT cover element field %q — I19's repair would be unreachable", elementPath, path)
			}
		}
	}
}

// TestEveryStructurePathDeclaresWellFormedness. An unregistered structure
// defaults to FROZEN, which is the safe direction — but it should be a
// DECISION, not an accident. This fails when a new nested structure
// appears with nobody having said what well-formed means for it.
func TestEveryStructurePathDeclaresWellFormedness(t *testing.T) {
	for _, path := range FrameStructurePaths() {
		if _, ok := wellFormedPredicates[path]; !ok {
			t.Errorf("structure path %q has no well-formedness predicate — it defaults to FROZEN, which is safe but must be chosen deliberately; register one or add it to this test's accepted list with a reason", path)
		}
	}
}

// zzSweepNestedProbe and zzSweepProbeFrame are a TEST-ONLY copy of a
// slice of the type tree, one level deeper than anything the real frame
// has.
//
// They exist for the mutation the orchestrator asked for: prove the sweep's
// path set GROWS when the type tree gains a nested field, WITHOUT any code
// change to the path derivation. If this ever needs editing to keep the
// sweep working, the sweep has stopped being tree-generic and the whole
// four-round lesson has been undone.
type zzSweepNestedProbe struct {
	Terms      []string    `json:"terms"`
	MemberKind SubjectKind `json:"member_kind"`
}

type zzSweepProbeOperand struct {
	Kind   string              `json:"kind"`
	Nested *zzSweepNestedProbe `json:"nested,omitempty"`
}

type zzSweepProbeExpression struct {
	Kind     string                `json:"kind"`
	Operands []zzSweepProbeOperand `json:"operands,omitempty"`
}

type zzSweepProbeFrame struct {
	Goals             []InvestigationGoal    `json:"goals"`
	SubjectExpression zzSweepProbeExpression `json:"subject_expression"`
}

// TestPathDerivationGrowsWithTheTypeTree is the mutation proof.
//
// The probe type above nests ONE level deeper than the real frame
// (`subject_expression.operands[].nested.terms`). The derivation walks it
// and produces that path with no code change — which is the property that
// makes "the next level down is always missing" impossible rather than
// merely unlikely.
func TestPathDerivationGrowsWithTheTypeTree(t *testing.T) {
	paths := map[FramePath]bool{}
	collectTypePaths(reflect.TypeOf(zzSweepProbeFrame{}), "", paths, map[reflect.Type]bool{})

	for _, want := range []FramePath{
		"goals",
		"subject_expression",
		"subject_expression.kind",
		"subject_expression.operands",
		"subject_expression.operands[]",
		"subject_expression.operands[].kind",
		"subject_expression.operands[].nested",
		// THE DEEP ONE. Nothing in the derivation knows this field, this
		// type, or this depth exists.
		"subject_expression.operands[].nested.terms",
		"subject_expression.operands[].nested.member_kind",
	} {
		if !paths[want] {
			t.Errorf("derived path set is missing %q — the derivation is not tree-generic, so a new nesting level would be a leaf nobody sweeps", want)
		}
	}

	// And the list/element distinction holds at the NEW depth too, without
	// anyone having written a rule for it.
	if PathConstrainedBy("subject_expression.operands", "subject_expression.operands[].nested.terms") {
		t.Error("constraining the probe's operand list reaches into its elements at the new depth — the grammar is not depth-generic")
	}
	if !PathConstrainedBy("subject_expression.operands[]", "subject_expression.operands[].nested.terms") {
		t.Error("constraining the probe's operand elements does not reach their own nested fields")
	}
}
