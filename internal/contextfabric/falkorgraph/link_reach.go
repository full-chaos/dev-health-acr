package falkorgraph

import (
	"context"
	"fmt"
	"maps"
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
// Same pattern as project_reach.go, but read per page. Each wrapped read is
// followed by ONE keyed read of the repo-less work items that read returned, and
// each of them that a link joins to the grant gains an in-memory attribute that
// the shared predicate tests. Nothing is stored on a node, so nothing needs a
// rebuild, and a read site that is not wrapped keeps the sentinel and denies: a
// missed site fails CLOSED. The sentinel itself stays, so every walk keeps its
// own tier rule (repositoryLess, admitted).
//
// THE KEYED READ RUNS ONCE FOR EVERY WRAPPED READ, an empty page included, and
// its text is one constant. So a subject outside the grant changes no statement
// and no count of statements: a hidden item changes only the id list. Reading
// only when a page held an id would make the read itself the signal.
//
// A failed read admits nothing. It is recorded, the call goes on, and the turn
// discloses it. A stored-subject decision fails instead (stored_subjects.go): a
// read that failed is not a denial.

type linkReachContextKey struct{}

// WorkItemLinkReachOutcome is how the link reads of one adapter call ended
// (eventspec.WorkItemLinkReach).
type WorkItemLinkReachOutcome string

const (
	// WorkItemLinkReachRead: every read of the call finished.
	WorkItemLinkReachRead WorkItemLinkReachOutcome = "read"
	// WorkItemLinkReachFailed: at least one read returned an error.
	WorkItemLinkReachFailed WorkItemLinkReachOutcome = "failed"
)

// WorkItemLinkReachOutcomeVocabulary is the closed list of outcomes.
func WorkItemLinkReachOutcomeVocabulary() []string {
	return []string{string(WorkItemLinkReachRead), string(WorkItemLinkReachFailed)}
}

// WorkItemLinkReachTally is the sum of the link reads of one adapter call: 27
// reads in a turn is too loud for one Info line each. Counts only.
type WorkItemLinkReachTally struct {
	// Reads is the keyed reads issued, one for each wrapped read.
	Reads int
	// IDs is the ids the reads carried, zero for an empty page.
	IDs int
	// Rows is the link rows the reads returned, before the gate's own check.
	Rows int
	// Failed is the reads that returned an error.
	Failed int
	// Elapsed is the time of the reads added up.
	Elapsed time.Duration
}

// Outcome is failed when any read failed.
func (t WorkItemLinkReachTally) Outcome() WorkItemLinkReachOutcome {
	if t.Failed > 0 {
		return WorkItemLinkReachFailed
	}
	return WorkItemLinkReachRead
}

// linkReachStatement is the keyed read. It is one constant: the page and the
// grant are parameters, so no subject can change its text.
var linkReachStatement = func() string {
	pullRequest := treeNodeVar{name: "p"}
	return fmt.Sprintf("MATCH (w:%s {%s:$org, %s:$workItem})-[r:%s]->(p:%s {%s:$org, %s:$pullRequest}) "+
		"WHERE w.%s IN $ids AND r.%s = $links AND r.%s = $native AND $noRepository IN w.%s AND %s "+
		"RETURN w.%s AS id, p.%s AS repos",
		labelSubject, propOrgID, propKind, labelRelation, labelSubject, propOrgID, propKind,
		propCanonicalID, propRelationType, propPropertyPrefix+linkTierProperty, propAuthzRepos, pullRequest.repositoryMatchClause("grant"),
		propCanonicalID, propAuthzRepos)
}()

// linkReach is the state of one call's link reads: the caller they read for, the
// tally of its line, and the first failure, which the call discloses.
type linkReach struct {
	key       string
	principal storage.Principal
	telemetry GraphTelemetry
	mu        sync.Mutex
	tally     WorkItemLinkReachTally
	err       error
}

// withLinkReach adds a reach to ctx for a restricted caller. A project-only
// caller holds no repository list to match, and an unrestricted or universal
// caller needs no link: both read nothing. A reach held on ctx is reused only by
// the same graph, organization and repository scopes: it is one caller's grant.
func (a *Adapter) withLinkReach(ctx context.Context, key string, principal storage.Principal) context.Context {
	if auth.ClassifyGrant(principal) != auth.GrantRestricted {
		return ctx
	}
	if held, ok := ctx.Value(linkReachContextKey{}).(*linkReach); ok && held.key == key && held.principal.OrgID == principal.OrgID &&
		slices.Equal(held.principal.RepositoryScopes, principal.RepositoryScopes) {
		return ctx
	}
	return context.WithValue(ctx, linkReachContextKey{}, &linkReach{key: key, principal: principal, telemetry: a.config.Telemetry})
}

// read is the one keyed read of a page. It runs whatever the page holds. It runs
// on the INNER conn, so it is never rewritten. A failed read is a nil map, which
// admits nothing.
func (l *linkReach) read(ctx context.Context, inner conn, page []row) linkedRepositories {
	ids := repoLessWorkItemIDs(page)
	params := map[string]interface{}{
		"org": strings.TrimSpace(l.principal.OrgID), "workItem": string(contractsv1.ContextFabricSubjectWorkItem),
		"pullRequest": string(contractsv1.ContextFabricSubjectPullRequest), "ids": ids,
		"links": string(contractsv1.ContextFabricRelationshipLinksPullRequest), "native": linkTiers[0].name, "noRepository": noRepositoryScope,
	}
	bindRepositoryMatch(params, "grant", l.principal.RepositoryScopes)
	started := time.Now()
	rows, err := inner.query(ctx, l.key, linkReachStatement, params, true)
	elapsed := time.Since(started)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.tally.Reads++
	l.tally.IDs += len(ids)
	l.tally.Rows += len(rows)
	l.tally.Elapsed += elapsed
	if err != nil {
		l.tally.Failed++
		if l.err == nil {
			l.err = graphNotProjectedError(safeDependencyError("read work item link reach", err))
		}
		return nil
	}
	sets := map[string]map[string]struct{}{}
	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		for _, repository := range rowStringSlice(row, "repos") {
			// The statement's match is a necessary condition only: an owner
			// grant also matches a three-part slug by its prefix. The map holds
			// what the gate admits, so a slug outside the grant never enters
			// this process's attributes.
			if !auth.RepositoryAllowed(l.principal.RepositoryScopes, repository) {
				continue
			}
			if sets[id] == nil {
				sets[id] = map[string]struct{}{}
			}
			sets[id][repository] = struct{}{}
		}
	}
	reached := make(linkedRepositories, len(sets))
	for id, set := range sets {
		list := make([]string, 0, len(set))
		for repository := range set {
			list = append(list, repository)
		}
		sort.Strings(list)
		reached[id] = list
	}
	return reached
}

// reportLinkReach writes the line of one adapter call and starts a new tally, so
// a call inside a call writes its own reads and the outer one the rest. A call
// that read nothing writes nothing. The failure stays: the turn still owes its
// sentence.
func reportLinkReach(ctx context.Context) {
	l, ok := ctx.Value(linkReachContextKey{}).(*linkReach)
	if !ok {
		return
	}
	l.mu.Lock()
	tally := l.tally
	l.tally = WorkItemLinkReachTally{}
	l.mu.Unlock()
	if tally.Reads > 0 && l.telemetry != nil {
		l.telemetry.RecordWorkItemLinkReach(ctx, l.principal.OrgID, tally)
	}
}

// repoLessWorkItem is a node the keyed read is about.
func repoLessWorkItem(n *node) bool {
	return n != nil && propStringValue(n.Properties[propKind]) == string(contractsv1.ContextFabricSubjectWorkItem) && repositoryLess(n)
}

// repoLessWorkItemIDs is the sorted, distinct ids of the repo-less work items of
// a page, walking lists (paths, collected nodes). It is never nil: an empty page
// binds an empty list, not null.
func repoLessWorkItemIDs(page []row) []interface{} {
	seen := map[string]struct{}{}
	var collect func(value interface{})
	collect = func(value interface{}) {
		switch v := value.(type) {
		case *node:
			if repoLessWorkItem(v) {
				seen[propStringValue(v.Properties[propCanonicalID])] = struct{}{}
			}
		case []interface{}:
			for _, item := range v {
				collect(item)
			}
		}
	}
	for _, r := range page {
		for _, value := range r {
			collect(value)
		}
	}
	ids := make([]interface{}, 0, len(seen))
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		ids = append(ids, id)
	}
	return ids
}

// linkedRepositories is what one keyed read proved: a page's repo-less work item
// to the granted repositories its native links reach. It lives for one read.
type linkedRepositories map[string][]string

// rewrite adds the attribute to a repo-less work item the read holds, walking
// lists (paths, collected nodes). The node is copied: a cached or shared node
// value is never mutated in place.
func (l linkedRepositories) rewrite(value interface{}) interface{} {
	switch v := value.(type) {
	case *node:
		if !repoLessWorkItem(v) {
			return v
		}
		repositories, linked := l[propStringValue(v.Properties[propCanonicalID])]
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

// linkReachError is the call's first failed read: an item that read did not
// prove is unknown, not unlinked. A decision must not read it as a denial. Nil
// with no reach on ctx.
func linkReachError(ctx context.Context) error {
	l, ok := ctx.Value(linkReachContextKey{}).(*linkReach)
	if !ok {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
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
