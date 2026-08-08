package hosted

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/api"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrWritebackNotEnabledForOrg is returned for an org outside the
// configured design-partner cohort (CHAOS-3565). It intentionally carries
// no detail distinguishing "the cohort exists but you're not in it" from
// "writeback is off": episode_routes.go's writeEpisodeError maps any error
// that isn't one of its named cases (ErrNoPersistAccepted, ErrNotFound,
// ErrConflict, an "invalid episode:" validation error, or a context
// cancellation/timeout) to the exact same 503 upstream_unavailable a nil
// EpisodeCreator already produces -- so a non-cohort org sees precisely
// what it would see if writeback were disabled outright, never a signal
// that the feature exists.
var ErrWritebackNotEnabledForOrg = errors.New("episode writeback is not enabled for this organization")

// cohortScopedEpisodeCreator restricts episode Create to a configured
// design-partner cohort of org IDs. It is a pure decorator in front of an
// api.EpisodeCreator: it never changes what the wrapped creator (and, in
// turn, episode.Service's own authorizeWrite) already enforces -- scope,
// entitlement, repository, redaction, retention -- it only adds one more
// gate ahead of all of that. An empty cohort rejects every org: config.go's
// Validate already requires a non-empty cohort whenever
// ACR_ENABLE_EPISODE_WRITEBACK is set, but this stays fail-closed even if
// that invariant is ever bypassed by a caller other than Open.
type cohortScopedEpisodeCreator struct {
	next   api.EpisodeCreator
	cohort map[string]struct{}
}

func newCohortScopedEpisodeCreator(next api.EpisodeCreator, orgIDs []string) *cohortScopedEpisodeCreator {
	cohort := make(map[string]struct{}, len(orgIDs))
	for _, id := range orgIDs {
		cohort[id] = struct{}{}
	}
	return &cohortScopedEpisodeCreator{next: next, cohort: cohort}
}

func (c *cohortScopedEpisodeCreator) Create(ctx context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	if !c.EpisodeWritebackAllowed(principal.OrgID) {
		return contractsv1.AgentEpisode{}, false, ErrWritebackNotEnabledForOrg
	}
	return c.next.Create(ctx, principal, create)
}

// EpisodeWritebackAllowed implements api.EpisodeWritebackGate: it is the
// exact predicate Create enforces, exposed so handleCapabilities can decide
// whether to advertise record_episode without guessing at (or drifting
// from) what a subsequent write would actually do (review finding H1).
func (c *cohortScopedEpisodeCreator) EpisodeWritebackAllowed(orgID string) bool {
	_, ok := c.cohort[orgID]
	return ok
}

var _ api.EpisodeWritebackGate = (*cohortScopedEpisodeCreator)(nil)
