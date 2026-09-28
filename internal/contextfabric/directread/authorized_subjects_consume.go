package directread

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// consume applies the same checks FactReader.Read applies to a gate proof,
// for a direct read that is not a fact read (read_relationships): issued to
// this principal, under the request id ctx carries, not expired, holding at
// least one subject, and never presented before. The first call that passes
// spends the value, copies included.
func (a AuthorizedSubjects) consume(ctx context.Context, principal storage.Principal, now time.Time) error {
	if !a.IssuedTo(principal) || a.Len() == 0 || !a.issuedFor(ctx) {
		return ErrUngatedRead
	}
	if !now.Before(a.grant.expiresAt) {
		return ErrAuthorizationExpired
	}
	if !a.grant.spent.CompareAndSwap(false, true) {
		return ErrAuthorizationSpent
	}
	return nil
}
