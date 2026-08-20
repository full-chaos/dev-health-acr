package structurepriorcuration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCurate_ExcludesFrozenCorpusQuestionHashes is the HARD REQUIREMENT pin
// (this repository's own mission acceptance row, and design brief §5's
// "frozen questions NEVER in training data (QuestionHash exclusion verified
// in curation tests)"): a selection event whose QuestionHash names a frozen
// corpus question is excluded from curation's output ENTIRELY, regardless
// of how strong its support otherwise is -- even a unanimous human_panel
// vote on a frozen question must never surface as a candidate prior.
func TestCurate_ExcludesFrozenCorpusQuestionHashes(t *testing.T) {
	t.Parallel()

	frozenHash := "frozen-question-hash-0001"
	liveHash := "live-question-hash-0001"
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: frozenHash, Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
		{OrgID: "org-1", QuestionHash: liveHash, Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, map[string]bool{frozenHash: true})

	require.Len(t, entries, 1, "only the live question's entry may survive")
	require.Equal(t, liveHash, entries[0].QuestionHash)
	for _, e := range entries {
		require.NotEqual(t, frozenHash, e.QuestionHash, "a frozen-corpus QuestionHash must NEVER appear in curated output")
	}
}

// TestCurate_ExcludesFrozenCorpusQuestionHashes_EvenWithOverwhelmingSupport
// is the same pin with support strong enough that a buggy "exclude only if
// support is weak" implementation would still promote it -- proving the
// exclusion runs BEFORE aggregation, unconditionally.
func TestCurate_ExcludesFrozenCorpusQuestionHashes_EvenWithOverwhelmingSupport(t *testing.T) {
	t.Parallel()
	frozenHash := "frozen-question-hash-0002"
	var events []SelectionEvent
	for i := 0; i < 50; i++ {
		events = append(events, SelectionEvent{OrgID: "org-1", QuestionHash: frozenHash, Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"})
	}
	entries := Curate(events, map[string]bool{frozenHash: true})
	require.Empty(t, entries, "50 unanimous human_panel votes on a frozen question must still promote NOTHING")
}

func TestCurate_HumanPanelSupport_Promotes(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, nil)
	require.Len(t, entries, 1)
	require.Equal(t, "pull_request", entries[0].Value)
	require.Equal(t, 1, entries[0].SupportHumanPanel)
	require.Equal(t, 0, entries[0].SupportConsensus, "SupportConsensus stays honestly zero -- P6/ConsensusEvidence has not landed")
}

// TestCurate_AgentOnlySupport_NeverPromotesAlone is design brief §3.2's own
// promotion rule pin: "single-model support alone can propose only at the
// lowest weight and never promotes by itself."
func TestCurate_AgentOnlySupport_NeverPromotesAlone(t *testing.T) {
	t.Parallel()
	var events []SelectionEvent
	for i := 0; i < 100; i++ {
		events = append(events, SelectionEvent{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "agent_receipt", SelectionProvenance: "credential_mcp"})
	}
	entries := Curate(events, nil)
	require.Empty(t, entries, "100 agent-only selections must never promote an entry by themselves")
}

func TestCurate_MixedSupport_HumanPanelUnlocksPromotion(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "agent_receipt", SelectionProvenance: "credential_mcp"},
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "agent_receipt", SelectionProvenance: "credential_mcp"},
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, nil)
	require.Len(t, entries, 1)
	require.Equal(t, 1, entries[0].SupportHumanPanel)
	require.Equal(t, 2, entries[0].SupportAgentReceipt)
}

func TestCurate_SubjectAnchorAndHandle_NeverPromoted_V1Scope(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: "q1", Member: "subject_anchor", SelectedAppliedValue: "canonical-id-1", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
		{OrgID: "org-1", QuestionHash: "q1", Member: "subject_handle", SelectedAppliedValue: "532", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, nil)
	require.Empty(t, entries, "v1 curation promotes expected_kind only -- see this package's own doc comment for the anchor/handle scope trim")
}

func TestCurate_InvalidKindValue_Excluded(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "not_a_real_kind", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, nil)
	require.Empty(t, entries, "a captured value outside the closed structure-offer kind set must never be promoted")
}

func TestCurate_Deterministic_SameInputSameOutput(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "work_item", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
		{OrgID: "org-1", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "work_item", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	a := Curate(events, nil)
	b := Curate(events, nil)
	require.Equal(t, a, b)
	require.Len(t, a, 2)
	require.Equal(t, "work_item", a[0].Value, "work_item has 2 supporters vs pull_request's 1 -- ranked first")
	require.Equal(t, 0, a[0].Rank)
	require.Equal(t, "pull_request", a[1].Value)
	require.Equal(t, 1, a[1].Rank)
}

func TestCurate_OrgIsolation(t *testing.T) {
	t.Parallel()
	events := []SelectionEvent{
		{OrgID: "org-a", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "pull_request", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
		{OrgID: "org-b", QuestionHash: "q1", Member: "expected_kind", SelectedAppliedValue: "work_item", SelectionMode: "human_panel", SelectionProvenance: "web_assertion"},
	}
	entries := Curate(events, nil)
	require.Len(t, entries, 2)
	for _, e := range entries {
		require.NotEmpty(t, e.EntryID)
	}
	require.NotEqual(t, entries[0].EntryID, entries[1].EntryID, "two different orgs' entries must never collide on EntryID")
}
