package projectionrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"strconv"
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
	// graphCountUnknownWarnAfter is the number of checks in a row a source may read
	// unknown (no owned marker) before its marker-absent line is a Warn.
	graphCountUnknownWarnAfter = 3
	// defaultGraphCountUnknownProbeInterval is the minimum gap between the source
	// and graph count reads a source with no owned marker costs: those statements
	// have never run on a large graph, so they run rarely.
	defaultGraphCountUnknownProbeInterval = time.Hour
)

// GraphCountObserver is an OPTIONAL Observer extension: when the configured
// Observer implements it, every kind the count check finds below its source is
// reported as a counter event.
type GraphCountObserver interface {
	ObserveGraphBelowSource(GraphBelowSource)
	ObserveGraphCountCheck(GraphCountCheck)
}

// GraphEdgeCountObserver is an OPTIONAL Observer extension: when the configured
// Observer implements it, every edge population the count check finds below its
// source is reported as a counter event.
type GraphEdgeCountObserver interface {
	ObserveGraphEdgesBelowSource(GraphEdgesBelowSource)
}

const (
	GraphCountCheckCompleted = "completed"
	GraphCountCheckCancelled = "cancelled"
	GraphCountCheckFailed    = "failed"
	// GraphCountCheckUnknown (CHAOS-12649): every read succeeded, and at least
	// one source's owned node counts could not be compared, because no walk from
	// zero has written its owned nodes with the owned marker yet. Never
	// "completed": a check that could not compare is not a check that found
	// nothing.
	GraphCountCheckUnknown = "unknown"
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
	// timeout or shutdown ended it), GraphCountCheckFailed (a count read failed)
	// or GraphCountCheckUnknown (a source's owned counts cannot be compared yet).
	Outcome        string
	SourcesChecked int
	// SourcesUnknown counts the sources whose node counts this check did not
	// compare because the graph holds no owned-marked walk of them (CHAOS-12649).
	SourcesUnknown int
	KindsCompared  int
	// Gaps counts the confirmed graph_below_source gaps this check reported.
	Gaps int
	// DaysCompared counts the (kind, UTC day of the sync stamp) buckets whose
	// source and graph counts were both read; DayGaps the confirmed
	// graph_below_source_on_day gaps among them (CHAOS-12649).
	DaysCompared int
	DayGaps      int
	// LowerBoundGaps counts the confirmed graph_below_source_lower_bound gaps this
	// check reported for sources whose owned counts cannot be compared: the source
	// holds more rows than the graph holds nodes of that kind, stubs included.
	LowerBoundGaps int
	// Errors counts the source or graph count reads that failed this check, and
	// every measurement the check could not make at all (a source or backend with
	// node counts and no edge counts).
	Errors int
	// RelationsCompared counts the edge populations (relation type, arm) whose
	// source and graph counts were both read. EdgeGaps counts the confirmed
	// graph_edges_below_source gaps. RelationsDeferred counts the distinct
	// relation types the sources produce and do not measure yet.
	RelationsCompared int
	EdgeGaps          int
	RelationsDeferred int
	Duration          time.Duration
	At                time.Time
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

// GraphEdgesBelowSource is one edge population (relation type and arm) whose
// graph edge count trails its source count by more than the tolerance. Counts
// only; content-safe.
type GraphEdgesBelowSource struct {
	OrgID       string
	Source      string
	Relation    contextfabric.RelationshipType
	Arm         string
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
	// seen holds the gaps the running check measured: a suspect a check did not
	// measure again (a failed read, an unmeasured source, a cancelled check) is
	// dropped at the end of it, so a gap confirms only across two checks that both
	// measured it.
	seen     map[string]bool
	passes   map[string]int
	instance string
	// unknownRuns counts, per (org, source), the checks in a row that read the
	// source unknown because its owned marker is absent.
	unknownRuns map[string]int
	// lastProbe is when each (org, source) last claimed its marker-absent reads,
	// whether they succeeded or not: a refused read is never retried inside the
	// interval.
	lastProbe map[string]time.Time
	// lowerBoundSuspects holds lower-bound gaps seen once. Probes are an interval
	// apart, so a gap confirms across two probes, outside the per-check bookkeeping.
	lowerBoundSuspects map[string]bool
}

func (g *graphCountState) claimProbe(key string, now time.Time, interval time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if interval <= 0 {
		return false
	}
	if last, ok := g.lastProbe[key]; ok && now.Sub(last) < interval {
		return false
	}
	if g.lastProbe == nil {
		g.lastProbe = map[string]time.Time{}
	}
	g.lastProbe[key] = now
	return true
}

func (g *graphCountState) confirmLowerBound(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lowerBoundSuspects[key] {
		return true
	}
	if g.lowerBoundSuspects == nil {
		g.lowerBoundSuspects = map[string]bool{}
	}
	g.lowerBoundSuspects[key] = true
	return false
}

// retainLowerBound drops every lower-bound suspect of one (org, source) that the
// probe did not measure short: a failed probe measured nothing, so it drops all.
func (g *graphCountState) retainLowerBound(prefix string, keep map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key := range g.lowerBoundSuspects {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			delete(g.lowerBoundSuspects, key)
		}
	}
}

func (g *graphCountState) clearLowerBound(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.lowerBoundSuspects, key)
}

func (g *graphCountState) noteUnknown(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unknownRuns == nil {
		g.unknownRuns = map[string]int{}
	}
	g.unknownRuns[key]++
	return g.unknownRuns[key]
}

func (g *graphCountState) resetUnknown(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.unknownRuns, key)
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
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	g.seen[key] = true
	if g.suspects[key] {
		return true
	}
	if g.suspects == nil {
		g.suspects = map[string]bool{}
	}
	g.suspects[key] = true
	return false
}

// beginPass starts a check of the organization: no gap is measured yet.
func (g *graphCountState) beginPass(orgID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key := range g.seen {
		if strings.HasPrefix(key, orgID+"\x00") {
			delete(g.seen, key)
		}
	}
}

// endPass drops every suspect of the organization that the check did not
// measure: a gap seen before a check that could not measure it is not a gap seen
// at two checks in a row.
func (g *graphCountState) endPass(orgID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key := range g.suspects {
		if strings.HasPrefix(key, orgID+"\x00") && !g.seen[key] {
			delete(g.suspects, key)
		}
	}
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
// graph's node count and, per relation type and arm, the source's distinct edge
// count against the graph's edge count. It warns `graph_below_source` and
// `graph_edges_below_source` when the graph trails the source beyond the
// tolerance. It never fails or blocks the tick: a count error is its own Warn
// class, and a measurement the check could not make is `graph_count_unmeasured`.
func (c *Coordinator) checkGraphCounts(ctx context.Context, orgID string) {
	if c.graphCountInterval <= 0 || ctx.Err() != nil {
		return
	}
	nodeGraph, hasNodeCounts := c.backend.(contextfabric.ProjectionGraphCounts)
	_, hasEdgeCounts := c.backend.(contextfabric.ProjectionGraphEdgeCounts)
	if !hasNodeCounts && !hasEdgeCounts {
		return
	}
	if !c.graphCounts.due(orgID, c.sourceNames, c.now(), c.graphCountInterval) {
		return
	}
	hash := orgIDHash(orgID)
	started := c.now()
	result := GraphCountCheck{OrgID: orgID, Instance: c.graphCounts.instanceID(), Pass: c.graphCounts.nextPass(orgID), At: started}
	c.graphCounts.beginPass(orgID)
	cancelled := false
	defer func() {
		c.graphCounts.endPass(orgID)
		result.Duration = c.now().Sub(started)
		switch {
		case cancelled || ctx.Err() != nil:
			result.Outcome = GraphCountCheckCancelled
		case result.Errors > 0:
			result.Outcome = GraphCountCheckFailed
		case result.SourcesUnknown > 0:
			result.Outcome = GraphCountCheckUnknown
		default:
			result.Outcome = GraphCountCheckCompleted
		}
		if obs, ok := c.observer.(GraphCountObserver); ok {
			obs.ObserveGraphCountCheck(result)
		}
	}()
	// Each phase has its own timeout: the edge read walks a full history and
	// must not starve the node read, nor the other way round.
	nodeCtx, cancelNodes := context.WithTimeout(ctx, graphCountCheckTimeout)
	var pending []unknownSource
	if hasNodeCounts {
		pending = c.checkNodeCounts(nodeCtx, orgID, hash, nodeGraph, &result)
	} else if c.anySourceReadsNodeCounts() {
		result.Errors++
		c.logger.WarnContext(nodeCtx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "node", "reason", "backend_no_node_counts",
			"org_id_hash", contextfabric.SanitizeLogAttr(hash))
	}
	cancelled = nodeCtx.Err() != nil
	cancelNodes()
	if ctx.Err() != nil {
		return
	}
	edgeCtx, cancelEdges := context.WithTimeout(ctx, graphCountCheckTimeout)
	defer cancelEdges()
	c.checkEdgeCounts(edgeCtx, orgID, hash, &result)
	cancelled = cancelled || edgeCtx.Err() != nil
	// The marker-absent reads are optional: they run after every measurement the
	// check owes, under their own deadline, so a slow one costs nothing else.
	if len(pending) > 0 && ctx.Err() == nil {
		probeCtx, cancelProbes := context.WithTimeout(ctx, graphCountCheckTimeout)
		defer cancelProbes()
		totals := &kindTotals{}
		for _, source := range pending {
			c.checkUnknownSource(probeCtx, orgID, hash, source.name, source.counter, nodeGraph, totals, &result)
		}
	}
}

// unknownSource is a source whose owned marker is absent, waiting for its probe.
type unknownSource struct {
	name    string
	counter contextfabric.ProjectionSourceCounts
}

func (c *Coordinator) anySourceReadsNodeCounts() bool {
	for _, source := range c.sourceNames {
		if _, ok := c.sources[source].(contextfabric.ProjectionSourceCounts); ok {
			return true
		}
	}
	return false
}

func (c *Coordinator) checkNodeCounts(ctx context.Context, orgID, hash string, graph contextfabric.ProjectionGraphCounts, result *GraphCountCheck) []unknownSource {
	var pending []unknownSource
	for _, source := range c.sourceNames {
		counter, ok := c.sources[source].(contextfabric.ProjectionSourceCounts)
		if !ok {
			continue
		}
		// A source its configuration switched off writes no watermark by design:
		// it reads unknown and says nothing, not even about a failed marker read.
		if enablement, ok := c.sources[source].(contextfabric.ProjectionSourceEnablement); ok && !enablement.Enabled() {
			c.graphCounts.resetUnknown(pairKey(orgID, source))
			result.SourcesUnknown++
			continue
		}
		// CHAOS-12649: the graph counts owned-marked nodes only, so a source no walk
		// from zero has marked yet would read as one large gap. It reads unknown.
		known, err := graph.OwnedCountsKnown(ctx, orgID, source)
		if err != nil {
			c.graphCounts.resetUnknown(pairKey(orgID, source))
			result.SourcesChecked++
			result.Errors++
			c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "owned_marker",
				"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash), "failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
			continue
		}
		if !known {
			result.SourcesUnknown++
			pending = append(pending, unknownSource{name: source, counter: counter})
			continue
		}
		c.graphCounts.resetUnknown(pairKey(orgID, source))
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
		c.checkDayCounts(ctx, orgID, hash, source, result)
	}
	return pending
}

// checkDayCounts (CHAOS-12649) compares, per stamped kind and per UTC day of the
// rows' sync stamp, the source's rows with the graph's owned nodes. The walk loses
// contiguous ranges of that stamp, so a lost range is a short day even when the
// kind's total is inside its tolerance. The same tolerance and the same two-check
// confirmation apply per day. A source without day counts is not day-checked; a
// backend without them, beside a source that has them, is unmeasured.
func (c *Coordinator) checkDayCounts(ctx context.Context, orgID, hash, source string, result *GraphCountCheck) {
	counter, ok := c.sources[source].(contextfabric.ProjectionSourceDayCounts)
	if !ok {
		return
	}
	graph, ok := c.backend.(contextfabric.ProjectionGraphDayCounts)
	if !ok {
		result.Errors++
		c.logger.WarnContext(ctx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "day", "reason", "backend_no_day_counts",
			"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash))
		return
	}
	sourceDays, err := counter.ProjectionSourceDayCounts(ctx, orgID)
	if err != nil {
		result.Errors++
		c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "source_day_count",
			"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash), "failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
		return
	}
	kinds := make([]contextfabric.SubjectKind, 0, len(sourceDays))
	for kind := range sourceDays {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	for _, kind := range kinds {
		graphDays, err := graph.CountKindByDay(ctx, orgID, kind)
		if err != nil {
			result.Errors++
			c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "graph_day_count",
				"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
				"failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
			continue
		}
		days := make([]int64, 0, len(sourceDays[kind]))
		for day := range sourceDays[kind] {
			days = append(days, day)
		}
		sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
		for _, day := range days {
			sourceCount, graphCount := sourceDays[kind][day], graphDays[day]
			result.DaysCompared++
			tolerance := graphCountTolerance(sourceCount, graphCount)
			gapKey := pairKey(orgID, source) + "\x00" + string(kind) + "\x00day\x00" + strconv.FormatInt(day, 10)
			if sourceCount-graphCount <= tolerance {
				c.graphCounts.clearGap(gapKey)
				continue
			}
			if !c.graphCounts.confirmGap(gapKey) {
				continue
			}
			c.logger.WarnContext(ctx, "context_fabric: graph_below_source_on_day", "check", "graph_below_source_on_day",
				"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
				"day", contextfabric.SanitizeLogAttr(time.Unix(day*24*60*60, 0).UTC().Format("2006-01-02")), "source_count", sourceCount, "graph_count", graphCount, "tolerance", tolerance)
			result.DayGaps++
		}
	}
}

// checkEdgeCounts is the edge phase of the check. Every source the node phase
// measured must also be measurable here: a source or backend that reads node
// counts and no edge counts is reported (graph_count_unmeasured) and fails
// the check, never skipped.
func (c *Coordinator) checkEdgeCounts(ctx context.Context, orgID, hash string, result *GraphCountCheck) {
	graph, ok := c.backend.(contextfabric.ProjectionGraphEdgeCounts)
	if !ok {
		result.Errors++
		c.logger.WarnContext(ctx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "edge", "reason", "backend_no_edge_counts",
			"org_id_hash", contextfabric.SanitizeLogAttr(hash))
		return
	}
	deferred := map[contextfabric.RelationshipType]bool{}
	defer func() { result.RelationsDeferred = len(deferred) }()
	for _, source := range c.sourceNames {
		_, hasNodeCounts := c.sources[source].(contextfabric.ProjectionSourceCounts)
		counter, hasEdgeCounts := c.sources[source].(contextfabric.ProjectionSourceEdgeCounts)
		if !hasEdgeCounts {
			if !hasNodeCounts {
				continue
			}
			result.Errors++
			c.logger.WarnContext(ctx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "edge", "reason", "source_no_edge_counts",
				"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash))
			continue
		}
		for _, relation := range counter.DeferredEdgeRelations() {
			deferred[relation] = true
		}
		sourceCounts, err := counter.ProjectionSourceEdgeCounts(ctx, orgID)
		unmeasured := map[contextfabric.EdgeCountKey]bool{}
		var partial *contextfabric.EdgeCountsUnmeasured
		if errors.As(err, &partial) {
			for _, key := range partial.Keys {
				unmeasured[key] = true
				result.Errors++
				c.logger.WarnContext(ctx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "edge", "reason", contextfabric.SanitizeLogAttr(partial.Reason),
					"source", contextfabric.SanitizeLogAttr(source), "relation_type", contextfabric.SanitizeLogAttr(string(key.Relation)), "arm", contextfabric.SanitizeLogAttr(key.Arm),
					"org_id_hash", contextfabric.SanitizeLogAttr(hash))
			}
			err = nil
		}
		if err != nil {
			result.Errors++
			c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "edge_source_count",
				"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash), "failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
			continue
		}
		for _, key := range counter.EdgeCountKeys() {
			if _, present := sourceCounts[key]; present || unmeasured[key] {
				continue
			}
			result.Errors++
			c.logger.WarnContext(ctx, "context_fabric: graph_count_unmeasured", "check", "graph_count_unmeasured", "scope", "edge", "reason", "source_missing_edge_count",
				"source", contextfabric.SanitizeLogAttr(source), "relation_type", contextfabric.SanitizeLogAttr(string(key.Relation)), "arm", contextfabric.SanitizeLogAttr(key.Arm),
				"org_id_hash", contextfabric.SanitizeLogAttr(hash))
		}
		keys := make([]contextfabric.EdgeCountKey, 0, len(sourceCounts))
		for key := range sourceCounts {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Relation != keys[j].Relation {
				return keys[i].Relation < keys[j].Relation
			}
			return keys[i].Arm < keys[j].Arm
		})
		for _, key := range keys {
			sourceCount := sourceCounts[key]
			graphCount, err := graph.CountEdges(ctx, orgID, key)
			if err != nil {
				result.Errors++
				c.logger.WarnContext(ctx, "context_fabric: projection count check failed", "check", "graph_count", "stage", "edge_graph_count",
					"source", contextfabric.SanitizeLogAttr(source), "relation_type", contextfabric.SanitizeLogAttr(string(key.Relation)), "arm", contextfabric.SanitizeLogAttr(key.Arm),
					"org_id_hash", contextfabric.SanitizeLogAttr(hash), "failure_class", contextfabric.SanitizeLogAttr(classifyOutcomeError(err)))
				continue
			}
			result.RelationsCompared++
			tolerance := graphCountTolerance(sourceCount, graphCount)
			gapKey := pairKey(orgID, source) + "\x00edge\x00" + string(key.Relation) + "\x00" + key.Arm
			if sourceCount-graphCount <= tolerance {
				c.graphCounts.clearGap(gapKey)
				continue
			}
			if !c.graphCounts.confirmGap(gapKey) {
				continue
			}
			c.logger.WarnContext(ctx, "context_fabric: graph_edges_below_source", "check", "graph_edges_below_source",
				"source", contextfabric.SanitizeLogAttr(source), "relation_type", contextfabric.SanitizeLogAttr(string(key.Relation)), "arm", contextfabric.SanitizeLogAttr(key.Arm),
				"org_id_hash", contextfabric.SanitizeLogAttr(hash), "source_count", sourceCount, "graph_count", graphCount, "tolerance", tolerance)
			result.EdgeGaps++
			if obs, ok := c.observer.(GraphEdgeCountObserver); ok {
				obs.ObserveGraphEdgesBelowSource(GraphEdgesBelowSource{OrgID: orgID, Source: source, Relation: key.Relation, Arm: key.Arm, SourceCount: sourceCount, GraphCount: graphCount, Tolerance: tolerance, At: c.now()})
			}
		}
	}
}

// kindTotals memoizes the graph's total node count per kind for one check: the
// total does not depend on the source, so sources sharing a kind read it once.
type kindTotals struct {
	counts map[contextfabric.SubjectKind]int64
}

// unknownProbe is what the marker-absent reads of one source found.
type unknownProbe struct {
	state    string
	compared int
	gapKinds int
	failed   int
	cause    error
}

// checkUnknownSource is the node phase for a source whose owned marker is absent.
// The owned counts cannot be compared, so the check reads outcome unknown; what it
// can still say is read against the graph's total kind count (stubs included): a
// source above it is a gap whatever the marker says, a lower bound only. A source
// its configuration switched off has no watermark by design and says nothing.
// The reads run at most once per unknown-probe interval per source (default 1h),
// and a refused read is logged with its ClickHouse code class and is not retried
// inside that interval. They never fail the check: the outcome stays unknown.
// Statements per probe: the source's kind count statements (totals only, never
// the per-day ones) and one graph scalar per distinct kind across the sources.
func (c *Coordinator) checkUnknownSource(ctx context.Context, orgID, hash, source string, counter contextfabric.ProjectionSourceCounts, graph contextfabric.ProjectionGraphCounts, totals *kindTotals, result *GraphCountCheck) {
	key := pairKey(orgID, source)
	if enablement, ok := c.sources[source].(contextfabric.ProjectionSourceEnablement); ok && !enablement.Enabled() {
		c.graphCounts.resetUnknown(key)
		return
	}
	runs := c.graphCounts.noteUnknown(key)
	probe := unknownProbe{state: "disabled"}
	totalGraph, hasTotals := graph.(contextfabric.ProjectionGraphTotalCounts)
	switch {
	case !hasTotals:
		probe.state = "no_total_counts"
	case c.unknownProbeInterval <= 0:
	case !c.graphCounts.claimProbe(key, c.now(), c.unknownProbeInterval):
		probe.state = "skipped_interval"
	default:
		probe = c.compareAgainstTotals(ctx, orgID, hash, source, counter, totalGraph, totals, result)
	}
	basis, coverage := "none", "none"
	if probe.compared > 0 {
		basis, coverage = "total_kind_count", "lower_bound"
	}
	level := slog.LevelInfo
	if runs >= graphCountUnknownWarnAfter || probe.failed > 0 {
		level = slog.LevelWarn
	}
	failureClass, limitClass, code := "", "", 0
	if probe.cause != nil {
		failureClass = classifyOutcomeError(probe.cause)
		var failure contextfabric.SourceReadFailure
		switch {
		case errors.As(probe.cause, &failure):
			code, limitClass = int(failure.ClickHouseCode()), failure.LimitClass()
		case errors.Is(probe.cause, context.DeadlineExceeded):
			limitClass = "execution_time"
		}
	}
	c.logger.Log(ctx, level, "context_fabric: graph_count_owned_marker_absent", "check", "graph_count_owned_marker_absent", "reason", "owned_marker_absent",
		"source", contextfabric.SanitizeLogAttr(source), "org_id_hash", contextfabric.SanitizeLogAttr(hash), "unknown_passes", runs,
		"probe", contextfabric.SanitizeLogAttr(probe.state), "basis", contextfabric.SanitizeLogAttr(basis), "coverage", contextfabric.SanitizeLogAttr(coverage),
		"lower_bound_kinds", probe.compared, "lower_bound_gap_kinds", probe.gapKinds, "probe_failed_reads", probe.failed,
		"failure_class", contextfabric.SanitizeLogAttr(failureClass), "clickhouse_code", code, "limit_class", contextfabric.SanitizeLogAttr(limitClass))
}

func (c *Coordinator) compareAgainstTotals(ctx context.Context, orgID, hash, source string, counter contextfabric.ProjectionSourceCounts, totalGraph contextfabric.ProjectionGraphTotalCounts, totals *kindTotals, result *GraphCountCheck) unknownProbe {
	probe := unknownProbe{state: "ran"}
	short := map[string]bool{}
	defer c.graphCounts.retainLowerBound(pairKey(orgID, source)+"\x00", short)
	sourceCounts, err := counter.ProjectionSourceCounts(ctx, orgID)
	if err != nil {
		probe.state, probe.failed, probe.cause = "failed", 1, err
		return probe
	}
	kinds := make([]contextfabric.SubjectKind, 0, len(sourceCounts))
	for kind := range sourceCounts {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	for _, kind := range kinds {
		total, seen := totals.counts[kind]
		if !seen {
			total, err = totalGraph.CountKindTotal(ctx, orgID, kind)
			if err != nil {
				probe.state, probe.failed, probe.cause = "failed", probe.failed+1, err
				continue
			}
			if totals.counts == nil {
				totals.counts = map[contextfabric.SubjectKind]int64{}
			}
			totals.counts[kind] = total
		}
		probe.compared++
		sourceCount := sourceCounts[kind]
		tolerance := graphCountTolerance(sourceCount, total)
		gapKey := pairKey(orgID, source) + "\x00" + string(kind) + "\x00lower_bound"
		if sourceCount-total <= tolerance {
			c.graphCounts.clearLowerBound(gapKey)
			continue
		}
		short[gapKey] = true
		if !c.graphCounts.confirmLowerBound(gapKey) {
			continue
		}
		c.logger.WarnContext(ctx, "context_fabric: graph_below_source_lower_bound", "check", "graph_below_source_lower_bound",
			"source", contextfabric.SanitizeLogAttr(source), "kind", contextfabric.SanitizeLogAttr(string(kind)), "org_id_hash", contextfabric.SanitizeLogAttr(hash),
			"source_count", sourceCount, "graph_total_count", total, "tolerance", tolerance, "basis", "total_kind_count", "coverage", "lower_bound")
		result.LowerBoundGaps++
		probe.gapKinds++
	}
	return probe
}
