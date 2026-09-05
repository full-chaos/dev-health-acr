package graphrank

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4975: "Why is the acr project struggling?" (rig row
// neg-single-subject-why) offers expected_kind =
// [ci_pipeline_run, pull_request, pull_request_review, repository];
// project is absent, because frameKindHints (cohort_kind.go) reads only
// SubjectExpression.MemberKind()/GroupKind(), and MemberKind() had no case
// for named_subject. frame.go's MemberKind() now reads
// NamedSubjectExpression.ExpectedKind for the Named case, and
// model_runtime.go's resolveFrame backfills that field from the
// classification receipt's RequestedSubjectKind at frame-build time.

func namedSubjectFrame(term string, expected *contractsv1.ContextFabricSubjectKind) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: expected},
		},
	}
}

func kindOf(kind contractsv1.ContextFabricSubjectKind) *contractsv1.ContextFabricSubjectKind {
	return &kind
}

// TestFrameKindHints_NamedSubjectExpectedKindIsAHint is the composer-side
// fix, at the exact function the ticket traced (frameKindHints ->
// resolve.go's declaredKinds). RED before the fix: frameKindHints had no
// path to a named_subject frame's declared kind at all.
func TestFrameKindHints_NamedSubjectExpectedKindIsAHint(t *testing.T) {
	t.Parallel()
	frame := namedSubjectFrame("acr", kindOf(contractsv1.ContextFabricSubjectProject))
	got := frameKindHints(frame)
	want := []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectProject}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frameKindHints() = %v, want %v", got, want)
	}
}

// TestFrameKindHints_NamedSubjectWithNoExpectedKindStillYieldsNothing is the
// unchanged case: a named_subject question the model (or, pre-fix, the
// receipt backfill) never assigned a kind to still declares nothing, the
// same refuse-to-guess behavior every other frameKindHints case has.
func TestFrameKindHints_NamedSubjectWithNoExpectedKindStillYieldsNothing(t *testing.T) {
	t.Parallel()
	frame := namedSubjectFrame("acr", nil)
	if got := frameKindHints(frame); got != nil {
		t.Fatalf("frameKindHints() = %v, want nothing", got)
	}
}

// TestHintedPoolKinds_NamedSubjectExpectedKindNowHintsRetrieval NAMES the
// new retrieval-side behavior team-lead's GO flagged explicitly: extending
// MemberKind() for named_subject also feeds hintedPoolKinds (CHAOS-4348's
// "no hint, no call" pool-search gate), not only the offer composer, since
// both read frameKindHints. This is a DELIBERATE, not incidental,
// consequence -- CHAOS-4348's own rationale is "a HINT is caller/interpreter
// intent, not a blind lexical guess" (chaos4348_reachability.go), and a
// named_subject's own stated expected kind is exactly that: real intent,
// sanitized through the SAME closed vocabulary GroupKind uses, not prose.
//
// No existing chaos4348 test regresses: TestFrameKindHints_ProseIsNeverRead
// and TestResolveSubjects_UnhintedNonNameQuestionProducesByteIdenticalPool
// only ever construct named_subject frames WITHOUT ExpectedKind set (the
// gap this ticket closes is additive to those, not a replacement).
func TestHintedPoolKinds_NamedSubjectExpectedKindNowHintsRetrieval(t *testing.T) {
	t.Parallel()
	request := testRequest()
	frame := namedSubjectFrame("acr", kindOf(contractsv1.ContextFabricSubjectProject))

	// "" for the scope-anchor kind: a named_subject frame declares no scope
	// anchor, so the anchor-hint source is inapplicable here and its
	// disabled state is what this assertion is about -- the kind must
	// reach the pool from frameKindHints ALONE.
	got := hintedPoolKinds(request, nil, frame, "")
	want := []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectProject}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hintedPoolKinds() = %v, want %v -- named_subject's declared kind must now reach the CHAOS-4348 pool-search gate, same as children_of_scope/discovered_kind/grouped_members already do", got, want)
	}

	// Control: named_subject with NO declared kind still produces nothing,
	// preserving CHAOS-4348's byte-identical "no hint, no call" guarantee
	// for the case that gap actually covers.
	unhinted := hintedPoolKinds(request, nil, namedSubjectFrame("acr", nil), "")
	if unhinted != nil {
		t.Fatalf("hintedPoolKinds() = %v, want nothing when named_subject declares no kind", unhinted)
	}
}

// TestKindOfferMaterial_NamedSubjectDeclaredKind is CHAOS-4975's acceptance
// case at the composer, SUPERSEDED IN PART by CHAOS-5218: the ticket's own rig
// pool from neg-single-subject-why holds ci_pipeline_run/pull_request/
// pull_request_review/repository and NO project, while the frame declares
// project. CHAOS-4975 asserted project ranked first; CHAOS-5218 measured that
// exact shape emptying the pool one turn later, so an unservable declared kind
// is now withheld. The half CHAOS-4975 owns and CHAOS-5218 keeps -- a
// named_subject frame's declared kind reaches declaredKinds at all, via the
// fixed frameKindHints/MemberKind() path -- is asserted on the second fixture
// below, where the pool actually holds it.
func TestKindOfferMaterial_NamedSubjectDeclaredKind(t *testing.T) {
	t.Parallel()
	pool := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectPullRequestReview,
		contractsv1.ContextFabricSubjectRepository,
	}
	declaredKinds := frameKindHints(namedSubjectFrame("acr", kindOf(contractsv1.ContextFabricSubjectProject)))

	material, diag := kindOfferMaterial(pool, nil, declaredKinds, heldFromKinds(pool...))
	for _, opt := range material.KindOptions {
		if opt.Kind == contractsv1.ContextFabricSubjectProject {
			t.Fatalf("KindOptions = %v, want NO project -- CHAOS-5218: the rig pool holds none, so the offer could not be honoured", material.KindOptions)
		}
	}
	if diag.DeclaredWithheldNotInPoolCount != 1 {
		t.Fatalf("diag = %+v, want DeclaredWithheldNotInPoolCount 1 (project declared, absent from the pool)", diag)
	}
	hasRepository := false
	for _, opt := range material.KindOptions {
		if opt.Kind == contractsv1.ContextFabricSubjectRepository {
			hasRepository = true
		}
	}
	if !hasRepository {
		t.Fatalf("KindOptions = %v, want repository still present -- withholding an unservable declared kind must not disturb the pool kinds", material.KindOptions)
	}

	// CHAOS-4975's own property, on the SAME fixture with project in the pool:
	// a named_subject frame's declared kind reaches the offer and ranks first.
	servable := append(append([]contractsv1.ContextFabricSubjectKind{}, pool...), contractsv1.ContextFabricSubjectProject)
	material, diag = kindOfferMaterial(servable, nil, declaredKinds, heldFromKinds(servable...))
	if len(material.KindOptions) == 0 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("KindOptions = %v, want project first", material.KindOptions)
	}
	if diag.DeclaredHintCount != 1 || diag.DeclaredWithheldNotInPoolCount != 0 {
		t.Fatalf("diag = %+v, want DeclaredHintCount 1 and DeclaredWithheldNotInPoolCount 0", diag)
	}
	hasRepository = false
	for _, opt := range material.KindOptions {
		if opt.Kind == contractsv1.ContextFabricSubjectRepository {
			hasRepository = true
		}
	}
	if !hasRepository {
		t.Fatalf("KindOptions = %v, want repository still present (declared kinds are additive, never exclusionary)", material.KindOptions)
	}
}

// TestKindOfferMaterial_NamedSubjectDeclaredKindAloneNeverRaisesTheNeed is
// the ticket's other acceptance line: a declared kind with an otherwise
// EMPTY pool is not a real ambiguity (nothing else to disambiguate
// against) and must not raise the expected_kind need by itself -- the
// SAME CHAOS-4967 cardinality gate that already protects
// children_of_scope/discovered_kind/grouped_members, now proven for the
// named_subject source of declaredKinds too.
func TestKindOfferMaterial_NamedSubjectDeclaredKindAloneNeverRaisesTheNeed(t *testing.T) {
	t.Parallel()
	declaredKinds := frameKindHints(namedSubjectFrame("acr", kindOf(contractsv1.ContextFabricSubjectProject)))

	material, diagnostics := kindOfferMaterial(nil, nil, declaredKinds, heldFromKinds())
	if len(material.KindOptions) != 0 || material.Missing != nil {
		t.Fatalf("kindOfferMaterial() = %+v, want a suppressed (empty) offer with an empty pool", material)
	}
	if !diagnostics.SuppressedByCardinality {
		t.Fatalf("diagnostics.SuppressedByCardinality = false, want true")
	}
}
