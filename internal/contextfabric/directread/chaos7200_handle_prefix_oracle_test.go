package directread_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7200 (permanent): for a repository-restricted caller the
// authorization refusal (scope_required) precedes ANY existence-derived
// validation. Whether a key PREFIX exists is existence-derived, so a
// restricted caller must not tell a known prefix from an unknown one.
// Org-wide callers may see the prefix set (invalid_find_request on an unknown
// prefix). A malformed shape is invalid_find_request for everyone: shape is
// not existence.
func TestChaos7200_RestrictedHandleAnswerDoesNotRevealKeyPrefixSet(t *testing.T) {
	inside := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:inside"}
	authority := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(inside): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
	}}
	empty := func(context.Context, string, graphrank.CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (graphrank.CensusOutcome, error) {
		return graphrank.CensusOutcome{}, nil
	}
	lookup := directread.NewSubjectLookup(workItemGraph{}, directread.NewSubjectGate(authority, nil), nil).
		WithOwnershipAndHandles(nil, empty, workItemGraph{}).
		WithCensusAnchorSupport(devhealthsource.CensusAnchorSupported)
	restricted := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/inside-repo"}}
	orgwide := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c"}

	const (
		scope   = "scope_required"
		invalid = "invalid_find_request"
		ok      = "ok"
	)
	cases := []struct {
		name, handle      string
		restrictedW, orgW string
	}{
		{"known prefix, existing shape", "CHAOS-4322", scope, ok},
		{"known prefix, missing key", "CHAOS-999999", scope, ok},
		{"unknown prefix", "ZZZ-99999", scope, invalid},
		{"unknown prefix, short", "ABC-123", scope, invalid},
		{"malformed", "not a handle", invalid, invalid},
		{"malformed key shape", "ZZZ-abc", invalid, invalid},
	}
	reasonOf := func(err error) string {
		switch {
		case err == nil:
			return ok
		case errors.Is(err, directread.ErrFindScopeRequired) && errors.Is(err, directread.ErrFindInvalidRequest):
			return scope
		case errors.Is(err, directread.ErrFindInvalidRequest):
			return invalid
		}
		return "other: " + err.Error()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for who, want := range map[string]string{"restricted": tc.restrictedW, "orgwide": tc.orgW} {
				p := restricted
				if who == "orgwide" {
					p = orgwide
				}
				_, err := lookup.Find(context.Background(), p, directread.FindRequest{Handle: tc.handle})
				if got := reasonOf(err); got != want {
					t.Errorf("%s %q: reason %s, want %s (err %v)", who, tc.handle, got, want, err)
				}
			}
		})
	}
}
