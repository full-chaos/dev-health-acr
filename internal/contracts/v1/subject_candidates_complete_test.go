package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func wholeSetCandidate(id, label string) ContextFabricSubjectCandidate {
	return ContextFabricSubjectCandidate{
		ReceiptID: "subr_" + id, Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project:" + id, Label: label},
		State: ContextFabricResolutionAmbiguous, Confidence: 0.9, MatchReasons: []string{"label match"},
	}
}

func TestCandidateSetProvenWholeNeedsTheSameMembers(t *testing.T) {
	alpha, beta, gamma := wholeSetCandidate("alpha", "Alpha"), wholeSetCandidate("beta", "Beta"), wholeSetCandidate("gamma", "Gamma")
	proof := ContextFabricSubjectRefsOf([]ContextFabricSubjectCandidate{alpha, beta})
	relabeled := beta
	relabeled.Subject.Label = "Beta (renamed)"
	cases := []struct {
		name       string
		resolution ContextFabricSubjectResolution
		want       bool
	}{
		{"same members", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, beta}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, true},
		{"same members reordered", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{beta, alpha}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, true},
		{"label changed after the proof", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, relabeled}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, true},
		{"no proof", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, beta}, Committed: []ContextFabricSubjectRef{}}, false},
		{"candidate added after the proof", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, beta, gamma}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, false},
		{"candidate dropped after the proof", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, false},
		{"candidate swapped after the proof", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, gamma}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, false},
		{"duplicate candidate", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, alpha}, Committed: []ContextFabricSubjectRef{}, WholeCandidates: proof}, false},
		{"something committed", ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{alpha, beta}, Committed: []ContextFabricSubjectRef{alpha.Subject}, WholeCandidates: proof}, false},
	}
	for _, tc := range cases {
		if got := tc.resolution.CandidateSetProvenWhole(); got != tc.want {
			t.Errorf("%s: CandidateSetProvenWhole() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if refs := ContextFabricSubjectRefsOf(nil); refs == nil || len(refs) != 0 {
		t.Errorf("ContextFabricSubjectRefsOf(nil) = %#v, want an empty, non-nil slice", refs)
	}
}

func TestWholeCandidatesNeverLeaveTheProcessInABody(t *testing.T) {
	alpha, beta := wholeSetCandidate("alpha", "Alpha"), wholeSetCandidate("beta", "Beta")
	resolution := ContextFabricSubjectResolution{
		Candidates: []ContextFabricSubjectCandidate{alpha, beta}, Committed: []ContextFabricSubjectRef{},
		CandidatesUncut: ContextFabricSubjectRefsOf([]ContextFabricSubjectCandidate{alpha, beta}),
		WholeCandidates: ContextFabricSubjectRefsOf([]ContextFabricSubjectCandidate{alpha, beta}),
	}
	if !resolution.CandidateSetProvenWhole() {
		t.Fatal("fixture defect: the resolution is not proven whole")
	}
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"uncut", "whole", "complete"} {
		if strings.Contains(strings.ToLower(string(encoded)), word) {
			t.Errorf("the encoded resolution names %q: %s", word, encoded)
		}
	}
	var decoded ContextFabricSubjectResolution
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CandidateSetProvenWhole() {
		t.Error("a decoded resolution is proven whole; a stored or reused result must never carry the claim")
	}
}
