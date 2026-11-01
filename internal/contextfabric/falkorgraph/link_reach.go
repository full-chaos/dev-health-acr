package falkorgraph

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12456. A repo-less work item carries only the no-repository sentinel,
// which admits no restricted caller. Such a caller may read the item when a
// native link of record joins it to a pull request of a repository it is
// granted (ruling (a); AGENTS.md "Entity tree"). explicit_text and heuristic
// links never grant, and a project's owning team is never the relation.
//
// Same pattern as project_reach.go. The reach is read LIVE once per adapter
// call, and each repo-less node a read returns gains an in-memory attribute that
// the shared predicate tests. Nothing is stored on a node, so nothing needs a
// rebuild, and a read site that is not wrapped keeps the sentinel and denies: a
// missed site fails CLOSED. The sentinel itself stays, so every walk keeps its
// own tier rule (repositoryLess, admitted).
//
// THE READ IS ANCHORED ON THE GRANT and runs at a fixed place, the first read of
// the call, whatever that read returned. So no statement depends on a subject
// outside the grant: a keyed read of the page's ids would run only when the page
// held a repo-less node, a hidden one included.
//
// A failed read admits nothing. It is recorded, the call goes on, and the turn
// discloses it. A stored-subject decision fails instead (stored_subjects.go): a
// read that failed is not a denial.

// linkReachMaxRows bounds one reach read. The row past it marks a cut. The ids a
// cut read holds are proven and stay; every other item stays denied and the turn
// discloses it.
const linkReachMaxRows = 20000

type linkReachContextKey struct{}

// WorkItemLinkReachOutcome is how one reach read ended
// (eventspec.WorkItemLinkReach).
type WorkItemLinkReachOutcome string

const (
	// WorkItemLinkReachRead: the read finished, inside the bound.
	WorkItemLinkReachRead WorkItemLinkReachOutcome = "read"
	// WorkItemLinkReachCut: the read stopped at linkReachMaxRows.
	WorkItemLinkReachCut WorkItemLinkReachOutcome = "cut"
	// WorkItemLinkReachFailed: the read returned an error.
	WorkItemLinkReachFailed WorkItemLinkReachOutcome = "failed"
)

// WorkItemLinkReachOutcomeVocabulary is the closed list of outcomes.
func WorkItemLinkReachOutcomeVocabulary() []string {
	return []string{string(WorkItemLinkReachRead), string(WorkItemLinkReachCut), string(WorkItemLinkReachFailed)}
}

// lazyLinkReach is one call's reach, read at its first read.
type lazyLinkReach struct {
	key       string
	principal storage.Principal
	telemetry GraphTelemetry
	once      sync.Once
	reach     map[string][]string
	err       error
	cut       bool
}

// withLinkReach adds a reach to ctx for a restricted caller. A project-only
// caller holds no repository list to anchor on, and an unrestricted or universal
// caller needs no link: both read nothing. A reach held on ctx is reused only by
// the same graph, organization and repository scopes: it is one caller's grant.
func (a *Adapter) withLinkReach(ctx context.Context, key string, principal storage.Principal) context.Context {
	if auth.ClassifyGrant(principal) != auth.GrantRestricted {
		return ctx
	}
	if held, ok := ctx.Value(linkReachContextKey{}).(*lazyLinkReach); ok && held.key == key && held.principal.OrgID == principal.OrgID &&
		slices.Equal(held.principal.RepositoryScopes, principal.RepositoryScopes) {
		return ctx
	}
	return context.WithValue(ctx, linkReachContextKey{}, &lazyLinkReach{key: key, principal: principal, telemetry: a.config.Telemetry})
}

func (l *lazyLinkReach) load(ctx context.Context, inner conn) {
	l.once.Do(func() {
		started := time.Now()
		outcome, read := WorkItemLinkReachFailed, 0
		defer func() {
			if l.telemetry != nil {
				l.telemetry.RecordWorkItemLinkReach(ctx, l.principal.OrgID, outcome, read, time.Since(started))
			}
		}()
		pullRequest := treeNodeVar{name: "p"}
		cypher := fmt.Sprintf("MATCH (w:%s {%s:$org, %s:$workItem})-[r:%s]->(p:%s {%s:$org, %s:$pullRequest}) "+
			"WHERE r.%s = $links AND r.%s = $native AND $noRepository IN w.%s AND %s "+
			"RETURN w.%s AS id, p.%s AS repos ORDER BY id LIMIT %d",
			labelSubject, propOrgID, propKind, labelRelation, labelSubject, propOrgID, propKind,
			propRelationType, propPropertyPrefix+linkTierProperty, propAuthzRepos, pullRequest.repositoryMatchClause("grant"),
			propCanonicalID, propAuthzRepos, linkReachMaxRows+1)
		params := map[string]interface{}{
			"org": strings.TrimSpace(l.principal.OrgID), "workItem": string(contractsv1.ContextFabricSubjectWorkItem),
			"pullRequest": string(contractsv1.ContextFabricSubjectPullRequest),
			"links":       string(contractsv1.ContextFabricRelationshipLinksPullRequest), "native": linkTiers[0].name, "noRepository": noRepositoryScope,
		}
		bindRepositoryMatch(params, "grant", l.principal.RepositoryScopes)
		// The reach read runs on the INNER conn: it is never rewritten.
		rows, err := inner.query(ctx, l.key, cypher, params, true)
		if err != nil {
			l.err = graphNotProjectedError(safeDependencyError("read work item link reach", err))
			return
		}
		outcome = WorkItemLinkReachRead
		if len(rows) > linkReachMaxRows {
			l.cut = true
			outcome = WorkItemLinkReachCut
			rows = rows[:linkReachMaxRows]
		}
		read = len(rows)
		sets := map[string]map[string]struct{}{}
		for _, row := range rows {
			id, _ := row["id"].(string)
			if id == "" {
				continue
			}
			for _, repository := range rowStringSlice(row, "repos") {
				// The statement's match is a necessary condition only: an
				// owner grant also matches a three-part slug by its prefix.
				// The map holds what the gate admits, so a slug outside the
				// grant never enters this process's attributes.
				if !auth.RepositoryAllowed(l.principal.RepositoryScopes, repository) {
					continue
				}
				if sets[id] == nil {
					sets[id] = map[string]struct{}{}
				}
				sets[id][repository] = struct{}{}
			}
		}
		l.reach = make(map[string][]string, len(sets))
		for id, set := range sets {
			list := make([]string, 0, len(set))
			for repository := range set {
				list = append(list, repository)
			}
			sort.Strings(list)
			l.reach[id] = list
		}
	})
}

// incomplete reports a read that failed or was cut: an item it does not hold
// is unknown, not unlinked.
func (l *lazyLinkReach) incomplete() bool { return l.err != nil || l.cut }

// rewrite adds the attribute to a repo-less work item the reach holds, walking
// lists (paths, collected nodes). The node is copied: a cached or shared node
// value is never mutated in place.
func (l *lazyLinkReach) rewrite(value interface{}) interface{} {
	switch v := value.(type) {
	case *node:
		if v == nil || propStringValue(v.Properties[propKind]) != string(contractsv1.ContextFabricSubjectWorkItem) || !repositoryLess(v) {
			return v
		}
		repositories, linked := l.reach[propStringValue(v.Properties[propCanonicalID])]
		if !linked {
			return v
		}
		properties := make(map[string]interface{}, len(v.Properties)+1)
		for key, property := range v.Properties {
			properties[key] = property
		}
		properties[graphrank.AuthorizationLinkRepositoriesAttr] = slices.Clone(repositories)
		copied := *v
		copied.Properties = properties
		return &copied
	case []interface{}:
		out := make([]interface{}, len(v))
		for index, item := range v {
			out[index] = l.rewrite(item)
		}
		return out
	default:
		return value
	}
}

// linkReachIncomplete reports the call's reach as failed or cut. False with no
// reach on ctx, and false until the first read.
func linkReachIncomplete(ctx context.Context) bool {
	lazy, ok := ctx.Value(linkReachContextKey{}).(*lazyLinkReach)
	return ok && lazy.incomplete()
}

// linkReachError is the failed read, for a decision that must not read it as a
// denial. A cut is not an error: the ids it holds are proven.
func linkReachError(ctx context.Context) error {
	if lazy, ok := ctx.Value(linkReachContextKey{}).(*lazyLinkReach); ok {
		return lazy.err
	}
	return nil
}

// repoLessNodesRead reports a repo-less work item among nodes.
func repoLessNodesRead(nodes map[string][]graphrank.CandidateNode) bool {
	for _, group := range nodes {
		for _, candidate := range group {
			if contextfabric.SubjectKind(graphrank.StringAttribute(candidate.Attributes, propKind)) != contextfabric.SubjectWorkItem {
				continue
			}
			if repositories, _ := candidate.Attributes[propAuthzRepos].([]string); len(repositories) == 1 && repositories[0] == noRepositoryScope {
				return true
			}
		}
	}
	return false
}
