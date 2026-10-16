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

// awaitFulltextIndexOperational holds a fulltext query back while the Subject fulltext index of key is still indexing
// the nodes just written. FalkorDB reports such an index as UNDER CONSTRUCTION and answers a query against it with
// fewer hits than the graph holds, down to none, which a caller reads as "no match". The wait is bounded by the request
// timeout and fulltextReadyMaxWait; when the bound wins the query runs anyway and the timeout is logged loudly, so a
// short answer is attributable. A status read that fails, or a graph with no fulltext index, does not block the
// query: the query itself reports a real dependency error.
func (a *Adapter) awaitFulltextIndexOperational(ctx context.Context, key, orgID string) {
	wait := a.config.RequestTimeout
	if wait <= 0 || wait > fulltextReadyMaxWait {
		wait = fulltextReadyMaxWait
	}
	began := time.Now()
	polls := 0
	for {
		statuses, err := a.api.indexes(ctx, key)
		if err != nil {
			slog.WarnContext(ctx, "context fabric: fulltext index status unreadable, querying anyway", "org_id", contextfabric.SanitizeLogAttr(orgID), "error_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", err)))
			return
		}
		pending := ""
		for _, status := range statuses {
			if status.Label != labelSubject || status.Status == "OPERATIONAL" {
				continue
			}
			for _, kind := range status.Types[propSearchText] {
				if strings.EqualFold(kind, "FULLTEXT") {
					pending = status.Status
				}
			}
		}
		if pending == "" {
			if polls > 0 {
				slog.DebugContext(ctx, "context fabric: fulltext index became operational", "org_id", contextfabric.SanitizeLogAttr(orgID), "waited_ms", time.Since(began).Milliseconds(), "polls", polls)
			}
			return
		}
		polls++
		if time.Since(began) >= wait {
			slog.WarnContext(ctx, "context fabric: fulltext index still not operational at the wait bound, querying anyway: the answer may be short",
				"org_id", contextfabric.SanitizeLogAttr(orgID), "waited_ms", time.Since(began).Milliseconds(), "polls", polls, "index_status", contextfabric.SanitizeLogAttr(pending))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(fulltextReadyPollInterval):
		}
	}
}
