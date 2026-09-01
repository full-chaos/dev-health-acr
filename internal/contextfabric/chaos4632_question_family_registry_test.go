package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 §3.1 registry assertions.
//
// RED ON origin/main: the whole file fails to COMPILE there -- no
// QuestionFamily type, no QuestionFamilyDefinitions, no HealthDimension.
// A compile failure is the strongest possible red for a slice that adds a
// vocabulary, and it is the same red-on-parent shape
// chaos4622_interpret_decoding_test.go's own header records for its
// generationRequest.Config field.
//
// WHAT THESE ARE FOR. §3.1 names five assertions; each exists because its
// absence would let a family row ship in a state that reads as a
// declaration but cannot be honoured.

// TestEveryFamilyHasANonEmptyApplicableAxes is §3.1's first assertion.
//
// A family with no applicable axes is not a family with "no clarification
// policy" -- it is a family S4 would gate every offer builder against and
// silently disclose NOTHING for, turning a question that needed a window
// into a dead end. Empty must be impossible to declare by accident.
func TestEveryFamilyHasANonEmptyApplicableAxes(t *testing.T) {
	t.Parallel()
	for _, definition := range QuestionFamilyDefinitions() {
		if len(definition.ApplicableAxes) == 0 {
			t.Errorf("family %q declares no ApplicableAxes", definition.Family)
		}
	}
}

// TestEveryAskOrderEntryIsApplicable is §3.1's second assertion.
//
// AskOrder is prompt PRIORITY over the applicable set, never a separate
// set. An entry outside ApplicableAxes would make the prompt lead with an
// axis the family's own gate then removes -- an offer promised and
// withdrawn inside one turn.
func TestEveryAskOrderEntryIsApplicable(t *testing.T) {
	t.Parallel()
	for _, definition := range QuestionFamilyDefinitions() {
		applicable := make(map[StructureNeedKind]struct{}, len(definition.ApplicableAxes))
		for _, axis := range definition.ApplicableAxes {
			applicable[axis] = struct{}{}
		}
		for _, axis := range definition.AskOrder {
			if _, ok := applicable[axis]; !ok {
				t.Errorf("family %q AskOrder names %q, which is not in its ApplicableAxes", definition.Family, axis)
			}
		}
	}
}

// TestEveryAskOrderIsDuplicateFree guards the other way an AskOrder can be
// wrong without failing the containment check above: naming the same axis
// twice, which would ask for the same thing twice in one prompt.
func TestEveryAskOrderIsDuplicateFree(t *testing.T) {
	t.Parallel()
	for _, definition := range QuestionFamilyDefinitions() {
		seen := make(map[StructureNeedKind]struct{}, len(definition.AskOrder))
		for _, axis := range definition.AskOrder {
			if _, ok := seen[axis]; ok {
				t.Errorf("family %q AskOrder names %q twice", definition.Family, axis)
			}
			seen[axis] = struct{}{}
		}
	}
}

// TestEveryRenderKindIsProducedOrDeclaredUnproduced is §3.1's third
// assertion.
//
// The point is HONESTY, not coverage: a family may name a render kind
// nothing can draw yet (CHAOS-4415 slice 1 declared seven such kinds
// deliberately), but it may not do so SILENTLY. A reader must be able to
// tell "this family will render a treemap" from "this family would render
// a treemap if a producer existed".
func TestEveryRenderKindIsProducedOrDeclaredUnproduced(t *testing.T) {
	t.Parallel()
	declaredUnproduced := make(map[contractsv1.ContextFabricRenderKind]struct{})
	for _, kind := range DeclaredUnproducedRenderKinds() {
		declaredUnproduced[kind] = struct{}{}
	}
	for _, definition := range QuestionFamilyDefinitions() {
		for _, kind := range definition.RenderKinds {
			if kind == contractsv1.ContextFabricRenderKindSeries {
				// series is the ONE kind with a producer today
				// (render_shapes.go:307,386 -- see
				// ContextFabricRenderKindSeries' own doc comment).
				continue
			}
			if _, ok := declaredUnproduced[kind]; !ok {
				t.Errorf("family %q names render kind %q, which has neither a producer nor an entry in DeclaredUnproducedRenderKinds", definition.Family, kind)
			}
		}
	}
}

// TestEveryFamilyHasAValidDimension is §3.1's fourth assertion.
//
// SCOPE NOTE, so this test is not mistaken for CHAOS-4468 being done:
// CHAOS-4468's deliverable is a Dimension on the FACTKIND registry plus a
// generated dimension<->FactKind<->ranking-family mapping, and the design's
// §9 slice plan puts that whole ticket in S3. What is asserted here is only
// §3.1's requirement of the FAMILY table.
func TestEveryFamilyHasAValidDimension(t *testing.T) {
	t.Parallel()
	for _, definition := range QuestionFamilyDefinitions() {
		if !ValidHealthDimension(definition.Dimension) {
			t.Errorf("family %q declares dimension %q, which is not a member of the closed vocabulary", definition.Family, definition.Dimension)
		}
	}
}

// TestHealthDimensionVocabularyIsExactlyNine pins the count against the
// two canonical documents, which call them "the nine canonical
// dimensions". A tenth appearing here without those documents changing is
// drift this build should not survive -- and since the vocabulary is NOT
// derived from anything in this repository, a count assertion is the only
// automatic check available for it.
func TestHealthDimensionVocabularyIsExactlyNine(t *testing.T) {
	t.Parallel()
	if HealthDimensionCount != 9 {
		t.Fatalf("HealthDimensionCount = %d, want 9", HealthDimensionCount)
	}
	seen := make(map[HealthDimension]struct{}, HealthDimensionCount)
	for _, dimension := range HealthDimensionVocabulary() {
		if _, ok := seen[dimension]; ok {
			t.Errorf("dimension %q appears twice in the vocabulary", dimension)
		}
		seen[dimension] = struct{}{}
	}
}

// TestFamilyTableCoversTheVocabularyExactly is §3.1's fifth assertion, and
// the one that makes the other four total: a family added to the
// vocabulary but forgotten in the table would otherwise pass every test
// above by simply not being iterated.
func TestFamilyTableCoversTheVocabularyExactly(t *testing.T) {
	t.Parallel()
	definitions := QuestionFamilyDefinitions()
	if len(definitions) != QuestionFamilyCount {
		t.Fatalf("family table has %d rows, vocabulary has %d members", len(definitions), QuestionFamilyCount)
	}
	for i, family := range QuestionFamilyVocabulary() {
		if definitions[i].Family != family {
			t.Errorf("family table row %d is %q, vocabulary member %d is %q -- the table must be in vocabulary order", i, definitions[i].Family, i, family)
		}
		if _, ok := LookupQuestionFamily(family); !ok {
			t.Errorf("LookupQuestionFamily(%q) found nothing", family)
		}
	}
}

// TestEveryFamilyDeclaresValidClosedVocabularyColumns closes the remaining
// enum-typed columns. Each is a closed vocabulary, and a zero value in any
// of them is a row that declares nothing while looking declared.
func TestEveryFamilyDeclaresValidClosedVocabularyColumns(t *testing.T) {
	t.Parallel()
	for _, definition := range QuestionFamilyDefinitions() {
		if !validSubjectAxisKind(definition.SubjectAxis) {
			t.Errorf("family %q declares SubjectAxis %q, not a vocabulary member", definition.Family, definition.SubjectAxis)
		}
		if !validPlanBudgetProfile(definition.Budget) {
			t.Errorf("family %q declares Budget %q, not a vocabulary member", definition.Family, definition.Budget)
		}
		// CHAOS-4735. The empty value is deliberately NOT a vocabulary
		// member, so a family added later cannot inherit "no continuation"
		// by forgetting the column -- it has to say `none` on purpose. This
		// column is CONSUMED and reaches the 413 body, so an undeclared one
		// would serve an empty token to a caller.
		if !ValidNarrowingContinuationAxis(definition.NarrowerContinuationAxis) {
			t.Errorf("family %q declares NarrowerContinuationAxis %q, not a vocabulary member", definition.Family, definition.NarrowerContinuationAxis)
		}
		for _, role := range definition.FactRoles {
			if !validFactRole(role) {
				t.Errorf("family %q declares FactRole %q, not a vocabulary member", definition.Family, role)
			}
		}
		if len(definition.CompatibleShapes) == 0 {
			t.Errorf("family %q declares no CompatibleShapes", definition.Family)
		}
	}
}

// TestNewStructureAxesAreNotYetWireVocabularyMembers is the SHADOW
// assertion, and it is the one that would catch this slice accidentally
// becoming a contract change.
//
// scope_anchor and group_kind are declared as package-local constants of
// the aliased wire type so a family row can name them. If either were ever
// added to contractsv1.ContextFabricStructureNeedKindVocabulary, every
// StructureNeeds payload could carry it, ask-dev's additionalProperties:
// false validator would meet an unknown enum value, and the shared rig
// would break exactly as it did for #336's render_shape field
// (CHAOS-4623). Promoting them is S4's job, WITH the schema/OpenAPI/MCP/
// fixture/parity work and the ask-dev pin bump -- not a side effect of
// this file.
func TestNewStructureAxesAreNotYetWireVocabularyMembers(t *testing.T) {
	t.Parallel()
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if member == StructureNeedScopeAnchor || member == StructureNeedGroupKind {
			t.Fatalf("%q is now a WIRE structure-need vocabulary member -- that is a contract widening and a two-step deploy (CHAOS-4623), not a shadow slice change", member)
		}
	}
	if contractsv1.ValidContextFabricStructureNeedKind(StructureNeedScopeAnchor) ||
		contractsv1.ValidContextFabricStructureNeedKind(StructureNeedGroupKind) {
		t.Fatal("the new axes validate as wire structure-need kinds -- see this test's doc comment")
	}
}

// TestUnclassifiedFamilyNarrowsNothing pins the property that makes
// unclassified safe to fall back to: it must leave EVERY axis applicable.
// A narrowed unclassified would silently suppress clarifications on
// exactly the questions the system understood least, which is the opposite
// of what refuse-to-guess means.
func TestUnclassifiedFamilyNarrowsNothing(t *testing.T) {
	t.Parallel()
	definition, ok := LookupQuestionFamily(QuestionFamilyUnclassified)
	if !ok {
		t.Fatal("unclassified is not in the family table")
	}
	applicable := make(map[StructureNeedKind]struct{}, len(definition.ApplicableAxes))
	for _, axis := range definition.ApplicableAxes {
		applicable[axis] = struct{}{}
	}
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if _, ok := applicable[member]; !ok {
			t.Errorf("unclassified does not list wire axis %q as applicable", member)
		}
	}
	for _, member := range []StructureNeedKind{StructureNeedScopeAnchor, StructureNeedGroupKind} {
		if _, ok := applicable[member]; !ok {
			t.Errorf("unclassified does not list new axis %q as applicable", member)
		}
	}
}

// TestScopedCohortNeverOffersASingleSubjectPick is the family-table
// expression of CHAOS-4622 §2, the actual defect Q-B exhibits: the caller
// was asked to pick ONE subject when the named term is the SCOPE, not the
// answer's subject.
//
// §3 says of this family: "scope_anchor (which 'fullchaos'?) and window;
// NEVER a single-subject pick". A subject_handle or subject_candidate axis
// appearing in this row is that defect re-entering as data.
func TestScopedCohortNeverOffersASingleSubjectPick(t *testing.T) {
	t.Parallel()
	definition, ok := LookupQuestionFamily(QuestionFamilyScopedCohortStatus)
	if !ok {
		t.Fatal("scoped_cohort_status is not in the family table")
	}
	for _, axis := range definition.ApplicableAxes {
		if axis == contractsv1.ContextFabricStructureNeedSubjectHandle ||
			axis == contractsv1.ContextFabricStructureNeedSubjectCandidate {
			t.Errorf("scoped_cohort_status declares %q applicable -- that is the CHAOS-4622 §2 defect (asking Q-B to pick ONE subject) expressed as a table row", axis)
		}
	}
}

// TestDiscoveredCohortRankingIsWindowOnly pins what CHAOS-4579 shipped as
// an after-the-fact filter: a discovered cohort has no named subject, so
// window is the only axis a caller can usefully be asked about. S4
// subsumes that filter by reading this row instead.
func TestDiscoveredCohortRankingIsWindowOnly(t *testing.T) {
	t.Parallel()
	definition, ok := LookupQuestionFamily(QuestionFamilyDiscoveredCohortRanking)
	if !ok {
		t.Fatal("discovered_cohort_ranking is not in the family table")
	}
	if len(definition.ApplicableAxes) != 1 || definition.ApplicableAxes[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("discovered_cohort_ranking ApplicableAxes = %v, want exactly [window]", definition.ApplicableAxes)
	}
}
