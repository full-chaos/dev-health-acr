package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// replayingStore keeps one snapshot per result id and refuses a second Save
// whose snapshot differs -- the rule both shipped stores apply, proven on
// each of them by RunSemanticStateExtensionReplaySuite.
type replayingStore struct {
	*staticResultStore
	saved map[string]*PersistedSemanticState
}

func (s *replayingStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, watermark SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, identity ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parentResultID string, semantic SemanticStateWrite) error {
	stored, ok := s.saved[result.ResultID]
	if ok && !SemanticStatesEqual(stored, semantic.State) {
		return ErrSemanticStateReplayConflict
	}
	if ok {
		return nil
	}
	if s.saved == nil {
		s.saved = map[string]*PersistedSemanticState{}
	}
	s.saved[result.ResultID] = cloneSemanticState(semantic.State)
	return nil
}

// TestTheShadowBindingNeverDecidesASave: a Save that the served path would
// have made succeeds whatever the shadow decided. Replay equality never
// compares the binding member, so a second Save of one result id with a
// different binding replays cleanly and the stored binding stays the one the
// first Save wrote; a difference in the READING is still a conflict.
func TestTheShadowBindingNeverDecidesASave(t *testing.T) {
	state := func() *PersistedSemanticState {
		return BuildSemanticState(SemanticStateInput{
			Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
			FamilyVersion:   QuestionFamilyTableVersion,
			RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		})
	}
	newTracker := func(binding AnchorBinding) *anchorBindingTracker {
		return &anchorBindingTracker{parent: anchorBindingParent{ResultID: "result_parent", Status: AnchorBindingParentPresent, Binding: binding}, evaluation: AnchorBindingEvaluationNotResolved, epoch: 7}
	}
	result := InvestigationResult{ResultID: "result_two_saves"}
	for _, tc := range []struct {
		name   string
		second *anchorBindingTracker
	}{
		{"the same binding", newTracker(heldBinding(AnchorBindingBound, bindAlpha))},
		{"a different binding", newTracker(heldBinding(AnchorBindingBound, bindBeta))},
		{"no binding at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &replayingStore{staticResultStore: &staticResultStore{results: map[string]InvestigationResult{}}}
			lines := &unrecordedTelemetry{recordingTelemetry: &recordingTelemetry{}}
			engine := &Engine{results: store, telemetry: lines}
			first := semanticStateCapture{Write: SemanticStateOf(state())}.withAnchorShadow(newTracker(heldBinding(AnchorBindingBound, bindAlpha)))
			if err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, result, nil, nil, "", 0, "", first); err != nil {
				t.Fatalf("first save: %v", err)
			}
			second := semanticStateCapture{Write: SemanticStateOf(state())}
			if tc.second != nil {
				second = second.withAnchorShadow(tc.second)
			}
			err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertStructureVeto, result, nil, nil, "", 0, "", second)
			stored := bindingMember(store.saved[result.ResultID])
			t.Logf("%-22s second save err=%v stored=%s", tc.name, err, stored.CanonicalID)
			if err != nil {
				t.Fatalf("the shadow failed a Save the served path would have made: %v", err)
			}
			if stored == nil || stored.CanonicalID != bindAlpha.ID {
				t.Fatalf("stored binding = %+v, want the first Save's", stored)
			}
		})
	}

	// A conflict the shadow did not cause is still a failed Save.
	store := &replayingStore{staticResultStore: &staticResultStore{results: map[string]InvestigationResult{}}}
	engine := &Engine{results: store, telemetry: &unrecordedTelemetry{recordingTelemetry: &recordingTelemetry{}}}
	tracker := newTracker(heldBinding(AnchorBindingBound, bindAlpha))
	if err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, result, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state())}.withAnchorShadow(tracker)); err != nil {
		t.Fatalf("first save: %v", err)
	}
	other := state()
	other.Family = QuestionFamilyScopedCohortStatus
	if err := engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertStructureVeto, result, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(other)}.withAnchorShadow(tracker)); !errors.Is(err, ErrSemanticStateReplayConflict) {
		t.Fatalf("a reading conflict = %v, want ErrSemanticStateReplayConflict", err)
	}
}

// TestNoSaveExitStoresOneResultTwice enumerates every save exit through the
// real engine and reports, per result id, how many Saves reached the store
// and how many stored a snapshot. No exit stores one result id twice, so the
// exclusion above is the invariant that keeps it that way, never a repair of
// a reachable failure.
func TestNoSaveExitStoresOneResultTwice(t *testing.T) {
	seen := map[BudgetAssertStage]bool{}
	for _, scenario := range anchorSiteScenarios() {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			out := anchorSiteArm(t, scenario, false, nil)
			saves, persisted := map[string]int{}, map[string]int{}
			for _, event := range out.rec.semanticStatePersistences {
				saves[event.ResultID]++
				if event.Decision == SemanticStatePersisted {
					persisted[event.ResultID]++
				}
			}
			for id, count := range saves {
				t.Logf("%-48s result %s: saves=%d stored=%d", scenario.name, id, count, persisted[id])
				if persisted[id] > 1 {
					t.Errorf("result %s stored %d times at the %s exit", id, persisted[id], scenario.site)
				}
			}
			seen[scenario.site] = true
		})
	}
	for _, stage := range BudgetAssertStageVocabulary() {
		if !seen[stage] {
			t.Errorf("no scenario drove the %q exit", stage)
		}
	}
}

// TestTheShadowIsSafeWithoutATracker: every reader of a turn's tracker holds
// a pointer that is nil while the shadow is off, so none of them may fault.
func TestTheShadowIsSafeWithoutATracker(t *testing.T) {
	var off *anchorBindingTracker
	off.observeReceipt(nil)
	off.observeReading(QuestionFamilyOutcome{}, nil)
	off.observeResolution(AnchorBindingEvaluationResolved, SubjectResolution{}, nil)
	off.observeServedCount(CountPopulationScope{})
	off.observeAnchorVeto(nil)
	binding, event := off.decide(BudgetAssertDecisive, InvestigationResult{ResultID: "result_off"}, nil)
	if binding != (AnchorBinding{}) || event.To.Reason != AnchorBindingReasonUnrecorded {
		t.Fatalf("a decision without a tracker: %+v / %+v", binding, event.To)
	}
	// And a decision with a tracker but no snapshot reads no served ledger.
	tracker := &anchorBindingTracker{parent: anchorBindingParent{Status: AnchorBindingParentNoReference}}
	if _, line := tracker.decide(BudgetAssertDecisive, InvestigationResult{ResultID: "result_nostate"}, nil); line.ServedAnchor != (anchorRef{}) || line.Agreement != AnchorBindingNotEvaluated {
		t.Fatalf("a Save with no snapshot: served=%+v agreement=%s", line.ServedAnchor, line.Agreement)
	}
}

// TestTheShadowMemberListIsClosed: replay equality ignores exactly the
// declared shadow members and nothing else, and every declared member is one
// no served path reads -- a snapshot that differs only there replays as the
// same snapshot and decides no continuation.
func TestTheShadowMemberListIsClosed(t *testing.T) {
	if len(semanticStateShadowMembers) != 1 || !semanticStateShadowMembers[anchorBindingExtension] {
		t.Fatalf("shadow members = %v, want exactly the anchor binding member", semanticStateShadowMembers)
	}
	for name := range semanticStateShadowMembers {
		carried, fresh := semanticFixture(t), semanticFixture(t)
		carried.Extensions = SemanticStateExtensions{name: json.RawMessage(`{"one":1}`)}
		fresh.Extensions = SemanticStateExtensions{name: json.RawMessage(`{"two":2}`)}
		if !SemanticStatesEqual(carried, fresh) {
			t.Errorf("member %q decides replay equality", name)
		}
		for field, differs := range semanticStateDifferences(carried, fresh) {
			if differs {
				t.Errorf("member %q decides the continuation on %s", name, field)
			}
		}
	}
	// A member outside the list is compared, so the list cannot grow silently.
	a, b := semanticFixture(t), semanticFixture(t)
	a.Extensions = SemanticStateExtensions{"served_member": json.RawMessage(`1`)}
	b.Extensions = SemanticStateExtensions{"served_member": json.RawMessage(`2`)}
	if SemanticStatesEqual(a, b) {
		t.Fatalf("a member outside the shadow list is not compared")
	}
}

// TestAnEnvelopeOnlyReaderReadsABindingRow: a binary that knows the envelope
// and nothing about this member reads a row this engine wrote, keeps the rest
// of the reading, and carries the member unread.
func TestAnEnvelopeOnlyReaderReadsABindingRow(t *testing.T) {
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	written, err := withAnchorBindingMember(state, heldBinding(AnchorBindingBound, bindAlpha))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	column, err := EncodeSemanticState(written)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The envelope reader: DecodeSemanticState knows the member only as raw
	// JSON, exactly as a binary without this change does.
	decoded, status := DecodeSemanticState(column)
	if status != SemanticStateReadAvailable {
		t.Fatalf("read = %s, want available", status)
	}
	raw, ok := decoded.Extensions[anchorBindingExtension]
	if !ok || !json.Valid(raw) {
		t.Fatalf("the member did not come back: %s", raw)
	}
	rest := *decoded
	rest.Extensions = nil
	plain, err := EncodeSemanticState(&rest)
	if err != nil {
		t.Fatalf("encode the rest: %v", err)
	}
	original, err := EncodeSemanticState(state)
	if err != nil || string(plain) != string(original) {
		t.Fatalf("the rest of the reading changed (err %v)", err)
	}
}
