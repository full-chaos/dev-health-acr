package contextfabric

import (
	"fmt"
	"testing"
)

// The GENERATED frame corpus every law and oracle in this slice quantifies
// over, and the coverage assertions that keep it from lying about itself.
//
// WHY GENERATED AND NOT HAND-WRITTEN. The design's own laws are stated as
// properties over the whole frame space ("for every legal frame and every
// single-value extension of it..."), and the space is FINITE -- the
// vocabularies are closed. A hand-written table can only exhibit the cases
// its author thought of, which is the failure this branch's review history
// is almost entirely about: a repair sweep that enumerated the outer axes
// and treated a nested structure as a leaf; a leak canary planted in one
// free-text field while a different field leaked; a coordinate derivation
// that read the named operand arm only, because no frame in the corpus had
// a scoped one.
//
// THE OPERAND SHAPE IS PART OF THE SWEEP KEY, for that last reason
// exactly. SubjectOperand is a union of named AND scoped (invariant I19),
// so "every variant" is not the population -- "every (variant, operand
// shape)" is. An explicit set of two named operands and an explicit set
// with a scoped operand are different inputs to anything that walks the
// expression, and a corpus that cannot exhibit a defect cannot gate
// against it.
//
// WHAT THIS CORPUS IS NOT. It is not a sample of real questions and makes
// no claim to be. It is the grammar-legal space, which is the right
// population for an ALGEBRA law: a law that holds on the questions we
// happen to have seen is not a law. Real-question agreement is measured
// separately, on the labelled corpus and in production
// (family_projection_agreement.go).

// projectionShape is one grammar-legal subject-expression SHAPE: a variant
// discriminator plus, where the variant has an inner union, the arrangement
// of that union's arms.
type projectionShape struct {
	// name identifies the shape in a failure message. STRUCTURAL only --
	// it names the shape, never a question.
	name string
	// kind is the union discriminator, carried separately so a test can
	// assert against it without re-reading the expression.
	kind SubjectExpressionKind
	// expression is the shape itself.
	expression SubjectExpression
}

func projectionKindPointer(kind SubjectKind) *SubjectKind {
	return &kind
}

func projectionNamedOperand(term string, kind SubjectKind) SubjectOperand {
	return SubjectOperand{
		Kind:  SubjectOperandNamed,
		Named: &NamedSubjectExpression{Terms: []string{term}, ExpectedKind: projectionKindPointer(kind)},
	}
}

func projectionScopedOperand(anchor string, member SubjectKind) SubjectOperand {
	return SubjectOperand{
		Kind:   SubjectOperandScoped,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{anchor}, MemberKind: member},
	}
}

// projectionShapes is EVERY grammar-legal subject-expression shape: the six
// union variants, with the explicit set expanded over every arrangement of
// its own two-armed operand union, and the organization scope expanded over
// its optional member kind.
//
// The two organization rows are separate shapes rather than one because
// MemberKind is what distinguishes "how many repositories are in the
// organization" from "how many teams" -- round-2 P1-4 showed those
// collapsing to an identical frame -- and because I17 validates the kind
// only when it is present, so the absent case and the present case take
// different paths through validation.
func projectionShapes() []projectionShape {
	repository := SubjectRepository
	return []projectionShape{
		{"named_subject", SubjectExpressionNamed, SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"s"}, ExpectedKind: projectionKindPointer(SubjectTeam)},
		}},
		{"discovered_kind", SubjectExpressionDiscoveredKind, SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam},
		}},
		{"children_of_scope", SubjectExpressionChildrenOfScope, SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: SubjectProject},
		}},
		{"grouped_members", SubjectExpressionGroupedMembers, SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
		}},
		{"explicit_set(named,named)", SubjectExpressionExplicitSet, SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				projectionNamedOperand("a", SubjectTeam), projectionNamedOperand("b", SubjectTeam),
			}},
		}},
		{"explicit_set(named,scoped)", SubjectExpressionExplicitSet, SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				projectionNamedOperand("a", SubjectTeam), projectionScopedOperand("b", SubjectProject),
			}},
		}},
		{"explicit_set(scoped,named)", SubjectExpressionExplicitSet, SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				projectionScopedOperand("a", SubjectProject), projectionNamedOperand("b", SubjectTeam),
			}},
		}},
		{"explicit_set(scoped,scoped)", SubjectExpressionExplicitSet, SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				projectionScopedOperand("a", SubjectProject), projectionScopedOperand("b", SubjectProject),
			}},
		}},
		{"organization_scope(no member kind)", SubjectExpressionOrganizationScope, SubjectExpression{
			Kind: SubjectExpressionOrganizationScope,
			Org:  &OrganizationScopeExpression{},
		}},
		{"organization_scope(member kind)", SubjectExpressionOrganizationScope, SubjectExpression{
			Kind: SubjectExpressionOrganizationScope,
			Org:  &OrganizationScopeExpression{MemberKind: projectionKindPointer(repository)},
		}},
	}
}

// projectionEmphasisSets is every subset of the two-member emphasis
// vocabulary. Written as subsets rather than as single members because
// EmphasisPositiveOutliers and EmphasisNegativeOutliers together are the
// "which teams are doing well and which are struggling" case, and a sweep
// over single members alone would never build it.
func projectionEmphasisSets() [][]AnswerEmphasis {
	return [][]AnswerEmphasis{
		nil,
		{EmphasisPositiveOutliers},
		{EmphasisNegativeOutliers},
		{EmphasisPositiveOutliers, EmphasisNegativeOutliers},
	}
}

// projectionDimensionSets is the empty set, every single dimension, and one
// two-member set.
//
// NOT the full power set, and the bound is stated rather than glossed: the
// dimension vocabulary has nine members, so the power set is 512 and would
// multiply this corpus by that. The pair is included because a duplicate or
// mis-ordered dimension set produced a DUPLICATE axis discharge once, and a
// single-member set has no order to get wrong -- the same reason the
// emphasis axis is swept as subsets. What this bound does NOT cover: an
// interaction that needs three or more dimensions at once. No law in §13.2.2a
// is stated over such an interaction, so nothing here claims to cover one.
func projectionDimensionSets() [][]HealthDimension {
	sets := [][]HealthDimension{nil}
	for _, dimension := range HealthDimensionVocabulary() {
		sets = append(sets, []HealthDimension{dimension})
	}
	sets = append(sets, []HealthDimension{HealthDimensionDeliveryFlow, HealthDimensionInvestmentBalance})
	return sets
}

// generatedFrame is one member of the corpus: the VALIDATED frame plus the
// sweep coordinates that produced it, so a failure names the input rather
// than dumping a struct.
type generatedFrame struct {
	shape     projectionShape
	goals     []InvestigationGoal
	temporal  TemporalIntent
	emphasis  []AnswerEmphasis
	dimension []HealthDimension
	// frame is the frame AFTER validation -- normalized, obligations
	// derived. Every law is stated over legal frames, and a proposal that
	// never became legal is not one.
	frame QuestionFrame
}

func (g generatedFrame) String() string {
	return fmt.Sprintf("%s goals=%v temporal=%s emphasis=%v dimensions=%v",
		g.shape.name, g.goals, g.temporal, g.emphasis, g.dimension)
}

// refusedCell records why a (shape, goal) pair produced no legal frame:
// the set of FIRST-failed invariants across every candidate built for it.
//
// The refusals are collected rather than discarded because an empty cell
// pinned by COUNT alone is a magic number -- it says a cell is empty and
// not why, so a validation change that empties a cell for a completely
// different reason satisfies the same pin. Naming the invariant makes the
// pin diagnosable from the run's own artifacts.
type refusedCell struct {
	invariants map[FrameInvariant]int
}

// generateFrames sweeps every (shape x goal x temporal x emphasis-subset x
// dimension-set) combination and keeps the ones that VALIDATE.
//
// The single-goal sweep is the base population. Multi-goal frames are not
// swept here as a cross product -- eight goals give 255 non-empty subsets
// and the corpus is already ten shapes wide -- they arrive through law L1's
// extension sweep, which adds a second goal to every base frame and is the
// place the design actually requires them.
//
// VALIDITY IS DECIDED BY ValidateFrame, which is a DIFFERENT function from
// any function under test here. Filtering a corpus with the thing being
// tested is how an expectation gets computed by its own subject; the
// validator is not the projection, not the obligation derivation and not
// any law's checker.
func generateFrames(t *testing.T) []generatedFrame {
	t.Helper()
	frames, _ := generateFramesWithRefusals(t)
	return frames
}

// generateFramesWithRefusals is generateFrames plus the refusal ledger the
// coverage gate reads. Split so that every law can take the frames alone
// without carrying a map it does not use.
func generateFramesWithRefusals(t *testing.T) ([]generatedFrame, map[string]*refusedCell) {
	t.Helper()
	var frames []generatedFrame
	refusals := map[string]*refusedCell{}
	for _, shape := range projectionShapes() {
		for _, goal := range InvestigationGoalVocabulary() {
			for _, temporal := range TemporalIntentVocabulary() {
				for _, emphasis := range projectionEmphasisSets() {
					for _, dimensions := range projectionDimensionSets() {
						goals := []InvestigationGoal{goal}
						proposed := QuestionFrame{
							Goals:             goals,
							SubjectExpression: shape.expression,
							Temporal:          temporal,
							Emphasis:          emphasis,
							Dimensions:        dimensions,
						}
						result := ValidateFrame(proposed, nil, "")
						if result.Outcome != FrameValidationOutcomeValid {
							key := shape.name + "/" + string(goal)
							cell, ok := refusals[key]
							if !ok {
								cell = &refusedCell{invariants: map[FrameInvariant]int{}}
								refusals[key] = cell
							}
							cell.invariants[result.Failure.Invariant]++
							continue
						}
						frames = append(frames, generatedFrame{
							shape: shape, goals: goals, temporal: temporal,
							emphasis: emphasis, dimension: dimensions, frame: result.Frame,
						})
					}
				}
			}
		}
	}
	if len(frames) == 0 {
		t.Fatal("the generated corpus is EMPTY -- every candidate was refused, so every law over it would pass vacuously")
	}
	return frames, refusals
}

// TestGeneratedCorpusCoversEveryShapeGoalPair is the corpus's own
// non-vacuity gate, and it asserts PAIRS rather than margins.
//
// "Every variant appears and every goal appears" accepted a hole where one
// (variant, goal) pair was never built -- both margins can be full while a
// cell is empty. Every law below quantifies over this corpus, so a missing
// cell is a law that was never tested on it, silently.
//
// A pair with NO legal frame is a legitimate outcome (the invariants refuse
// some combinations by design), but it must be VISIBLE: the test lists the
// empty cells and fails if the count moves, so a validation change that
// silently empties a cell shows up here rather than as a law quietly
// covering less.
// refusedByDesign is every (shape, goal) pair the invariants refuse for
// EVERY temporal, emphasis and dimension combination, with the invariant
// that refuses it.
//
// Each row is a design statement, not an accident of the generator:
//
//   - compare on anything but an explicit set. A comparison needs two
//     operands to compare, and the goal axis cannot conjure a second
//     subject out of a single-subject topology. I7 ("compare implies
//     explicit_set") is what refuses it -- NOT I2, which is the operand
//     COUNT inside a set that already exists. This pin was written as I2
//     first and this gate rejected it, which is the point of naming the
//     invariant rather than counting empty cells.
//   - count_or_aggregate on a single named subject and on an explicit
//     set. Counting is a question about a POPULATION; a named subject is
//     a population of one and an explicit comparison is not a population
//     at all.
//   - count_or_aggregate on an organization scope with NO member kind.
//     This is O3's own case: MemberKind is what distinguishes "how many
//     repositories are in the organization" from "how many teams", and
//     without it the question has no countable population. I17.
//
// A cell that leaves this map, or arrives in it under a DIFFERENT
// invariant, is a validation change and must be described as one.
var refusedByDesign = map[string]FrameInvariant{
	"named_subject/compare":                                 FrameInvariantI7,
	"discovered_kind/compare":                               FrameInvariantI7,
	"children_of_scope/compare":                             FrameInvariantI7,
	"grouped_members/compare":                               FrameInvariantI7,
	"organization_scope(no member kind)/compare":            FrameInvariantI7,
	"organization_scope(member kind)/compare":               FrameInvariantI7,
	"named_subject/count_or_aggregate":                      FrameInvariantI9,
	"explicit_set(named,named)/count_or_aggregate":          FrameInvariantI9,
	"explicit_set(named,scoped)/count_or_aggregate":         FrameInvariantI9,
	"explicit_set(scoped,named)/count_or_aggregate":         FrameInvariantI9,
	"explicit_set(scoped,scoped)/count_or_aggregate":        FrameInvariantI9,
	"organization_scope(no member kind)/count_or_aggregate": FrameInvariantI17,
}

func TestGeneratedCorpusCoversEveryShapeGoalPair(t *testing.T) {
	t.Parallel()
	frames, refusals := generateFramesWithRefusals(t)

	covered := map[string]int{}
	for _, generated := range frames {
		covered[generated.shape.name+"/"+string(generated.goals[0])]++
	}

	var empty []string
	for _, shape := range projectionShapes() {
		for _, goal := range InvestigationGoalVocabulary() {
			key := shape.name + "/" + string(goal)
			if covered[key] == 0 {
				empty = append(empty, key)
			}
		}
	}

	shapes := len(projectionShapes())
	goals := InvestigationGoalCount
	if got, want := shapes*goals-len(empty), len(covered); got != want {
		t.Fatalf("cell accounting disagrees: %d shape-goal pairs minus %d empty = %d, but %d distinct pairs were built", shapes*goals, len(empty), got, want)
	}

	// Every empty cell must be one this design REFUSES, named by the
	// invariant that refuses it -- not merely counted.
	for _, key := range empty {
		want, declared := refusedByDesign[key]
		if !declared {
			t.Errorf("shape-goal pair %q has no legal frame and is not in refusedByDesign -- a law quantifying over this corpus is silently untested on it. If the refusal is intended, add the row with the invariant that causes it.", key)
			continue
		}
		cell := refusals[key]
		if cell == nil {
			t.Errorf("shape-goal pair %q is empty but no refusal was recorded for it", key)
			continue
		}
		if _, refused := cell.invariants[want]; !refused {
			got := make([]FrameInvariant, 0, len(cell.invariants))
			for invariant := range cell.invariants {
				got = append(got, invariant)
			}
			t.Errorf("shape-goal pair %q is refused, but never by %s (refusing invariants: %v) -- the pin names an invariant that does not refuse it", key, want, got)
		}
	}
	for key := range refusedByDesign {
		if covered[key] != 0 {
			t.Errorf("shape-goal pair %q is declared refused-by-design but %d legal frames were built for it -- a validation change made it legal; remove the row and say so", key, covered[key])
		}
	}

	t.Logf("corpus: %d legal frames over %d shape-goal pairs; %d pairs refused by design", len(frames), len(covered), len(empty))
}

// TestGeneratedCorpusExhibitsBothOperandArms pins the specific blindness
// that cost the declaration slice a round: a corpus whose explicit sets are
// all two NAMED operands cannot fail against code that reads the named arm
// only.
//
// It asserts on the frames that SURVIVED validation, not on the shape list,
// because a shape that is generated and then refused by every combination
// is not in the population any law quantifies over.
func TestGeneratedCorpusExhibitsBothOperandArms(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)

	arms := map[SubjectOperandKind]int{}
	for _, generated := range frames {
		if generated.frame.SubjectExpression.Kind != SubjectExpressionExplicitSet {
			continue
		}
		for _, operand := range generated.frame.SubjectExpression.Explicit.Operands {
			arms[operand.Kind]++
		}
	}
	for _, arm := range SubjectOperandKindVocabulary() {
		if arms[arm] == 0 {
			t.Fatalf("no legal frame in the corpus carries a %q operand -- every law over operand shapes would be blind to that arm", arm)
		}
	}
}
