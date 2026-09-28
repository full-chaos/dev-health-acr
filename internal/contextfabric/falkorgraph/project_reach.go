package falkorgraph

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7080. A project node's authorization_repositories is always the "*"
// wildcard (projection writes a project's empty repository list that way),
// and since CHAOS-7080 the shared predicate admits a "*" node to NO
// repository-restricted caller. A project is still visible to such a caller
// when it reaches a granted repository through OWNERSHIP: its CURRENT
// OWNED_BY_TEAM edges name teams whose own ownership-derived repository lists
// (CHAOS-4390) hold a granted repository.
//
// That reach is computed LIVE, once per adapter call, and substituted for the
// project node's "*" on every node the call reads -- so every read site the
// shared predicate decides (resolution, discovery, anchor membership, stored
// subject re-reads) sees the real reach without a per-site change. A read
// site that is not wrapped keeps the "*" and, under the predicate, denies the
// project: a missed site fails CLOSED, never open.
//
// Unrestricted and universal ("*") callers are never wrapped: the predicate
// does not consult repositories for them, so their reads are byte-identical
// to before.

// noProjectOwnershipSentinel is the reach of a project with no current owning
// team that owns a repository. Like noTeamOwnershipSentinel, it is a list that
// admits no real repository slug (it has no '/'), never the "*" wildcard.
const noProjectOwnershipSentinel = "acr-context-fabric:no-project-repository-ownership"

type projectReachContextKey struct{}

// projectReach maps a project's canonical id to its ownership-reached
// repositories (sorted, never empty: noProjectOwnershipSentinel when none).
type projectReach map[string][]string

// needsProjectReach: only a repository-restricted caller's reads change.
func needsProjectReach(principal storage.Principal) bool {
	if len(principal.RepositoryScopes) == 0 {
		return false
	}
	return !slices.ContainsFunc(principal.RepositoryScopes, func(scope string) bool { return strings.TrimSpace(scope) == "*" })
}

// withProjectReach returns ctx carrying a LAZY project reach for a
// repository-restricted caller: the reach is read from the caller's own
// organization graph, at the adapter clock, the first time a read of this
// call returns a project node carrying the "*" wildcard, and at most once per
// call. A call that never reads a project node issues no extra query. ctx is
// returned unchanged for an unrestricted or universal caller.
func (a *Adapter) withProjectReach(ctx context.Context, key string, principal storage.Principal) (context.Context, error) {
	if !needsProjectReach(principal) {
		return ctx, nil
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	return context.WithValue(ctx, projectReachContextKey{}, &lazyProjectReach{adapter: a, key: key, orgID: orgID}), nil
}

// lazyProjectReach loads one call's project reach on first use.
type lazyProjectReach struct {
	adapter *Adapter
	key     string
	orgID   string
	once    sync.Once
	reach   projectReach
	err     error
}

func (l *lazyProjectReach) load(ctx context.Context, inner conn) (projectReach, error) {
	l.once.Do(func() {
		now := l.adapter.now()
		current := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
		cypher := fmt.Sprintf("MATCH (p:%s {%s:$org, %s:$project})-[r:%s]->(t:%s {%s:$org, %s:$team}) WHERE r.%s = $owned%s%s RETURN p.%s AS id, t.%s AS repos",
			labelSubject, propOrgID, propKind, labelRelation, labelSubject, propOrgID, propKind,
			propRelationType, current.predicate("r"), current.predicate("t"), propCanonicalID, propAuthzRepos)
		params := current.bind(map[string]interface{}{
			"org": l.orgID, "project": string(contractsv1.ContextFabricSubjectProject), "team": string(contractsv1.ContextFabricSubjectTeam),
			"owned": string(contractsv1.ContextFabricRelationshipOwnedByTeam),
		})
		// The reach read runs on the INNER conn: it is never rewritten.
		rows, err := inner.query(ctx, l.key, cypher, params, true)
		if err != nil {
			l.err = graphNotProjectedError(safeDependencyError("read project ownership reach", err))
			return
		}
		sets := map[string]map[string]struct{}{}
		for _, row := range rows {
			id, _ := row["id"].(string)
			if id == "" {
				continue
			}
			if sets[id] == nil {
				sets[id] = map[string]struct{}{}
			}
			for _, repository := range rowStringSlice(row, "repos") {
				if repository = strings.TrimSpace(repository); repository != "" && repository != "*" {
					sets[id][repository] = struct{}{}
				}
			}
		}
		l.reach = make(projectReach, len(sets))
		for id, set := range sets {
			list := make([]string, 0, len(set))
			for repository := range set {
				list = append(list, repository)
			}
			sort.Strings(list)
			l.reach[id] = list
		}
	})
	return l.reach, l.err
}

// reachFor is a project's reach, never empty.
func (r projectReach) reachFor(canonicalID string) []string {
	if list := r[canonicalID]; len(list) > 0 {
		return slices.Clone(list)
	}
	return []string{noProjectOwnershipSentinel}
}

// projectReachConn rewrites project nodes on READ-ONLY queries whose context
// carries a project reach: the "*" repository list becomes the project's
// ownership reach. Write queries and contexts without a reach pass through
// unchanged.
type projectReachConn struct {
	conn
}

func (c projectReachConn) query(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
	rows, err := c.conn.query(ctx, graphKey, cypher, params, readOnly)
	if err != nil || !readOnly {
		return rows, err
	}
	lazy, ok := ctx.Value(projectReachContextKey{}).(*lazyProjectReach)
	if !ok || !rowsCarryWildcardProject(rows) {
		return rows, nil
	}
	reach, err := lazy.load(ctx, c.conn)
	if err != nil {
		// Fail closed: the caller gets the read error, never a project
		// admitted on the wildcard.
		return nil, err
	}
	for _, r := range rows {
		for column, value := range r {
			r[column] = reach.rewrite(value)
		}
	}
	return rows, nil
}

func rowsCarryWildcardProject(rows []row) bool {
	for _, r := range rows {
		for _, value := range r {
			if carriesWildcardProject(value) {
				return true
			}
		}
	}
	return false
}

func carriesWildcardProject(value interface{}) bool {
	switch v := value.(type) {
	case *node:
		return isWildcardProject(v)
	case []interface{}:
		for _, item := range v {
			if carriesWildcardProject(item) {
				return true
			}
		}
	}
	return false
}

func isWildcardProject(n *node) bool {
	if n == nil || propStringValue(n.Properties[propKind]) != string(contractsv1.ContextFabricSubjectProject) {
		return false
	}
	wildcard, isString := n.Properties[propAuthzRepos].(string)
	return isString && wildcard == "*"
}

// rewrite substitutes a project node's "*" repository list with its reach,
// walking lists (paths, collected nodes). The node is copied: a cached or
// shared node value is never mutated in place.
func (r projectReach) rewrite(value interface{}) interface{} {
	switch v := value.(type) {
	case *node:
		if !isWildcardProject(v) {
			return v
		}
		properties := make(map[string]interface{}, len(v.Properties))
		for key, property := range v.Properties {
			properties[key] = property
		}
		properties[propAuthzRepos] = r.reachFor(propStringValue(v.Properties[propCanonicalID]))
		copied := *v
		copied.Properties = properties
		return &copied
	case []interface{}:
		out := make([]interface{}, len(v))
		for index, item := range v {
			out[index] = r.rewrite(item)
		}
		return out
	default:
		return value
	}
}
