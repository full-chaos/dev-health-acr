package graphrank

// An identity-proven carried anchor reaches resolution's own caller-hint
// exact-commit exit through a SubjectHint sourced from
// hintsource.EngineCommittedAnchorCarry -- the SAME channel a caller-
// explicit hint reaches, never a second, parallel notion of "prefer this
// candidate." This file pins that the new source is wired into that
// registry correctly: the hint short-circuits before any search runs, and
// the committed subject's own basis is proof, never a score comparison a
// same-kind decoy could win instead.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestEngineCommittedAnchorCarryHintShortCircuitsWithProvenBasis(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:carried", Label: "carried"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{
		Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: string(hintsource.EngineCommittedAnchorCarry),
	}}
	resolution, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps(), nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject || len(backend.searchCalls) != 0 {
		t.Fatalf("resolution = %#v searches = %#v, want the hint alone committed with no search", resolution, backend.searchCalls)
	}
	if basis := bases.For(subject); basis != contextfabric.CommitBasisCallerCanonicalID {
		t.Fatalf("basis = %q, want %q -- an identity-proven carry must commit on proof, never a score comparison a same-kind decoy could win", basis, contextfabric.CommitBasisCallerCanonicalID)
	}
}

// TestEngineCommittedAnchorCarryHintFailsClosedNeverBlocksSearch pins the
// SearchFallback half of the same registry entry: an exact-hint lookup that
// finds nothing for the carried id must not abandon the turn -- unlike the
// two identity-authorization sources, this turn still has its own anchor
// term to search for.
func TestEngineCommittedAnchorCarryHintFailsClosedNeverBlocksSearch(t *testing.T) {
	t.Parallel()
	term := "chaos"
	found := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:found-by-search", Label: "CHAOS"}
	backend := &fakeGraphBackend{
		exactHints:    map[string]CandidateNode{},
		searchResults: map[string][]CandidateNode{term: {candidateNode(found.Kind, found.CanonicalID, found.Label, 0.9, "*")}},
	}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{
		Kind: contextfabric.SubjectTeam, ID: "team:gone", Label: "gone", Source: string(hintsource.EngineCommittedAnchorCarry),
	}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(term), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchCalls) == 0 {
		t.Fatalf("resolution = %#v, want the term search to still have run: a carry the graph can no longer find must not block the turn's own search", resolution)
	}
}
