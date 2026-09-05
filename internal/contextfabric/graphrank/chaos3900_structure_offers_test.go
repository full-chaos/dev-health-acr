package graphrank

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func candidateOf(kind contractsv1.ContextFabricSubjectKind, id string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id},
	}
}

func TestKindOfferMaterial_EmptyPoolOffersNothing(t *testing.T) {
	t.Parallel()
	material, diag := kindOfferMaterial(nil, nil, nil, nil)
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(nil, nil) = %+v, want empty (nothing to disambiguate)", material)
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DistinctKindCount: 0, SuppressedByCardinality: true}) {
		t.Errorf("diag = %+v, want {0, 0, true} -- genuinely zero offerable kinds", diag)
	}
}

func TestKindOfferMaterial_SingleKindPoolOffersNothing(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_2"),
	}
	material, diag := kindOfferMaterial(distinctOfferableKinds(candidates), nil, nil, heldFromKinds(distinctOfferableKinds(candidates)...))
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(single-kind pool) = %+v, want empty: nothing to disambiguate when every candidate is the same kind", material)
	}
	// CHAOS-4012 v20: this is the exact "in pool, still not offered" shape
	// the ticket investigates -- DistinctKindCount==1, not 0, distinguishes
	// it from the genuinely-empty-pool case above.
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DistinctKindCount: 1, SuppressedByCardinality: true}) {
		t.Errorf("diag = %+v, want {0, 1, true} -- one distinct offerable kind present, still suppressed by cardinality", diag)
	}
}

// TestKindOfferMaterial_MultiKindPoolOffersDisambiguation is the P1.C
// acceptance shape for expected_kind: "30 of 41 stalled pools span >=2
// census kinds" (design brief §1.2 reading 1) is exactly the case this
// proves the engine now discloses.
func TestKindOfferMaterial_MultiKindPoolOffersDisambiguation(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material, diag := kindOfferMaterial(distinctOfferableKinds(candidates), nil, nil, heldFromKinds(distinctOfferableKinds(candidates)...))
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want exactly [expected_kind]", material.Missing)
	}
	if len(material.KindOptions) != 2 {
		t.Fatalf("len(material.KindOptions) = %d, want 2", len(material.KindOptions))
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DistinctKindCount: 2, SuppressedByCardinality: false}) {
		t.Errorf("diag = %+v, want {0, 2, false} -- not suppressed", diag)
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, opt := range material.KindOptions {
		seen[opt.Kind] = true
		if opt.Label == "" {
			t.Errorf("KindOption for %q has an empty Label", opt.Kind)
		}
		if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
			t.Errorf("KindOption for %q OfferSource = %q, want %q", opt.Kind, opt.OfferSource, contractsv1.ContextFabricStructureOfferEngine)
		}
		// ReceiptID/OptionID are deliberately unset here -- minted later
		// by composeStructureNeeds once a ResultID exists (see
		// StructureOfferMaterial's own doc comment).
		if opt.ReceiptID != "" || opt.OptionID != "" {
			t.Errorf("KindOption for %q carries a pre-minted ReceiptID/OptionID (%q/%q), want both unset at this stage", opt.Kind, opt.ReceiptID, opt.OptionID)
		}
	}
	if !seen[contractsv1.ContextFabricSubjectPullRequest] || !seen[contractsv1.ContextFabricSubjectWorkItem] {
		t.Errorf("material.KindOptions = %+v, want one entry per distinct kind in the pool", material.KindOptions)
	}
}

// TestKindOfferMaterial_DuplicateKindsCollapseToOneOption pins that a pool
// with many candidates of the SAME kind contributes exactly one
// KindOption for it, not one per candidate.
func TestKindOfferMaterial_DuplicateKindsCollapseToOneOption(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_2"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_3"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material, diag := kindOfferMaterial(distinctOfferableKinds(candidates), nil, nil, heldFromKinds(distinctOfferableKinds(candidates)...))
	if len(material.KindOptions) != 2 {
		t.Fatalf("len(material.KindOptions) = %d, want 2 (one per DISTINCT kind, not one per candidate)", len(material.KindOptions))
	}
	if diag.DistinctKindCount != 2 {
		t.Errorf("diag.DistinctKindCount = %d, want 2 -- duplicate candidates of the same kind must not inflate the count", diag.DistinctKindCount)
	}
}

// TestKindOfferMaterial_NonOfferableKindsAreIgnoredForDisambiguation pins
// the closed structureOfferKinds set: a pool spanning a non-offerable kind
// (e.g. document) alongside exactly one offerable kind must NOT be treated
// as ambiguous on the expected_kind axis, since only one OFFERABLE kind is
// actually in contention.
func TestKindOfferMaterial_NonOfferableKindsAreIgnoredForDisambiguation(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectDocument, "doc_1"),
	}
	material, diag := kindOfferMaterial(distinctOfferableKinds(candidates), nil, nil, heldFromKinds(distinctOfferableKinds(candidates)...))
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(pull_request + document) = %+v, want empty: document is not in the offerable expected_kind vocabulary", material)
	}
	// CHAOS-4012 v20: document must never count toward DistinctKindCount --
	// only pull_request does, so this is the SAME "1 distinct, suppressed"
	// shape as the single-kind-pool case, for a different reason (a
	// non-offerable second kind, not merely a duplicate of the first).
	if diag.DistinctKindCount != 1 || !diag.SuppressedByCardinality {
		t.Errorf("diag = %+v, want DistinctKindCount=1, SuppressedByCardinality=true -- document never counts as a second distinct offerable kind", diag)
	}
}

// TestKindOfferMaterial_DeclaredKindRanksFirstAlongsidePool (CHAOS-4967,
// SUPERSEDED IN PART by CHAOS-5218) covers both halves of the declared-kind
// ranking rule on the SAME fixture shape, because CHAOS-4967's own rep 1 --
// a frame declaring member kind `repository` ("Which repositories does the
// platform team own?") whose kind-hinted lexical search found nothing, so the
// pool holds only ci_pipeline_run/project/pull_request/pull_request_review --
// turns out to be a CHAOS-5218 instance itself: `repository` has ZERO
// candidates in that pool, so offering it (as this test used to assert)
// hands the caller a receipt-bound option that
// filterCandidatesByConfirmedKind will turn into an empty pool and a
// guaranteed no_match one turn later.
//
// What CHAOS-4967 established and CHAOS-5218 KEEPS: a declared kind the pool
// can actually serve ranks FIRST, ahead of every pool-derived kind. What
// CHAOS-5218 changes: a declared kind the pool cannot serve is withheld and
// counted, instead of ranked first.
func TestKindOfferMaterial_DeclaredKindRanksFirstAlongsidePool(t *testing.T) {
	t.Parallel()
	// CHAOS-4967's rep 1 verbatim -- repository is DECLARED but absent from
	// the pool. CHAOS-5218: withheld, and the offer is the pool's own kinds.
	poolKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectProject,
		contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectPullRequestReview,
	}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository}
	material, diag := kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))
	// CHAOS-5218: repository cannot be offered, so under CHAOS-4967's own
	// disjunction the need is not raised at all -- offering the four unrelated
	// pool kinds instead is exactly the list-that-omits-the-named-kind state
	// CHAOS-4967 filed.
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Fatalf("kindOfferMaterial() = %+v, want NO need raised -- the declared kind has no candidate of its own", material)
	}
	if !diag.SuppressedByUnservableDeclaredKind {
		t.Fatalf("diag = %+v, want SuppressedByUnservableDeclaredKind true", diag)
	}
	if diag.SuppressedByCardinality {
		t.Fatalf("diag = %+v, want SuppressedByCardinality FALSE -- the pool held four distinct kinds; cardinality is not the reason", diag)
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DeclaredHintCount: 0, DeclaredWithheldNotInPoolCount: 1, DeclaredWithheldNotInPoolKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository}, SuppressedByUnservableDeclaredKind: true, DistinctKindCount: 4, SuppressedByCardinality: false}) {
		t.Errorf("diag = %+v, want DeclaredWithheldNotInPoolCount:1 Kinds:[repository] SuppressedByUnservableDeclaredKind:true DistinctKindCount:4 SuppressedByCardinality:false", diag)
	}

	// The CHAOS-4967 property CHAOS-5218 preserves, on the SAME fixture with
	// repository ADDED to the pool: the declared kind is served, so it is
	// offered and ranks FIRST.
	servablePool := append(append([]contractsv1.ContextFabricSubjectKind{}, poolKinds...), contractsv1.ContextFabricSubjectRepository)
	material, diag = kindOfferMaterial(servablePool, nil, declaredKinds, heldFromKinds(servablePool...))
	if len(material.KindOptions) != 5 {
		t.Fatalf("len(material.KindOptions) = %d, want 5 (the declared kind plus the 4 pool kinds)", len(material.KindOptions))
	}
	if got := material.KindOptions[0].Kind; got != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("KindOptions[0].Kind = %q, want repository ranked FIRST -- CHAOS-4967's own defect was the declared kind absent from, not merely buried within, its own offer", got)
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DeclaredHintCount: 1, DeclaredWithheldNotInPoolCount: 0, DistinctKindCount: 5, SuppressedByCardinality: false}) {
		t.Errorf("diag = %+v, want {ExplicitHintCount:0 DeclaredHintCount:1 DeclaredWithheldNotInPoolCount:0 DistinctKindCount:5 SuppressedByCardinality:false}", diag)
	}
}

// TestKindOfferMaterial_DeclaredKindAloneSuppressesNeed pins the other half
// of the CHAOS-4967 ruling: a declared kind is the frame's OWN prior
// conclusion about this question, not a real ambiguity, so a declared kind
// with an otherwise empty pool and no explicit hint must NOT raise
// expected_kind -- there is nothing left to disambiguate. This is why
// declaredKinds is a parameter separate from explicitKinds (an explicit
// hint bypasses this same gate alone, see the explicit-kind tests below) --
// a declared kind must not.
func TestKindOfferMaterial_DeclaredKindAloneSuppressesNeed(t *testing.T) {
	t.Parallel()
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository}
	material, diag := kindOfferMaterial(nil, nil, declaredKinds, heldFromKinds())
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(nil pool, nil explicit, [repository] declared) = %+v, want empty -- a declared kind alone is not an ambiguity to disclose", material)
	}
	// CHAOS-5218: an EMPTY pool holds no candidate of the declared kind
	// either, so the kind is now WITHHELD rather than ranked-then-suppressed.
	// The outcome this test exists for -- no need raised -- is unchanged; the
	// diagnostics move because DistinctKindCount now reads 0 ("genuinely
	// nothing offerable") instead of 1, which is the more accurate reading of
	// an empty pool.
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DeclaredHintCount: 0, DeclaredWithheldNotInPoolCount: 1, DeclaredWithheldNotInPoolKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository}, SuppressedByUnservableDeclaredKind: true, DistinctKindCount: 0}) {
		t.Errorf("diag = %+v, want SuppressedByUnservableDeclaredKind:true (an empty pool cannot serve the declared kind either, and that reason now fires BEFORE the cardinality gate, so SuppressedByCardinality stays false)", diag)
	}
}

// TestKindOfferMaterial_TwoDeclaredKindsAloneSuppressesNeed (CHAOS-4967
// codex round 1, P2) is TestKindOfferMaterial_DeclaredKindAloneSuppressesNeed's
// own sibling for the case that finding caught: declaredKinds can carry
// TWO distinct entries at once -- a valid grouped_members frame declares
// both a member_kind and a group_kind, always different (invariant I6) --
// and two distinct declared kinds alone used to satisfy the old
// "len(ranked)<2" cardinality check even though neither the pool nor an
// explicit hint contributed anything: two already-known, frame-declared
// axes are not a real ambiguity any more than one is.
func TestKindOfferMaterial_TwoDeclaredKindsAloneSuppressesNeed(t *testing.T) {
	t.Parallel()
	declaredKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectProject,
	}
	material, diag := kindOfferMaterial(nil, nil, declaredKinds, heldFromKinds())
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(nil pool, nil explicit, [team, project] declared) = %+v, want empty -- two already-declared axes are not an ambiguity to disclose", material)
	}
	// CHAOS-5218: both declared kinds are withheld against an empty pool.
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DeclaredHintCount: 0, DeclaredWithheldNotInPoolCount: 2, DeclaredWithheldNotInPoolKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject}, SuppressedByUnservableDeclaredKind: true, DistinctKindCount: 0}) {
		t.Errorf("diag = %+v, want {ExplicitHintCount:0 DeclaredHintCount:0 DeclaredWithheldNotInPoolCount:2 DeclaredWithheldNotInPoolKinds:[team project] DistinctKindCount:0 SuppressedByCardinality:true}", diag)
	}

	// CHAOS-5218 SUPERSEDES the old complement here. It asserted that the same
	// two declared kinds alongside a pool contributing ONE genuinely new kind
	// (pull_request) raised the need with all three options. Neither declared
	// kind has a candidate in that pool, so both are withheld, `ranked` is the
	// lone pool kind, and the existing len(ranked)<2 gate suppresses -- the
	// same outcome a single-kind pool has always had.
	poolKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest}
	material, diag = kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Fatalf("kindOfferMaterial([pull_request] pool, nil explicit, [team, project] declared) = %+v, want empty -- neither declared kind is in the pool, so only pull_request remains and one kind is not an ambiguity", material)
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 0, DeclaredHintCount: 0, DeclaredWithheldNotInPoolCount: 2, DeclaredWithheldNotInPoolKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject}, SuppressedByUnservableDeclaredKind: true, DistinctKindCount: 1}) {
		t.Errorf("diag = %+v, want {ExplicitHintCount:0 DeclaredHintCount:0 DeclaredWithheldNotInPoolCount:2 DeclaredWithheldNotInPoolKinds:[team project] DistinctKindCount:1 SuppressedByCardinality:true}", diag)
	}

	// The complement CHAOS-5218 KEEPS: when the pool genuinely holds one of
	// the declared kinds AND a second kind, the need is raised and the served
	// declared kind ranks first -- pool contribution is still what makes it a
	// real ambiguity, now measured per-kind instead of in aggregate.
	servablePool := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectPullRequest,
	}
	material, diag = kindOfferMaterial(servablePool, nil, declaredKinds, heldFromKinds(servablePool...))
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want [expected_kind] -- a served declared kind plus a second pool kind must raise", material.Missing)
	}
	if len(material.KindOptions) != 2 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectTeam || material.KindOptions[1].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("KindOptions = %+v, want [team, pull_request] -- the SERVED declared kind ranked first, the unservable one (project) withheld, pool kind last", material.KindOptions)
	}
	if diag.DeclaredHintCount != 1 || diag.DeclaredWithheldNotInPoolCount != 1 {
		t.Errorf("diag = %+v, want DeclaredHintCount 1 (team, served) and DeclaredWithheldNotInPoolCount 1 (project, not in pool)", diag)
	}

	// CHAOS-4967's ORIGINAL property, restored as its own case (codex round 1,
	// P3): when the pool serves BOTH declared kinds, BOTH rank ahead of the
	// unrelated pool kind, in declaredKinds order. The partial fixture above
	// cannot see this -- it only ever admits one declared kind, so a rule that
	// dropped every declared kind after the first would pass it. That rule is
	// exactly the mutant codex named.
	bothServed := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectProject,
		contractsv1.ContextFabricSubjectPullRequest,
	}
	material, diag = kindOfferMaterial(bothServed, nil, declaredKinds, heldFromKinds(bothServed...))
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want [expected_kind]", material.Missing)
	}
	if len(material.KindOptions) != 3 ||
		material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectTeam ||
		material.KindOptions[1].Kind != contractsv1.ContextFabricSubjectProject ||
		material.KindOptions[2].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("KindOptions = %+v, want [team, project, pull_request] -- BOTH declared kinds ranked first, in declaredKinds order, pool kind last", material.KindOptions)
	}
	if diag.DeclaredHintCount != 2 || diag.DeclaredWithheldNotInPoolCount != 0 {
		t.Errorf("diag = %+v, want DeclaredHintCount 2 and DeclaredWithheldNotInPoolCount 0 -- both declared kinds served", diag)
	}
	if diag.SuppressedByUnservableDeclaredKind {
		t.Errorf("diag = %+v, want SuppressedByUnservableDeclaredKind false", diag)
	}
}

// TestCandidateOfferMaterial_EmptyPoolOffersNothing pins the "genuinely
// nothing to rank" case -- distinct from the cardinality question
// kindOfferMaterial's own gate asks; candidateOfferMaterial has no
// cardinality gate at all, only "is there anything to rank."
func TestCandidateOfferMaterial_EmptyPoolOffersNothing(t *testing.T) {
	t.Parallel()
	material, diag := candidateOfferMaterial(nil, 0)
	if len(material.Missing) != 0 || len(material.CandidateOptions) != 0 {
		t.Errorf("candidateOfferMaterial(nil, 0) = %+v, want empty", material)
	}
	if diag != (candidateOfferDiagnostics{}) {
		t.Errorf("diag = %+v, want the zero value", diag)
	}
}

// TestCandidateOfferMaterial_CommittedSuppressesTheOffer pins chris's own
// precondition: candidate-list fires ONLY when nothing committed, mirroring
// kindOfferMaterial/anchorOfferMaterial's own "nothing committed" scoping
// (CHAOS-3900 P1.C's own doc comment) -- a committed resolution has nothing
// left to disambiguate on ANY axis.
func TestCandidateOfferMaterial_CommittedSuppressesTheOffer(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material, diag := candidateOfferMaterial(candidates, 1)
	if len(material.Missing) != 0 || len(material.CandidateOptions) != 0 {
		t.Errorf("candidateOfferMaterial(candidates, committedCount=1) = %+v, want empty", material)
	}
	if diag != (candidateOfferDiagnostics{}) {
		t.Errorf("diag = %+v, want the zero value", diag)
	}
}

// TestCandidateOfferMaterial_SingleCandidateIsAListOfOne pins chris's own
// framing: "a single candidate is a list of one" -- the exact "1 distinct
// kind, cardinality-suppressed" shape kindOfferMaterial refuses to offer on
// its own axis must still be offered here, unconditionally on kind count.
func TestCandidateOfferMaterial_SingleCandidateIsAListOfOne(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material, diag := candidateOfferMaterial(candidates, 0)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectCandidate {
		t.Fatalf("material.Missing = %v, want exactly [subject_candidate]", material.Missing)
	}
	if len(material.CandidateOptions) != 1 || material.CandidateOptions[0].CanonicalID != "wi_1" {
		t.Fatalf("material.CandidateOptions = %+v, want exactly one entry naming wi_1", material.CandidateOptions)
	}
	if diag.CandidateOfferCount != 1 || diag.OfferKind != "candidate" {
		t.Errorf("diag = %+v, want {CandidateOfferCount: 1, OfferKind: \"candidate\"}", diag)
	}
}

// TestCandidateOfferMaterial_CapsAtTopN pins candidateOfferTopN=5: a pool
// bigger than the cap contributes only its first N candidates, in the SAME
// order the (already-ranked) pool arrived in -- this function never
// re-sorts.
func TestCandidateOfferMaterial_CapsAtTopN(t *testing.T) {
	t.Parallel()
	var candidates []contextfabric.SubjectCandidate
	for i := 0; i < 8; i++ {
		candidates = append(candidates, candidateOf(contractsv1.ContextFabricSubjectWorkItem, fmt.Sprintf("wi_%d", i)))
	}
	material, diag := candidateOfferMaterial(candidates, 0)
	if len(material.CandidateOptions) != candidateOfferTopN {
		t.Fatalf("len(material.CandidateOptions) = %d, want %d (candidateOfferTopN)", len(material.CandidateOptions), candidateOfferTopN)
	}
	if diag.CandidateOfferCount != candidateOfferTopN {
		t.Errorf("diag.CandidateOfferCount = %d, want %d", diag.CandidateOfferCount, candidateOfferTopN)
	}
	for i, opt := range material.CandidateOptions {
		want := fmt.Sprintf("wi_%d", i)
		if opt.CanonicalID != want {
			t.Errorf("material.CandidateOptions[%d].CanonicalID = %q, want %q (rank order preserved, never re-sorted)", i, opt.CanonicalID, want)
		}
	}
}

// TestCandidateOfferMaterial_LabelAndOfferSource pins the minted option's
// own field contents -- Label is the candidate's own Subject.Label
// verbatim (candidateOfferLabel's own "show the entity's own name"
// discipline), OfferSource is always engine (this axis has no prior/Bridge
// concept).
func TestCandidateOfferMaterial_LabelAndOfferSource(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repoA", Label: "full-chaos/widget-service"}},
	}
	material, _ := candidateOfferMaterial(candidates, 0)
	if len(material.CandidateOptions) != 1 {
		t.Fatalf("len(material.CandidateOptions) = %d, want 1", len(material.CandidateOptions))
	}
	opt := material.CandidateOptions[0]
	if opt.Label != "full-chaos/widget-service" {
		t.Errorf("opt.Label = %q, want the candidate's own Subject.Label verbatim", opt.Label)
	}
	if opt.Kind != contractsv1.ContextFabricSubjectRepository {
		t.Errorf("opt.Kind = %q, want repository", opt.Kind)
	}
	if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
		t.Errorf("opt.OfferSource = %q, want engine", opt.OfferSource)
	}
}

// TestCandidateOfferMaterial_LabelOverV1BoundIsBoundedAtTheProducer
// (CHAOS-4210, ext65 corpus case index 6) pins the fix for the exact
// pre-existing turn-1 validation error the ledger recorded:
// "candidate option option_id or label violates v1 bounds"
// (validate_context_fabric_structure.go's ContextFabricCandidateOption.
// Validate(), Label bounded to [1,200] runes). tables.go's queryWorkItems
// already guards Subject.Label against EMPTY (falls back to the work item
// id) but never against a real title exceeding 200 runes -- an ordinary,
// legitimate long title, not malformed data. candidateOfferLabel passed
// Subject.Label through verbatim, so any candidate whose title is long
// enough reaches composeStructureNeeds with a Label the wire contract
// rejects outright, turning a single long-titled candidate into a whole
// investigation-result validation failure. The receipt_id/option_id this
// test supplies are synthetically well-formed (real values are minted
// later, by composeStructureNeeds in the parent package) so only the
// Label bound is under test.
func TestCandidateOfferMaterial_LabelOverV1BoundIsBoundedAtTheProducer(t *testing.T) {
	t.Parallel()
	longTitle := strings.Repeat("a", 250)
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "wi_1", Label: longTitle}},
	}
	material, diag := candidateOfferMaterial(candidates, 0)
	if len(material.CandidateOptions) != 1 {
		t.Fatalf("len(material.CandidateOptions) = %d, want 1", len(material.CandidateOptions))
	}
	if diag.LabelsNormalizedCount != 1 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 1 -- the run's own artifacts must show this label was normalized", diag.LabelsNormalizedCount)
	}
	opt := material.CandidateOptions[0]
	opt.ReceiptID = contractsv1.ContextFabricCandidateOptionReceiptPrefix + strings.Repeat("a", 24)
	opt.OptionID = "opt_" + strings.Repeat("a", 16)
	if err := opt.Validate(); err != nil {
		t.Fatalf("opt.Validate() = %v, want nil -- the producer must bound Label to the wire contract, not the caller", err)
	}
	if got := utf8.RuneCountInString(opt.Label); got > 200 {
		t.Errorf("len(opt.Label) = %d runes, want <= 200 (v1 bound)", got)
	}
	if opt.Label != longTitle[:200] {
		t.Errorf("opt.Label = %q, want the first 200 runes of the original title verbatim", opt.Label)
	}
}

// TestCandidateOfferMaterial_LabelOverV1BoundMultibyteIsTruncatedOnRuneBoundary
// pins truncation correctness for a title whose runes are NOT one byte
// each -- codex xhigh review (CHAOS-4210 round 1): the all-ASCII case above
// cannot distinguish rune-safe truncation from a byte-slice cut that would
// split a multibyte rune and corrupt the label (or panic on re-encoding).
func TestCandidateOfferMaterial_LabelOverV1BoundMultibyteIsTruncatedOnRuneBoundary(t *testing.T) {
	t.Parallel()
	longTitle := strings.Repeat("é", 250)
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "wi_1", Label: longTitle}},
	}
	material, diag := candidateOfferMaterial(candidates, 0)
	if len(material.CandidateOptions) != 1 {
		t.Fatalf("len(material.CandidateOptions) = %d, want 1", len(material.CandidateOptions))
	}
	if diag.LabelsNormalizedCount != 1 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 1", diag.LabelsNormalizedCount)
	}
	opt := material.CandidateOptions[0]
	opt.ReceiptID = contractsv1.ContextFabricCandidateOptionReceiptPrefix + strings.Repeat("a", 24)
	opt.OptionID = "opt_" + strings.Repeat("a", 16)
	if err := opt.Validate(); err != nil {
		t.Fatalf("opt.Validate() = %v, want nil", err)
	}
	if !utf8.ValidString(opt.Label) {
		t.Fatalf("opt.Label is not valid UTF-8 -- truncation split a multibyte rune")
	}
	if got := utf8.RuneCountInString(opt.Label); got != 200 {
		t.Errorf("utf8.RuneCountInString(opt.Label) = %d, want exactly 200", got)
	}
	if opt.Label != strings.Repeat("é", 200) {
		t.Errorf("opt.Label = %q, want the first 200 runes verbatim, not a byte-boundary cut", opt.Label)
	}
}

// TestCandidateOfferMaterial_LabelAtOrUnderV1BoundIsUnchanged pins the
// companion invariant: bounding must not clip a title that already fits --
// exactly 200 runes (the bound itself) and a short ordinary title both
// survive verbatim.
func TestCandidateOfferMaterial_LabelAtOrUnderV1BoundIsUnchanged(t *testing.T) {
	t.Parallel()
	exactTitle := strings.Repeat("b", 200)
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "wi_1", Label: exactTitle}},
	}
	material, diag := candidateOfferMaterial(candidates, 0)
	if len(material.CandidateOptions) != 1 || material.CandidateOptions[0].Label != exactTitle {
		t.Fatalf("material.CandidateOptions[0].Label = %q, want the exact-200-rune title unchanged", material.CandidateOptions[0].Label)
	}
	if diag.LabelsNormalizedCount != 0 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 0 -- a title already at the bound must not be counted as normalized", diag.LabelsNormalizedCount)
	}
}

// TestDistinctCandidateKinds_DedupedFirstOccurrenceOrder pins the
// call-boundary telemetry helper (CHAOS-4012 v22, team-lead ruling
// 2026-08-23): deduped, first-occurrence order, string-valued.
func TestDistinctCandidateKinds_DedupedFirstOccurrenceOrder(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "wi_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repo_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "wi_2"}},
	}
	got := distinctCandidateKinds(candidates)
	want := []string{"work_item", "repository"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("distinctCandidateKinds() = %v, want %v", got, want)
	}
}

// TestDistinctCandidateKinds_EmptyPoolReturnsNil pins the empty case: no
// candidates means nothing survived to this boundary, distinguishable from
// a populated-but-filtered-to-nothing result.
func TestDistinctCandidateKinds_EmptyPoolReturnsNil(t *testing.T) {
	t.Parallel()
	if got := distinctCandidateKinds(nil); got != nil {
		t.Errorf("distinctCandidateKinds(nil) = %v, want nil", got)
	}
}

// TestUnionCandidatesForOffer_NoOverlapConcatenates is the ordinary case:
// disjoint subjects, resolutionCandidates first, coverageCandidates after,
// in each side's own order.
func TestUnionCandidatesForOffer_NoOverlapConcatenates(t *testing.T) {
	t.Parallel()
	resolutionCandidates := []contextfabric.SubjectCandidate{candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1")}
	coverageCandidates := []contextfabric.SubjectCandidate{candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1")}
	got := unionCandidatesForOffer(resolutionCandidates, coverageCandidates)
	want := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
	}
	if len(got) != len(want) || got[0].Subject.CanonicalID != want[0].Subject.CanonicalID || got[1].Subject.CanonicalID != want[1].Subject.CanonicalID {
		t.Errorf("unionCandidatesForOffer() = %+v, want %+v", got, want)
	}
}

// TestUnionCandidatesForOffer_OverlapDedupesBySubject is the codex xhigh R2
// regression pin (2026-08-23, NEW HIGH): a coverage-floor find that SURVIVED
// ResolveFromMergedCandidatesWithGate's own final truncation appears in
// BOTH resolutionCandidates (the ranked pool) and coverageCandidates
// (applyKindCoverageFloor's own `added` return) -- see this function's own
// doc comment for the mechanism. A naive concatenation would emit that
// subject TWICE, which candidateOfferMaterial would then turn into two
// CandidateOptions minting the SAME deterministic receipt id, failing
// structure.go's uniqueness validation. This pins that the union instead
// contains the subject exactly ONCE.
func TestUnionCandidatesForOffer_OverlapDedupesBySubject(t *testing.T) {
	t.Parallel()
	// codex xhigh R3 (2026-08-23, LOW test-gap note): the resolution-side
	// and coverage-side copies carry DIFFERENT Label values -- same subject
	// key, different content -- so this proves resolution.Candidates' own
	// metadata wins the collision, not merely that object identity happens
	// to match.
	resolutionSideCopy := candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1")
	resolutionSideCopy.Subject.Label = "resolution-side label"
	coverageSideCopy := candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1")
	coverageSideCopy.Subject.Label = "coverage-side label"
	resolutionCandidates := []contextfabric.SubjectCandidate{
		resolutionSideCopy,
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
	}
	// The SAME subject key, also returned by applyKindCoverageFloor's own
	// `added` -- exactly the overlap the doc comment describes.
	coverageCandidates := []contextfabric.SubjectCandidate{coverageSideCopy}

	got := unionCandidatesForOffer(resolutionCandidates, coverageCandidates)
	if len(got) != 2 {
		t.Fatalf("unionCandidatesForOffer() = %+v (len %d), want exactly 2 -- the overlapping subject must appear once", got, len(got))
	}
	var wi1 *contextfabric.SubjectCandidate
	count := 0
	for i, c := range got {
		if c.Subject.Kind == contractsv1.ContextFabricSubjectWorkItem && c.Subject.CanonicalID == "wi_1" {
			count++
			wi1 = &got[i]
		}
	}
	if count != 1 {
		t.Fatalf("wi_1 appears %d times in unionCandidatesForOffer(), want exactly 1", count)
	}
	if wi1.Subject.Label != "resolution-side label" {
		t.Errorf("surviving wi_1 label = %q, want the resolution-side copy's own label -- resolution.Candidates must win a collision, never coverageCandidates", wi1.Subject.Label)
	}
}

// TestUnionCandidatesForOffer_DroppedCoverageFindStillIncluded is
// CHAOS-4038 finding 1's own case, still exercised through the extracted
// helper: a coverage-floor find that truncation DROPPED from
// resolutionCandidates entirely (present in coverageCandidates only) must
// still reach the union.
func TestUnionCandidatesForOffer_DroppedCoverageFindStillIncluded(t *testing.T) {
	t.Parallel()
	resolutionCandidates := []contextfabric.SubjectCandidate{candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1")}
	droppedFind := candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_dropped")
	got := unionCandidatesForOffer(resolutionCandidates, []contextfabric.SubjectCandidate{droppedFind})
	if len(got) != 2 {
		t.Fatalf("unionCandidatesForOffer() = %+v, want 2 -- the dropped coverage find must still be included", got)
	}
	if got[1].Subject.CanonicalID != "pr_dropped" {
		t.Errorf("unionCandidatesForOffer()[1] = %+v, want the dropped coverage find", got[1])
	}
}

// TestUnionCandidatesForOffer_EmptyInputsReturnEmpty pins the zero-value
// boundary: no panics, a non-nil-but-empty slice either way.
func TestUnionCandidatesForOffer_EmptyInputsReturnEmpty(t *testing.T) {
	t.Parallel()
	if got := unionCandidatesForOffer(nil, nil); len(got) != 0 {
		t.Errorf("unionCandidatesForOffer(nil, nil) = %+v, want empty", got)
	}
}

func poolOf(candidates ...contextfabric.SubjectCandidate) map[string]contextfabric.SubjectCandidate {
	pool := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, c := range candidates {
		pool[SubjectKey(c.Subject)] = c
	}
	return pool
}

// TestFilterCandidatesByConfirmedKind_NilIsNoOp is the P1.D structural
// pin: an ordinary request (no kindr_ receipt confirmed, the overwhelming
// common case) must see BYTE-IDENTICAL pool composition, proving the
// filter never touches the pool absent a confirmation.
func TestFilterCandidatesByConfirmedKind_NilIsNoOp(t *testing.T) {
	t.Parallel()
	pool := poolOf(
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	)
	got := filterCandidatesByConfirmedKind(pool, nil)
	if len(got) != len(pool) {
		t.Fatalf("filterCandidatesByConfirmedKind(pool, nil) returned %d entries, want %d (unchanged)", len(got), len(pool))
	}
	for key, candidate := range pool {
		if got[key].Subject.CanonicalID != candidate.Subject.CanonicalID {
			t.Errorf("filterCandidatesByConfirmedKind(pool, nil)[%q] = %+v, want unchanged %+v", key, got[key], candidate)
		}
	}
}

func TestFilterCandidatesByConfirmedKind_NarrowsToConfirmedKindOnly(t *testing.T) {
	t.Parallel()
	pool := poolOf(
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_2"),
	)
	got := filterCandidatesByConfirmedKind(pool, &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (only the two work_item candidates)", len(got))
	}
	for _, candidate := range got {
		if candidate.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("filtered pool contains a non-confirmed kind %q: %+v", candidate.Subject.Kind, candidate)
		}
	}
}

func TestFilterCandidatesByConfirmedKind_NoMatchingCandidatesEmptiesThePool(t *testing.T) {
	t.Parallel()
	pool := poolOf(candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"))
	got := filterCandidatesByConfirmedKind(pool, &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem})
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0: nothing in the pool matches the confirmed kind", len(got))
	}
}

// fakeCensusFn builds a CensusFunc returning a fixed count per kind, for
// kindInsensitivityProof's own tests.
func fakeCensusFn(counts map[CensusKind]int, err error) CensusFunc {
	return func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contractsv1.ContextFabricSubjectKind, _ string, _ bool) (CensusOutcome, error) {
		if err != nil {
			return CensusOutcome{}, err
		}
		return CensusOutcome{Count: counts[kind]}, nil
	}
}

// TestKindInsensitivityProof pins design brief §2.0's own all-kinds
// census proof and its two implementation pins (registry-miss poison,
// error-fails-safe) -- this primitive is UNWIRED today (see its own
// doc comment) but must still be correct and independently testable
// ahead of the decisive-path wiring a future inferred-tier kind source
// requires.
func TestKindInsensitivityProof(t *testing.T) {
	t.Parallel()

	// pull_request and ci_pipeline_run both accept a repository anchor
	// (KindHasAnchorFK: pull_request explicitly, ci_pipeline_run via the
	// default case) -- the ONE anchor kind that reaches both pooled kinds
	// in these tests, so anchorApplies is true for every censused kind.
	const anchorKind = contextfabric.SubjectRepository
	const anchorCanonicalID = "repository:r-1"

	t.Run("single all-kinds satisfier is commit-sound", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 1,
			contractsv1.ContextFabricSubjectCIRun:       0,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivityCommitSound {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivityCommitSound)
		}
	})
	t.Run("zero all-kinds satisfiers is no-match-sound", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 0,
			contractsv1.ContextFabricSubjectCIRun:       0,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivityNoMatchSound {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivityNoMatchSound)
		}
	})
	t.Run("more than one all-kinds satisfier is kind_sensitive_outcome", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 1,
			contractsv1.ContextFabricSubjectCIRun:       1,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivitySensitive)
		}
	})
	t.Run("a pre-narrowing kind outside the closed registry poisons the round", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 1}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectDocument},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q (registry-miss poison)", got, kindInsensitivitySensitive)
		}
	})
	t.Run("a census error fails safe, not open", func(t *testing.T) {
		census := fakeCensusFn(nil, errors.New("boom"))
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest}, "", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on census error", got, kindInsensitivitySensitive)
		}
	})
	t.Run("nil census is sensitive, not a panic", func(t *testing.T) {
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest}, "", "", false, anchorKind, anchorCanonicalID, true, nil)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on nil census", got, kindInsensitivitySensitive)
		}
	})
	t.Run("empty pre-narrowing kind set is sensitive", func(t *testing.T) {
		got := kindInsensitivityProof(context.Background(), "org_1", nil, "", "", false, anchorKind, anchorCanonicalID, true, fakeCensusFn(nil, nil))
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on empty kind set", got, kindInsensitivitySensitive)
		}
	})
	// codex xhigh review, CHAOS-3972 round 1, finding 1's own regression
	// pin: a handle bound to ONE kind must never be applied to another
	// pooled kind as if it were valid there too.
	t.Run("a handle bound to one kind is never applied to another pooled kind", func(t *testing.T) {
		calls := map[CensusKind]struct {
			value       string
			handleBound bool
			anchorBound bool
		}{}
		census := func(_ context.Context, _ string, kind CensusKind, value string, handleBound bool, _ contextfabric.SubjectKind, _ string, anchorBound bool) (CensusOutcome, error) {
			calls[kind] = struct {
				value       string
				handleBound bool
				anchorBound bool
			}{value, handleBound, anchorBound}
			if kind == contractsv1.ContextFabricSubjectCIRun {
				return CensusOutcome{Count: 1}, nil
			}
			return CensusOutcome{Count: 0}, nil
		}
		// A repository anchor reaches BOTH pooled kinds (pull_request
		// explicitly, ci_pipeline_run via the default KindHasAnchorFK
		// case), so pull_request is still legitimately censused -- via the
		// anchor, never the ci_pipeline_run handle.
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			contractsv1.ContextFabricSubjectCIRun, "18234567", true, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivityCommitSound {
			t.Fatalf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivityCommitSound)
		}
		if pr := calls[contractsv1.ContextFabricSubjectPullRequest]; pr.handleBound || pr.value != "" {
			t.Errorf("pull_request census call carried handleBound=%v value=%q -- the ci_pipeline_run handle must never apply to a kind it was never bound to", pr.handleBound, pr.value)
		}
		if pr := calls[contractsv1.ContextFabricSubjectPullRequest]; !pr.anchorBound {
			t.Errorf("pull_request census call carried anchorBound=false, want true -- it must still be censused via the anchor")
		}
		if ci := calls[contractsv1.ContextFabricSubjectCIRun]; !ci.handleBound || ci.value != "18234567" {
			t.Errorf("ci_pipeline_run census call = %+v, want handleBound=true value=18234567", ci)
		}
	})
	// codex xhigh review, CHAOS-3972 round 1, finding 2's own regression
	// pin: a census outcome that could not prove closure must never feed
	// the total, in either direction.
	t.Run("closure mismatch poisons the proof even at count 1", func(t *testing.T) {
		census := func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
			if kind == contractsv1.ContextFabricSubjectPullRequest {
				return CensusOutcome{Count: 1, ClosureMismatch: true}, nil
			}
			return CensusOutcome{Count: 0}, nil
		}
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q -- a ClosureMismatch outcome must never certify commit_sound", got, kindInsensitivitySensitive)
		}
	})
	t.Run("a pooled kind no keyed predicate reaches poisons the proof", func(t *testing.T) {
		// work_item's own anchor FK is project, not repository -- the
		// shared anchorKind above (repository) does not reach it, and no
		// handle is bound, so nothing censuses it.
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 1}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectWorkItem},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q -- work_item has no reachable keyed predicate here", got, kindInsensitivitySensitive)
		}
	})
	// codex xhigh review, CHAOS-3972 round 2 coverage gap: a direct
	// SatisfierSetClosureMismatch pin, distinct from ClosureMismatch above
	// -- CHAOS-3896 Slice B's own second closure flag must be checked too.
	t.Run("satisfier set closure mismatch poisons the proof even at count 1", func(t *testing.T) {
		census := func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
			if kind == contractsv1.ContextFabricSubjectPullRequest {
				return CensusOutcome{Count: 1, SatisfierSetClosureMismatch: true}, nil
			}
			return CensusOutcome{Count: 0}, nil
		}
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun},
			"", "", false, anchorKind, anchorCanonicalID, true, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q -- a SatisfierSetClosureMismatch outcome must never certify commit_sound", got, kindInsensitivitySensitive)
		}
	})
}

// TestResolveSubjects_ConfirmedKindNarrowsThePool is the P1.D end-to-end
// integration pin: a confirmedKind passed into ResolveSubjects itself
// (not just filterCandidatesByConfirmedKind in isolation) actually
// narrows resolution.Candidates to that kind alone.
func TestResolveSubjects_ConfirmedKindNarrowsThePool(t *testing.T) {
	t.Parallel()
	pr := candidateNode(contractsv1.ContextFabricSubjectPullRequest, "pr_1", "PR 1", 0.5, "*")
	wi := candidateNode(contractsv1.ContextFabricSubjectWorkItem, "wi_1", "WI 1", 0.5, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {pr, wi}}}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(),
		&contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem}, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("resolution.Candidates contains a non-confirmed kind %q: %+v (confirmed kind must have narrowed the pool)", candidate.Subject.Kind, candidate)
		}
	}
	for _, committed := range resolution.Committed {
		if committed.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("resolution.Committed contains a non-confirmed kind %q: %+v", committed.Kind, committed)
		}
	}
}

// TestResolveSubjects_NilConfirmedKindIsByteIdenticalToPreP1D is the
// structural pin at the ResolveSubjects call level: an ordinary request
// (nil confirmedKind) must resolve EXACTLY as it did before P1.D existed
// -- both kinds present in the pool, neither dropped.
func TestResolveSubjects_NilConfirmedKindIsByteIdenticalToPreP1D(t *testing.T) {
	t.Parallel()
	pr := candidateNode(contractsv1.ContextFabricSubjectPullRequest, "pr_1", "PR 1", 0.5, "*")
	wi := candidateNode(contractsv1.ContextFabricSubjectWorkItem, "wi_1", "WI 1", 0.5, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {pr, wi}}}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, candidate := range resolution.Candidates {
		seen[candidate.Subject.Kind] = true
	}
	if !seen[contractsv1.ContextFabricSubjectPullRequest] || !seen[contractsv1.ContextFabricSubjectWorkItem] {
		t.Errorf("resolution.Candidates = %#v, want BOTH kinds present (nil confirmedKind must not narrow anything)", resolution.Candidates)
	}
}

// TestResolveSubjects_AnchorAndHandleOffersEndToEnd is P1.C”s own
// end-to-end integration pin (mirroring P1.D's TestResolveSubjects_
// ConfirmedKindNarrowsThePool pattern): proves resolve.go's OWN wiring
// (not anchorOfferMaterial/handleOfferMaterial in isolation) produces the
// combined offer material. Two different terms each uniquely alias-match a
// DIFFERENT repository (the disagreement case), and the question text
// carries a grammar-bound PR number -- neither commits, and both surface
// as offers on the SAME ResolveSubjects call.
func TestResolveSubjects_AnchorAndHandleOffersEndToEnd(t *testing.T) {
	t.Parallel()
	repoA := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", -1, []string{"widget-service"}, nil, true)
	repoB := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", -1, []string{"widget-svc"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup: true,
		aliasLookupClaimants: map[string][]CandidateNode{
			"widget-service": {repoA},
			"widget-svc":     {repoB},
		},
		aliasLookupComplete: true,
	}
	request := testRequest()
	request.Question = "is PR 532 related to widget-service or widget-svc?"

	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("widget-service", "widget-svc"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed: two terms disagree on the anchor, genuinely ambiguous", resolution.Committed)
	}

	// CHAOS-4012: subject_candidate joins Missing here too -- it fires
	// independently of kind-pick (nothing committed, pool non-empty is the
	// whole precondition), so "kind is unambiguous" no longer means
	// "nothing else is offered."
	wantMissing := map[contractsv1.ContextFabricStructureNeedKind]bool{
		contractsv1.ContextFabricStructureNeedSubjectAnchor:    true,
		contractsv1.ContextFabricStructureNeedSubjectHandle:    true,
		contractsv1.ContextFabricStructureNeedSubjectCandidate: true,
	}
	if len(material.Missing) != len(wantMissing) {
		t.Fatalf("material.Missing = %v, want exactly %v (kind is unambiguous here: only repositories are in the pool)", material.Missing, wantMissing)
	}
	for _, m := range material.Missing {
		if !wantMissing[m] {
			t.Errorf("material.Missing contains unexpected member %q", m)
		}
	}

	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2 (one per disagreeing candidate)", len(material.AnchorOptions))
	}
	seenAnchors := map[string]bool{}
	for _, opt := range material.AnchorOptions {
		seenAnchors[opt.CanonicalID] = true
	}
	if !seenAnchors["repoA"] || !seenAnchors["repoB"] {
		t.Errorf("material.AnchorOptions = %+v, want repoA AND repoB", material.AnchorOptions)
	}

	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1", len(material.HandleOptions))
	}
	if material.HandleOptions[0].Value != "532" || material.HandleOptions[0].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Errorf("material.HandleOptions[0] = %+v, want value=532 kind=pull_request", material.HandleOptions[0])
	}
	// CHAOS-4012: candidate-list fired too -- both repoA and repoB are
	// ranked candidates in the (uncommitted) pool.
	if len(material.CandidateOptions) != 2 {
		t.Fatalf("len(material.CandidateOptions) = %d, want 2", len(material.CandidateOptions))
	}
	seenCandidates := map[string]bool{}
	for _, opt := range material.CandidateOptions {
		seenCandidates[opt.CanonicalID] = true
	}
	if !seenCandidates["repoA"] || !seenCandidates["repoB"] {
		t.Errorf("material.CandidateOptions = %+v, want repoA AND repoB", material.CandidateOptions)
	}
}

// TestResolveSubjects_AnchorOffersExcludeUnauthorizedClaimant is CHAOS-4042's
// auth-gap closure proof (sol-max ruling): aliasClaimantsByTerm was org-scoped
// but never AuthorizedAttributes-filtered before reaching anchorOfferMaterial
// (unlike candidatesBySubject, which always passes through NodeCandidate's
// own AuthorizedAttributes check). A claimant principal cannot see must never
// surface as a disclosed AnchorOption -- that would leak the existence of an
// entity the principal has no other way to read.
func TestResolveSubjects_AnchorOffersExcludeUnauthorizedClaimant(t *testing.T) {
	t.Parallel()
	repoA := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", -1, []string{"widget-service"}, nil, true)
	repoA.Attributes["authorization_repositories"] = []string{"repoA"}
	repoB := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", -1, []string{"widget-svc"}, nil, true)
	repoB.Attributes["authorization_repositories"] = []string{"repoA"}
	// repoC is a genuine claimant the identity universe read still contains
	// -- principal simply may not see it. Without the auth-split fix, its
	// (kind, canonical_id, label) would leak as a third AnchorOption even
	// though repoA and repoB (on their own, unrelated terms) already
	// disagree and would be offered regardless of repoC's presence.
	repoC := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoC", "repoC", -1, []string{"gadget-svc"}, nil, true)
	repoC.Attributes["authorization_repositories"] = []string{"repoC"}
	backend := &fakeGraphBackend{
		enableAliasLookup: true,
		aliasLookupClaimants: map[string][]CandidateNode{
			"widget-service": {repoA},
			"widget-svc":     {repoB},
			"gadget-svc":     {repoC},
		},
		aliasLookupComplete: true,
	}
	request := testRequest()
	request.Question = "is PR 532 related to widget-service, widget-svc, or gadget-svc?"
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"repoA"}}

	_, material, err := ResolveSubjects(context.Background(), principal, request, testInterpreted("widget-service", "widget-svc", "gadget-svc"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2 (repoA and repoB only; repoC is unauthorized): %+v", len(material.AnchorOptions), material.AnchorOptions)
	}
	seenAnchors := map[string]bool{}
	for _, opt := range material.AnchorOptions {
		seenAnchors[opt.CanonicalID] = true
	}
	if !seenAnchors["repoA"] || !seenAnchors["repoB"] {
		t.Errorf("material.AnchorOptions = %+v, want repoA AND repoB", material.AnchorOptions)
	}
	if seenAnchors["repoC"] {
		t.Errorf("material.AnchorOptions = %+v, must NOT disclose repoC -- principal is not authorized to see it", material.AnchorOptions)
	}
}

func TestValidateHandleGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		kind      contractsv1.ContextFabricSubjectKind
		patternID string
		value     string
		want      bool
	}{
		{"pull request number, bare digits, valid", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", true},
		{"pull request number, with PR context, invalid (value must be bare)", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "PR 532", false},
		{"pull request number, non-numeric, invalid", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "abc", false},
		{"work item ticket key, valid", contractsv1.ContextFabricSubjectWorkItem, "work_item_ticket_key", "CHAOS-3896", true},
		{"work item ticket key, missing prefix, invalid", contractsv1.ContextFabricSubjectWorkItem, "work_item_ticket_key", "3896", false},
		{"ci run id, valid", contractsv1.ContextFabricSubjectCIRun, "ci_run_id", "18234567", true},
		{"ci run id, too short, invalid", contractsv1.ContextFabricSubjectCIRun, "ci_run_id", "123", false},
		{"kind and pattern_id mismatched, invalid", contractsv1.ContextFabricSubjectPullRequest, "work_item_ticket_key", "CHAOS-3896", false},
		{"unknown pattern_id, invalid", contractsv1.ContextFabricSubjectPullRequest, "bogus_pattern", "532", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateHandleGrammar(tc.kind, tc.patternID, tc.value)
			if got != tc.want {
				t.Errorf("ValidateHandleGrammar(%q, %q, %q) = %v, want %v", tc.kind, tc.patternID, tc.value, got, tc.want)
			}
		})
	}
}

func TestVerifyHandle(t *testing.T) {
	t.Parallel()

	t.Run("grammar mismatch short-circuits before any census call", func(t *testing.T) {
		called := false
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			called = true
			return CensusOutcome{Count: 1}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "not-a-number", census)
		if valid || reason != HandleVerificationGrammarMismatch {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationGrammarMismatch)
		}
		if called {
			t.Error("census was called despite a grammar mismatch -- existence check must never run on an already-invalid value")
		}
	})
	t.Run("grammar valid, census confirms existence", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 1}, nil)
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if !valid || reason != HandleVerificationValid {
			t.Errorf("VerifyHandle() = (%v, %q), want (true, %q)", valid, reason, HandleVerificationValid)
		}
	})
	t.Run("grammar valid, census finds nothing", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 0}, nil)
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationNotFound {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationNotFound)
		}
	})
	t.Run("census unavailable (nil) fails safe", func(t *testing.T) {
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", nil)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	t.Run("census error fails safe, not open", func(t *testing.T) {
		census := fakeCensusFn(nil, errors.New("boom"))
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	// Codex xhigh review (chaos-pivot-p1, first round), finding 1: a
	// nonzero Count alone is not sufficient -- a ClosureMismatch (or
	// SatisfierSetClosureMismatch) means the census could not PROVE the
	// fetched set/witness actually closed against the aggregate (the same
	// "race can only demote, never mint" rule chaos3899_census.go's own
	// producer and every other CensusOutcome consumer in this package
	// already apply). VerifyHandle must fail the SAME way an unreachable
	// census does, never validate on a bare Count>0.
	t.Run("closure mismatch fails safe, not open", func(t *testing.T) {
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 1, ClosureMismatch: true}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	t.Run("satisfier-set closure mismatch fails safe, not open", func(t *testing.T) {
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 3, SatisfierSetClosureMismatch: true}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
}

func identityRow(kind contractsv1.ContextFabricSubjectKind, id, label string, aliases ...string) IdentityRow {
	return IdentityRow{Kind: kind, CanonicalID: id, Label: label, Aliases: aliases}
}

func fakeIdentityUniverseFn(rows []IdentityRow, complete bool, err error) IdentityUniverseFunc {
	return func(context.Context, string) ([]IdentityRow, time.Time, bool, error) {
		return rows, time.Time{}, complete, err
	}
}

// TestHashAliasTerm pins HashAliasTerm's own contract: deterministic,
// case/whitespace-insensitive (it hashes the NORMALIZED term, exactly what
// NormalizeAliasTerm produces), and a fixed 24-character digest -- the same
// length mintStructureReceiptID/mintStructureOptionID already use.
func TestHashAliasTerm(t *testing.T) {
	t.Parallel()

	a := HashAliasTerm("widget-service")
	b := HashAliasTerm("  Widget-Service  ")
	if a != b {
		t.Errorf("HashAliasTerm(%q) = %q, HashAliasTerm(%q) = %q, want equal: both normalize to the same term", "widget-service", a, "  Widget-Service  ", b)
	}
	if len(a) != 24 {
		t.Errorf("len(HashAliasTerm(...)) = %d, want 24", len(a))
	}
	if c := HashAliasTerm("a-different-term"); c == a {
		t.Errorf("HashAliasTerm(%q) = %q, want different from HashAliasTerm(%q) = %q", "a-different-term", c, "widget-service", a)
	}
}

// TestNormalizationParity_MatchIdentityRowsAndHashAliasTermAgree is the
// team-lead-mandated normalization-parity pin: MatchIdentityRows (the
// derive-side match) and HashAliasTerm (the verify-side hash) must reach
// the SAME verdict on the same (row, term) input -- a row MatchIdentityRows
// says matches a term must ALSO be a row identityRowCarriesTermHash finds
// via HashAliasTerm(term). If the two sides ever normalized differently,
// this is the test that would catch it; without it, the check "fails
// toward fine" (silently never contests anything) rather than loud.
func TestNormalizationParity_MatchIdentityRowsAndHashAliasTermAgree(t *testing.T) {
	t.Parallel()

	row := identityRow(contractsv1.ContextFabricSubjectRepository, "repo_1", "Widget Service", "  WIDGET-service  ", "widget_svc")
	for _, term := range []string{"widget service", "WIDGET SERVICE", "  Widget-Service  ", "widget_svc"} {
		matches := MatchIdentityRows([]IdentityRow{row}, []string{term})
		derivedMatch := len(matches[term]) == 1
		hashMatch := identityRowCarriesTermHash(row, HashAliasTerm(term))
		if derivedMatch != hashMatch {
			t.Errorf("term %q: MatchIdentityRows found a match = %v, identityRowCarriesTermHash found a match = %v -- the two sides disagree", term, derivedMatch, hashMatch)
		}
	}
}

// TestVerifyAnchorClaimantUnique_CaseFortyFiveTwinRepoShape is the
// team-lead-mandated case-45-shaped regression (CHAOS-3917's own corpus
// case, reused here for narrative continuity with
// TestResolveFromMergedCandidatesWithGate_ExactLabelNeverCommitsOverACollidingAliasClaimant):
// an anchor offer minted while "widget-service" uniquely names repoA must
// re-verify VALID before a rival claims the same alias, and CONTESTED
// (never silently still-valid, never a different wrong verdict) the moment
// a second repo (repoB) gains the identical alias.
func TestVerifyAnchorClaimantUnique_CaseFortyFiveTwinRepoShape(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)
	repoB := identityRow(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", term)

	t.Run("unique claimant re-verifies valid", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (true, %q)", valid, reason, AnchorVerificationValid)
		}
	})
	t.Run("a rival gaining the SAME alias contests the claim", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA, repoB}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimContested {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimContested)
		}
	})
}

func TestVerifyAnchorClaimantUnique(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)

	t.Run("claim lost: no row carries the hash any longer", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("claim lost: the unique claimant is now a DIFFERENT canonical id", func(t *testing.T) {
		renamed := identityRow(contractsv1.ContextFabricSubjectRepository, "repoZ", "repoZ", term)
		universe := fakeIdentityUniverseFn([]IdentityRow{renamed}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("incomplete enumeration fails closed, not open", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, false, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
	t.Run("identity universe error fails closed, not open", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, errors.New("boom"))
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
	t.Run("nil identity universe dependency fails closed, not open", func(t *testing.T) {
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, nil)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
}

// validGraphAnchorMember is a GraphAnchorMemberFunc test double that always
// reports the node exists and is authorized -- used wherever a test wants
// the ClickHouse-side check to be the ONLY thing under test, with the
// graph-side check trivially agreeing.
func validGraphAnchorMember(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
	return GraphAnchorMemberResult{Exists: true, Authorized: true}, nil
}

// TestVerifyAnchorClaimantMembership_RivalsDoNotInvalidateTheSelectedClaimant
// is CHAOS-4042's (sol-max ruling) own defining-difference proof: the SAME
// scenario TestVerifyAnchorClaimantUnique_CaseFortyFiveTwinRepoShape treats
// as CONTESTED under v1's unique-claimant rule must stay VALID here -- a
// rival gaining (or losing) the term is not, by itself, a reason to refuse
// the selected claimant's own membership.
func TestVerifyAnchorClaimantMembership_RivalsDoNotInvalidateTheSelectedClaimant(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)
	repoB := identityRow(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", term)
	repoC := identityRow(contractsv1.ContextFabricSubjectRepository, "repoC", "repoC", term)

	t.Run("selected remains, rival added: still valid", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA, repoB}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, validGraphAnchorMember)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (true, %q)", valid, reason, AnchorVerificationValid)
		}
	})
	t.Run("selected remains, rival removed: still valid", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, validGraphAnchorMember)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (true, %q)", valid, reason, AnchorVerificationValid)
		}
	})
	t.Run("selected remains among THREE claimants: still valid, never contested", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA, repoB, repoC}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoB", hash, universe, validGraphAnchorMember)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (true, %q) -- multiplicity is never an error under membership semantics", valid, reason, AnchorVerificationValid)
		}
	})
	t.Run("incomplete enumeration still applies membership's fail-closed rule", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, false, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
}

func TestVerifyAnchorClaimantMembership(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)

	t.Run("selected claimant removed: claim lost", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("selected claimant re-keyed to a different canonical id: claim lost", func(t *testing.T) {
		renamed := identityRow(contractsv1.ContextFabricSubjectRepository, "repoZ", "repoZ", term)
		universe := fakeIdentityUniverseFn([]IdentityRow{renamed}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("selected claimant exists but lost the alias: claim lost", func(t *testing.T) {
		// repoA still exists in the universe, but no longer carries `term`
		// (a different label/alias set) -- the term-hash match must fail,
		// not the bare (kind, canonical_id) existence check alone.
		driftedAway := IdentityRow{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repoA", Label: "repoA", Aliases: []string{"a-completely-different-alias"}}
		universe := fakeIdentityUniverseFn([]IdentityRow{driftedAway}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("identity universe error fails closed, not open", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, errors.New("boom"))
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
	t.Run("nil identity universe dependency fails closed, not open", func(t *testing.T) {
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{}, contextfabric.ResolvedGraphBinding{}, contractsv1.ContextFabricSubjectRepository, "repoA", hash, nil, nil)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
}

// TestVerifyAnchorClaimantMembership_GraphSideReconciliation is CHAOS-4042
// PR3's own graph-side proof (the sol-max ruling's "8 targeted redemption
// tests" #5, #7, and the graph-vs-ClickHouse disagreement class,
// team-lead's own enumeration): ClickHouse alone is no longer sufficient --
// the selected claimant's graph node must ALSO be found and re-authorized
// under the pinned binding.
func TestVerifyAnchorClaimantMembership_GraphSideReconciliation(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)
	principal := storage.Principal{OrgID: "org_1"}
	scope := contextfabric.RequestedScope{}
	binding := contextfabric.ResolvedGraphBinding{GraphKey: "graph_org_1_epoch_7", Epoch: 7}

	t.Run("graph agrees: exists and authorized -> valid", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, validGraphAnchorMember)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (true, %q)", valid, reason, AnchorVerificationValid)
		}
	})

	// Test #5 (sol-max ruling): authorization loss -> unresolved WITHOUT
	// identity disclosure. AnchorVerificationUnauthorized was UNREACHABLE
	// in production before this PR (nothing produced it) -- this is the
	// first test to actually reach it, proving the wired dependency chain
	// makes it live, not merely a defined-but-dead constant.
	t.Run("ClickHouse agrees but graph says unauthorized: unresolved, no disclosure", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		graphFn := func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
			return GraphAnchorMemberResult{Exists: true, Authorized: false}, nil
		}
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, graphFn)
		if valid || reason != AnchorVerificationUnauthorized {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationUnauthorized)
		}
	})

	// Disagreement class, direction 1: ClickHouse says the claimant is
	// still there, but a LIVE-epoch graph read says the node does not
	// exist. Must fail closed -- ClickHouse alone is never sufficient
	// after this PR.
	t.Run("ClickHouse says found but graph (live epoch) says not found: claim lost", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		graphFn := func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
			return GraphAnchorMemberResult{Exists: false}, nil
		}
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, graphFn)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})

	// Disagreement class, direction 2: ClickHouse ALREADY says the claim is
	// lost. The graph dependency must never even be consulted -- a
	// ClickHouse-side negative is sufficient on its own to fail closed,
	// proven here by wiring a graph fake that would say "valid" if it were
	// ever called, and asserting it never was.
	t.Run("ClickHouse says lost: fails closed without ever consulting the graph", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, nil)
		called := false
		graphFn := func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
			called = true
			return GraphAnchorMemberResult{Exists: true, Authorized: true}, nil
		}
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, graphFn)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
		if called {
			t.Error("graphAnchorMember was called even though ClickHouse already reported the claim lost -- a ClickHouse-side negative must short-circuit, never need graph agreement")
		}
	})

	// Test #7 (sol-max ruling, team-lead's cf_binding_epoch_delta mapping
	// correction): a retired epoch's graph key is CANNOT-VERIFY, never
	// claim-lost -- a stale binding proves nothing about a live epoch.
	t.Run("pinned binding's graph key does not exist (retired epoch): cannot verify, not claim lost", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		graphFn := func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
			return GraphAnchorMemberResult{Unverifiable: true}, nil
		}
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, graphFn)
		if valid || reason != AnchorVerificationGraphUnverifiable {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationGraphUnverifiable)
		}
	})

	t.Run("graph read itself errors: fails closed as cannot-verify, not claim lost", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		graphFn := func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (GraphAnchorMemberResult, error) {
			return GraphAnchorMemberResult{}, errors.New("graph backend unavailable")
		}
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, graphFn)
		if valid || reason != AnchorVerificationGraphUnverifiable {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationGraphUnverifiable)
		}
	})

	t.Run("nil graphAnchorMember with a valid ClickHouse read still fails closed", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		valid, reason := VerifyAnchorClaimantMembership(context.Background(), principal, scope, binding, contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe, nil)
		if valid || reason != AnchorVerificationGraphUnverifiable {
			t.Errorf("VerifyAnchorClaimantMembership() = (%v, %q), want (false, %q) -- a nil graph dependency is NOT \"trust ClickHouse alone\"", valid, reason, AnchorVerificationGraphUnverifiable)
		}
	})
}

func identityMatch(kind contractsv1.ContextFabricSubjectKind, id, label string) IdentityMatch {
	return IdentityMatch{Row: IdentityRow{Kind: kind, CanonicalID: id, Label: label}}
}

func TestAnchorOfferMaterial_UniqueClaimantOffersNothing(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, false)
	if len(material.Missing) != 0 || len(material.AnchorOptions) != 0 {
		t.Errorf("material = %+v, want empty: a unique claimant is already decisive, nothing to elicit", material)
	}
}

func TestAnchorOfferMaterial_DisagreementOffersOnePerCandidate(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
		"widget-svc":     {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB")},
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, false)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
		t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
	}
	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2", len(material.AnchorOptions))
	}
	for _, opt := range material.AnchorOptions {
		if opt.Kind != contractsv1.ContextFabricSubjectRepository {
			t.Errorf("AnchorOption.Kind = %q, want repository", opt.Kind)
		}
		if len(opt.MatchedTermHash) != 24 {
			t.Errorf("len(AnchorOption.MatchedTermHash) = %d, want 24", len(opt.MatchedTermHash))
		}
		if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
			t.Errorf("AnchorOption.OfferSource = %q, want engine", opt.OfferSource)
		}
	}
}

// TestAnchorOfferMaterial_LabelOverV1BoundIsBoundedAtTheProducer (CHAOS-4210
// follow-up) pins the anchor-axis twin of
// TestCandidateOfferMaterial_LabelOverV1BoundIsBoundedAtTheProducer: the
// identity-universe Label anchorOfferMaterial disagreement offers is a real
// display name with no upstream length guard, and ContextFabricAnchorOption's
// v1 wire contract bounds Label to [1,200] runes
// (validate_context_fabric_structure.go's Validate(), "anchor option
// option_id or label violates v1 bounds"). The receipt_id/option_id this
// test supplies are synthetically well-formed (real values are minted
// later, by composeStructureNeeds in the parent package) so only the Label
// bound is under test.
func TestAnchorOfferMaterial_LabelOverV1BoundIsBoundedAtTheProducer(t *testing.T) {
	t.Parallel()
	longLabel := strings.Repeat("a", 250)
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", longLabel)},
		"widget-svc":     {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB")},
	}
	material, diag := anchorOfferMaterial(claimants, claimants, true, false)
	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2", len(material.AnchorOptions))
	}
	if diag.LabelsNormalizedCount != 1 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 1 -- the run's own artifacts must show this label was normalized", diag.LabelsNormalizedCount)
	}
	for _, opt := range material.AnchorOptions {
		if opt.CanonicalID != "repoA" {
			continue
		}
		opt.ReceiptID = contractsv1.ContextFabricAnchorOptionReceiptPrefix + strings.Repeat("a", 24)
		opt.OptionID = "opt_" + strings.Repeat("a", 16)
		if err := opt.Validate(); err != nil {
			t.Fatalf("opt.Validate() = %v, want nil -- the producer must bound Label to the wire contract, not the caller", err)
		}
		if got := utf8.RuneCountInString(opt.Label); got > 200 {
			t.Errorf("len(opt.Label) = %d runes, want <= 200 (v1 bound)", got)
		}
		if opt.Label != longLabel[:200] {
			t.Errorf("opt.Label = %q, want the first 200 runes of the original label verbatim", opt.Label)
		}
	}
}

// TestAnchorOfferMaterial_LabelOverV1BoundMultibyteIsTruncatedOnRuneBoundary
// mirrors the candidate axis's own multibyte regression test: an all-ASCII
// case cannot distinguish rune-safe truncation from a byte-slice cut that
// would split a multibyte rune.
func TestAnchorOfferMaterial_LabelOverV1BoundMultibyteIsTruncatedOnRuneBoundary(t *testing.T) {
	t.Parallel()
	longLabel := strings.Repeat("é", 250)
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", longLabel)},
		"widget-svc":     {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB")},
	}
	material, diag := anchorOfferMaterial(claimants, claimants, true, false)
	if diag.LabelsNormalizedCount != 1 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 1", diag.LabelsNormalizedCount)
	}
	for _, opt := range material.AnchorOptions {
		if opt.CanonicalID != "repoA" {
			continue
		}
		if !utf8.ValidString(opt.Label) {
			t.Fatalf("opt.Label is not valid UTF-8 -- truncation split a multibyte rune")
		}
		if got := utf8.RuneCountInString(opt.Label); got != 200 {
			t.Errorf("utf8.RuneCountInString(opt.Label) = %d, want exactly 200", got)
		}
		if opt.Label != strings.Repeat("é", 200) {
			t.Errorf("opt.Label = %q, want the first 200 runes verbatim, not a byte-boundary cut", opt.Label)
		}
	}
}

// TestAnchorOfferMaterial_LabelAtOrUnderV1BoundIsUnchanged pins the
// companion invariant: bounding must not clip a label that already fits.
func TestAnchorOfferMaterial_LabelAtOrUnderV1BoundIsUnchanged(t *testing.T) {
	t.Parallel()
	exactLabel := strings.Repeat("b", 200)
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", exactLabel)},
		"widget-svc":     {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB")},
	}
	material, diag := anchorOfferMaterial(claimants, claimants, true, false)
	if diag.LabelsNormalizedCount != 0 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 0 -- a label already at the bound must not be counted as normalized", diag.LabelsNormalizedCount)
	}
	found := false
	for _, opt := range material.AnchorOptions {
		if opt.CanonicalID == "repoA" {
			found = true
			if opt.Label != exactLabel {
				t.Errorf("opt.Label = %q, want the exact-200-rune label unchanged", opt.Label)
			}
		}
	}
	if !found {
		t.Fatal("repoA option not found in material.AnchorOptions")
	}
}

// TestAnchorOfferMaterialV2_LabelOverV1BoundIsBoundedAtTheProducer pins the
// SAME bound on the v2 (ambiguous-claimant, membership-verify) option shape
// -- a separate accumulation path from the v1 loop above, sharing only
// anchorOfferLabel itself.
func TestAnchorOfferMaterialV2_LabelOverV1BoundIsBoundedAtTheProducer(t *testing.T) {
	t.Parallel()
	longLabel := strings.Repeat("a", 250)
	claimants := map[string][]IdentityMatch{
		"widget": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", longLabel),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
		},
	}
	material, diag := anchorOfferMaterial(claimants, claimants, true, true)
	if len(material.AnchorOptions) != 2 || !material.AnchorOptionsRequireV2 {
		t.Fatalf("material = %+v, want 2 v2 AnchorOptions", material)
	}
	if diag.LabelsNormalizedCount != 1 {
		t.Errorf("diag.LabelsNormalizedCount = %d, want 1", diag.LabelsNormalizedCount)
	}
	for _, opt := range material.AnchorOptions {
		if opt.CanonicalID != "repoA" {
			continue
		}
		if got := utf8.RuneCountInString(opt.Label); got > 200 {
			t.Errorf("len(opt.Label) = %d runes, want <= 200 (v1 bound)", got)
		}
	}
}

func TestAnchorOfferMaterial_NoCandidatesStillMissingWithEmptyOptions(t *testing.T) {
	t.Parallel()

	t.Run("zero claimants", func(t *testing.T) {
		material, _ := anchorOfferMaterial(map[string][]IdentityMatch{}, map[string][]IdentityMatch{}, true, false)
		if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
		}
		if len(material.AnchorOptions) != 0 {
			t.Errorf("len(material.AnchorOptions) = %d, want 0", len(material.AnchorOptions))
		}
	})
	t.Run("incomplete read", func(t *testing.T) {
		claimants := map[string][]IdentityMatch{
			"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
		}
		material, _ := anchorOfferMaterial(claimants, claimants, false, false)
		if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
		}
		if len(material.AnchorOptions) != 0 {
			t.Errorf("len(material.AnchorOptions) = %d, want 0", len(material.AnchorOptions))
		}
	})
}

// TestAnchorOfferMaterial_MoreThanMaxCandidatesIsCapped pins the codex
// xhigh review finding (chaos-pivot-p1, round 2, finding 1): an ambiguous
// term matching more than structureOfferMaxOptions distinct
// (kind, canonical_id) claimants must never mint an AnchorOptions list the
// wire Validate() would then reject (len > 20) -- it must be capped,
// deterministically, to the first structureOfferMaxOptions candidates in
// the SAME sorted (kind, canonical_id) order anchorOfferMaterial already
// uses.
func TestAnchorOfferMaterial_MoreThanMaxCandidatesIsCapped(t *testing.T) {
	t.Parallel()
	claimants := make(map[string][]IdentityMatch, structureOfferMaxOptions+5)
	for i := 0; i < structureOfferMaxOptions+5; i++ {
		term := fmt.Sprintf("term-%02d", i)
		id := fmt.Sprintf("repo-%02d", i)
		claimants[term] = []IdentityMatch{identityMatch(contractsv1.ContextFabricSubjectRepository, id, id)}
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, false)
	if len(material.AnchorOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.AnchorOptions) = %d, want %d (capped)", len(material.AnchorOptions), structureOfferMaxOptions)
	}
	// The kept candidates must be the lexicographically-first
	// canonical_ids ("repo-00".."repo-19"), not an arbitrary subset.
	seen := make(map[string]bool, len(material.AnchorOptions))
	for _, opt := range material.AnchorOptions {
		seen[opt.CanonicalID] = true
	}
	for i := 0; i < structureOfferMaxOptions; i++ {
		want := fmt.Sprintf("repo-%02d", i)
		if !seen[want] {
			t.Errorf("capped AnchorOptions missing %q, want the first %d ids kept", want, structureOfferMaxOptions)
		}
	}
}

// TestAnchorOfferMaterial_DarkByDefault is the team-lead-mandated proof:
// PR2 ships the v2 ambiguous-claimant path DARK. Even when a genuinely
// ambiguous term is present in the read, membershipOffersEnabled=false
// (every production deployment until PR3 lands pinned-epoch reconciliation
// and redemption-time re-authorization) must produce EXACTLY the v1
// behavior for this input -- zero candidates, since neither term has a
// unique claimant -- never a v2 AnchorOption, never AnchorOptionsRequireV2.
func TestAnchorOfferMaterial_DarkByDefault(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
		},
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, false)
	if material.AnchorOptionsRequireV2 {
		t.Error("material.AnchorOptionsRequireV2 = true with membershipOffersEnabled=false; the v2 path must be completely inert when the gate is off")
	}
	if len(material.AnchorOptions) != 0 {
		t.Errorf("material.AnchorOptions = %+v, want empty: with the gate off, an ambiguous term (2+ claimants, no unique claimant anywhere) must fall back to v1's own zero-candidates case, never mint a v2 option", material.AnchorOptions)
	}
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
		t.Fatalf("material.Missing = %v, want [subject_anchor] (still disclosed as missing-and-helpful, v1's own case-3 shape)", material.Missing)
	}
}

// TestAnchorOfferMaterial_HiddenRivalOnOtherwiseUniqueTermNeverLooksUnique
// is codex xhigh review's round-2 finding (HIGH, confirmed): a term with
// ONE visible claimant and ONE HIDDEN rival has authorized count 1 -- the
// exact shape ambiguousAnchorTermClaimants (which requires 2+ AUTHORIZED
// claimants to even notice a term) cannot see, so it silently fell through
// to the v1 unique-claimant scan, which then treated the visible claimant
// as genuinely unique and offered it once real ambiguity existed elsewhere
// (case 2, 2+ distinct candidates). This is exactly "filter the proof
// universe by authorization then claim uniqueness" -- forbidden regardless
// of whether the v2 gate is on. The fix applies to v1's OWN candidate scan
// unconditionally, not only inside the v2 path.
func TestAnchorOfferMaterial_HiddenRivalOnOtherwiseUniqueTermNeverLooksUnique(t *testing.T) {
	t.Parallel()
	raw := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoHidden", "repoHidden"), // hidden from principal
		},
		"acr": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoC", "repoC"),
		},
	}
	authorized := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
		},
		"acr": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoC", "repoC"),
		},
	}

	t.Run("v1 path (gate off): repoA is never offered as a false-unique candidate", func(t *testing.T) {
		t.Parallel()
		material, _ := anchorOfferMaterial(raw, authorized, true, false)
		for _, opt := range material.AnchorOptions {
			if opt.CanonicalID == "repoA" {
				t.Errorf("material.AnchorOptions = %+v, must NOT offer repoA as unique -- it has a hidden rival (repoHidden) on the same term", material.AnchorOptions)
			}
		}
		// repoC is the ONLY genuinely (fully-visible) unique claimant left,
		// which BindAnchor would independently resolve decisively --
		// case 1's own "already decisive, nothing to elicit" rationale, so
		// this must return completely empty, not an offer for repoC either.
		if len(material.AnchorOptions) != 0 || material.AnchorOptionsRequireV2 {
			t.Errorf("material = %+v, want a fully empty StructureOfferMaterial (repoC alone is decisive, repoA's term is excluded entirely)", material)
		}
	})
	t.Run("v2 path (gate on): repoA's term is excluded from the ambiguous scan too", func(t *testing.T) {
		t.Parallel()
		material, _ := anchorOfferMaterial(raw, authorized, true, true)
		for _, opt := range material.AnchorOptions {
			if opt.CanonicalID == "repoA" {
				t.Errorf("material.AnchorOptions = %+v, must NOT offer repoA -- its term has a hidden rival, not just an authorized ambiguity", material.AnchorOptions)
			}
		}
	})
}

// TestAnchorOfferMaterial_AmbiguousTermOffersV2PerClaimant is CHAOS-4042's
// (sol-max ruling) own offer-generation proof: a term with TWO OR MORE
// claimants -- the exact shape anchorTermCandidates skips -- must offer
// EVERY claimant as a v2 (membership-verify) option, all sharing that
// term's own matched_term_hash, and the material must be flagged as
// requiring the v2 schema major.
func TestAnchorOfferMaterial_AmbiguousTermOffersV2PerClaimant(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
		},
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, true)
	if !material.AnchorOptionsRequireV2 {
		t.Error("material.AnchorOptionsRequireV2 = false, want true for an ambiguous term")
	}
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
		t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
	}
	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2 (one per claimant)", len(material.AnchorOptions))
	}
	seen := map[string]bool{}
	for _, opt := range material.AnchorOptions {
		seen[opt.CanonicalID] = true
		if opt.MatchedTermHash != material.AnchorOptions[0].MatchedTermHash {
			t.Errorf("AnchorOption %+v has a different matched_term_hash than its sibling claimant; both name the SAME term and must share one hash", opt)
		}
	}
	if !seen["repoA"] || !seen["repoB"] {
		t.Errorf("material.AnchorOptions = %+v, want repoA AND repoB", material.AnchorOptions)
	}
}

// TestAnchorOfferMaterial_MixedVisibilitySuppressesWholeGroup proves the
// ruling's auth-gap closure at the ambiguous-term boundary: when the raw
// (complete) claimant read for a term differs from the authorized
// (caller-visible) read for the SAME term, the entire candidate group for
// that term is suppressed -- never a partial list, never even a claimant
// count, which would itself disclose that a hidden rival exists.
func TestAnchorOfferMaterial_MixedVisibilitySuppressesWholeGroup(t *testing.T) {
	t.Parallel()
	raw := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoC", "repoC"), // hidden from principal
		},
	}
	authorized := map[string][]IdentityMatch{
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
		},
	}
	material, _ := anchorOfferMaterial(raw, authorized, true, true)
	if material.AnchorOptionsRequireV2 {
		t.Error("material.AnchorOptionsRequireV2 = true, want false: the only ambiguous term is mixed-visibility and must be suppressed entirely")
	}
	if len(material.AnchorOptions) != 0 {
		t.Errorf("material.AnchorOptions = %+v, want empty: a mixed-visibility term must never disclose a partial claimant list", material.AnchorOptions)
	}
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
		t.Fatalf("material.Missing = %v, want [subject_anchor] (still disclosed as missing-and-helpful)", material.Missing)
	}
}

// TestAnchorOfferMaterial_AmbiguousTermCappedAtMaxOptions mirrors
// TestAnchorOfferMaterial_MoreThanMaxCandidatesIsCapped for the NEW v2
// path: a single term with more than structureOfferMaxOptions claimants
// must still cap, deterministically, at structureOfferMaxOptions.
func TestAnchorOfferMaterial_AmbiguousTermCappedAtMaxOptions(t *testing.T) {
	t.Parallel()
	matches := make([]IdentityMatch, 0, structureOfferMaxOptions+5)
	for i := 0; i < structureOfferMaxOptions+5; i++ {
		id := fmt.Sprintf("repo-%02d", i)
		matches = append(matches, identityMatch(contractsv1.ContextFabricSubjectRepository, id, id))
	}
	claimants := map[string][]IdentityMatch{"widget-service": matches}
	material, _ := anchorOfferMaterial(claimants, claimants, true, true)
	if !material.AnchorOptionsRequireV2 {
		t.Error("material.AnchorOptionsRequireV2 = false, want true")
	}
	if len(material.AnchorOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.AnchorOptions) = %d, want %d (capped)", len(material.AnchorOptions), structureOfferMaxOptions)
	}
	seen := make(map[string]bool, len(material.AnchorOptions))
	for _, opt := range material.AnchorOptions {
		seen[opt.CanonicalID] = true
	}
	for i := 0; i < structureOfferMaxOptions; i++ {
		want := fmt.Sprintf("repo-%02d", i)
		if !seen[want] {
			t.Errorf("capped AnchorOptions missing %q, want the first %d ids kept", want, structureOfferMaxOptions)
		}
	}
}

// TestAnchorOfferMaterial_DecisiveCandidateIgnoredWhenAmbiguousTermExists
// proves the case-1 v1 short-circuit does not silently swallow a genuinely
// ambiguous SEPARATE term: a decisive unique-claimant candidate needs no
// disambiguation of its own, so it must NOT be offered, but the unrelated
// ambiguous term must still be disclosed.
func TestAnchorOfferMaterial_DecisiveCandidateIgnoredWhenAmbiguousTermExists(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"acr": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoDecisive", "repoDecisive")},
		"widget-service": {
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA"),
			identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB"),
		},
	}
	material, _ := anchorOfferMaterial(claimants, claimants, true, true)
	if !material.AnchorOptionsRequireV2 {
		t.Error("material.AnchorOptionsRequireV2 = false, want true")
	}
	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2 (only the ambiguous term's claimants; the decisive candidate needs no disambiguation)", len(material.AnchorOptions))
	}
	for _, opt := range material.AnchorOptions {
		if opt.CanonicalID == "repoDecisive" {
			t.Errorf("material.AnchorOptions unexpectedly offered the decisive candidate %+v", opt)
		}
	}
}

// TestHandleOfferMaterial_DuplicateOccurrencesAreDeduped pins the codex
// xhigh review finding (chaos-pivot-p1, round 2, finding 1): the SAME
// handle text repeated in one question must not mint two options with
// identical content (and therefore identical receipt_id/option_id, which
// the wire Validate() rejects as a duplicate).
func TestHandleOfferMaterial_DuplicateOccurrencesAreDeduped(t *testing.T) {
	t.Parallel()
	material, _ := handleOfferMaterial("PR 532 relates to PR 532 which also mentions PR 532", nil, nil, nil)
	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1 (three identical occurrences deduped)", len(material.HandleOptions))
	}
	if material.HandleOptions[0].Value != "532" {
		t.Errorf("HandleOptions[0].Value = %q, want 532", material.HandleOptions[0].Value)
	}
}

// TestHandleOfferMaterial_MoreThanMaxDistinctMatchesIsCapped is the dedup
// fix's companion: enough DISTINCT handle-shaped tokens in one question
// must still be capped at structureOfferMaxOptions, for the same
// never-fail-Validate reasoning as the anchor cap above.
func TestHandleOfferMaterial_MoreThanMaxDistinctMatchesIsCapped(t *testing.T) {
	t.Parallel()
	question := "compare"
	for i := 0; i < structureOfferMaxOptions+5; i++ {
		question += fmt.Sprintf(" PR %d", 1000+i)
	}
	material, _ := handleOfferMaterial(question, nil, nil, nil)
	if len(material.HandleOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.HandleOptions) = %d, want %d (capped)", len(material.HandleOptions), structureOfferMaxOptions)
	}
}

func TestHandleOfferMaterial_NoGrammarMatchStillMissingWithEmptyOptions(t *testing.T) {
	t.Parallel()
	material, _ := handleOfferMaterial("how healthy is the payments team", nil, nil, nil)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectHandle {
		t.Fatalf("material.Missing = %v, want [subject_handle]", material.Missing)
	}
	if len(material.HandleOptions) != 0 {
		t.Errorf("len(material.HandleOptions) = %d, want 0", len(material.HandleOptions))
	}
}

func TestHandleOfferMaterial_GrammarMatchOffersHandle(t *testing.T) {
	t.Parallel()
	material, _ := handleOfferMaterial("what is the status of PR 532?", nil, nil, nil)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectHandle {
		t.Fatalf("material.Missing = %v, want [subject_handle]", material.Missing)
	}
	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1", len(material.HandleOptions))
	}
	opt := material.HandleOptions[0]
	if opt.Kind != contractsv1.ContextFabricSubjectPullRequest || opt.PatternID != "pull_request_number" || opt.Value != "532" {
		t.Errorf("HandleOptions[0] = %+v, want kind=pull_request pattern_id=pull_request_number value=532", opt)
	}
	if opt.SourceColumn != "git_pull_requests.number" {
		t.Errorf("HandleOptions[0].SourceColumn = %q, want %q", opt.SourceColumn, "git_pull_requests.number")
	}
	if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
		t.Errorf("HandleOptions[0].OfferSource = %q, want engine", opt.OfferSource)
	}
}

func TestHandleOfferMaterial_MultipleGrammarMatchesOfferAll(t *testing.T) {
	t.Parallel()
	material, _ := handleOfferMaterial("does PR 532 relate to CHAOS-3896?", nil, nil, nil)
	if len(material.HandleOptions) != 2 {
		t.Fatalf("len(material.HandleOptions) = %d, want 2", len(material.HandleOptions))
	}
}

func TestCombineStructureOfferMaterial(t *testing.T) {
	t.Parallel()
	kind := contextfabric.StructureOfferMaterial{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{Kind: contractsv1.ContextFabricSubjectPullRequest}},
	}
	anchor := contextfabric.StructureOfferMaterial{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{{CanonicalID: "repoA"}},
	}
	handle := contextfabric.StructureOfferMaterial{}

	combined := combineStructureOfferMaterial(kind, anchor, handle)
	wantMissing := []contractsv1.ContextFabricStructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
	}
	if len(combined.Missing) != len(wantMissing) {
		t.Fatalf("combined.Missing = %v, want %v", combined.Missing, wantMissing)
	}
	for i, m := range wantMissing {
		if combined.Missing[i] != m {
			t.Errorf("combined.Missing[%d] = %q, want %q (order pin: kind before anchor)", i, combined.Missing[i], m)
		}
	}
	if len(combined.KindOptions) != 1 || len(combined.AnchorOptions) != 1 || len(combined.HandleOptions) != 0 {
		t.Errorf("combined = %+v, want 1 kind option, 1 anchor option, 0 handle options", combined)
	}
}

// TestNarrowPooledKindsByExplicitKinds (CHAOS-3972 P3) pins
// narrowPooledKindsByExplicitKinds' own three no-narrowing cases (empty
// explicit set, zero survivors, every pooled kind survives) alongside the
// genuine-narrowing case, since runShadowEvidenceRoundForResolution's own
// PreNarrowingExplicitKinds gating depends on telling them apart.
func TestNarrowPooledKindsByExplicitKinds(t *testing.T) {
	t.Parallel()
	pooled := []CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}

	if got := narrowPooledKindsByExplicitKinds(pooled, nil); got != nil {
		t.Errorf("empty explicit set: got %v, want nil (no narrowing applied)", got)
	}
	if got := narrowPooledKindsByExplicitKinds(pooled, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}); got != nil {
		t.Errorf("zero survivors: got %v, want nil (explicit hint disagreeing with the whole pool is not authoritative)", got)
	}
	if got := narrowPooledKindsByExplicitKinds(pooled, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectCIRun}); got != nil {
		t.Errorf("every pooled kind survives: got %v, want nil (intersecting changed nothing)", got)
	}
	got := narrowPooledKindsByExplicitKinds(pooled, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectCIRun})
	want := []CensusKind{contractsv1.ContextFabricSubjectCIRun}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("genuine narrowing: got %v, want %v", got, want)
	}
}

// TestDistinctOfferableKinds_DedupesFirstOccurrenceOrderRestrictedToOfferable
// pins distinctOfferableKinds' own contract, extracted verbatim from
// kindOfferMaterial's old inline poolDistinct loop (CHAOS-4183 phase 3): a
// pool's distinct offerable kinds, first-occurrence order, with any
// non-offerable kind (document -- not in structureOfferKinds) silently
// dropped.
func TestDistinctOfferableKinds_DedupesFirstOccurrenceOrderRestrictedToOfferable(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
		candidateOf(contractsv1.ContextFabricSubjectDocument, "doc_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_2"),
	}
	got := distinctOfferableKinds(candidates)
	want := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectPullRequest}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("distinctOfferableKinds = %v, want %v -- first-occurrence order, document dropped, wi_2 not a second entry", got, want)
	}
}

func TestDistinctOfferableKinds_EmptyPool(t *testing.T) {
	t.Parallel()
	if got := distinctOfferableKinds(nil); got != nil {
		t.Errorf("distinctOfferableKinds(nil) = %v, want nil", got)
	}
}

// TestProjectKindOfferKinds_CausalUnitFixture is the CHAOS-4183 phase 3
// validation step 1 causal fixture (sol design consult, team-lead ratified
// 2026-08-23): visible carries only the top-ranked kind (pull_request); the
// full merged pool additionally carries two lower-ranked kinds that never
// reached `visible` -- ci_pipeline_run and work_item -- PLUS a non-offerable
// kind (document) that must never be admitted regardless of pool presence.
// Asserts: before is untouched (visible's own distinct kinds only); after
// admits exactly the two missing OFFERABLE kinds, appended in the FIXED
// sortedKinds(structureOfferKinds) lexicographic order
// (ci_pipeline_run < work_item), never fullPool's own (map, thus
// non-deterministic) iteration order, and never document.
func TestProjectKindOfferKinds_CausalUnitFixture(t *testing.T) {
	t.Parallel()
	visible := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
	}
	fullPool := map[string]contextfabric.SubjectCandidate{
		"pr_1":  candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		"ci_1":  candidateOf(contractsv1.ContextFabricSubjectCIRun, "ci_1"),
		"wi_1":  candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
		"doc_1": candidateOf(contractsv1.ContextFabricSubjectDocument, "doc_1"),
	}

	before, after := projectKindOfferKinds(visible, fullPool, 0)

	wantBefore := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest}
	if len(before) != 1 || before[0] != wantBefore[0] {
		t.Fatalf("before = %v, want %v -- unchanged from visible's own distinct kinds", before, wantBefore)
	}
	wantAfter := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectWorkItem,
	}
	if len(after) != len(wantAfter) {
		t.Fatalf("after = %v, want %v", after, wantAfter)
	}
	for i, kind := range wantAfter {
		if after[i] != kind {
			t.Fatalf("after = %v, want %v -- FIXED closed-vocab (sortedKinds) order, not pool iteration order", after, wantAfter)
		}
	}
}

// TestProjectKindOfferKinds_CommittedResolutionSkipsRepair pins the design's
// own "stalled resolutions ONLY" scope: once anything has committed, the
// pre-repair boundary is returned for BOTH before and after unchanged --
// the kind-only completion never fires on a committed resolution, even when
// the full pool has an absent offerable kind.
func TestProjectKindOfferKinds_CommittedResolutionSkipsRepair(t *testing.T) {
	t.Parallel()
	visible := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
	}
	fullPool := map[string]contextfabric.SubjectCandidate{
		"pr_1": candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		"wi_1": candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}

	before, after := projectKindOfferKinds(visible, fullPool, 1)

	if len(after) != 1 || after[0] != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("after = %v, want [pull_request] -- committed resolutions must not repair", after)
	}
	if len(before) != 1 || before[0] != after[0] {
		t.Fatalf("before = %v, after = %v, want identical when committed", before, after)
	}
}

// TestProjectKindOfferKinds_NoAbsentKindsIsANoOp pins the case where fullPool
// has nothing visible does not already carry: after must equal before, not
// merely equal in content -- no spurious repair entries appended.
func TestProjectKindOfferKinds_NoAbsentKindsIsANoOp(t *testing.T) {
	t.Parallel()
	visible := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	fullPool := map[string]contextfabric.SubjectCandidate{
		"pr_1": candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		"wi_1": candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}

	before, after := projectKindOfferKinds(visible, fullPool, 0)
	if len(before) != 2 || len(after) != 2 || before[0] != after[0] || before[1] != after[1] {
		t.Fatalf("before = %v, after = %v, want identical two-kind lists -- nothing absent to repair", before, after)
	}
}

// TestKindOfferMaterial_ExplicitKindAlwaysOfferedEvenAloneInThePool
// (CHAOS-3972 P3, design brief §2.3) pins the asymmetry explicit kinds
// introduce: the pool-derived >=2-distinct-kinds gate stays in force for
// the pool alone, but ANY non-empty explicit kind list is offered
// regardless of pool cardinality -- a caller's own named kind is always
// worth offering back, receipt-bound, for the deterministic upgrade turn.
func TestKindOfferMaterial_ExplicitKindAlwaysOfferedEvenAloneInThePool(t *testing.T) {
	t.Parallel()
	// Empty pool, one explicit kind: still offered.
	material, diag := kindOfferMaterial(nil, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest}, nil, nil)
	if len(material.KindOptions) != 1 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("material.KindOptions = %+v, want exactly the one explicit kind", material.KindOptions)
	}
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Errorf("material.Missing = %v, want [expected_kind]", material.Missing)
	}
	// CHAOS-4012 v20: an explicit hint bypasses the cardinality gate
	// entirely -- ExplicitHintCount=1 alone is why this is not suppressed
	// despite DistinctKindCount also reading 1 (the explicit kind is its
	// own, and only, ranked entry here).
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 1, DistinctKindCount: 1, SuppressedByCardinality: false}) {
		t.Errorf("diag = %+v, want {1, 1, false} -- an explicit hint is never suppressed", diag)
	}
	// Explicit kind ranked FIRST, ahead of pool-derived kinds.
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem}},
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest}},
	}
	material, diag = kindOfferMaterial(distinctOfferableKinds(candidates), []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectWorkItem}, nil, heldFromKinds(distinctOfferableKinds(candidates)...))
	if len(material.KindOptions) != 2 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectWorkItem {
		t.Fatalf("material.KindOptions = %+v, want the explicit kind ranked first", material.KindOptions)
	}
	if !reflect.DeepEqual(diag, kindOfferDiagnostics{ExplicitHintCount: 1, DistinctKindCount: 2, SuppressedByCardinality: false}) {
		t.Errorf("diag = %+v, want {1, 2, false} -- explicit hint plus one NEW pool-derived kind (work_item already claimed by the hint)", diag)
	}
}

// TestHandleOfferMaterial_ExplicitHandleRequiresAWiredChecker (CHAOS-3972
// P3) pins HandleGrammarChecker's own documented safe-degradation contract:
// nil checker means NO explicit handle ever becomes an offer, never a
// panic and never a veto.
func TestHandleOfferMaterial_ExplicitHandleRequiresAWiredChecker(t *testing.T) {
	t.Parallel()
	explicit := []contractsv1.ContextFabricRequestedHandle{{Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}
	material, _ := handleOfferMaterial("no handles in this question text", explicit, nil, nil)
	if len(material.HandleOptions) != 0 {
		t.Fatalf("material.HandleOptions = %+v, want empty (nil checker degrades safely)", material.HandleOptions)
	}

	checker := func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
		if kind == contractsv1.ContextFabricSubjectPullRequest && patternID == "pull_request_number" {
			return "git_pull_requests.number", true
		}
		return "", false
	}
	material, _ = handleOfferMaterial("no handles in this question text", explicit, checker, nil)
	if len(material.HandleOptions) != 1 || material.HandleOptions[0].Value != "532" || material.HandleOptions[0].SourceColumn != "git_pull_requests.number" {
		t.Fatalf("material.HandleOptions = %+v, want the one explicit handle validated and offered", material.HandleOptions)
	}

	// An invalid explicit value (checker returns ok=false) is silently
	// omitted, never offered.
	invalid := []contractsv1.ContextFabricRequestedHandle{{Kind: contractsv1.ContextFabricSubjectWorkItem, PatternID: "bogus_pattern", Value: "x"}}
	material, _ = handleOfferMaterial("", invalid, checker, nil)
	if len(material.HandleOptions) != 0 {
		t.Fatalf("material.HandleOptions = %+v, want empty (checker rejected the explicit value)", material.HandleOptions)
	}
}

// heldFromKinds builds kindOfferMaterial's own poolHeldKinds set from a POOL
// KIND list, for the unit tests that model the pool as kinds rather than as
// candidates. Production builds the same set from the full candidate map
// (poolHeldKindsOf, chaos3900_structure_offers.go); the two agree by
// construction because both are "the offerable kinds this pool holds".
func heldFromKinds(kinds ...contractsv1.ContextFabricSubjectKind) poolHeldKinds {
	held := make(poolHeldKinds, len(kinds))
	for _, kind := range kinds {
		held[kind] = true
	}
	return held
}
