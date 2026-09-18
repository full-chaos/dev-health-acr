package contextfabric

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// hintShape is one subject hint the public v1 request contract can carry.
type hintShape struct {
	name     string
	hint     contractsv1.ContextFabricSubjectHint
	admitted bool // the v1 contract admits it
}

// admittedHintShapes enumerates the shapes ContextFabricSubjectHint.Validate
// admits, plus the one it refuses, so the refusal is asserted rather than
// assumed. A label-only hint is the chat surface's ordinary case: the caller
// names a subject it has no canonical id for.
func admittedHintShapes() []hintShape {
	kind := SubjectRepository
	return []hintShape{
		{"id only", contractsv1.ContextFabricSubjectHint{Kind: kind, ID: bindBeta.ID, Source: "ask-dev"}, true},
		{"label only", contractsv1.ContextFabricSubjectHint{Kind: kind, Label: "Beta", Source: "ask-dev"}, true},
		{"id and label", contractsv1.ContextFabricSubjectHint{Kind: kind, ID: bindBeta.ID, Label: "Beta", Source: "ask-dev"}, true},
		{"kind alone", contractsv1.ContextFabricSubjectHint{Kind: kind, Source: "ask-dev"}, false},
	}
}

// TestTheBinderDecidesAPersistableBindingForEveryAdmittedHint: the binder's
// output is valid by construction for every caller hint the public request
// contract admits. A hint that names no identity -- v1 admits a label with no
// canonical id -- proves nothing and contests nothing: it is recorded on the
// line as an unproved hint, and never becomes an anchor or a contender with
// an empty id, which ValidateAnchorBinding refuses.
func TestTheBinderDecidesAPersistableBindingForEveryAdmittedHint(t *testing.T) {
	held := []struct {
		name string
		from AnchorBinding
	}{
		{"nothing held", unboundFrom},
		{"held, the hint's own kind", heldBinding(AnchorBindingBound, bindAlpha)},
		{"held, another kind", heldBinding(AnchorBindingBound, bindProject)},
	}
	evaluations := []AnchorBindingEvaluation{AnchorBindingEvaluationResolved, AnchorBindingEvaluationReused}
	proofs := []struct {
		name string
		refs []anchorRef
	}{
		{"nothing proven", nil},
		{"the held identity proven", []anchorRef{bindAlpha}},
		{"another identity proven", []anchorRef{bindGamma}},
		// A committed subject that names no identity is no proof either: the
		// binder would otherwise bind an anchor with no id, or no kind, and
		// its own validator refuses both.
		{"a committed subject with no canonical id", []anchorRef{{Kind: SubjectRepository}}},
		{"a committed subject with no kind", []anchorRef{{ID: "repository:bind-no-kind"}}},
		{"one identity and one subject with no canonical id", []anchorRef{bindAlpha, {Kind: SubjectRepository}}},
	}

	cells := 0
	for _, shape := range admittedHintShapes() {
		err := shape.hint.Validate()
		if (err == nil) != shape.admitted {
			t.Fatalf("hint shape %q: v1 Validate() = %v, admitted = %v", shape.name, err, shape.admitted)
		}
		for _, h := range held {
			for _, evaluation := range evaluations {
				for _, proof := range proofs {
					resolution, bases := proofOf(CommitBasisAuthoritativeIdentity, proof.refs...)
					in := anchorBindingInput{
						From: h.from, Evaluation: evaluation, Frame: countingFrame(SubjectTeam),
						CallerHints: []SubjectHint{{Kind: shape.hint.Kind, ID: shape.hint.ID, Label: shape.hint.Label, Source: shape.hint.Source}},
						Resolution:  resolution, Bases: bases,
						ResultID: "result_domain", GraphEpoch: 5,
					}
					to, proposal := bindAnchor(in)
					cells++
					name := fmt.Sprintf("%s / %s / %s / %s", shape.name, h.name, evaluation, proof.name)
					if err := ValidateAnchorBinding(to); err != nil {
						t.Errorf("%s: the binder decided a binding its own validator refuses: %v (to=%+v)", name, err, to)
						continue
					}
					// Nothing that names no identity may reach the decision,
					// whichever field it would have landed in.
					if (to.Kind == "") != (to.CanonicalID == "") {
						t.Errorf("%s: a half-named anchor: kind=%q id=%q", name, to.Kind, to.CanonicalID)
					}
					if (to.ContenderKind == "") != (to.ContenderID == "") {
						t.Errorf("%s: a half-named contender: kind=%q id=%q", name, to.ContenderKind, to.ContenderID)
					}
					if shape.hint.ID != "" {
						continue
					}
					// A hint with no canonical id names no identity.
					if to.CanonicalID == "" && to.ContenderID == "" {
						// Nothing was taken from it, which is the point.
					}
					if to.ContenderID == "" && to.ContenderKind != "" {
						t.Errorf("%s: a contender kind with no id", name)
					}
					tracker := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}, frame: in.Frame}
					event := tracker.lineFor(BudgetAssertDecisive, InvestigationResult{ResultID: in.ResultID}, in, proposal, to)
					if !containsString(event.CallerHintIDs, string(shape.hint.Kind)+":") {
						t.Errorf("%s: the unproved hint is not recorded on the line: caller_hint_ids=%v", name, event.CallerHintIDs)
					}
				}
			}
		}
	}
	t.Logf("cells: %d", cells)
	if want := len(admittedHintShapes()) * len(held) * len(evaluations) * len(proofs); cells != want {
		t.Fatalf("ran %d cells, want %d", cells, want)
	}
}

// anchorBindingLineInputKeys are the line's keys that describe what the
// binder READ. anchorBindingLineOutputKeys are what it decided and what
// happened to the decision afterwards. Every key of the line belongs to
// exactly one of the two, asserted below, so a key added without being
// classified fails here.
var (
	anchorBindingLineInputKeys = []string{
		"org_id", "result_id", "parent_result_id", "site", "evaluation",
		"parent_binding", "carry_checks",
		"from_state", "from_kind", "from_id", "from_proof", "from_reason",
		"from_origin_result_id", "from_graph_epoch", "from_contender_kind", "from_contender_id",
		"parent_graph_epoch", "graph_epoch",
		"frame_expression_kind", "frame_member_kind", "anchor_term_count", "anchor_term_matched_ids",
		"committed_subjects", "model_anchor_kind", "named_expected_kind",
		"receipt_anchor_kind", "receipt_anchor_id", "caller_hint_ids",
		// The subject-substitution guard's decision is what the tracker read
		// to choose the proof: a fired guard hands the binder the decisive
		// resolution, and the withheld commit then appears under
		// committed_subjects.
		"substitution_guard",
	}
	anchorBindingLineOutputKeys = []string{
		"proven_anchor_ids", "effective_kind",
		"to_state", "to_kind", "to_id", "proof", "reason", "origin_result_id",
		"to_graph_epoch", "contender_kind", "contender_id",
		"persisted", "shadow_agreement", "disagreement_field",
		"served_anchor_kind", "served_anchor_id",
		"served_count_decision", "served_count_kind", "served_count_id",
	}
)

// TestEveryLineKeyIsClassifiedAsAnInputOrAnOutput: the partition the
// completeness proof below reads is total over the line. A key added to the
// line without being classified fails here rather than silently counting as
// an input.
func TestEveryLineKeyIsClassifiedAsAnInputOrAnOutput(t *testing.T) {
	classified := map[string]string{}
	for _, key := range anchorBindingLineInputKeys {
		classified[key] = "input"
	}
	for _, key := range anchorBindingLineOutputKeys {
		if was, ok := classified[key]; ok {
			t.Errorf("key %q is classified twice (%s and output)", key, was)
		}
		classified[key] = "output"
	}
	written := anchorBindingLineKeys(AnchorBindingTransitionEvent{})
	for _, key := range written {
		if _, ok := classified[key]; !ok {
			t.Errorf("line key %q is neither a declared input key nor a declared output key", key)
		}
	}
	for key := range classified {
		if !containsString(written, key) {
			t.Errorf("classified key %q is not written on the line", key)
		}
	}
}

// anchorBindingLineKeys is the line's keys, in order.
func anchorBindingLineKeys(event AnchorBindingTransitionEvent) []string {
	args := AnchorBindingTransitionLogArgs(event, "org_line_keys")
	out := make([]string, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		out = append(out, args[i].(string))
	}
	return out
}

// anchorBindingLineValues is the line as key -> rendered value.
func anchorBindingLineValues(t *testing.T, in anchorBindingInput) map[string]string {
	t.Helper()
	to, proposal := bindAnchor(in)
	tracker := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}, frame: in.Frame}
	event := tracker.lineFor(BudgetAssertDecisive, InvestigationResult{ResultID: in.ResultID}, in, proposal, to)
	event.Persisted, event.Agreement, event.DisagreementField = AnchorBindingNotSaved, AnchorBindingNotEvaluated, AnchorBindingFieldNone
	args := AnchorBindingTransitionLogArgs(event, "org_line_values")
	out := map[string]string{}
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i].(string)] = fmt.Sprint(args[i+1])
	}
	return out
}

// TestTwoTurnsWithEqualLineInputsDecideTheSameBinding is the line's
// COMPLETENESS, proved behaviourally rather than by a hand-written map of
// field names to keys. For each pair of turns that differ in exactly ONE
// binder input, if the two turns decide differently then at least one INPUT
// key of the line differs: an operator holding two lines can always name what
// made them differ. A binder input with no key fails here, because the two
// lines would be identical in every input key while the decision changed.
func TestTwoTurnsWithEqualLineInputsDecideTheSameBinding(t *testing.T) {
	provenAlpha, alphaBases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha)
	provenGamma, gammaBases := proofOf(CommitBasisAuthoritativeIdentity, bindGamma)
	callerCanonical, callerBases := proofOf(CommitBasisCallerCanonicalID, bindAlpha)
	statistical, statisticalBases := proofOf(CommitBasisStatistical, bindAlpha)

	// The same proof with the candidate's matched terms changed to a term the
	// frame never names: a commit whose matched terms the frame never names is
	// not admitted.
	unmatched, unmatchedBases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha)
	unmatched.Candidates[0].MatchedTerms = []string{"not-an-anchor-term"}

	base := func() anchorBindingInput {
		return anchorBindingInput{
			From: unboundFrom, Evaluation: AnchorBindingEvaluationResolved,
			Frame: countingFrame(SubjectTeam), Resolution: provenAlpha, Bases: alphaBases,
			ResultID: "result_completeness", GraphEpoch: 7,
		}
	}
	with := func(edit func(in *anchorBindingInput)) anchorBindingInput {
		in := base()
		edit(&in)
		return in
	}

	cases := []struct {
		name  string
		input string // the binder input this pair varies
		other anchorBindingInput
	}{
		{"the binding this turn starts from", "From", with(func(in *anchorBindingInput) { in.From = heldBinding(AnchorBindingBound, bindGamma) })},
		{"a pending carry instead of none", "From", with(func(in *anchorBindingInput) { in.From = heldBinding(AnchorBindingPendingWindowConfirmation, bindAlpha) })},
		{"the evaluation", "Evaluation", with(func(in *anchorBindingInput) { in.Evaluation = AnchorBindingEvaluationNotResolved })},
		{"a window gate instead of a resolved turn", "Evaluation", with(func(in *anchorBindingInput) { in.Evaluation = AnchorBindingEvaluationWindowGated })},
		{"no frame at all", "Frame", with(func(in *anchorBindingInput) { in.Frame = nil })},
		{"the frame's member kind", "Frame", with(func(in *anchorBindingInput) { in.Frame = countingFrame(SubjectRepository) })},
		{"the anchor-term match over the candidates", "Resolution", with(func(in *anchorBindingInput) { in.Resolution, in.Bases = unmatched, unmatchedBases })},
		{"which identity the turn committed", "Resolution", with(func(in *anchorBindingInput) { in.Resolution, in.Bases = provenGamma, gammaBases })},
		{"nothing committed", "Resolution", with(func(in *anchorBindingInput) { in.Resolution, in.Bases = SubjectResolution{}, CommitBasisSet{} })},
		{"the commit basis: caller canonical id", "Bases", with(func(in *anchorBindingInput) { in.Resolution, in.Bases = callerCanonical, callerBases })},
		{"the commit basis: statistical only", "Bases", with(func(in *anchorBindingInput) { in.Resolution, in.Bases = statistical, statisticalBases })},
		{"a redeemed anchor receipt", "Receipt", with(func(in *anchorBindingInput) {
			in.Receipt = &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: bindGamma.Kind, AppliedValue: bindGamma.ID}
		})},
		{"the model's stated anchor kind", "ModelAnchorKind", with(func(in *anchorBindingInput) {
			in.Frame, in.ModelAnchorKind = namedFrameWithoutKind(), SubjectProject
		})},
		{"a caller hint naming the proven identity", "CallerHints", with(func(in *anchorBindingInput) {
			in.CallerHints = []SubjectHint{{Kind: bindAlpha.Kind, ID: bindAlpha.ID, Source: "ask-dev"}}
		})},
		{"the turn's result id", "ResultID", with(func(in *anchorBindingInput) { in.ResultID = "result_completeness_other" })},
		{"the turn's graph epoch", "GraphEpoch", with(func(in *anchorBindingInput) { in.GraphEpoch = 9 })},
	}

	inputKeys := map[string]bool{}
	for _, key := range anchorBindingLineInputKeys {
		inputKeys[key] = true
	}
	baseLine := anchorBindingLineValues(t, base())
	baseTo, _ := bindAnchor(base())
	explained := 0
	for _, tc := range cases {
		otherTo, _ := bindAnchor(tc.other)
		otherLine := anchorBindingLineValues(t, tc.other)
		var differingInputs, differingOutputs []string
		for key, value := range baseLine {
			if otherLine[key] == value {
				continue
			}
			if inputKeys[key] {
				differingInputs = append(differingInputs, key)
			} else {
				differingOutputs = append(differingOutputs, key)
			}
		}
		sort.Strings(differingInputs)
		sort.Strings(differingOutputs)
		if baseTo == otherTo {
			t.Logf("%-46s (%s): same decision; differing input keys %v", tc.name, tc.input, differingInputs)
			continue
		}
		explained++
		t.Logf("%-46s (%s): decision %s/%s -> %s/%s; differing input keys %v",
			tc.name, tc.input, baseTo.State, baseTo.Reason, otherTo.State, otherTo.Reason, differingInputs)
		if len(differingInputs) == 0 {
			t.Errorf("%s: two turns decided differently (%+v vs %+v) while EVERY input key of the line is equal -- the binder read %s, and the line does not carry it. Differing output keys only: %v",
				tc.name, baseTo, otherTo, tc.input, differingOutputs)
		}
	}
	if explained == 0 {
		t.Fatalf("no case changed the decision, so nothing was proved")
	}
	t.Logf("cases: %d, of which changed the decision: %d", len(cases), explained)
}

// namedFrameWithoutKind is a scoped frame whose named expression states no
// expected kind, so the model's own anchor kind is what decides.
func namedFrameWithoutKind() *QuestionFrame {
	frame := countingFrame(SubjectTeam)
	if frame.SubjectExpression.Scoped != nil {
		frame.SubjectExpression.Scoped.AnchorTerms = []string{"a"}
	}
	return frame
}

// TestTheLineNamesTheMemberKindAndTheAnchorTermMatch pins the two keys the
// completeness proof above added, with the values the real producer writes.
func TestTheLineNamesTheMemberKindAndTheAnchorTermMatch(t *testing.T) {
	resolution, bases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha)
	in := anchorBindingInput{
		From: unboundFrom, Evaluation: AnchorBindingEvaluationResolved,
		Frame: countingFrame(SubjectTeam), Resolution: resolution, Bases: bases,
		ResultID: "result_member_kind", GraphEpoch: 3,
	}
	line := anchorBindingLineValues(t, in)
	if got := line["frame_member_kind"]; got != string(SubjectTeam) {
		t.Errorf("frame_member_kind = %q, want %q", got, SubjectTeam)
	}
	if got, want := line["anchor_term_matched_ids"], "["+string(bindAlpha.Kind)+":"+bindAlpha.ID+"]"; got != want {
		t.Errorf("anchor_term_matched_ids = %q, want %q", got, want)
	}

	// No frame: no member kind, and no term can be matched.
	noFrame := in
	noFrame.Frame = nil
	line = anchorBindingLineValues(t, noFrame)
	if line["frame_member_kind"] != "" || line["anchor_term_matched_ids"] != "[]" {
		t.Errorf("with no frame: frame_member_kind=%q anchor_term_matched_ids=%q, want empty and []",
			line["frame_member_kind"], line["anchor_term_matched_ids"])
	}

	// A candidate that matched no stated anchor term is not admitted, and the
	// line says so rather than leaving the operator to guess.
	unmatched, unmatchedBases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha)
	unmatched.Candidates[0].MatchedTerms = []string{"not-an-anchor-term"}
	missed := in
	missed.Resolution, missed.Bases = unmatched, unmatchedBases
	line = anchorBindingLineValues(t, missed)
	if line["anchor_term_matched_ids"] != "[]" {
		t.Errorf("an unmatched candidate: anchor_term_matched_ids = %q, want []", line["anchor_term_matched_ids"])
	}
	if !strings.Contains(line["committed_subjects"], bindAlpha.ID) {
		t.Errorf("the subject is still committed and must still be listed: %q", line["committed_subjects"])
	}

	// One subject committed twice is ONE matched identity: the key is a set of
	// identities, so an operator reading it counts anchors, not commits.
	twice, twiceBases := proofOf(CommitBasisAuthoritativeIdentity, bindAlpha, bindAlpha)
	if len(twice.Committed) != 2 {
		t.Fatalf("fixture defect: %d committed", len(twice.Committed))
	}
	repeated := in
	repeated.Resolution, repeated.Bases = twice, twiceBases
	line = anchorBindingLineValues(t, repeated)
	if got, want := line["anchor_term_matched_ids"], "["+string(bindAlpha.Kind)+":"+bindAlpha.ID+"]"; got != want {
		t.Errorf("the same subject committed twice: anchor_term_matched_ids = %q, want %q", got, want)
	}
}

// TestARequestCarriesOneLinePerSavedResultAndSite is the declared
// multiplicity, counted through the real engine. eventspec declares exactly
// one line per ATTEMPT, and this event's attempt is its whole Attribution --
// (org_id, result_id, site) -- so a request that saves two results carries
// two lines, discriminated by site, and never two for one result at one site.
func TestARequestCarriesOneLinePerSavedResultAndSite(t *testing.T) {
	lines := func(t *testing.T, supersede bool) []AnchorBindingTransitionEvent {
		t.Helper()
		rig := newAnchorProbeRig(t, false)
		rec := &recordingTelemetry{}
		rig.engine.telemetry = rec
		if supersede {
			rig.engine.results = failingSaveStore{staticResultStore: rig.store,
				saveErr: &ErrStructureOfferSuperseded{Members: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}}}
		}
		rig.interpreter.read("", false)
		rig.graph.response = identityProvenResponse(probeAlpha)
		_, _ = rig.engine.Investigate(t.Context(), acceptancePrincipal(), needTurnRequest("request_one_line_per_attempt", true))
		return rec.anchorBindingTransitions
	}

	one := lines(t, false)
	if len(one) != 1 {
		t.Fatalf("a request that saves once emitted %d lines, want 1", len(one))
	}
	if one[0].Site != BudgetAssertDecisive {
		t.Errorf("the single line's site = %s, want %s", one[0].Site, BudgetAssertDecisive)
	}

	two := lines(t, true)
	if len(two) != 2 {
		t.Fatalf("a request whose decisive Save loses the structure claim emitted %d lines, want 2", len(two))
	}
	attempts := map[string]bool{}
	decisive := 0
	for i, line := range two {
		t.Logf("line %d: result_id=%s site=%s reason=%s persisted=%s", i, line.ResultID, line.Site, line.To.Reason, line.Persisted)
		key := line.ResultID + "\x00" + string(line.Site)
		if attempts[key] {
			t.Fatalf("two lines for one attempt (result_id=%q, site=%s)", line.ResultID, line.Site)
		}
		attempts[key] = true
		if line.Site == BudgetAssertDecisive {
			decisive++
		}
	}
	if decisive != 1 {
		t.Errorf("%d lines carry site=decisive, want exactly 1 -- site is the discriminator a reader joins on", decisive)
	}
	if two[0].ResultID == two[1].ResultID {
		t.Errorf("both lines name result_id %q; the two Saves stored two results", two[0].ResultID)
	}
}

// TestARowNearTheCapDropsTheMemberAndNeverTheCapture: the shadow's member is
// accounted as the SHADOW's. A snapshot whose own content already fills the
// column keeps exactly what it would have written without the shadow; it is
// the binding member that is dropped, with persisted=binding_unencodable on
// the line, and never the capture.
func TestARowNearTheCapDropsTheMemberAndNeverTheCapture(t *testing.T) {
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})

	// Measure the member this binding costs, then fill the row with another
	// member until the two together cannot fit.
	tracker := func() *anchorBindingTracker {
		return &anchorBindingTracker{
			parent:     anchorBindingParent{ResultID: "result_parent", Status: AnchorBindingParentPresent, Binding: heldBinding(AnchorBindingBound, bindAlpha)},
			evaluation: AnchorBindingEvaluationNotResolved, epoch: 7,
		}
	}
	bare, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("fixture defect: the base snapshot must encode: %v", err)
	}
	withMember, err := withAnchorBindingMember(state, heldBinding(AnchorBindingBound, bindAlpha))
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	encodedWithMember, err := EncodeSemanticState(withMember)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	memberCost := len(encodedWithMember) - len(bare)
	t.Logf("the binding member costs %d bytes; the column cap is %d", memberCost, SemanticStateMaxEncodedBytes)
	if memberCost <= 0 {
		t.Fatalf("fixture defect: the member costs %d bytes", memberCost)
	}

	// A row that fits with 8 bytes to spare, and cannot fit the member.
	filler := SemanticStateMaxEncodedBytes - len(bare) - memberCost/2
	state.Extensions = SemanticStateExtensions{"filler": jsonStringOfLength(t, filler)}
	full, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("the near-cap row must still encode on its own: %v", err)
	}
	t.Logf("near-cap row: %d bytes of %d, %d to spare, member needs %d", len(full), SemanticStateMaxEncodedBytes, SemanticStateMaxEncodedBytes-len(full), memberCost)
	if len(full) > SemanticStateMaxEncodedBytes || SemanticStateMaxEncodedBytes-len(full) >= memberCost {
		t.Fatalf("fixture defect: the row is not inside the boundary being tested")
	}

	capture := semanticStateCapture{Write: SemanticStateOf(state), EncodedBytes: len(full)}
	out, event := capture.withAnchorShadow(tracker()).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_near_cap"})
	if event == nil || event.Persisted != AnchorBindingUnencodable {
		t.Fatalf("persisted = %v, want binding_unencodable", event)
	}
	if out.Write.State != state || out.EncodedBytes != len(full) {
		t.Fatalf("the capture changed: state=%p (want %p) bytes=%d (want %d)", out.Write.State, state, out.EncodedBytes, len(full))
	}
	if bindingMember(state) != nil {
		t.Fatalf("the member was written into a row that cannot hold it")
	}
	if _, ok := state.Extensions["filler"]; !ok {
		t.Fatalf("the row's own member was dropped; only the shadow's may be")
	}
	if event.To.CanonicalID != bindAlpha.ID {
		t.Fatalf("the line lost the decision: %+v", event.To)
	}

	// The control: the same row with room for the member takes it.
	state.Extensions = SemanticStateExtensions{"filler": jsonStringOfLength(t, filler-2*memberCost)}
	roomy, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	capture = semanticStateCapture{Write: SemanticStateOf(state), EncodedBytes: len(roomy)}
	attached, attachedEvent := capture.withAnchorShadow(tracker()).attachAnchorBinding(BudgetAssertDecisive, InvestigationResult{ResultID: "result_near_cap_control"})
	if attachedEvent.Persisted != "" || bindingMember(attached.Write.State) == nil || attached.EncodedBytes <= len(roomy) {
		t.Fatalf("control: persisted=%q bytes=%d (base %d), want the member attached", attachedEvent.Persisted, attached.EncodedBytes, len(roomy))
	}
	// The capture's own byte count is what the cap is measured against, so it
	// is the encoded length of the row that will be written, exactly.
	writtenBytes, err := EncodeSemanticState(attached.Write.State)
	if err != nil {
		t.Fatalf("control: the attached row must encode: %v", err)
	}
	if attached.EncodedBytes != len(writtenBytes) {
		t.Fatalf("control: the capture reports %d bytes for a row that encodes to %d -- the cap would be measured against the wrong number", attached.EncodedBytes, len(writtenBytes))
	}
	t.Logf("control: %d bytes of %d with the member attached", attached.EncodedBytes, SemanticStateMaxEncodedBytes)
}

// jsonStringOfLength is a JSON string member value whose encoded form is n
// bytes long.
func jsonStringOfLength(t *testing.T, n int) json.RawMessage {
	t.Helper()
	if n < 2 {
		t.Fatalf("fixture defect: a JSON string cannot be %d bytes", n)
	}
	return json.RawMessage(`"` + strings.Repeat("x", n-2) + `"`)
}
