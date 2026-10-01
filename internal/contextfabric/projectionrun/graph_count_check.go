package projectionrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
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
	// graphCountCheckTimeout bounds the whole check for one organization. It runs
	// under the org lock, so a slow count read must not hold the tick longer.
	graphCountCheckTimeout = 30 * time.Second
)

// GraphCountObserver is an OPTIONAL Observer extension: when the configured
// Observer implements it, every kind the count check finds below its source is
// reported as a counter event.
type GraphCountObserver interface {
	ObserveGraphBelowSource(GraphBelowSource)
	ObserveGraphCountCheck(GraphCountCheck)
}

const (
	GraphCountCheckCompleted = "completed"
	GraphCountCheckCancelled = "cancelled"
	GraphCountCheckFailed    = "failed"
)

// GraphCountCheck is one completed count check of one organization: what it
// compared and what it found. Reported once per check that actually ran, so
// "the check ran and found nothing" is observable. Counts only; content-safe.
type GraphCountCheck struct {
	OrgID string
	// Instance identifies this projector process; Pass is the organization's
	// 1-based check sequence within it.
	Instance string
	Pass     int
	// Outcome is GraphCountCheckCompleted, GraphCountCheckCancelled (the check
	// timeout or shutdown ended it) or GraphCountCheckFailed (a count read failed).
	Outcome        string
	SourcesChecked int
	KindsCompared  int
	// Gaps counts the confirmed graph_below_source gaps this check reported.
	Gaps int
	// Errors counts the source or graph count reads that failed this check.
	Errors   int
	Duration time.Duration
	At       time.Time
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
func graphCountTolerance(sourceCount, graphCount int64) int64 {
	// A kind the source holds and the graph holds none of is a gap whatever its
	// size: the absolute floor exists for stragglers, not for an empty kind.
	if graphCount == 0 && sourceCount > 0 {
		return 0
	}
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
	// suspects holds gaps seen once. A gap warns only when the next check
	// sees it again: rows landing in the source between a drain and the count
	// read close on their own by the next tick, a skipped row does not.
	suspects map[string]bool
	passes   map[string]int
	instance string
}

// instanceID is a random per-process identifier: every replica numbers its own
// passes, so (org, instance, pass) is what identifies one check.
func (g *graphCountState) instanceID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.instance == "" {
		var raw [6]byte
		if _, err := rand.Read(raw[:]); err != nil {
			ns := uint64(time.Now().UnixNano())
			for i := range raw {
				raw[i] = byte(ns >> (8 * i))
			}
		}
		g.instance = hex.EncodeToString(raw[:])
	}
	return g.instance
}

// nextPass numbers the organization's checks in this process, from 1.
func (g *graphCountState) nextPass(orgID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.passes == nil {
		g.passes = map[string]int{}
	}
	g.passes[orgID]++
	return g.passes[orgID]
}

func pairKey(orgID, source string) string { return orgID + "\x00" + source }

// resetOrg forgets every drain fact for the organization. runOrg calls it
// before each tick body, so a check is authorized only by pairs that drained
// in THIS tick: a recovery path that returns before any pair runs (a purge)
// leaves nothing to authorize one.
func (g *graphCountState) resetOrg(orgID string, sources []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, source := range sources {
		delete(g.drained, pairKey(orgID, source))
	}
}

// confirmGap reports whether this gap was already seen at the previous check,
// and records it as seen.
func (g *graphCountState) confirmGap(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.suspects[key] {
		return true
	}
	if g.suspects == nil {
		g.suspects = map[string]bool{}
	}
	g.suspects[key] = true
	return false
}

func (g *graphCountState) clearGap(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.suspects, key)
}

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
			for key := range g.suspects {
				if strings.HasPrefix(key, orgID+"\x00") {
					delete(g.suspects, key)
				}
			}
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
	ctx, cancel := context.WithTimeout(ctx, graphCountCheckTimeout)
	defer cancel()
	hash := orgIDHash(orgID)
	started := c.now()
	result := GraphCountCheck{OrgID: orgID, Instance: c.graphCounts.instanceID(), Pass: c.graphCounts.nextPass(orgID), At: started}
	defer func() {
		result.Duration = c.now().Sub(started)
		switch {
		case ctx.Err() != nil:
			result.Outcome = GraphCountCheckCancelled
		case result.Errors > 0:
			result.Outcome = GraphCountCheckFailed
		default:
			result.Outcome = GraphCountCheckCompleted
		}
		if obs, ok := c.observer.(GraphCountObserver); ok {
			obs.ObserveGraphCountCheck(result)
		}
	}()
	for _, source := range c.sourceNames {
		counter, ok := c.sources[source].(contextfabric.ProjectionSourceCounts)
		if !ok {
			continue
		}
		sourceCounts, err := counter.ProjectionSourceCounts(ctx, orgID)
		result.SourcesChecked++
		if err != nil {
			result.Errors++
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
				result.Errors++
				c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "graph_count",
					"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
					"failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
				continue
			}
			result.KindsCompared++
			tolerance := graphCountTolerance(sourceCount, graphCount)
			gapKey := pairKey(orgID, source) + "\x00" + string(kind)
			if sourceCount-graphCount <= tolerance {
				c.graphCounts.clearGap(gapKey)
				continue
			}
			if !c.graphCounts.confirmGap(gapKey) {
				continue
			}
			c.logger.WarnContext(ctx, "context_fabric: graph_below_source", "check", "graph_below_source",
				"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
				"source_count", sourceCount, "graph_count", graphCount, "tolerance", tolerance)
			result.Gaps++
			if obs, ok := c.observer.(GraphCountObserver); ok {
				obs.ObserveGraphBelowSource(GraphBelowSource{OrgID: orgID, Source: source, Kind: kind, SourceCount: sourceCount, GraphCount: graphCount, Tolerance: tolerance, At: c.now()})
			}
		}
	}
}
