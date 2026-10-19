package falkorgraph

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	fulltextReadyPollInterval = 25 * time.Millisecond
	fulltextReadyMaxWait      = 5 * time.Second
)

// fulltextReadyBudget is how long a fulltext query may be held back: the request timeout capped at
// fulltextReadyMaxWait, and no more than half of what the caller's own deadline has left, so the query that follows
// the wait still has time to run.
func (a *Adapter) fulltextReadyBudget(ctx context.Context) time.Duration {
	budget := a.config.RequestTimeout
	if budget <= 0 || budget > fulltextReadyMaxWait {
		budget = fulltextReadyMaxWait
	}
	if deadline, ok := ctx.Deadline(); ok {
		if half := time.Until(deadline) / 2; half < budget {
			budget = half
		}
	}
	return budget
}

// awaitFulltextIndexOperational holds a fulltext query back while the Subject fulltext index of key is still indexing
// the nodes just written. FalkorDB reports such an index as UNDER CONSTRUCTION and answers a query against it with
// fewer hits than the graph holds, down to none, which a caller reads as "no match". The whole wait, status reads
// included, runs inside fulltextReadyBudget, and the bound counts as reached by elapsed time, not only by the wait
// context: a status read can fail on its socket deadline a moment before the context timer fires; when the budget wins the query runs anyway and the timeout is logged
// loudly, so a short answer is attributable. A status read that fails, or a graph with no fulltext index, does not
// block the query: the query itself reports a real dependency error. A cancelled caller context ends the wait at
// once, the query then reports the cancellation.
func (a *Adapter) awaitFulltextIndexOperational(ctx context.Context, key, orgID string) {
	budget := a.fulltextReadyBudget(ctx)
	if budget <= 0 {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	began := a.now()
	boundReached := func() bool { return waitCtx.Err() != nil || a.now().Sub(began) >= budget }
	polls := 0
	pending := ""
	for {
		statuses, err := a.api.indexes(waitCtx, key)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !boundReached() {
				slog.WarnContext(ctx, "context fabric: fulltext index status unreadable, querying anyway", "org_id", contextfabric.SanitizeLogAttr(orgID), "error_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", err)))
				return
			}
		} else {
			pending = fulltextIndexPendingStatus(statuses)
			if pending == "" {
				if polls > 0 {
					slog.DebugContext(ctx, "context fabric: fulltext index became operational", "org_id", contextfabric.SanitizeLogAttr(orgID), "waited_ms", a.now().Sub(began).Milliseconds(), "polls", polls)
				}
				return
			}
		}
		polls++
		if boundReached() {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "context fabric: fulltext index still not operational at the wait bound, querying anyway: the answer may be short",
				"org_id", contextfabric.SanitizeLogAttr(orgID), "waited_ms", a.now().Sub(began).Milliseconds(), "polls", polls, "index_status", contextfabric.SanitizeLogAttr(pending))
			return
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(fulltextReadyPollInterval):
		}
	}
}

func fulltextIndexPendingStatus(statuses []indexStatus) string {
	for _, status := range statuses {
		if status.Label != labelSubject || status.Status == "OPERATIONAL" {
			continue
		}
		for _, kind := range status.Types[propSearchText] {
			if strings.EqualFold(kind, "FULLTEXT") {
				return status.Status
			}
		}
	}
	return ""
}
