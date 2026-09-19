package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The carried chain member's own input domains. The whole chain decision is
// enumerated through the real engine over both production stores by
// paritytest.RunCarriedParentChainSuite; these pin the member's minting,
// decoding and attachment on every input they read.

// TestCarriedParentIdentityOfOverTheEvidenceDomain mints a member from every
// combination of the six evidence facts it reads and checks the state, that
// the member is internally consistent, and that a held identity's receipt is
// the one minted from its result and subject.
func TestCarriedParentIdentityOfOverTheEvidenceDomain(t *testing.T) {
	t.Parallel()
	bools := []bool{false, true}
	cells := 0
	for _, referenced := range bools {
		for _, loaded := range bools {
			for _, held := range bools {
				for _, named := range bools {
					for _, guardIssued := range bools {
						for _, carried := range bools {
							for _, depth := range []int{0, 1, 2} {
								cells++
								evidence := parentAnchorEvidence{Referenced: referenced, Loaded: loaded, GuardIssued: guardIssued, Carried: carried, ChainDepth: depth}
								if held {
									evidence.Subject = substitutionRepoOne
								}
								if named {
									evidence.ResultID = "result_answered"
								}
								member := carriedParentIdentityOf(evidence)
								want := CarriedParentNoIdentity
								switch {
								case !referenced:
									want = CarriedParentNoParentReference
								case !loaded:
									want = CarriedParentUnreadable
								case held && named:
									want = CarriedParentIdentityHeld
								case held || guardIssued || carried:
									want = CarriedParentIdentityUnavailable
								}
								name := fmt.Sprintf("ref=%t loaded=%t held=%t named=%t guard=%t carried=%t depth=%d", referenced, loaded, held, named, guardIssued, carried, depth)
								if member.State != want {
									t.Errorf("%s: state = %q, want %q", name, member.State, want)
								}
								if member.Depth != depth+1 {
									t.Errorf("%s: depth = %d, want %d", name, member.Depth, depth+1)
								}
								if !member.valid() {
									t.Errorf("%s: minted an inconsistent member %+v", name, member)
								}
								if want == CarriedParentIdentityHeld && member.ReceiptID != subjectSubstitutionReceiptID("result_answered", substitutionRepoOne) {
									t.Errorf("%s: receipt %q is not minted from the answered result and subject", name, member.ReceiptID)
								}
							}
						}
					}
				}
			}
		}
	}
	if cells != 2*2*2*2*2*2*3 {
		t.Fatalf("enumerated %d cells", cells)
	}
}

func carriedStored(read SemanticStateReadStatus, raw json.RawMessage) StoredInvestigationResult {
	stored := StoredInvestigationResult{SemanticStateRead: read}
	if read == SemanticStateReadAvailable {
		stored.SemanticState = &PersistedSemanticState{}
		if raw != nil {
			stored.SemanticState.Extensions = SemanticStateExtensions{carriedParentIdentityExtension: raw}
		}
	}
	return stored
}

// TestStoredCarriedParentIdentityOverItsDecodeDomain reads the member from
// every snapshot shape a store can return and every malformed member shape:
// only an available snapshot carrying a consistent member reads as present
// and valid; everything malformed is an error, never a zero member.
func TestStoredCarriedParentIdentityOverItsDecodeDomain(t *testing.T) {
	t.Parallel()
	held := carriedParentIdentityOf(parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne, ResultID: "result_answered"})
	valid, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(v any) json.RawMessage {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	withField := func(field string, value any) json.RawMessage {
		var m map[string]any
		if err := json.Unmarshal(valid, &m); err != nil {
			t.Fatal(err)
		}
		if value == nil {
			delete(m, field)
		} else {
			m[field] = value
		}
		return encode(m)
	}
	cases := []struct {
		name        string
		stored      StoredInvestigationResult
		wantPresent bool
		wantErr     bool
	}{
		{"snapshot absent", carriedStored(SemanticStateReadAbsent, nil), false, false},
		{"snapshot malformed", carriedStored(SemanticStateReadMalformed, nil), false, false},
		{"snapshot status unreported", carriedStored("", nil), false, false},
		{"available with nil state", StoredInvestigationResult{SemanticStateRead: SemanticStateReadAvailable}, false, false},
		{"available, no member", carriedStored(SemanticStateReadAvailable, nil), false, false},
		{"valid member", carriedStored(SemanticStateReadAvailable, valid), true, false},
		{"unknown field", carriedStored(SemanticStateReadAvailable, withField("extra", "x")), true, true},
		{"state out of vocabulary", carriedStored(SemanticStateReadAvailable, withField("state", "identity_guessed")), true, true},
		{"state empty", carriedStored(SemanticStateReadAvailable, withField("state", "")), true, true},
		{"state wrong type", carriedStored(SemanticStateReadAvailable, withField("state", 7)), true, true},
		{"depth zero", carriedStored(SemanticStateReadAvailable, withField("depth", 0)), true, true},
		{"depth negative", carriedStored(SemanticStateReadAvailable, withField("depth", -1)), true, true},
		{"depth absent", carriedStored(SemanticStateReadAvailable, withField("depth", nil)), true, true},
		{"depth fractional", carriedStored(SemanticStateReadAvailable, withField("depth", 1.5)), true, true},
		{"held without receipt", carriedStored(SemanticStateReadAvailable, withField("receipt_id", nil)), true, true},
		{"held without result", carriedStored(SemanticStateReadAvailable, withField("result_id", "")), true, true},
		{"held without kind", carriedStored(SemanticStateReadAvailable, withField("kind", nil)), true, true},
		{"held without canonical id", carriedStored(SemanticStateReadAvailable, withField("canonical_id", nil)), true, true},
		{"no identity naming a result", carriedStored(SemanticStateReadAvailable, encode(map[string]any{"state": "parent_no_identity", "result_id": "result_answered", "depth": 1})), true, true},
		{"no identity naming a kind", carriedStored(SemanticStateReadAvailable, encode(map[string]any{"state": "parent_no_identity", "kind": "repository", "depth": 1})), true, true},
		{"no identity naming an id", carriedStored(SemanticStateReadAvailable, encode(map[string]any{"state": "parent_no_identity", "canonical_id": "repository:alpha-service", "depth": 1})), true, true},
		{"no identity naming a receipt", carriedStored(SemanticStateReadAvailable, encode(map[string]any{"state": "identity_unavailable", "receipt_id": "subr_x", "depth": 1})), true, true},
		{"no identity, consistent", carriedStored(SemanticStateReadAvailable, encode(map[string]any{"state": "parent_no_identity", "depth": 2})), true, false},
		{"not an object", carriedStored(SemanticStateReadAvailable, json.RawMessage(`"identity_held"`)), true, true},
		{"null", carriedStored(SemanticStateReadAvailable, json.RawMessage(`null`)), true, true},
		{"trailing bytes", carriedStored(SemanticStateReadAvailable, append(append(json.RawMessage{}, valid...), []byte(` {}`)...)), true, true},
	}
	for _, tc := range cases {
		member, present, err := storedCarriedParentIdentity(tc.stored)
		if present != tc.wantPresent || (err != nil) != tc.wantErr {
			t.Errorf("%s: present=%t err=%v, want present=%t err=%t", tc.name, present, err, tc.wantPresent, tc.wantErr)
		}
		if err == nil && present && !member.valid() {
			t.Errorf("%s: an accepted member is inconsistent: %+v", tc.name, member)
		}
	}
}

// TestAttachCarriedParentOnEveryBranch: a Save writes the member only into a
// prompt's snapshot, and every branch that writes nothing names itself.
func TestAttachCarriedParentOnEveryBranch(t *testing.T) {
	t.Parallel()
	prompt := InvestigationResult{Status: InvestigationClarificationRequired, SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}
	answered := InvestigationResult{Status: InvestigationComplete, SubjectResolution: SubjectResolution{Committed: []SubjectRef{substitutionRepoOne}}}
	clarifyingWithCommit := InvestigationResult{Status: InvestigationClarificationRequired, SubjectResolution: SubjectResolution{Committed: []SubjectRef{substitutionRepoOne}}}
	refusal := InvestigationResult{Status: InvestigationNoMatch, SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}
	evidence := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne, ResultID: "result_answered"}
	withState := captureSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	if withState.Write.State == nil {
		t.Fatalf("fixture defect: no snapshot captured (%+v)", withState.Write)
	}
	bad := evidence
	bad.Subject.CanonicalID = "repository:\xff"
	cases := []struct {
		name    string
		capture semanticStateCapture
		result  InvestigationResult
		want    CarriedParentAttachment
	}{
		{"answer", withState.withCarriedParent(evidence), answered, CarriedParentAttachmentNotAPrompt},
		{"refusal", withState.withCarriedParent(evidence), refusal, CarriedParentAttachmentNotAPrompt},
		{"clarification that committed", withState.withCarriedParent(evidence), clarifyingWithCommit, CarriedParentAttachmentNotAPrompt},
		{"prompt, unwired", withState, prompt, CarriedParentAttachmentUnwired},
		{"prompt, no snapshot", absentSemanticState(SemanticStateAbsenceSnapshotOversized).withCarriedParent(evidence), prompt, CarriedParentAttachmentNoSnapshot},
		{"prompt, unencodable", withState.withCarriedParent(bad), prompt, CarriedParentAttachmentUnencodable},
		{"prompt", withState.withCarriedParent(evidence), prompt, CarriedParentAttachmentAttached},
	}
	for _, tc := range cases {
		out, outcome := tc.capture.attachCarriedParent(tc.result)
		if outcome.Attachment != tc.want {
			t.Errorf("%s: attachment = %q, want %q", tc.name, outcome.Attachment, tc.want)
		}
		stored := StoredInvestigationResult{SemanticStateRead: SemanticStateReadAvailable, SemanticState: out.Write.State}
		member, present, err := storedCarriedParentIdentity(stored)
		if tc.want != CarriedParentAttachmentAttached {
			if present {
				t.Errorf("%s: a member was written", tc.name)
			}
			if outcome.State != "" || outcome.ResultID != "" || outcome.Depth != 0 {
				t.Errorf("%s: an unattached outcome reports %+v", tc.name, outcome)
			}
			continue
		}
		if !present || err != nil || member.State != CarriedParentIdentityHeld || member.ResultID != "result_answered" {
			t.Errorf("%s: member = %+v present=%t err=%v", tc.name, member, present, err)
		}
		if outcome.State != member.State || outcome.ResultID != member.ResultID || outcome.Depth != member.Depth {
			t.Errorf("%s: outcome %+v does not describe the member %+v", tc.name, outcome, member)
		}
		if len(tc.capture.Write.State.Extensions) != 0 {
			t.Errorf("%s: the caller's snapshot was modified", tc.name)
		}
		if encoded, err := EncodeSemanticState(out.Write.State); err != nil || len(encoded) != out.EncodedBytes {
			t.Errorf("%s: encoded bytes %d, measured %d (err %v)", tc.name, len(encoded), out.EncodedBytes, err)
		}
	}
}

// TestParentReadErrorOfClassifiesWithoutTheMessage: the class is decided by
// the error's identity, never its text.
func TestParentReadErrorOfClassifiesWithoutTheMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want SubjectSubstitutionChainError
	}{
		{nil, SubjectSubstitutionChainErrorNone},
		{context.Canceled, SubjectSubstitutionChainErrorCanceled},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), SubjectSubstitutionChainErrorDeadline},
		{fmt.Errorf("get: %w", ErrInvestigationResultNotFound), SubjectSubstitutionChainErrorNotFound},
		{errors.New("not found"), SubjectSubstitutionChainErrorStore},
		{errors.Join(context.Canceled, ErrInvestigationResultNotFound), SubjectSubstitutionChainErrorCanceled},
	}
	for _, tc := range cases {
		if got := parentReadErrorOf(tc.err); got != tc.want {
			t.Errorf("parentReadErrorOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestCarriedParentVocabulariesAreClosedAndDistinct pins each vocabulary's
// size and membership, and that the line tokens never render empty.
func TestCarriedParentVocabulariesAreClosedAndDistinct(t *testing.T) {
	t.Parallel()
	distinct := func(name string, members []string) {
		seen := map[string]bool{}
		for _, member := range members {
			if member == "" || seen[member] {
				t.Errorf("%s: member %q empty or repeated", name, member)
			}
			seen[member] = true
		}
	}
	var states, kinds, chains, errs, attachments []string
	for _, v := range CarriedParentIdentityStateVocabulary() {
		states = append(states, string(v))
		if !ValidCarriedParentIdentityState(v) {
			t.Errorf("state %q not valid", v)
		}
	}
	for _, v := range SubjectSubstitutionParentResultKindVocabulary() {
		kinds = append(kinds, string(v))
	}
	for _, v := range SubjectSubstitutionParentChainVocabulary() {
		chains = append(chains, string(v))
	}
	for _, v := range SubjectSubstitutionChainErrorVocabulary() {
		errs = append(errs, string(v))
	}
	for _, v := range CarriedParentAttachmentVocabulary() {
		attachments = append(attachments, string(v))
	}
	distinct("states", states)
	distinct("kinds", kinds)
	distinct("chains", chains)
	distinct("errors", errs)
	distinct("attachments", attachments)
	if ValidCarriedParentIdentityState("") || ValidCarriedParentIdentityState("identity_guessed") {
		t.Error("an out-of-vocabulary state is valid")
	}
	if parentResultKindToken("") != SubjectSubstitutionParentResultNone || parentChainToken("") != SubjectSubstitutionChainNotAPrompt ||
		noneChainError("") != SubjectSubstitutionChainErrorNone || carriedParentAttachmentToken("") != CarriedParentAttachmentUnwired ||
		carriedParentAttachmentToken("bogus") != CarriedParentAttachmentUnwired {
		t.Error("an empty value renders as something other than its explicit token")
	}
	if parentResultKindToken(SubjectSubstitutionParentResultPrompt) != SubjectSubstitutionParentResultPrompt || parentChainToken(SubjectSubstitutionChainVerified) != SubjectSubstitutionChainVerified ||
		noneChainError(SubjectSubstitutionChainErrorStore) != SubjectSubstitutionChainErrorStore || carriedParentAttachmentToken(CarriedParentAttachmentAttached) != CarriedParentAttachmentAttached {
		t.Error("a recorded value is rewritten")
	}
	for _, tc := range []struct {
		evidence parentAnchorEvidence
		want     SubjectSubstitutionParentResultKind
	}{
		{parentAnchorEvidence{}, SubjectSubstitutionParentResultNone},
		{parentAnchorEvidence{Referenced: true}, SubjectSubstitutionParentResultUnreadable},
		{parentAnchorEvidence{Referenced: true, ResultKind: SubjectSubstitutionParentResultAnswer}, SubjectSubstitutionParentResultAnswer},
	} {
		if got := parentResultKindForLine(tc.evidence); got != tc.want {
			t.Errorf("parentResultKindForLine(%+v) = %q, want %q", tc.evidence, got, tc.want)
		}
	}
}

// TestParentResultKindOfReadsThePayload classifies every payload shape.
func TestParentResultKindOfReadsThePayload(t *testing.T) {
	t.Parallel()
	guard := guardIssuedStored("result_parent", "result_parent", true)
	cases := []struct {
		name   string
		stored StoredInvestigationResult
		want   SubjectSubstitutionParentResultKind
	}{
		{"guard clarification", guard, SubjectSubstitutionParentResultGuardClarification},
		{"prompt", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationClarificationRequired, SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}}, SubjectSubstitutionParentResultPrompt},
		{"clarification that committed", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationClarificationRequired, SubjectResolution: SubjectResolution{Committed: []SubjectRef{substitutionRepoOne}}}}, SubjectSubstitutionParentResultAnswer},
		{"refusal", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationNoMatch}}, SubjectSubstitutionParentResultRefusal},
		{"complete", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationComplete}}, SubjectSubstitutionParentResultAnswer},
		{"partial", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationPartial}}, SubjectSubstitutionParentResultAnswer},
		{"degraded", StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationDegraded}}, SubjectSubstitutionParentResultAnswer},
	}
	for _, tc := range cases {
		if got := parentResultKindOf(tc.stored); got != tc.want {
			t.Errorf("%s: kind = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDecideSubjectSubstitutionReadsACarriedChain: a prompt that speaks for a
// chain it could not verify holds no subject and is never read as asserting
// no identity; one that verified its chain decides like its answered result.
func TestDecideSubjectSubstitutionReadsACarriedChain(t *testing.T) {
	t.Parallel()
	unverified := parentAnchorEvidence{Referenced: true, Loaded: true, Carried: true}
	verified := parentAnchorEvidence{Referenced: true, Loaded: true, Carried: true, Subject: substitutionRepoOne, ResultID: "result_answered"}
	noIdentity := parentAnchorEvidence{Referenced: true, Loaded: true}
	cases := []struct {
		name string
		in   subjectSubstitutionInput
		want SubjectSubstitutionOutcome
	}{
		{"unverified chain, another subject", subjectSubstitutionInput{Parent: unverified, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarifiedRememberedUnavailable},
		{"unverified chain, the answered subject", subjectSubstitutionInput{Parent: unverified, Committed: []SubjectRef{substitutionRepoOne}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarifiedRememberedUnavailable},
		{"unverified chain, caller cannot be asked", subjectSubstitutionInput{Parent: unverified, Committed: []SubjectRef{substitutionRepoTwo}, RememberedAvailable: true}, SubjectSubstitutionRefusedRememberedUnavailable},
		{"unverified chain, commits nothing", subjectSubstitutionInput{Parent: unverified, AllowClarification: true}, SubjectSubstitutionNoCommittedSubject},
		{"unverified chain, redeemed choice", subjectSubstitutionInput{Parent: unverified, Committed: []SubjectRef{substitutionRepoTwo}, RedeemedChoice: true, AllowClarification: true}, SubjectSubstitutionRedeemedChoice},
		{"verified chain, same subject", subjectSubstitutionInput{Parent: verified, Committed: []SubjectRef{substitutionRepoOne}, AllowClarification: true}, SubjectSubstitutionSameSubject},
		{"verified chain, another subject", subjectSubstitutionInput{Parent: verified, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified},
		{"chain carries no identity", subjectSubstitutionInput{Parent: noIdentity, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionParentNoIdentity},
	}
	for _, tc := range cases {
		got := decideSubjectSubstitution(tc.in)
		if got.Outcome != tc.want {
			t.Errorf("%s: outcome = %q, want %q", tc.name, got.Outcome, tc.want)
		}
		if tc.in.Parent.Carried && !tc.in.Parent.held() && got.RememberedListed {
			t.Errorf("%s: listed a remembered subject the chain never verified", tc.name)
		}
	}
}

// TestAChoiceRedeemedFromTheAnsweredResultPastAPromptIsServed: a follow-up
// that names a window prompt and redeems an offer the ANSWERED result made is
// answering that result, so the choice is served; the same follow-up
// committing a subject nobody offered is clarified against the answered
// subject.
func TestAChoiceRedeemedFromTheAnsweredResultPastAPromptIsServed(t *testing.T) {
	t.Parallel()
	answered := substitutionResponse(substitutionRepoOne, "receipt_chain_one")
	answered.resolution.Candidates = append(answered.resolution.Candidates, SubjectCandidate{
		ReceiptID: "receipt_chain_two", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionAmbiguous,
		MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 0.5, EvidenceRefIDs: []string{},
	})
	for _, redeem := range []bool{true, false} {
		redeem := redeem
		t.Run(fmt.Sprintf("redeem=%t", redeem), func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			one := h.turn(needTurnRequest(fmt.Sprintf("request_chain_redeem_%t_one", redeem), true), answered)
			// A different question carries no window, so the window gate asks.
			second := continuingNeedTurn(needTurnRequest(fmt.Sprintf("request_chain_redeem_%t_two", redeem), false), one.result.ResultID)
			second.Question = "And how has it trended?"
			prompt := h.turn(second, substitutionResponse(substitutionRepoOne, "receipt_chain_prompt"))
			if !resultIsPrompt(prompt.result) || prompt.result.WindowClarification == nil {
				t.Fatalf("premise: turn two served %q, want a window prompt", prompt.result.Status)
			}
			var receipts []BoundSubjectReceipt
			if redeem {
				receipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "receipt_chain_two"}}
			}
			three := answerTurn(h, fmt.Sprintf("request_chain_redeem_%t_three", redeem), prompt.result.ResultID, receipts, substitutionResponse(substitutionRepoTwo, "receipt_chain_three"))
			event := lastSubstitution(t, three)
			if event.SubstitutionParentChain != SubjectSubstitutionChainVerified || event.SubstitutionParentResultID != one.result.ResultID {
				t.Fatalf("chain = %q parent result = %q, want verified %q", event.SubstitutionParentChain, event.SubstitutionParentResultID, one.result.ResultID)
			}
			if redeem {
				assertServed(t, three.result, substitutionRepoTwo)
				assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
				return
			}
			assertServedNothing(t, three.result)
			assertGuard(t, event, SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
		})
	}
}

// TestChainIdentityOfNeverKeepsAnUnverifiedSubject: whatever subject the
// evidence arrived holding, a prompt whose chain does not verify leaves it
// holding none, and an engine with no store verifies nothing.
func TestChainIdentityOfNeverKeepsAnUnverifiedSubject(t *testing.T) {
	t.Parallel()
	prompt := StoredInvestigationResult{Result: InvestigationResult{Status: InvestigationClarificationRequired, SubjectResolution: SubjectResolution{Committed: []SubjectRef{}}}, SemanticStateRead: SemanticStateReadAvailable, SemanticState: &PersistedSemanticState{}}
	member := carriedParentIdentityOf(parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne, ResultID: "result_answered"})
	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	withMember := prompt
	withMember.SemanticState = &PersistedSemanticState{Extensions: SemanticStateExtensions{carriedParentIdentityExtension: raw}}
	arriving := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoTwo, ResultID: "result_prompt", IssuedFor: "result_elsewhere"}
	storeless := &Engine{}
	for _, tc := range []struct {
		name   string
		engine *Engine
		stored StoredInvestigationResult
		chain  SubjectSubstitutionParentChain
		reason SubjectSubstitutionChainError
	}{
		{"no member", storeless, prompt, SubjectSubstitutionChainAbsent, ""},
		{"no store", storeless, withMember, SubjectSubstitutionChainAnswerUnreadable, SubjectSubstitutionChainErrorNoStore},
	} {
		got := tc.engine.chainIdentityOf(context.Background(), acceptancePrincipal(), arriving, tc.stored)
		if got.Chain != tc.chain || got.ChainError != tc.reason {
			t.Errorf("%s: chain = %q (%q), want %q (%q)", tc.name, got.Chain, got.ChainError, tc.chain, tc.reason)
		}
		if got.Subject.CanonicalID != "" || got.ResultID != "" || got.IssuedFor != "" || !got.Carried {
			t.Errorf("%s: evidence = %+v, want a carried chain holding nothing", tc.name, got)
		}
	}
}

// TestSemanticStateExtensionKeepsEveryOtherMember: writing the member never
// drops a member the snapshot already carried, and never touches the
// caller's snapshot.
func TestSemanticStateExtensionKeepsEveryOtherMember(t *testing.T) {
	t.Parallel()
	state := &PersistedSemanticState{Extensions: SemanticStateExtensions{"anchor_binding": json.RawMessage(`{"state":"unbound"}`)}}
	out, err := withSemanticStateExtension(state, carriedParentIdentityExtension, map[string]int{"depth": 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Extensions["anchor_binding"]) != `{"state":"unbound"}` || len(out.Extensions) != 2 {
		t.Errorf("extensions = %v, want both members", out.Extensions)
	}
	if len(state.Extensions) != 1 {
		t.Errorf("the caller's snapshot gained a member")
	}
}

// TestSaveReadsTheTurnParentFromItsContext: a capture takes the turn's
// evidence from the context unless it already carries some, and a context
// without any leaves the capture unwired.
func TestSaveReadsTheTurnParentFromItsContext(t *testing.T) {
	t.Parallel()
	recorded := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne, ResultID: "result_recorded"}
	own := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoTwo, ResultID: "result_own"}
	ctx := withTurnParentEvidence(context.Background(), recorded)
	if got := (semanticStateCapture{}).withTurnParentFrom(context.Background()); got.carriedParent != nil {
		t.Errorf("a context with no evidence wired %+v", got.carriedParent)
	}
	if got := (semanticStateCapture{}).withTurnParentFrom(ctx); got.carriedParent == nil || got.carriedParent.ResultID != "result_recorded" {
		t.Errorf("the recorded evidence was not taken: %+v", got.carriedParent)
	}
	if got := (semanticStateCapture{}).withCarriedParent(own).withTurnParentFrom(ctx); got.carriedParent == nil || got.carriedParent.ResultID != "result_own" {
		t.Errorf("the capture's own evidence was replaced: %+v", got.carriedParent)
	}
}

// TestSubjectSubstitutionReceiptIssuerNamesTheAnsweredResult: the remembered
// offer's receipt is minted from the result whose subject it is, and from
// the named parent only when the decision proved none.
func TestSubjectSubstitutionReceiptIssuerNamesTheAnsweredResult(t *testing.T) {
	t.Parallel()
	if got := subjectSubstitutionReceiptIssuer(subjectSubstitutionDecision{ParentResultID: "result_answered"}, "result_prompt"); got != "result_answered" {
		t.Errorf("issuer = %q, want the answered result", got)
	}
	if got := subjectSubstitutionReceiptIssuer(subjectSubstitutionDecision{}, "result_prompt"); got != "result_prompt" {
		t.Errorf("issuer = %q, want the named parent", got)
	}
}

// TestAGuardClarificationKeepsItsReceiptAndReportsAMalformedMember: a guard
// clarification is decided by its own receipt whatever its member says, and
// a member that does not decode is reported, not dropped.
func TestAGuardClarificationKeepsItsReceiptAndReportsAMalformedMember(t *testing.T) {
	t.Parallel()
	guard := guardIssuedStored("result_parent", "result_parent", true)
	guard.SemanticStateRead = SemanticStateReadAvailable
	guard.SemanticState = &PersistedSemanticState{Extensions: SemanticStateExtensions{carriedParentIdentityExtension: json.RawMessage(`{"state":"identity_held","depth":0}`)}}
	base := parentIdentityOf(parentAnchorEvidence{Referenced: true, Loaded: true}, guard, "result_clarification")
	got := (&Engine{}).chainIdentityOf(context.Background(), acceptancePrincipal(), base, guard)
	if got.Chain != SubjectSubstitutionChainGuardReceipt || got.ChainError != SubjectSubstitutionChainErrorMalformedMember || got.ChainDepth != 1 {
		t.Errorf("chain = %q (%q) depth %d, want guard_receipt reporting a malformed member at depth 1", got.Chain, got.ChainError, got.ChainDepth)
	}
	if !sameSubjectIdentity(got.Subject, substitutionRepoOne) || got.ResultID != "result_parent" || got.Carried {
		t.Errorf("evidence = %+v, want the receipt-proven parent", got)
	}
}

// TestSemanticStateExtensionRefusesAnUnencodableValue: a value that does not
// encode is refused, never written as something else.
func TestSemanticStateExtensionRefusesAnUnencodableValue(t *testing.T) {
	t.Parallel()
	if _, err := withSemanticStateExtension(&PersistedSemanticState{}, carriedParentIdentityExtension, make(chan int)); !errors.Is(err, ErrSemanticStateRejected) {
		t.Errorf("err = %v, want a rejection", err)
	}
}
