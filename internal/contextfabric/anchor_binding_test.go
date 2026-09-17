package contextfabric

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	bindAlpha   = anchorRef{Kind: SubjectRepository, ID: "repository:bind-alpha"}
	bindBeta    = anchorRef{Kind: SubjectRepository, ID: "repository:bind-beta"}
	bindGamma   = anchorRef{Kind: SubjectRepository, ID: "repository:bind-gamma"}
	bindProject = anchorRef{Kind: SubjectProject, ID: "project:bind-project"}
	bindTeam    = anchorRef{Kind: SubjectTeam, ID: "team:bind-team"}
)

// proofOf builds a resolution committing refs on basis, each a candidate
// matched on the counting frame's anchor term.
// bindingMember is the snapshot's decoded binding member, nil when it is
// absent or does not decode.
func bindingMember(state *PersistedSemanticState) *AnchorBinding {
	binding, present, err := storedAnchorBinding(state)
	if !present || err != nil {
		return nil
	}
	return &binding
}

// setBindingMember writes binding as the snapshot's member, or removes the
// member when binding is nil.
func setBindingMember(state *PersistedSemanticState, binding *AnchorBinding) {
	if binding == nil {
		delete(state.Extensions, anchorBindingExtension)
		if len(state.Extensions) == 0 {
			state.Extensions = nil
		}
		return
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		panic(err)
	}
	if state.Extensions == nil {
		state.Extensions = SemanticStateExtensions{}
	}
	state.Extensions[anchorBindingExtension] = raw
}

// editBindingMember applies edit to the snapshot's member when it decodes.
func editBindingMember(state *PersistedSemanticState, edit func(*AnchorBinding)) {
	if binding := bindingMember(state); binding != nil {
		edit(binding)
		setBindingMember(state, binding)
	}
}

func proofOf(basis CommitBasis, refs ...anchorRef) (SubjectResolution, CommitBasisSet) {
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	bases := CommitBasisSet{}
	for _, ref := range refs {
		subject := SubjectRef{Kind: ref.Kind, CanonicalID: ref.ID, Label: ref.ID}
		resolution.Committed = append(resolution.Committed, subject)
		resolution.Candidates = append(resolution.Candidates, SubjectCandidate{
			ReceiptID: "receipt_bind", Subject: subject, State: ResolutionCommitted,
			MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		})
		bases.Record(subject, basis)
	}
	return resolution, bases
}

func heldBinding(state AnchorBindingState, ref anchorRef) AnchorBinding {
	return AnchorBinding{State: state, Kind: ref.Kind, CanonicalID: ref.ID, Proof: AnchorBindingProofIdentityProven, Reason: AnchorBindingReasonIdentityProven, OriginResultID: "result_bind_parent", GraphEpoch: 7}
}

func contestedBinding(held, contender anchorRef) AnchorBinding {
	b := heldBinding(AnchorBindingContested, held)
	b.Reason, b.ContenderKind, b.ContenderID = AnchorBindingReasonContestedByResolution, contender.Kind, contender.ID
	return b
}

var unboundFrom = AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof}

type bindCase struct {
	name          string
	from          AnchorBinding
	evaluation    AnchorBindingEvaluation
	modelKind     SubjectKind
	receipt       *anchorRef
	hints         []anchorRef
	basis         CommitBasis
	proven        []anchorRef
	want          AnchorBinding
	wantEffective SubjectKind
	wantProven    []anchorRef
}

// bindTransitionTable is the binder's transition table: every state and every
// persistable reason is reached by at least one row.
func bindTransitionTable() []bindCase {
	fresh := func(state AnchorBindingState, ref anchorRef, proof AnchorBindingProof, reason AnchorBindingReason) AnchorBinding {
		return AnchorBinding{State: state, Kind: ref.Kind, CanonicalID: ref.ID, Proof: proof, Reason: reason, OriginResultID: "result_bind_this", GraphEpoch: 7}
	}
	kept := func(b AnchorBinding, state AnchorBindingState, reason AnchorBindingReason) AnchorBinding {
		b.State, b.Reason = state, reason
		if state != AnchorBindingContested {
			b.ContenderKind, b.ContenderID = "", ""
		}
		return b
	}
	unbound := func(reason AnchorBindingReason) AnchorBinding {
		return AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: reason, GraphEpoch: 7}
	}
	bound := heldBinding(AnchorBindingBound, bindAlpha)
	pending := heldBinding(AnchorBindingPendingWindowConfirmation, bindAlpha)
	contested := contestedBinding(bindAlpha, bindBeta)
	contestedAfter := func(contender anchorRef, reason AnchorBindingReason) AnchorBinding {
		b := kept(bound, AnchorBindingContested, reason)
		b.ContenderKind, b.ContenderID = contender.Kind, contender.ID
		return b
	}
	id := CommitBasisAuthoritativeIdentity
	return []bindCase{
		{name: "nothing carried, turn never resolved", from: unboundFrom, evaluation: AnchorBindingEvaluationNotResolved, want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, a commit on a turn that never resolved is no proof", from: unboundFrom, evaluation: AnchorBindingEvaluationNotResolved, basis: id, proven: []anchorRef{bindAlpha},
			want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, nothing proven", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: id, want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, one identity proven", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindAlpha},
			want: fresh(AnchorBindingBound, bindAlpha, AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), wantProven: []anchorRef{bindAlpha}},
		{name: "nothing carried, the proven identity is a caller hint", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindAlpha}, hints: []anchorRef{bindAlpha},
			want: fresh(AnchorBindingBound, bindAlpha, AnchorBindingProofCallerHint, AnchorBindingReasonCallerHint), wantProven: []anchorRef{bindAlpha}},
		{name: "nothing carried, two identities proven", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindAlpha, bindBeta},
			want: unbound(AnchorBindingReasonAmbiguousProof), wantProven: []anchorRef{bindAlpha, bindBeta}},
		{name: "nothing carried, the same identity committed twice", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindAlpha, bindAlpha},
			want: fresh(AnchorBindingBound, bindAlpha, AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), wantProven: []anchorRef{bindAlpha}},
		{name: "nothing carried, a statistical commit is no proof", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisStatistical, proven: []anchorRef{bindAlpha},
			want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, an unrecorded basis is no proof", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisUnknown, proven: []anchorRef{bindAlpha},
			want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, a member-kind commit is no anchor", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindTeam},
			want: unbound(AnchorBindingReasonNoProof)},
		{name: "nothing carried, the model kind excludes the proven identity", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, modelKind: SubjectProject, basis: id, proven: []anchorRef{bindAlpha},
			want: unbound(AnchorBindingReasonNoProof), wantEffective: SubjectProject},
		{name: "nothing carried, the model kind admits the proven identity", from: unboundFrom, evaluation: AnchorBindingEvaluationResolved, modelKind: SubjectProject, basis: id, proven: []anchorRef{bindProject},
			want: fresh(AnchorBindingBound, bindProject, AnchorBindingProofIdentityProven, AnchorBindingReasonIdentityProven), wantEffective: SubjectProject, wantProven: []anchorRef{bindProject}},
		{name: "nothing carried, window gate proved one identity", from: unboundFrom, evaluation: AnchorBindingEvaluationWindowGated, basis: id, proven: []anchorRef{bindAlpha},
			want: fresh(AnchorBindingPendingWindowConfirmation, bindAlpha, AnchorBindingProofIdentityProven, AnchorBindingReasonPendingWindowConfirmation), wantProven: []anchorRef{bindAlpha}},
		{name: "nothing carried, window gate proved two identities", from: unboundFrom, evaluation: AnchorBindingEvaluationWindowGated, basis: id, proven: []anchorRef{bindAlpha, bindBeta},
			want: unbound(AnchorBindingReasonAmbiguousProof), wantProven: []anchorRef{bindAlpha, bindBeta}},
		{name: "nothing carried, window gate proved nothing", from: unboundFrom, evaluation: AnchorBindingEvaluationWindowGated, want: unbound(AnchorBindingReasonNoProof)},
		{name: "receipt, nothing carried", from: unboundFrom, evaluation: AnchorBindingEvaluationNotResolved, receipt: &bindProject,
			want: fresh(AnchorBindingBound, bindProject, AnchorBindingProofCallerReceipt, AnchorBindingReasonCallerReceipt), wantEffective: SubjectProject},
		{name: "receipt replaces a different carried identity", from: bound, evaluation: AnchorBindingEvaluationResolved, receipt: &bindBeta, basis: id, proven: []anchorRef{bindBeta},
			want: fresh(AnchorBindingBound, bindBeta, AnchorBindingProofCallerReceipt, AnchorBindingReasonReplacedByCaller), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "receipt restates the carried identity", from: bound, evaluation: AnchorBindingEvaluationWindowGated, receipt: &bindAlpha,
			want: fresh(AnchorBindingBound, bindAlpha, AnchorBindingProofCallerReceipt, AnchorBindingReasonCallerReceipt), wantEffective: SubjectRepository},
		{name: "receipt with no value falls back to the carry", from: bound, evaluation: AnchorBindingEvaluationNotResolved, receipt: &anchorRef{Kind: SubjectProject},
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedNotEvaluated), wantEffective: SubjectProject},
		{name: "carried, turn never resolved", from: bound, evaluation: AnchorBindingEvaluationNotResolved,
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedNotEvaluated), wantEffective: SubjectRepository},
		{name: "carried pending, gated again", from: pending, evaluation: AnchorBindingEvaluationWindowGated, basis: id, proven: []anchorRef{bindBeta},
			want: kept(pending, AnchorBindingPendingWindowConfirmation, AnchorBindingReasonCarriedNotEvaluated), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "carried, re-proven", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindAlpha},
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedReconfirmed), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
		{name: "carried, resolution silent", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: id,
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedSilent), wantEffective: SubjectRepository},
		{name: "carried, a distinct identity proven without a caller choice", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindBeta},
			want: contestedAfter(bindBeta, AnchorBindingReasonContestedByResolution), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "carried, the held and a distinct identity both proven", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindAlpha, bindBeta},
			want: contestedAfter(bindBeta, AnchorBindingReasonContestedByResolution), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha, bindBeta}},
		{name: "carried, a caller hint proves a distinct identity", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindBeta}, hints: []anchorRef{bindBeta},
			want: fresh(AnchorBindingBound, bindBeta, AnchorBindingProofCallerHint, AnchorBindingReasonReplacedByCaller), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "carried, two caller hints prove distinct identities", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindBeta, bindGamma}, hints: []anchorRef{bindBeta, bindGamma},
			want: contestedAfter(bindBeta, AnchorBindingReasonAmbiguousProof), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta, bindGamma}},
		{name: "carried, a hint of another kind is no caller choice", from: bound, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindBeta}, hints: []anchorRef{{Kind: SubjectProject, ID: bindBeta.ID}},
			want: contestedAfter(bindBeta, AnchorBindingReasonContestedByResolution), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "carried pending, the gate passed", from: pending, evaluation: AnchorBindingEvaluationResolved, basis: id,
			want: kept(pending, AnchorBindingBound, AnchorBindingReasonWindowConfirmed), wantEffective: SubjectRepository},
		{name: "carried pending, the gate passed and a distinct identity proven", from: pending, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindBeta},
			want: func() AnchorBinding {
				b := kept(pending, AnchorBindingContested, AnchorBindingReasonContestedByResolution)
				b.ContenderKind, b.ContenderID = bindBeta.Kind, bindBeta.ID
				return b
			}(), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "carried contested, the held identity re-proven", from: contested, evaluation: AnchorBindingEvaluationResolved, basis: id, proven: []anchorRef{bindAlpha},
			want: kept(contested, AnchorBindingBound, AnchorBindingReasonCarriedReconfirmed), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
		{name: "carried contested, resolution silent", from: contested, evaluation: AnchorBindingEvaluationResolved, basis: id,
			want: kept(contested, AnchorBindingContested, AnchorBindingReasonCarriedSilent), wantEffective: SubjectRepository},
		{name: "reused, the replayed row re-proves the carried identity", from: bound, evaluation: AnchorBindingEvaluationReused, basis: id, proven: []anchorRef{bindAlpha},
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedReconfirmed), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
		{name: "reused, the replayed row proves a distinct identity", from: bound, evaluation: AnchorBindingEvaluationReused, basis: id, proven: []anchorRef{bindBeta},
			want: contestedAfter(bindBeta, AnchorBindingReasonContestedByResolution), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "reused, the replayed row proves nothing", from: bound, evaluation: AnchorBindingEvaluationReused, basis: id,
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedSilent), wantEffective: SubjectRepository},
		{name: "reused, an unproven hint names another identity of the carried kind", from: bound, evaluation: AnchorBindingEvaluationReused, basis: id, proven: []anchorRef{bindAlpha}, hints: []anchorRef{bindGamma},
			want: contestedAfter(bindGamma, AnchorBindingReasonAmbiguousProof), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
		{name: "reused, a hint the replayed row proved replaces the carried identity", from: bound, evaluation: AnchorBindingEvaluationReused, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindBeta}, hints: []anchorRef{bindBeta},
			want: fresh(AnchorBindingBound, bindBeta, AnchorBindingProofCallerHint, AnchorBindingReasonReplacedByCaller), wantEffective: SubjectRepository, wantProven: []anchorRef{bindBeta}},
		{name: "reused, nothing carried, an unproven hint contests the replayed anchor", from: unboundFrom, evaluation: AnchorBindingEvaluationReused, basis: id, proven: []anchorRef{bindAlpha}, hints: []anchorRef{bindBeta},
			want: func() AnchorBinding {
				b := fresh(AnchorBindingContested, bindAlpha, AnchorBindingProofIdentityProven, AnchorBindingReasonAmbiguousProof)
				b.ContenderKind, b.ContenderID = bindBeta.Kind, bindBeta.ID
				return b
			}(), wantProven: []anchorRef{bindAlpha}},
		{name: "reused, a pending carry, the replayed row re-proves it", from: pending, evaluation: AnchorBindingEvaluationReused, basis: id, proven: []anchorRef{bindAlpha},
			want: kept(pending, AnchorBindingBound, AnchorBindingReasonWindowConfirmed), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
		{name: "carried kind outranks a conflicting model kind", from: bound, evaluation: AnchorBindingEvaluationResolved, modelKind: SubjectProject, basis: CommitBasisCallerCanonicalID, proven: []anchorRef{bindAlpha},
			want: kept(bound, AnchorBindingBound, AnchorBindingReasonCarriedReconfirmed), wantEffective: SubjectRepository, wantProven: []anchorRef{bindAlpha}},
	}
}

func TestBindAnchorTransitionTable(t *testing.T) {
	states := map[AnchorBindingState]bool{}
	reasons := map[AnchorBindingReason]bool{}
	for _, tc := range bindTransitionTable() {
		t.Run(tc.name, func(t *testing.T) {
			resolution, bases := proofOf(tc.basis, tc.proven...)
			in := anchorBindingInput{
				From: tc.from, Evaluation: tc.evaluation, Frame: countingFrame(SubjectTeam), ModelAnchorKind: tc.modelKind,
				Resolution: resolution, Bases: bases, ResultID: "result_bind_this", GraphEpoch: 7,
			}
			if tc.receipt != nil {
				in.Receipt = &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: tc.receipt.Kind, AppliedValue: tc.receipt.ID}
			}
			for _, hint := range tc.hints {
				in.CallerHints = append(in.CallerHints, SubjectHint{Kind: hint.Kind, ID: hint.ID})
			}
			got, proposal := bindAnchor(in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("binding\n got %+v\nwant %+v", got, tc.want)
			}
			if proposal.EffectiveKind != tc.wantEffective {
				t.Fatalf("effective kind = %q, want %q", proposal.EffectiveKind, tc.wantEffective)
			}
			if !reflect.DeepEqual(proposal.Proven, tc.wantProven) {
				t.Fatalf("proven = %+v, want %+v", proposal.Proven, tc.wantProven)
			}
			if err := ValidateAnchorBinding(got); err != nil {
				t.Fatalf("the binder produced an invalid binding: %v", err)
			}
			states[got.State] = true
			reasons[got.Reason] = true
		})
	}
	for _, state := range anchorBindingStates() {
		if !states[state] {
			t.Errorf("no row reaches state %q", state)
		}
	}
	for _, reason := range anchorBindingReasons() {
		if reason == AnchorBindingReasonUnrecorded {
			continue
		}
		if !reasons[reason] {
			t.Errorf("no row reaches reason %q", reason)
		}
	}
}

// TestBindAnchorIsPureAndOrderIndependentOfHints: the same input decides the
// same binding however the caller's hints are ordered.
func TestBindAnchorIsPureAndOrderIndependentOfHints(t *testing.T) {
	resolution, bases := proofOf(CommitBasisCallerCanonicalID, bindBeta)
	in := anchorBindingInput{From: heldBinding(AnchorBindingBound, bindAlpha), Evaluation: AnchorBindingEvaluationResolved, Frame: countingFrame(SubjectTeam),
		Resolution: resolution, Bases: bases, ResultID: "result_bind_this",
		CallerHints: []SubjectHint{{Kind: SubjectProject, ID: "project:other"}, {Kind: bindBeta.Kind, ID: bindBeta.ID}}}
	first, _ := bindAnchor(in)
	in.CallerHints[0], in.CallerHints[1] = in.CallerHints[1], in.CallerHints[0]
	second, _ := bindAnchor(in)
	if !reflect.DeepEqual(first, second) || first.CanonicalID != bindBeta.ID {
		t.Fatalf("hint order changed the decision: %+v vs %+v", first, second)
	}
}

// TestValidateAnchorBindingInputDomain executes every cell of the binding's
// input domain against the validator.
func TestValidateAnchorBindingInputDomain(t *testing.T) {
	long := strings.Repeat("x", SemanticStateMaxTermBytes)
	held := heldBinding(AnchorBindingBound, bindAlpha)
	contested := contestedBinding(bindAlpha, bindBeta)
	unbound := AnchorBinding{State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof}
	with := func(b AnchorBinding, mutate func(*AnchorBinding)) AnchorBinding {
		mutate(&b)
		return b
	}
	for _, cell := range []struct {
		name  string
		value AnchorBinding
		valid bool
	}{
		{"canonical unbound", unbound, true},
		{"state out of vocabulary on an unbound shape", with(unbound, func(b *AnchorBinding) { b.State = "unknown_state" }), false},
		{"state empty on an unbound shape", with(unbound, func(b *AnchorBinding) { b.State = "" }), false},
		{"canonical bound", held, true},
		{"canonical pending", with(held, func(b *AnchorBinding) { b.State = AnchorBindingPendingWindowConfirmation }), true},
		{"canonical contested", contested, true},
		{"zero value", AnchorBinding{}, false},
		{"state empty", with(held, func(b *AnchorBinding) { b.State = "" }), false},
		{"state out of vocabulary", with(held, func(b *AnchorBinding) { b.State = "Bound" }), false},
		{"proof empty", with(held, func(b *AnchorBinding) { b.Proof = "" }), false},
		{"proof out of vocabulary", with(held, func(b *AnchorBinding) { b.Proof = "guessed" }), false},
		{"reason empty", with(held, func(b *AnchorBinding) { b.Reason = "" }), false},
		{"reason out of vocabulary", with(held, func(b *AnchorBinding) { b.Reason = "unknown_reason" }), false},
		{"reason unrecorded is not persistable", with(held, func(b *AnchorBinding) { b.Reason = AnchorBindingReasonUnrecorded }), false},
		{"reason reused_stored is not a vocabulary member", with(held, func(b *AnchorBinding) { b.Reason = "reused_stored" }), false},
		{"held kind empty", with(held, func(b *AnchorBinding) { b.Kind = "" }), false},
		{"held kind out of vocabulary", with(held, func(b *AnchorBinding) { b.Kind = "Repository" }), false},
		{"held id empty", with(held, func(b *AnchorBinding) { b.CanonicalID = "" }), false},
		{"held proof none", with(held, func(b *AnchorBinding) { b.Proof = AnchorBindingProofNone }), false},
		{"held id at the bound", with(held, func(b *AnchorBinding) { b.CanonicalID = long }), true},
		{"held id one past the bound", with(held, func(b *AnchorBinding) { b.CanonicalID = long + "x" }), false},
		{"origin at the bound", with(held, func(b *AnchorBinding) { b.OriginResultID = long }), true},
		{"origin one past the bound", with(held, func(b *AnchorBinding) { b.OriginResultID = long + "x" }), false},
		{"origin empty", with(held, func(b *AnchorBinding) { b.OriginResultID = "" }), true},
		{"epoch zero", with(held, func(b *AnchorBinding) { b.GraphEpoch = 0 }), true},
		{"epoch minus one", with(held, func(b *AnchorBinding) { b.GraphEpoch = -1 }), false},
		{"bound carries a contender", with(held, func(b *AnchorBinding) { b.ContenderKind, b.ContenderID = bindBeta.Kind, bindBeta.ID }), false},
		{"bound carries only a contender kind", with(held, func(b *AnchorBinding) { b.ContenderKind = bindBeta.Kind }), false},
		{"unbound carries a kind", with(unbound, func(b *AnchorBinding) { b.Kind = SubjectRepository }), false},
		{"unbound carries an id", with(unbound, func(b *AnchorBinding) { b.CanonicalID = bindAlpha.ID }), false},
		{"unbound carries a proof", with(unbound, func(b *AnchorBinding) { b.Proof = AnchorBindingProofIdentityProven }), false},
		{"unbound carries a contender", with(unbound, func(b *AnchorBinding) { b.ContenderID = bindBeta.ID }), false},
		{"unbound carries an origin", with(unbound, func(b *AnchorBinding) { b.OriginResultID = "result_x" }), true},
		{"contested contender kind empty", with(contested, func(b *AnchorBinding) { b.ContenderKind = "" }), false},
		{"contested contender kind out of vocabulary", with(contested, func(b *AnchorBinding) { b.ContenderKind = "repo" }), false},
		{"contested contender id empty", with(contested, func(b *AnchorBinding) { b.ContenderID = "" }), false},
		{"contested contender duplicates the held identity", with(contested, func(b *AnchorBinding) { b.ContenderKind, b.ContenderID = bindAlpha.Kind, bindAlpha.ID }), false},
		{"contested contender same id, other kind", with(contested, func(b *AnchorBinding) { b.ContenderKind, b.ContenderID = SubjectProject, bindAlpha.ID }), true},
		{"contested contender at the bound", with(contested, func(b *AnchorBinding) { b.ContenderID = long }), true},
		{"contested contender one past the bound", with(contested, func(b *AnchorBinding) { b.ContenderID = long + "x" }), false},
	} {
		err := ValidateAnchorBinding(cell.value)
		t.Logf("cell %-48s valid=%v err=%v", cell.name, err == nil, err)
		if (err == nil) != cell.valid {
			t.Errorf("cell %q: valid=%v, want %v (err=%v)", cell.name, err == nil, cell.valid, err)
		}
	}
}

// decodeWithBinding returns a valid persisted snapshot's JSON with its
// anchor_binding key replaced by raw (or removed when raw is empty).
func decodeWithBinding(t *testing.T, raw string) (*PersistedSemanticState, SemanticStateReadStatus) {
	t.Helper()
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	if raw != "" {
		extensions, err := json.Marshal(map[string]json.RawMessage{anchorBindingExtension: json.RawMessage(raw)})
		if err != nil {
			t.Fatalf("fixture defect: %v", err)
		}
		document["extensions"] = extensions
	}
	column, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	return DecodeSemanticState(column)
}

// TestAnchorBindingReadDomain executes the stored-column shapes a binding can
// take and what the reader then reports for the parent.
func TestAnchorBindingReadDomain(t *testing.T) {
	canonical, _ := json.Marshal(heldBinding(AnchorBindingBound, bindAlpha))
	for _, cell := range []struct {
		name       string
		raw        string
		wantRead   SemanticStateReadStatus
		wantParent AnchorBindingParentStatus
	}{
		{"key absent", "", SemanticStateReadAvailable, AnchorBindingParentAbsent},
		{"canonical", string(canonical), SemanticStateReadAvailable, AnchorBindingParentPresent},
		{"state token outside this build's vocabulary", strings.Replace(string(canonical), `"bound"`, `"unknown_state"`, 1), SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"empty object", `{}`, SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"null", `null`, SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"wrong container type", `[]`, SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"wrong scalar type", `"bound"`, SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"wrong field type", strings.Replace(string(canonical), `"graph_epoch":7`, `"graph_epoch":"7"`, 1), SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"fractional epoch", strings.Replace(string(canonical), `"graph_epoch":7`, `"graph_epoch":7.5`, 1), SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"unknown inner key", strings.Replace(string(canonical), `{`, `{"future_key":1,`, 1), SemanticStateReadAvailable, AnchorBindingParentInvalid},
		{"stale epoch", strings.Replace(string(canonical), `"graph_epoch":7`, `"graph_epoch":8`, 1), SemanticStateReadAvailable, AnchorBindingParentStaleGraphEpoch},
	} {
		state, status := decodeWithBinding(t, cell.raw)
		var parent AnchorBindingParentStatus
		if status == SemanticStateReadAvailable {
			parent = anchorBindingParentOf("result_parent", state, 7).Status
		}
		t.Logf("cell %-22s read=%s parent=%s", cell.name, status, parent)
		if status != cell.wantRead || parent != cell.wantParent {
			t.Errorf("cell %q: read=%s parent=%s, want read=%s parent=%s", cell.name, status, parent, cell.wantRead, cell.wantParent)
		}
	}
	other := &PersistedSemanticState{Extensions: SemanticStateExtensions{"another_member": json.RawMessage(`{"state":"bound"}`)}}
	if got := anchorBindingParentOf("result_parent", other, 7).Status; got != AnchorBindingParentAbsent {
		t.Errorf("a snapshot carrying only another member: parent=%s, want absent", got)
	}
	if got := anchorBindingParentOf("result_parent", nil, 7).Status; got != AnchorBindingParentAbsent {
		t.Errorf("no snapshot: parent=%s, want absent", got)
	}
}

// TestReadAnchorBindingParentNeverCallsTheStore: every parent status, read
// from the carry memo alone.
func TestReadAnchorBindingParentNeverCallsTheStore(t *testing.T) {
	bound := heldBinding(AnchorBindingBound, bindAlpha)
	invalid := bound
	invalid.Proof = AnchorBindingProofNone
	store := &staticResultStore{
		results: map[string]InvestigationResult{
			"result_present": validInvestigationResult(), "result_absent": validInvestigationResult(),
			"result_invalid": validInvestigationResult(), "result_bare": validInvestigationResult(),
		},
		states: map[string]*PersistedSemanticState{},
	}
	for id, binding := range map[string]*AnchorBinding{"result_present": &bound, "result_absent": nil, "result_invalid": &invalid} {
		state := BuildSemanticState(SemanticStateInput{
			Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
			FamilyVersion:   QuestionFamilyTableVersion,
			RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		})
		setBindingMember(state, binding)
		store.states[id] = state
	}
	ctx := withCarryResultCache(context.Background())
	for _, id := range []string{"result_present", "result_absent", "result_invalid", "result_bare"} {
		if _, err := carryLoadResult(ctx, store, acceptancePrincipal(), id); err != nil {
			t.Fatalf("fixture defect: %v", err)
		}
	}
	loads := len(store.gotIDs)
	for _, cell := range []struct {
		parent string
		ctx    context.Context
		epoch  int64
		want   AnchorBindingParentStatus
	}{
		{"", ctx, 7, AnchorBindingParentNoReference},
		{"  ", ctx, 7, AnchorBindingParentNoReference},
		{"result_present", ctx, 7, AnchorBindingParentPresent},
		{" result_present ", ctx, 7, AnchorBindingParentPresent},
		{"result_present", ctx, 8, AnchorBindingParentStaleGraphEpoch},
		{"result_absent", ctx, 7, AnchorBindingParentAbsent},
		{"result_invalid", ctx, 7, AnchorBindingParentInvalid},
		{"result_bare", ctx, 7, AnchorBindingParentUnloadable},
		{"result_never_loaded", ctx, 7, AnchorBindingParentUnloadable},
		{"result_present", context.Background(), 7, AnchorBindingParentUnloadable},
	} {
		request := validInvestigationRequest()
		request.ParentResultID = cell.parent
		got := readAnchorBindingParent(cell.ctx, request, cell.epoch)
		if got.Status != cell.want {
			t.Errorf("parent %q epoch %d: status %s, want %s", cell.parent, cell.epoch, got.Status, cell.want)
		}
		if got.Status == AnchorBindingParentPresent && !reflect.DeepEqual(got.Binding, bound) {
			t.Errorf("parent %q: binding %+v, want %+v", cell.parent, got.Binding, bound)
		}
	}
	if len(store.gotIDs) != loads {
		t.Fatalf("the parent reader called the store %d times", len(store.gotIDs)-loads)
	}
}

// TestAnchorBindingOverTheCapLeavesTheCaptureUnchanged: a snapshot the binding
// would push over the cap is saved exactly as captured, and the line says the
// binding was left out.
// TestTheBindingMemberKeepsEveryOtherExtensionMember: attaching the binding
// keeps the snapshot's other members and never writes into the source map.
func TestTheBindingMemberKeepsEveryOtherExtensionMember(t *testing.T) {
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	state.Extensions = SemanticStateExtensions{"other_member": json.RawMessage(`{"kept":true}`)}
	tracker := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
	out, event := semanticStateCapture{Write: SemanticStateOf(state)}.withAnchorShadow(tracker).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_members"})
	if event == nil || event.Persisted != "" || out.Write.State == nil {
		t.Fatalf("attach: event=%+v state=%v", event, out.Write.State)
	}
	if got := string(out.Write.State.Extensions["other_member"]); got != `{"kept":true}` {
		t.Fatalf("the other member = %q", got)
	}
	if binding := bindingMember(out.Write.State); binding == nil || binding.Reason != AnchorBindingReasonNoProof {
		t.Fatalf("binding member = %+v", binding)
	}
	if len(state.Extensions) != 1 || bindingMember(state) != nil {
		t.Fatalf("the source snapshot's members changed: %v", state.Extensions)
	}
}

func TestAnAnchorBindingThatCannotEncodeLeavesTheCaptureUnchanged(t *testing.T) {
	// A carried identity larger than the whole cap: the binder copies it into
	// the decision, and the snapshot with that binding cannot encode.
	long := strings.Repeat(`"`, SemanticStateMaxEncodedBytes)
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("fixture defect: the base snapshot must encode: %v", err)
	}
	capture := semanticStateCapture{Write: SemanticStateOf(state), EncodedBytes: len(encoded)}
	carried := AnchorBinding{State: AnchorBindingContested, Kind: SubjectRepository, CanonicalID: long, Proof: AnchorBindingProofIdentityProven,
		Reason: AnchorBindingReasonContestedByResolution, OriginResultID: "result_parent", ContenderKind: SubjectRepository, ContenderID: bindBeta.ID}
	tracker := &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_parent", Status: AnchorBindingParentPresent, Binding: carried}, evaluation: AnchorBindingEvaluationNotResolved}
	out, event := capture.withAnchorShadow(tracker).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_over_cap"})
	if event == nil || event.Persisted != AnchorBindingUnencodable {
		t.Fatalf("event = %+v, want persisted=binding_unencodable", event)
	}
	if out.Write.State != state || out.EncodedBytes != len(encoded) || bindingMember(state) != nil {
		t.Fatalf("the capture changed: state=%p (want %p) bytes=%d (want %d) binding=%+v", out.Write.State, state, out.EncodedBytes, len(encoded), bindingMember(state))
	}
	if event.To.CanonicalID != long {
		t.Fatalf("the line lost the decision: %+v", event.To)
	}

	// The control: the same snapshot with an ordinary carried identity gets
	// the binding, and the source snapshot is not modified.
	ordinary := &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_parent", Status: AnchorBindingParentPresent, Binding: contestedBinding(bindAlpha, bindBeta)}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
	attached, smallEvent := capture.withAnchorShadow(ordinary).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_small"})
	if smallEvent.Persisted != "" || attached.Write.State == nil || bindingMember(attached.Write.State) == nil || bindingMember(state) != nil || attached.EncodedBytes <= len(encoded) {
		t.Fatalf("control: persisted=%q attached=%+v bytes=%d; the source snapshot must stay unmodified", smallEvent.Persisted, attached.Write.State, attached.EncodedBytes)
	}

	// A carried identity the store cannot hold, under the cap.
	for name, value := range map[string]string{"nul": "repository:a\x00b", "invalid utf-8": "repository:a\xffb"} {
		unholdable := &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_parent", Status: AnchorBindingParentPresent, Binding: heldBinding(AnchorBindingBound, anchorRef{Kind: SubjectRepository, ID: value})}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
		kept, keptEvent := capture.withAnchorShadow(unholdable).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_unholdable"})
		if keptEvent.Persisted != AnchorBindingUnencodable || kept.Write.State != state || kept.EncodedBytes != len(encoded) {
			t.Fatalf("%s: persisted=%q state=%p bytes=%d, want the capture unchanged", name, keptEvent.Persisted, kept.Write.State, kept.EncodedBytes)
		}
	}

	// No snapshot: nothing to attach to.
	absent, absentEvent := absentSemanticState(SemanticStateAbsenceContinuationRefused).withAnchorShadow(tracker).attachAnchorBinding(BudgetAssertContinuationRefusal, InvestigationResult{ResultID: "result_absent"})
	if absentEvent.Persisted != AnchorBindingStateAbsent || absentEvent.Agreement != AnchorBindingNotEvaluated || absent.Write.State != nil {
		t.Fatalf("absent: event=%+v", absentEvent)
	}

	// No tracker: no decision.
	if _, none := capture.attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{}); none != nil {
		t.Fatalf("a capture with no tracker decided %+v", none)
	}
}

// TestAnchorBindingLineVocabularyIsClosedOverItsProducers: every token an
// emitter can write for a closed key is in that key's vocabulary, and an
// unknown value renders the out-of-vocabulary token.
func TestAnchorBindingLineVocabularyIsClosedOverItsProducers(t *testing.T) {
	for key, members := range map[string][]string{
		"from_state":            stringsOf(anchorBindingStates()),
		"to_state":              stringsOf(anchorBindingStates()),
		"proof":                 stringsOf(anchorBindingProofs()),
		"reason":                stringsOf(anchorBindingReasons()),
		"parent_binding":        stringsOf(anchorBindingParentStatuses()),
		"evaluation":            stringsOf(anchorBindingEvaluations()),
		"shadow_agreement":      {"agree", "disagree", "not_evaluated"},
		"disagreement_field":    {"none", "pending_proof", "carried_anchor", "count_anchor"},
		"persisted":             {"persisted", "payload_rejected", "replay_conflict", "superseded", "save_failed", "state_absent", "binding_unencodable", "not_saved"},
		"served_count_decision": {"not_evaluated", "anchor_committed", "anchor_ambiguous", "anchor_unresolved"},
		"carry_checks":          {"not_applicable", "not_evaluated"},
	} {
		if got := AnchorBindingTransitionLineVocabulary(key); !reflect.DeepEqual(got, members) {
			t.Errorf("vocabulary %q = %v, want %v", key, got, members)
		}
		if got := anchorBindingClosedToken(key, "not-a-member"); got != "undeclared" {
			t.Errorf("key %q renders an unknown value as %q", key, got)
		}
	}
	if got := AnchorBindingTransitionLineVocabulary("from_id"); got != nil {
		t.Errorf("an open key has vocabulary %v", got)
	}
	unrecorded := unrecordedAnchorBindingEvent(BudgetAssertDecisive, InvestigationResult{ResultID: "result_u"})
	args := AnchorBindingTransitionLogArgs(unrecorded, "org_x")
	fields := map[string]any{}
	for i := 0; i+1 < len(args); i += 2 {
		fields[args[i].(string)] = args[i+1]
	}
	if fields["reason"] != "unrecorded" || fields["site"] != "decisive" || fields["persisted"] != "undeclared" || fields["carry_checks"] != "not_applicable" {
		t.Fatalf("unrecorded line fields = %v", fields)
	}
}

// TestASupersededCaptureKeepsItsTracker: removing refused members from a
// capture keeps the tracker on both of its outcomes.
func TestASupersededCaptureKeepsItsTracker(t *testing.T) {
	tracker := &anchorBindingTracker{}
	entries := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectRepository, AppliedValue: bindAlpha.ID}}
	valid := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		ConfirmedNeeds:  entries,
	})
	unencodable := *valid
	unencodable.FormatVersion = "semantic-state.v0"
	for name, state := range map[string]*PersistedSemanticState{"re-encoded": valid, "refused on re-encode": &unencodable} {
		out := semanticStateCapture{Write: SemanticStateOf(state)}.withAnchorShadow(tracker).withoutSupersededNeeds([]contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor})
		if out.anchorShadow != tracker {
			t.Errorf("%s: the capture lost its tracker", name)
		}
	}
	if out := (semanticStateCapture{Write: SemanticStateOf(&unencodable)}).withoutSupersededNeeds([]contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}); out.Write.State != nil {
		t.Fatalf("premise: the refused re-encode must yield an absence, got %+v", out.Write)
	}
}
