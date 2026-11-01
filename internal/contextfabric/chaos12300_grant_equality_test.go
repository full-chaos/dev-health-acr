package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12300: every read of a stored result records the grant-equality
// result: the class of the digest the result was computed under and whether
// the reader's own digest equals it. A different grant of the same class and
// size (here {P-1,P-2} against {P-1}) reads as unequal; the R2-4 read of a
// p: result by an unrestricted reader reads as admitted AND unequal.
func TestEveryStoredResultReadRecordsTheGrantEqualityResult(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project.v2:github:P-1", Label: "P-1"}
	sameSize := storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-1", "project.v2:github:P-3"}}
	for _, tc := range []struct {
		name      string
		reader    storage.Principal
		digest    string
		wantClass GrantDigestClass
		wantEqual bool
	}{
		{"identical project grant", wideProjectGrant, StoredResultGrantDigest(wideProjectGrant), GrantDigestProject, true},
		{"other project grant, same size", sameSize, StoredResultGrantDigest(wideProjectGrant), GrantDigestProject, false},
		{"narrower project grant", narrowProjectGrant, StoredResultGrantDigest(wideProjectGrant), GrantDigestProject, false},
		{"unrestricted reader, p: result (R2-4)", storage.Principal{OrgID: "org_reuse"}, StoredResultGrantDigest(wideProjectGrant), GrantDigestProject, false},
		{"restricted reader, own g: result", storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/a"}}, StoredResultGrantDigest(storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/a"}}), GrantDigestRepository, true},
		{"unrecorded digest", wideProjectGrant, "", GrantDigestUnrecorded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
			decision := NewStoredResultGate(graph).Authorize(context.Background(), tc.reader, StoredInvestigationResult{Result: gateResult(project), GrantDigest: tc.digest}, StoredResultSurfacePriorResult)
			if !decision.StoredDigestRead || decision.StoredGrantDigestClass != tc.wantClass || decision.GrantDigestEqual != tc.wantEqual {
				t.Fatalf("read=%v class=%q equal=%v, want read=true class=%q equal=%v", decision.StoredDigestRead, decision.StoredGrantDigestClass, decision.GrantDigestEqual, tc.wantClass, tc.wantEqual)
			}
		})
	}
	// The fact-root recheck decides without a stored result: no equality
	// result is claimed for it.
	graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
	if decision := NewStoredResultGate(graph).decide(context.Background(), wideProjectGrant, gateResult(project), StoredResultSurfaceFactRootRecheck); decision.StoredDigestRead {
		t.Fatal("a recheck with no stored result claims a grant-equality result")
	}
}

func TestGrantDigestClassOfNamesEachForm(t *testing.T) {
	for digest, want := range map[string]GrantDigestClass{
		"": GrantDigestUnrecorded, "open": GrantDigestOpen, "g:0123": GrantDigestRepository,
		"p:0123": GrantDigestProject, "x:1": GrantDigestMalformed, "OPEN": GrantDigestMalformed,
	} {
		if got := GrantDigestClassOf(digest); got != want {
			t.Errorf("GrantDigestClassOf(%q) = %q, want %q", digest, got, want)
		}
	}
	if got := GrantDigestClassOf(StoredResultGrantDigest(wideProjectGrant)); got != GrantDigestProject {
		t.Errorf("a project grant's digest class = %q", got)
	}
}
