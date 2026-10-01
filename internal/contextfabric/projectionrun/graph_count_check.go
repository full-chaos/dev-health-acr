package projectionrun

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	defaultGraphCountCheckInterval = 10 * time.Minute
	// graphCountToleranceFloor and graphCountToleranceDivisor define how many
	// nodes the graph may trail the source before the check warns:
	// max(floor, source/divisor) (2% of the source, at least 3). The slack
	// covers rows the projection omits by design (quarantined or malformed
	// items, in-batch duplicates) and rows landing between the two reads.
	graphCountToleranceFloor   = 3
	graphCountToleranceDivisor = 50
)

// GraphCountObserver is an OPTIONAL Observer extension: when the configured
// Observer implements it, every kind the count check finds below its source is
// reported as a counter event.
type GraphCountObserver interface {
	ObserveGraphBelowSource(GraphBelowSource)
}

// GraphBelowSource is one kind whose graph node count trails its source count
// by more than the tolerance. Counts only; content-safe.
type GraphBelowSource struct {
	OrgID       string
	Source      string
	Kind        contextfabric.SubjectKind
	SourceCount int64
	GraphCount  int64
	Tolerance   int64
	At          time.Time
}

// graphCountTolerance is the number of nodes the graph may trail sourceCount.
func graphCountTolerance(sourceCount int64) int64 {
	if t := sourceCount / graphCountToleranceDivisor; t > graphCountToleranceFloor {
		return t
	}
	return graphCountToleranceFloor
}

// graphCountState is the check's own bookkeeping: which pairs ended the last
// tick fully drained (a graph still catching up trails its source by design)
// and when each organization was last checked.
type graphCountState struct {
	mu        sync.Mutex
	drained   map[string]bool
	lastCheck map[string]time.Time
}

func pairKey(orgID, source string) string { return orgID + "\x00" + source }

func (g *graphCountState) markPair(orgID, source string, drained bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.drained == nil {
		g.drained = map[string]bool{}
	}
	g.drained[pairKey(orgID, source)] = drained
}

// due reports whether the organization may be checked now, and claims the slot
// when it may: every configured source ended its last tick fully drained and
// the interval has elapsed.
func (g *graphCountState) due(orgID string, sources []string, now time.Time, interval time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, source := range sources {
		if !g.drained[pairKey(orgID, source)] {
			return false
		}
	}
	if last, ok := g.lastCheck[orgID]; ok && now.Sub(last) < interval {
		return false
	}
	if g.lastCheck == nil {
		g.lastCheck = map[string]time.Time{}
	}
	g.lastCheck[orgID] = now
	return true
}

// checkGraphCounts compares, per kind, the source's row count against the
// graph's node count and warns `graph_below_source` when the graph trails the
// source beyond the tolerance. It never fails or blocks the tick: a count
// error is its own Warn class.
func (c *Coordinator) checkGraphCounts(ctx context.Context, orgID string) {
	if c.graphCountInterval <= 0 || ctx.Err() != nil {
		return
	}
	graph, ok := c.backend.(contextfabric.ProjectionGraphCounts)
	if !ok {
		return
	}
	if !c.graphCounts.due(orgID, c.sourceNames, c.now(), c.graphCountInterval) {
		return
	}
	hash := orgIDHash(orgID)
	for _, source := range c.sourceNames {
		counter, ok := c.sources[source].(contextfabric.ProjectionSourceCounts)
		if !ok {
			continue
		}
		sourceCounts, err := counter.ProjectionSourceCounts(ctx, orgID)
		if err != nil {
			c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "source_count",
				"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash), "failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
			continue
		}
		kinds := make([]contextfabric.SubjectKind, 0, len(sourceCounts))
		for kind := range sourceCounts {
			kinds = append(kinds, kind)
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
		for _, kind := range kinds {
			sourceCount := sourceCounts[kind]
			graphCount, err := graph.CountKind(ctx, orgID, kind)
			if err != nil {
				c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "graph_count",
					"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
					"failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
				continue
			}
			tolerance := graphCountTolerance(sourceCount)
			if sourceCount-graphCount <= tolerance {
				continue
			}
			c.logger.WarnContext(ctx, "context_fabric: graph_below_source", "check", "graph_below_source",
				"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
				"source_count", sourceCount, "graph_count", graphCount, "tolerance", tolerance)
			if obs, ok := c.observer.(GraphCountObserver); ok {
				obs.ObserveGraphBelowSource(GraphBelowSource{OrgID: orgID, Source: source, Kind: kind, SourceCount: sourceCount, GraphCount: graphCount, Tolerance: tolerance, At: c.now()})
			}
		}
	}
}
