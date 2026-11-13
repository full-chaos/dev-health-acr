package directread

import (
	"context"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// MaxGrantedRepositories bounds how many repositories a restricted caller's
// grant may reach before run_operation stops scoping a request to the WHOLE
// grant. Past it the grant listing is incomplete, and the runner refuses
// with scope_required (the caller names its repositories itself) rather
// than serve a partial grant as if it were the whole one.
//
// It is 256 (contractsv1.MaxRepositoryScopes, the most repositories one
// credential can carry), not larger: every restricted operation's forced
// repository variable allows at most that many items (operations.v1.json
// max_items, generated from this constant), and one subject gate decision
// takes at most MaxSubjectsPerRequest (256) subjects. A larger cap could only
// produce a request the gate or the policy refuses.
// TestGrantedRepositoriesCapFitsGateAndPolicy holds both bounds.
const MaxGrantedRepositories = contractsv1.MaxRepositoryScopes

// ErrGrantedRepositoriesIncomplete: the caller's grant reaches more
// repositories than MaxGrantedRepositories, or the graph listing was
// truncated. The runner answers scope_required, never a partial scope.
var ErrGrantedRepositoriesIncomplete = errors.New("granted repositories: the grant listing is incomplete")

// LookupGrantedRepositories is the production GrantedRepositories port. It
// turns a restricted caller's grant (repository SLUGS in
// principal.RepositoryScopes) into repository subjects ("repository:<uuid>")
// by listing kind=repository through the SubjectLookup, which admits every
// node through the S0 subject gate for this principal. Only admitted nodes
// are returned; the runner then passes every one through the gate AGAIN, in
// its own decision for the request, so this list is a candidate set, never
// a proof.
type LookupGrantedRepositories struct {
	lookup *SubjectLookup
}

// NewGrantedRepositories wraps a lookup. A nil lookup makes every call fail
// (the runner then answers unavailable, never an unscoped request).
func NewGrantedRepositories(lookup *SubjectLookup) *LookupGrantedRepositories {
	return &LookupGrantedRepositories{lookup: lookup}
}

// GrantedRepositories lists the admitted repositories of the caller's own
// organization graph, up to MaxGrantedRepositories. An incomplete listing
// is ErrGrantedRepositoriesIncomplete; a lookup failure is returned wrapped.
func (g *LookupGrantedRepositories) GrantedRepositories(ctx context.Context, principal storage.Principal) ([]contextfabric.SubjectRef, error) {
	if g == nil || g.lookup == nil {
		return nil, fmt.Errorf("%w: no subject lookup", ErrFindUnavailable)
	}
	var refs []contextfabric.SubjectRef
	cursor := ""
	// A page is at most MaxFindLimit (200), below the cap, so the loop
	// follows the cursor: all pages, or an error. A grant past the cap is
	// over it as soon as the listing holds more than MaxGrantedRepositories.
	for {
		response, err := g.lookup.Find(ctx, principal, FindRequest{Kind: SubjectKindRepository, Limit: MaxFindLimit, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if response.Population.Truncated {
			return nil, ErrGrantedRepositoriesIncomplete
		}
		for _, subject := range response.Subjects {
			if subject.Kind != SubjectKindRepository {
				continue
			}
			refs = append(refs, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: subject.CanonicalID})
		}
		if len(refs) > MaxGrantedRepositories {
			return nil, ErrGrantedRepositoriesIncomplete
		}
		if response.Page.Complete {
			return refs, nil
		}
		if len(refs) >= MaxGrantedRepositories || response.Page.NextCursor == "" || response.Page.NextCursor == cursor {
			return nil, ErrGrantedRepositoriesIncomplete
		}
		cursor = response.Page.NextCursor
	}
}
