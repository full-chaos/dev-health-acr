package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// tiedAlphaNodes are lexical matches of the term "alpha" that tie, so no
// gate commits either and the resolution asks the caller to choose.
func tiedAlphaNodes(ids ...string) []CandidateNode {
	nodes := make([]CandidateNode, 0, len(ids))
	for _, id := range ids {
		node := candidateNode(contextfabric.SubjectProject, "project_"+id, "Alpha "+id, 0.8, "*")
		node.Mechanism = contextfabric.MatchLexical
		nodes = append(nodes, node)
	}
	return nodes
}

func resolveTiedAlpha(t *testing.T, backend *fakeGraphBackend, maxCandidates int) contextfabric.SubjectResolution {
	t.Helper()
	request := testRequest()
	request.Options.MaxSubjectCandidates = maxCandidates
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 0 || len(resolution.Candidates) < 2 {
		t.Fatalf("fixture defect: want an uncommitted choice of two or more, got committed=%d candidates=%d", len(resolution.Committed), len(resolution.Candidates))
	}
	return resolution
}

func TestResolveSubjectsProvesAWholeCandidateSet(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": tiedAlphaNodes("one", "two")}}
	resolution := resolveTiedAlpha(t, backend, 10)
	if !resolution.CandidateSetProvenWhole() {
		t.Fatalf("CandidateSetProvenWhole() = false for an untruncated, undegraded, uncut search; WholeCandidates = %#v", resolution.WholeCandidates)
	}
	if len(resolution.WholeCandidates) != 2 {
		t.Fatalf("WholeCandidates = %#v, want the two candidates", resolution.WholeCandidates)
	}
}

func TestResolveSubjectsNeverProvesACutCandidateSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		backend       *fakeGraphBackend
		maxCandidates int
	}{
		{"search truncated", &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": tiedAlphaNodes("one", "two")}, searchTruncated: true}, 10},
		{"retrieval degraded", &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": tiedAlphaNodes("one", "two")}, searchDegraded: true}, 10},
		{"candidate budget cut", &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": tiedAlphaNodes("one", "two", "three")}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolution := resolveTiedAlpha(t, tc.backend, tc.maxCandidates)
			if resolution.WholeCandidates != nil || resolution.CandidateSetProvenWhole() {
				t.Fatalf("WholeCandidates = %#v, want nil: the candidate set may be missing a subject", resolution.WholeCandidates)
			}
		})
	}
}

func TestResolveSubjectsNeverProvesACommittedResolution(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("fixture defect: committed = %#v", resolution.Committed)
	}
	if resolution.WholeCandidates != nil {
		t.Fatalf("WholeCandidates = %#v on a committed resolution, want nil", resolution.WholeCandidates)
	}
}
