package directread_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// upstreamCountingGraph counts every upstream read a lookup makes.
type upstreamCountingGraph struct {
	workItemGraph
	calls *int
}

func (g upstreamCountingGraph) ResolveInvestigationBinding(ctx context.Context, p storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	*g.calls++
	return g.workItemGraph.ResolveInvestigationBinding(ctx, p)
}

func (g upstreamCountingGraph) ListSubjectsByKind(ctx context.Context, p storage.Principal, b contextfabric.ResolvedGraphBinding, kind, after string, limit int) (directread.LookupPage, error) {
	*g.calls++
	return g.workItemGraph.ListSubjectsByKind(ctx, p, b, kind, after, limit)
}

// CHAOS-7200 (permanent): for a repository-restricted caller the
// authorization refusal (scope_required) precedes ANY existence-derived step.
// Whether a key PREFIX is registered is existence-derived, so a restricted
// caller must not tell a known prefix from an unknown one through ANY
// observable: reason, telemetry kind, or upstream calls, for an exact key or
// a key embedded in text alike. Org-wide and universal callers may see the
// prefix set (invalid_find_request on an unknown prefix). A malformed shape
// is invalid_find_request for everyone: shape is not existence.
func TestChaos7200_RestrictedHandleAnswerDoesNotRevealKeyPrefixSet(t *testing.T) {
	inside := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:inside"}
	authority := certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(inside): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
	}}
	var upstream int
	census := func(context.Context, string, graphrank.CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (graphrank.CensusOutcome, error) {
		upstream++
		return graphrank.CensusOutcome{}, nil
	}
	recorder := &capturingFindRecorder{}
	lookup := directread.NewSubjectLookup(upstreamCountingGraph{calls: &upstream}, directread.NewSubjectGate(authority, nil), recorder).
		WithOwnershipAndHandles(nil, census, workItemGraph{}).
		WithCensusAnchorSupport(devhealthsource.CensusAnchorSupported)
	principals := map[string]storage.Principal{
		"restricted": {OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/inside-repo"}},
		"orgwide":    {OrgID: "org_1", Subject: "u", CredentialID: "c"},
		"universal":  {OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"*"}},
	}

	const (
		scope   = "scope_required"
		invalid = "invalid_find_request"
		ok      = "ok"
	)
	cases := []struct {
		name, handle              string
		restricted, orgwide, univ string
	}{
		{"exact known prefix, key present", "CHAOS-4322", scope, ok, ok},
		{"exact known prefix, key missing", "CHAOS-999999", scope, ok, ok},
		{"exact unknown prefix", "ZZZ-99999", scope, invalid, invalid},
		{"exact unknown prefix, short", "ABC-123", scope, invalid, invalid},
		{"embedded known prefix", "please inspect CHAOS-4322", scope, ok, ok},
		{"embedded unknown prefix", "please inspect ZZZ-4322", scope, invalid, invalid},
		{"two known keys", "CHAOS-1 CHAOS-2", scope, invalid, invalid},
		{"two unknown keys", "ZZZ-1 ZZZ-2", scope, invalid, invalid},
		{"malformed", "not a handle", invalid, invalid, invalid},
		{"malformed key shape", "ZZZ-abc", invalid, invalid, invalid},
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
			for who, want := range map[string]string{"restricted": tc.restricted, "orgwide": tc.orgwide, "universal": tc.univ} {
				upstream = 0
				recorder.calls = nil
				_, err := lookup.Find(context.Background(), principals[who], directread.FindRequest{Handle: tc.handle})
				if got := reasonOf(err); got != want {
					t.Errorf("%s %q: reason %s, want %s (err %v)", who, tc.handle, got, want, err)
				}
				if who != "restricted" || want != scope {
					continue
				}
				// Uniform refusal: same message, no upstream call, same
				// telemetry kind and class as any other work-item handle.
				if upstream != 0 {
					t.Errorf("restricted %q made %d upstream calls; the refusal must precede any", tc.handle, upstream)
				}
				if len(recorder.calls) != 1 || recorder.calls[0].ErrorClass != "scope_required" ||
					recorder.calls[0].Mode != "handle" || !slices.Equal(recorder.calls[0].Kinds, []string{"work_item"}) {
					t.Errorf("restricted %q telemetry = %+v, want one handle/work_item/scope_required record", tc.handle, recorder.calls)
				}
				if msg := err.Error(); msg != "find_subjects: invalid_request: find_subjects: scope_required: work_item handles cannot be looked up inside a repository grant" {
					t.Errorf("restricted %q message differs across prefixes: %q", tc.handle, msg)
				}
			}
		})
	}
}
