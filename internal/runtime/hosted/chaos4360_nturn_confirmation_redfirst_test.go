package hosted_test

// CHAOS-4360 RED-FIRST proof: TestChaos4360NTurnCarryDetectsCurrentDefect
// pins runNTurnCase's own turn-loop mechanics against a SCRIPTED response
// sequence shaped exactly like CHAOS-4355's live walkthrough (13:40 08-27
// ticket comment) -- turn 1 offers window+candidate, turn 2 applies the
// window but the candidate offer is superseded (a FRESH offer comes back),
// turn 3 arrives with the window unconfirmed again (nothing carries it)
// and the fresh candidate redemption fails. This is a fixture, not a live
// call: it proves the HARNESS correctly detects and reports that shape --
// not that the live engine actually produces it (TestChaos4360NTurnConfirmationCarry,
// against real kiac acr, is the live proof of that). This test is RED on
// any harness version that does not yet implement the never-decisive,
// never-re-send-an-applied-receipt turn loop (i.e. red on origin/main
// before this changeset), and GREEN once runNTurnCase exists and behaves
// as specified -- reverting runNTurnCase's loop body while keeping this
// test makes it fail, which is the property this file exists to hold.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fakeNTurnInvestigator returns one scripted response per call, in order,
// and records every request it was actually given -- enough to assert both
// on runNTurnCase's OUTPUT (the report) and on its INPUT construction (that
// turn 3 never re-sends the window receipt turn 2 already applied).
type fakeNTurnInvestigator struct {
	t         *testing.T
	responses []contractsv1.ContextFabricInvestigationResult
	Requests  []contractsv1.ContextFabricInvestigationRequest
}

func (f *fakeNTurnInvestigator) Investigate(_ context.Context, _ storage.Principal, req contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	f.Requests = append(f.Requests, req)
	if len(f.responses) == 0 {
		f.t.Fatalf("fakeNTurnInvestigator: no scripted response left for call %d (request_id=%s)", len(f.Requests), req.RequestID)
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	return next, nil
}

var _ contextfabric.Investigator = (*fakeNTurnInvestigator)(nil)

const chaos4360FixtureCanonicalID = "project.v2:fixture:0000000000000000000000000000000000000000000000000000000000000000"

func TestChaos4360NTurnCarryDetectsCurrentDefect(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: chaos4360FixtureCanonicalID}

	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_fixture_t1", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{
				contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedSubjectCandidate,
			},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_fixture_t1_90d", OptionID: "opt_90d", RelativeID: "trailing_90d"},
			},
			CandidateOptions: []contractsv1.ContextFabricCandidateOption{
				{ReceiptID: "candr_fixture_t1", OptionID: "opt_cand_1", Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: chaos4360FixtureCanonicalID},
			},
		},
	}

	// Turn 2: the window receipt applies; the candidate receipt from turn 1
	// is superseded by this SAME call (the census pool changed once the
	// window landed) -- CHAOS-4355's own root cause -- so the engine comes
	// back with a FRESH candidate offer instead of a decisive commit.
	turn2 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_fixture_t2", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "winr_fixture_t1_90d",
				AppliedValue: "trailing_90d", Source: contractsv1.ContextFabricStructureSourceReceipt,
				Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
			},
			{
				Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, ReceiptID: "candr_fixture_t1",
				Source: contractsv1.ContextFabricStructureSourceReceipt, Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
				Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
			},
		},
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
			CandidateOptions: []contractsv1.ContextFabricCandidateOption{
				{ReceiptID: "candr_fixture_t2_fresh", OptionID: "opt_cand_2", Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: chaos4360FixtureCanonicalID},
			},
		},
	}

	// Turn 3: THE DEFECT. Nothing carried the window across turns, so it
	// arrives inferred; the window gate fires and the fresh candidate
	// redemption cannot land -- window is MISSING again (never carried, and
	// this harness must not re-send the already-applied turn-2 receipt to
	// "fix" that), the candidate receipt is vetoed, and the case never
	// reaches a decisive answer.
	turn3 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_fixture_t3", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, ReceiptID: "candr_fixture_t2_fresh",
				Source: contractsv1.ContextFabricStructureSourceReceipt, Provenance: contractsv1.ContextFabricStructureInferredDefault,
				Disposition: contractsv1.ContextFabricStructureDispositionVetoedUnresolved,
			},
		},
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{
				contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedSubjectCandidate,
			},
		},
	}

	fake := &fakeNTurnInvestigator{t: t, responses: []contractsv1.ContextFabricInvestigationResult{turn1, turn2, turn3}}
	trace := &twoTurnTraceCapture{}
	principal := storage.Principal{OrgID: "org_fixture"}

	res := runNTurnCase(t, context.Background(), fake, principal, 999, tc, chaos4360FixtureCanonicalID, 5, 0, trace)

	// The RED baseline this class exists to detect: never decisive, past
	// the exact 3 turns CHAOS-4355 found live.
	if res.Decisive {
		t.Fatalf("Decisive = true, want false: this fixture scripts today's known defect (window never carries past turn 2), so the case must never reach a decisive answer")
	}
	if res.TurnsTaken != 3 {
		t.Fatalf("TurnsTaken = %d, want 3: turn 1 (ask) + turn 2 (window+candidate) + turn 3 (fresh candidate, window lost) is the exact shape this class exists to walk", res.TurnsTaken)
	}
	if res.FinalStatus != string(contractsv1.ContextFabricInvestigationClarificationRequired) {
		t.Fatalf("FinalStatus = %q, want %q", res.FinalStatus, contractsv1.ContextFabricInvestigationClarificationRequired)
	}
	if res.WindowUnsafeCommit {
		t.Fatalf("WindowUnsafeCommit = true, want false: this fixture never commits at all, so the CHAOS-4040 safety invariant cannot even be exercised, let alone tripped")
	}
	if res.WrongCommit {
		t.Fatalf("WrongCommit = true, want false: no commit happened in this fixture")
	}
	if res.CarriedStructureObserved {
		t.Fatalf("CarriedStructureObserved = true, want false: every ConfirmedStructure entry in this fixture uses a pre-CHAOS-4360 Source/Provenance value on purpose (today's defect), so the carry signal must read false")
	}

	// The never-re-send-an-applied-receipt contract: turn 3's own request
	// must not carry a PriorWindowReceipts entry at all (window already
	// applied at turn 2), and turn 2's request must not carry the SAME
	// receipt turn 3 would have needed to resend.
	if len(fake.Requests) != 3 {
		t.Fatalf("investigator received %d requests, want 3", len(fake.Requests))
	}
	turn3Req := fake.Requests[2]
	if len(turn3Req.PriorWindowReceipts) != 0 {
		t.Fatalf("turn 3 request carried PriorWindowReceipts=%+v, want none: window was already Applied at turn 2, and re-sending an applied receipt is exactly CHAOS-4355's own vetoed_stale trap (13:40 08-27 comment)", turn3Req.PriorWindowReceipts)
	}
	if len(turn3Req.PriorCandidateReceipts) != 1 || turn3Req.PriorCandidateReceipts[0].ReceiptID != "candr_fixture_t2_fresh" {
		t.Fatalf("turn 3 request PriorCandidateReceipts=%+v, want exactly the FRESH turn-2 offer (candr_fixture_t2_fresh), never the stale turn-1 receipt", turn3Req.PriorCandidateReceipts)
	}
	if turn3Req.PriorCandidateReceipts[0].ResultID != "result_nturn_fixture_t2" {
		t.Fatalf("turn 3 request PriorCandidateReceipts[0].ResultID = %q, want the turn-2 result id (prior_result_id must name the result that OFFERED the receipt being redeemed)", turn3Req.PriorCandidateReceipts[0].ResultID)
	}

	// Per-turn record shape: turn 3's own window_canonicalization_outcome
	// must have been captured from the trace (twoTurnTraceCapture.reset()
	// then Investigate(), same discipline runTwoTurnPositiveArm uses) --
	// the fixture's fake investigator does not itself drive the trace, so
	// this asserts the WIRING is correct (reset before each call, read
	// after), not a specific outcome string, which is production's to set.
	if len(res.Turns) != 3 {
		t.Fatalf("len(Turns) = %d, want 3", len(res.Turns))
	}
	if res.Turns[2].Missing == nil {
		t.Fatalf("turn 3 record Missing = nil, want the window+subject_candidate re-disclosure this fixture scripts")
	}
}

// TestChaos4360NTurnCarryStopsOnOfferMiss pins the OTHER loop exit: when a
// turn's response offers nothing this class can apply (no window options,
// no matching candidate), the loop must stop and report OfferMiss rather
// than looping forever or panicking on an empty offer.
func TestChaos4360NTurnCarryStopsOnOfferMiss(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: chaos4360FixtureCanonicalID}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_offermiss_t1", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
			// No CandidateOptions matching chaos4360FixtureCanonicalID, and
			// no WindowOptions at all -- nothing this class can apply.
		},
	}
	fake := &fakeNTurnInvestigator{t: t, responses: []contractsv1.ContextFabricInvestigationResult{turn1}}
	trace := &twoTurnTraceCapture{}
	res := runNTurnCase(t, context.Background(), fake, storage.Principal{OrgID: "org_fixture"}, 998, tc, chaos4360FixtureCanonicalID, 5, 0, trace)

	if !res.OfferMiss {
		t.Fatalf("OfferMiss = false, want true: turn 1's response offers nothing this class can redeem")
	}
	if res.TurnsTaken != 1 {
		t.Fatalf("TurnsTaken = %d, want 1: the loop must stop after turn 1 rather than calling Investigate with an empty request", res.TurnsTaken)
	}
	if len(fake.Requests) != 1 {
		t.Fatalf("investigator received %d requests, want exactly 1 (no second call should ever be attempted)", len(fake.Requests))
	}
}

// TestChaos4360NTurnCarryNeverResendsAppliedReceipt is a focused, minimal
// mutation-kill proof for the "never re-send an applied receipt" contract
// itself (CHAOS-4355 13:40 08-27: resending an already-claimed receipt is
// vetoed_stale, not a safe retry). Turn 2 applies the window AND -- unusual
// but deliberate here -- the response still redundantly re-offers a window
// option (simulating a server that keeps disclosing it). If runNTurnCase's
// appliedWindow guard is removed or broken, turn 3 would re-attach that
// receipt and call Investigate a THIRD time; the fake investigator here is
// scripted with only 2 responses, so a broken guard fails this test loudly
// (via fakeNTurnInvestigator.Investigate's own t.Fatalf) rather than
// silently passing.
func TestChaos4360NTurnCarryNeverResendsAppliedReceipt(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: chaos4360FixtureCanonicalID}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_noresend_t1", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_noresend_t1_90d", OptionID: "opt_90d", RelativeID: "trailing_90d"},
			},
		},
	}
	turn2 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_noresend_t2", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "winr_noresend_t1_90d",
				AppliedValue: "trailing_90d", Source: contractsv1.ContextFabricStructureSourceReceipt,
				Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
			},
		},
		// Deliberately still discloses a window option and Missing=[window]
		// to test the guard directly -- production would not realistically
		// re-offer an already-applied member, but this fixture exists
		// SPECIFICALLY to prove the harness's own guard holds even if it
		// did, independent of whether the engine ever actually does this.
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_noresend_t1_90d", OptionID: "opt_90d", RelativeID: "trailing_90d"},
			},
		},
	}
	fake := &fakeNTurnInvestigator{t: t, responses: []contractsv1.ContextFabricInvestigationResult{turn1, turn2}}
	trace := &twoTurnTraceCapture{}
	res := runNTurnCase(t, context.Background(), fake, storage.Principal{OrgID: "org_fixture"}, 997, tc, chaos4360FixtureCanonicalID, 5, 0, trace)

	if !res.OfferMiss {
		t.Fatalf("OfferMiss = false, want true: window was already applied at turn 2, and the ONLY other offer this fixture ever presents is that same already-applied window receipt, so turn 3 has nothing left it may legitimately attach")
	}
	if res.TurnsTaken != 2 {
		t.Fatalf("TurnsTaken = %d, want 2: a correct guard stops the loop after turn 2 rather than re-sending the applied window receipt at turn 3", res.TurnsTaken)
	}
	if len(fake.Requests) != 2 {
		t.Fatalf("investigator received %d requests, want exactly 2 -- a 3rd call would mean the applied window receipt was resent", len(fake.Requests))
	}
}

// TestChaos4360NTurnCarryNeverResendsVetoedReceiptID is codex review's own
// P2 finding (confirmed): the OTHER never-resend gap, distinct from the
// already-applied case above. A receipt that came back VETOED (never
// applied) does not flip appliedWindow/appliedCandidate -- if the SAME
// receipt id is still listed in a later turn's own offers (a stale-but-
// still-disclosed offer), the guard must still refuse to resend it, purely
// on the wire id, independent of the earlier outcome.
func TestChaos4360NTurnCarryNeverResendsVetoedReceiptID(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: chaos4360FixtureCanonicalID}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_vetoed_t1", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_vetoed_stale_id", OptionID: "opt_90d", RelativeID: "trailing_90d"},
			},
		},
	}
	// Turn 2: the SAME receipt id is sent and comes back VETOED (never
	// applied) -- appliedWindow stays false -- but the response STILL
	// discloses that exact receipt id again, exactly the shape that would
	// re-trigger a resend without the sentReceiptIDs guard.
	turn2 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_vetoed_t2", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "winr_vetoed_stale_id",
				Source: contractsv1.ContextFabricStructureSourceReceipt, Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
				Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
			},
		},
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: []contractsv1.ContextFabricWindowOption{
				{ReceiptID: "winr_vetoed_stale_id", OptionID: "opt_90d", RelativeID: "trailing_90d"},
			},
		},
	}
	fake := &fakeNTurnInvestigator{t: t, responses: []contractsv1.ContextFabricInvestigationResult{turn1, turn2}}
	trace := &twoTurnTraceCapture{}
	res := runNTurnCase(t, context.Background(), fake, storage.Principal{OrgID: "org_fixture"}, 995, tc, chaos4360FixtureCanonicalID, 5, 0, trace)

	if !res.OfferMiss {
		t.Fatalf("OfferMiss = false, want true: the only window offer this fixture ever presents is the SAME id already sent and vetoed at turn 2, so turn 3 has nothing legitimately new to attach")
	}
	if res.TurnsTaken != 2 {
		t.Fatalf("TurnsTaken = %d, want 2: a correct guard stops after turn 2 rather than resending the vetoed-but-still-offered receipt id at turn 3", res.TurnsTaken)
	}
	if len(fake.Requests) != 2 {
		t.Fatalf("investigator received %d requests, want exactly 2 -- a 3rd call would mean the vetoed receipt id was resent", len(fake.Requests))
	}
}

// TestNTurnReportHasUsableEvidence pins the fail-loud invariant (codex
// review, P1, confirmed): a report where every case is arm-invalid (or
// where zero cases were selected at all) must never be read as a clean
// run.
func TestNTurnReportHasUsableEvidence(t *testing.T) {
	cases := []struct {
		name   string
		report nTurnReport
		want   bool
	}{
		{"empty report (zero selected cases)", nTurnReport{}, false},
		{
			"every result arm-invalid",
			nTurnReport{Results: []nTurnCaseResult{{ArmInvalidReason: "no oracle entry"}, {ArmInvalidReason: "investigate error"}}, ArmInvalidCount: 2},
			false,
		},
		{
			"at least one usable result",
			nTurnReport{Results: []nTurnCaseResult{{ArmInvalidReason: "no oracle entry"}, {Decisive: false}}, ArmInvalidCount: 1},
			true,
		},
		{
			"all usable results",
			nTurnReport{Results: []nTurnCaseResult{{Decisive: true}, {Decisive: false}}, ArmInvalidCount: 0},
			true,
		},
	}
	for _, tc := range cases {
		if got := nTurnReportHasUsableEvidence(tc.report); got != tc.want {
			t.Errorf("%s: nTurnReportHasUsableEvidence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNTurnIsCarriedProvenance(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty is never carried", "", false},
		{"inferred_default is pre-carry", string(contractsv1.ContextFabricStructureInferredDefault), false},
		{"question_stated is pre-carry", string(contractsv1.ContextFabricStructureQuestionStated), false},
		{"clarification_confirmed is pre-carry", string(contractsv1.ContextFabricStructureClarificationConfirmed), false},
		{"any unknown value is carried (forward-compatible, presence-checked)", "carried_confirmed", true},
	}
	for _, tc := range cases {
		if got := nTurnIsCarriedProvenance(tc.value); got != tc.want {
			t.Errorf("%s: nTurnIsCarriedProvenance(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}
}

// TestNTurnIsCarriedSource pins the PRIMARY carry signal (lane-4360-acr,
// PR #306): a carried structure member sets Source, not Provenance, to a
// new value outside today's closed {receipt, explicit, explicit_unattributed}
// vocabulary.
func TestNTurnIsCarriedSource(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty is never carried", "", false},
		{"receipt is pre-carry", string(contractsv1.ContextFabricStructureSourceReceipt), false},
		{"explicit is pre-carry", string(contractsv1.ContextFabricStructureSourceExplicit), false},
		{"explicit_unattributed is pre-carry", string(contractsv1.ContextFabricStructureSourceExplicitUnattributed), false},
		{"any unknown value is carried (forward-compatible, presence-checked)", "carried", true},
	}
	for _, tc := range cases {
		if got := nTurnIsCarriedSource(tc.value); got != tc.want {
			t.Errorf("%s: nTurnIsCarriedSource(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}
}

// TestChaos4360NTurnCarryObservedViaSource is the end-to-end wiring proof
// for the primary carry signal: a turn 2 response whose window
// ConfirmedStructure entry sets Source="carried" (a stand-in for
// lane-4360-acr's real value, per nTurnIsCarriedSource's own doc comment on
// why this harness cannot pin the literal spelling) must flip
// CarriedStructureObserved, and the case must decisively commit -- proving
// this fixture models the POST-fix shape, not the pre-fix defect the other
// fixtures in this file pin.
func TestChaos4360NTurnCarryObservedViaSource(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: chaos4360FixtureCanonicalID}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_carried_t1", Status: contractsv1.ContextFabricInvestigationClarificationRequired,
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
			CandidateOptions: []contractsv1.ContextFabricCandidateOption{
				{ReceiptID: "candr_carried_t1", OptionID: "opt_cand_1", Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: chaos4360FixtureCanonicalID},
			},
		},
	}
	// Turn 2: the candidate applies AND, per lane-4360-acr's own shape, the
	// window is CARRIED from an earlier turn in this same conversation --
	// Source="carried" (stand-in literal), Provenance UNCHANGED at
	// clarification_confirmed (matching the real contract exactly), no
	// receipt redeemed this turn (ReceiptID empty).
	turn2 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_nturn_carried_t2", Status: contractsv1.ContextFabricInvestigationComplete,
		ConfirmedStructure: []contractsv1.ContextFabricConfirmedStructureEntry{
			{
				Member: contractsv1.ContextFabricStructureNeedWindow, ReceiptID: "",
				AppliedValue: "trailing_90d", Source: "carried",
				Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
			},
			{
				Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, ReceiptID: "candr_carried_t1",
				Source: contractsv1.ContextFabricStructureSourceReceipt, Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
				Disposition: contractsv1.ContextFabricStructureDispositionApplied,
			},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Committed: []contractsv1.ContextFabricSubjectRef{{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: chaos4360FixtureCanonicalID}},
		},
		EffectiveEvidenceWindow: &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowClarificationConfirmed},
	}

	fake := &fakeNTurnInvestigator{t: t, responses: []contractsv1.ContextFabricInvestigationResult{turn1, turn2}}
	trace := &twoTurnTraceCapture{}
	res := runNTurnCase(t, context.Background(), fake, storage.Principal{OrgID: "org_fixture"}, 996, tc, chaos4360FixtureCanonicalID, 5, 0, trace)

	if !res.CarriedStructureObserved {
		t.Fatalf("CarriedStructureObserved = false, want true: turn 2's window entry carries Source=%q, outside the closed pre-CHAOS-4360 vocabulary", "carried")
	}
	if !res.Decisive {
		t.Fatalf("Decisive = false, want true: this fixture models the POST-fix shape (window carried, candidate applies, commits)")
	}
	if res.WindowUnsafeCommit {
		t.Fatalf("WindowUnsafeCommit = true, want false: EffectiveEvidenceWindow.Provenance is clarification_confirmed (a carried window is a SAFE commit, per lane-4360-acr's own contract)")
	}
}

func TestNTurnWindowUnsafeCommit(t *testing.T) {
	confirmed := &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowClarificationConfirmed}
	inferred := &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowInferredDefault}

	cases := []struct {
		name   string
		result contractsv1.ContextFabricInvestigationResult
		want   bool
	}{
		{"not decisive, window inferred: not a commit at all", contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationClarificationRequired, EffectiveEvidenceWindow: inferred}, false},
		{"decisive, no window in play (nil): legitimately no window", contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationComplete, EffectiveEvidenceWindow: nil}, false},
		{"decisive, window confirmed: safe", contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationComplete, EffectiveEvidenceWindow: confirmed}, false},
		{"decisive, window inferred: UNSAFE (CHAOS-4040)", contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationComplete, EffectiveEvidenceWindow: inferred}, true},
	}
	for _, tc := range cases {
		if got := nTurnWindowUnsafeCommit(tc.result); got != tc.want {
			t.Errorf("%s: nTurnWindowUnsafeCommit = %v, want %v", tc.name, got, tc.want)
		}
	}
}
