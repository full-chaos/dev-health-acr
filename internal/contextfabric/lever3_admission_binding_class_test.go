package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// This file closes the CLASS behind codex round 3's first High finding, not
// the instance.
//
// THE DEFECT. synthesisSubjects admitted Paths[].Edges[].From/To as citable;
// canonicalSubjectLabels bound only Paths[].Nodes. requireBoundLabel is
// deliberately a no-op for an id it has no binding for -- an unbound id is
// out of bounds and the caller's membership check has already rejected it --
// so an id that IS admitted and NOT bound passes both gates under whatever
// label the model wrote. A real, in-bounds edge endpoint could therefore be
// cited under an arbitrary FALSE label and copied into the served result
// verbatim: CHAOS-3755 H3 reopened.
//
// THE CLASS. Admission and label binding are two halves of one decision that
// lived in two hand-maintained lists. The branch split them three times --
// groups (caught before merge), the payload census (caught by round 2), and
// edge endpoints (caught by round 3, after a test had already been written
// for the groups instance of the same pairing). Fixing the third instance
// would leave the fourth.
//
// So the fix is structural: forEachCitableSynthesisSubject is the single
// admission walk and both consumers derive from it. These three tests pin
// that from three independent directions -- the served behaviour
// (TestForgedLabelOnAnEdgeOnlySubjectIsRejected), the invariant over every
// admission source (TestEveryAdmittedSynthesisSubjectIsLabelBound), and the
// structure that makes the invariant hold for sources not yet written
// (TestSubjectAdmissionAndLabelBindingShareOneWalk).

// TestForgedLabelOnAnEdgeOnlySubjectIsRejected is the round-3 finding's own
// concrete input, verbatim: the endpoint-only fixture, with work_edge_only
// appearing ONLY in Edges[].To, cited by a valid relationship driver under a
// label no source carries.
//
// RED at the fix parent (3e7bbeed) with ValidateAgainst returning nil -- the
// forged label is accepted and served. GREEN here.
func TestForgedLabelOnAnEdgeOnlySubjectIsRejected(t *testing.T) {
	t.Parallel()
	input, draft, endpoint := edgeEndpointOnlyInput()
	forged := endpoint
	forged.Label = "Arbitrary false label"
	draft.Drivers[0].AffectedSubjects = []SubjectRef{forged}
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"

	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil: an edge endpoint is citable but its label is not bound, so a real in-bounds subject was served under a forged name")
	}
	if !strings.Contains(err.Error(), "label that does not match the investigation input") {
		t.Fatalf("ValidateAgainst() error = %v, want the bound-label rejection", err)
	}
	// The CONSUMER, not the producer: this rejection is diagnosable from the
	// run's own artifacts only if it carries the existing closed-vocabulary
	// reason rather than falling through to `unclassified`. No new reason is
	// minted here -- a forged label on an edge endpoint is the same decision
	// driver_subject_label_mismatch already names, now reachable on a source
	// that previously escaped the check entirely.
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectLabelMismatch {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSubjectLabelMismatch)
	}
}

// TestTheTrueLabelOnAnEdgeOnlySubjectIsStillAccepted is the positive control
// for the test above. Without it, binding every id to the empty string would
// pass the forged-label test and reject every honest citation too.
func TestTheTrueLabelOnAnEdgeOnlySubjectIsStillAccepted(t *testing.T) {
	t.Parallel()
	input, draft, endpoint := edgeEndpointOnlyInput()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{endpoint}
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"

	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want the endpoint's own label to be accepted", err)
	}
}

// everyAdmissionSourceInput carries a DISTINCT subject in every source
// forEachCitableSynthesisSubject visits, so no source can be satisfied by
// another source's subject. The returned map is the fixture's own claim
// about what it covers, and the test below checks that claim against
// synthesisSubjects before checking anything else -- otherwise a fixture
// that silently stopped populating a source would keep passing.
func everyAdmissionSourceInput() (SynthesisInput, map[string]SubjectRef) {
	input, _, team := groupedCohortFixture()

	committed := input.Graph.Resolution.Committed[0]
	member := input.Graph.Cohort.Members[1].Subject
	node := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_path_node", Label: "Path Node"}
	edgeFrom := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_edge_from", Label: "Edge From"}
	edgeTo := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_edge_to", Label: "Edge To"}
	factSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_fact_only", Label: "Fact Only"}
	candidateSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_driver_candidate", Label: "Driver Candidate"}

	path := input.Graph.Paths[0]
	path.Nodes = append(path.Nodes, node)
	path.Edges = append(path.Edges, RelationshipEdge{
		Type: "BLOCKS", From: edgeFrom, To: edgeTo,
		Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
		EvidenceRefIDs: []string{"evidence_release_1234"},
	})
	input.Graph.Paths = []RelationshipPath{path}

	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactReadiness, Subject: factSubject,
		Fields:         map[string]FactValue{"release_ready": BooleanFactValue(true)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	})
	input.Graph.DriverCandidates = []DriverJudgment{{
		DriverID: "driver_87654321", Standing: DriverPrincipal,
		Category: "relationship", Title: "Engine candidate",
		Summary: "Engine-minted candidate driver.", AffectedSubjects: []SubjectRef{candidateSubject},
		PathIDs: []string{"path_12345678"}, EvidenceRefIDs: []string{"evidence_release_1234"},
		Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
		Confidence: 0.8, Current: true,
	}}

	return input, map[string]SubjectRef{
		"Resolution.Committed":                committed,
		"Cohort.Members":                      member,
		"Cohort.Groups":                       team,
		"Paths[].Nodes":                       node,
		"Paths[].Edges[].From":                edgeFrom,
		"Paths[].Edges[].To":                  edgeTo,
		"Facts[].Subject":                     factSubject,
		"DriverCandidates[].AffectedSubjects": candidateSubject,
	}
}

// TestEveryAdmittedSynthesisSubjectIsLabelBound is the invariant the class
// fix exists to hold: nothing is citable without a canonical label to check
// a citation against.
//
// It enumerates every admission path rather than asserting the one that
// broke, so the next source added to only one of the two consumers fails
// here rather than in production.
//
// RED at the fix parent: Paths[].Edges[].From and .To are in
// synthesisSubjects and absent from canonicalSubjectLabels.
func TestEveryAdmittedSynthesisSubjectIsLabelBound(t *testing.T) {
	t.Parallel()
	input, bySource := everyAdmissionSourceInput()
	admitted := synthesisSubjects(input)
	labels := canonicalSubjectLabels(input)

	// The fixture's own claim first: a source that stopped contributing
	// would otherwise make this test pass by testing less.
	for source, subject := range bySource {
		if _, ok := admitted[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("fixture claims to cover %s with %q, but synthesisSubjects does not admit it -- the fixture, not the invariant, is what changed", source, subject.CanonicalID)
		}
	}

	var unbound []string
	for key := range admitted {
		if _, bound := labels[key]; !bound {
			unbound = append(unbound, key)
		}
	}
	sort.Strings(unbound)
	if len(unbound) > 0 {
		t.Fatalf("admitted but not label-bound: %v -- requireBoundLabel never fires for these ids, so a real in-bounds subject can be cited under a forged label", unbound)
	}

	// Bound to the subject's OWN label, not merely present. A binding to
	// some other subject's label would satisfy the loop above and still
	// reject every honest citation.
	for source, subject := range bySource {
		if got := labels[subjectKeyForModel(subject)]; got != subject.Label {
			t.Fatalf("%s: label bound to %q, want %q", source, got, subject.Label)
		}
	}
}

// TestResolutionCandidatesAreBoundWithoutBeingAdmitted pins the ONE
// deliberate asymmetry: binding is a superset of admission. A resolution
// candidate is shown to the model and uncitable on purpose, so it needs a
// binding (its rejection should read "shown, uncitable by policy") and must
// not enter the allow-set. Without this test the shared walk could be
// "simplified" by moving candidates into it, which would admit an
// unresolved alternative as citable.
func TestResolutionCandidatesAreBoundWithoutBeingAdmitted(t *testing.T) {
	t.Parallel()
	input, _, _ := groupedCohortFixture()
	candidate := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: candidate,
		MatchReasons: []string{"lexical"}, Confidence: 0.4,
	}}

	key := subjectKeyForModel(candidate)
	if _, admitted := synthesisSubjects(input)[key]; admitted {
		t.Fatal("a resolution candidate is citable: the investigation never committed to it")
	}
	if got, bound := canonicalSubjectLabels(input)[key]; !bound || got != candidate.Label {
		t.Fatalf("candidate label bound = %q, %v; want %q, true", got, bound, candidate.Label)
	}
}

// admissionWalkFunction is the single enumeration of the admission set.
const admissionWalkFunction = "forEachCitableSynthesisSubject"

// admissionWalkConsumers are the functions that must derive from it rather
// than enumerate the input themselves.
var admissionWalkConsumers = []string{"synthesisSubjects", "canonicalSubjectLabels"}

// admissionWalkPermittedDirectSources are the input selectors a consumer may
// still walk on its own, each with the reason.
//
// Two-sided, in this package's audit style: an unlisted direct walk fails,
// and a listed entry that matches nothing fails too, so the exemption cannot
// outlive the code it excuses.
var admissionWalkPermittedDirectSources = map[string]string{
	"canonicalSubjectLabels/Candidates": "resolution candidates are bound but NOT admitted (shown-but-uncitable, see synthesisUncitableShownSubjects). Binding without admitting is the safe direction and cannot be expressed by the shared walk, whose whole contract is that everything it visits is citable",
}

// TestSubjectAdmissionAndLabelBindingShareOneWalk is the structural half.
//
// The behavioural test above proves the invariant for the sources a fixture
// happens to carry. This one proves it for sources NOT YET WRITTEN: as long
// as both consumers derive from one walk and neither enumerates the input
// itself, a new admission source is admitted and bound by the same edit and
// there is no second site at which to forget. That is the difference between
// fixing the instance and closing the class, and it is the property the
// round-3 finding proves was missing -- a test for the GROUPS instance of
// this exact pairing already existed and did not generalise one field over.
//
// RED at the fix parent: no shared walk exists and canonicalSubjectLabels
// ranges over Committed, Members, Groups, Paths[].Nodes, Facts and
// DriverCandidates on its own.
func TestSubjectAdmissionAndLabelBindingShareOneWalk(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	bodies := map[string]*ast.FuncDecl{}
	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil || function.Body == nil {
					continue
				}
				bodies[function.Name.Name] = function
			}
		}
	}

	if _, ok := bodies[admissionWalkFunction]; !ok {
		t.Fatalf("%s does not exist: admission and label binding are enumerated separately, which is the class this test closes", admissionWalkFunction)
	}

	matchedExemptions := map[string]bool{}
	for _, name := range admissionWalkConsumers {
		function, ok := bodies[name]
		if !ok {
			t.Fatalf("%s not found in the package", name)
		}

		callsWalk := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == admissionWalkFunction {
				callsWalk = true
			}
			return true
		})
		if !callsWalk {
			t.Errorf("%s does not call %s: it enumerates the admission set itself, so a source can be added here and not to the other consumer -- exactly how edge endpoints came to be admitted and unbound", name, admissionWalkFunction)
		}

		// Any remaining range over an input field is a private enumeration,
		// which is the shape that drifted.
		for _, statement := range function.Body.List {
			ast.Inspect(statement, func(node ast.Node) bool {
				rangeStatement, ok := node.(*ast.RangeStmt)
				if !ok {
					return true
				}
				source := rangedInputSelector(rangeStatement.X)
				if source == "" {
					return true
				}
				key := name + "/" + source
				if _, permitted := admissionWalkPermittedDirectSources[key]; permitted {
					matchedExemptions[key] = true
					return true
				}
				t.Errorf("%s ranges over %s directly: every citable source belongs to %s so admission and binding cannot diverge (add a two-sided exemption with its reason if this one is genuinely bind-only)", name, source, admissionWalkFunction)
				return true
			})
		}
	}

	for key := range admissionWalkPermittedDirectSources {
		if !matchedExemptions[key] {
			t.Errorf("exemption %q matched no direct walk: it has outlived the code it excused and must be removed", key)
		}
	}
}

// rangedInputSelector returns the final field name of a range over a
// selector rooted at the `input` parameter, or "" for anything else (a
// range over a local, a map, or a slice built in the function).
func rangedInputSelector(expression ast.Expr) string {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	root := expression
	for {
		next, ok := root.(*ast.SelectorExpr)
		if !ok {
			break
		}
		root = next.X
	}
	identifier, ok := root.(*ast.Ident)
	if !ok || identifier.Name != "input" {
		return ""
	}
	return selector.Sel.Name
}
