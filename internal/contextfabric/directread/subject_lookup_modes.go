package directread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// find_subjects modes (CHAOS-7036 C.4; owned_by and handle are CHAOS-7126).
const (
	FindModeList    = "list"
	FindModeName    = "name"
	FindModeOwnedBy = "owned_by"
	FindModeHandle  = "handle"
)

// FindModeVocabulary is the closed set of find_subjects modes.
func FindModeVocabulary() [4]string {
	return [4]string{FindModeList, FindModeName, FindModeOwnedBy, FindModeHandle}
}

// OwnedByKinds are the subject kinds owned_by returns: the ends of a team's
// OWNED_BY_TEAM in-edges that are OWNED (repositories from
// team_repo_ownership, projects from team_project_ownership). A work item's
// OWNED_BY_TEAM edge is a team attribution, not ownership, and is not read.
func OwnedByKinds() []string { return []string{"project", "repository"} }

// ErrFindModeNotComposed: the deployment composed no edge graph (owned_by)
// or no census (handle). The tool answers unavailable.
var ErrFindModeNotComposed = errors.New("find_subjects mode not composed")

// WithOwnershipAndHandles composes the owned_by and handle modes. edges
// serves owned_by (the S3a bounded edge page); census and nodes serve
// handle (the engine's own census function and a node read for labels). A
// nil part leaves its mode unavailable (fail closed). It returns l.
func (l *SubjectLookup) WithOwnershipAndHandles(edges EdgeGraph, census graphrank.CensusFunc, nodes SubjectNodeReader) *SubjectLookup {
	if l == nil {
		return nil
	}
	if !storage.IsNil(edges) {
		l.edges = edges
	}
	l.census = census
	if !storage.IsNil(nodes) {
		l.nodes = nodes
	}
	return l
}

func planOwnedBy(plan findPlan, owner string) (findPlan, error) {
	plan.mode, plan.owner = FindModeOwnedBy, owner
	allowed := map[string]bool{}
	for _, kind := range OwnedByKinds() {
		allowed[kind] = true
	}
	for _, kind := range plan.kinds {
		if !allowed[kind] {
			return plan, fmt.Errorf("%w: owned_by returns repository and project only", ErrFindInvalidRequest)
		}
	}
	if len(plan.kinds) == 0 {
		plan.kinds = OwnedByKinds()
	}
	return plan, nil
}

func planHandle(plan findPlan, handle string) (findPlan, error) {
	plan.mode = FindModeHandle
	if len(plan.kinds) > 0 {
		return plan, fmt.Errorf("%w: handle mode takes no kind; the handle names it", ErrFindInvalidRequest)
	}
	if len([]rune(handle)) > MaxFindQueryRunes {
		return plan, fmt.Errorf("%w: handle is too long", ErrFindInvalidRequest)
	}
	bound := graphrank.BindHandles(handle)
	if len(bound) != 1 {
		return plan, fmt.Errorf("%w: handle must name exactly one pull request number, work item key or CI run id", ErrFindInvalidRequest)
	}
	plan.handle = bound[0]
	plan.kinds = []string{string(bound[0].Kind)}
	return plan, nil
}

// scanOwnedBy reads the team's OWNED_BY_TEAM in-edges whose owned end is a
// repository or project, through the same gates as read_relationships: the
// team takes a fresh subject-gate decision (spent here); every end node of
// every examined edge takes one; every edge passes EdgeGate. A refused team,
// like a missing one, gives an empty answer. At most MaxFindScanNodes edges
// are examined; past that the answer is truncated.
func (l *SubjectLookup) scanOwnedBy(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, plan findPlan) ([]FoundSubject, bool, error) {
	if l.edges == nil {
		return nil, false, fmt.Errorf("%w: owned_by", ErrFindModeNotComposed)
	}
	team := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: plan.owner}
	proof, decision := l.gate.Authorize(ctx, principal, []contextfabric.SubjectRef{team})
	if decision.Decision == DecisionUnavailable {
		return nil, false, fmt.Errorf("subject gate unavailable: %w", gateError(decision))
	}
	if proof.Len() == 0 {
		return nil, false, nil
	}
	if err := proof.consume(ctx, principal, l.clock()); err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrUngatedRead, err)
	}
	edges := &RelationshipsReader{gate: l.gate}
	var record RelationshipsReadRecord
	record.EdgesWithheld = map[EdgeWithheldReason]int{}
	seen := map[string]bool{}
	var out []FoundSubject
	var after *EdgeKey
	scanned := 0
	for {
		page, err := l.edges.DirectEdgePage(ctx, principal, binding, EdgePageQuery{
			Origins: []contextfabric.SubjectRef{team}, Types: []string{string(contractsv1.ContextFabricRelationshipOwnedByTeam)},
			Direction: EdgeDirectionIn, EndKinds: plan.kinds, After: after, Limit: MaxLookupPageSize, ValidAt: l.clock().UTC(),
		})
		if err != nil {
			return nil, false, err
		}
		admitted, err := edges.gateEnds(ctx, principal, page.Edges, nil, &record)
		if err != nil {
			return nil, false, err
		}
		for _, candidate := range page.Edges {
			if EdgeGate(principal, candidate.Attributes, admitted[graphrank.SubjectKey(candidate.From.Subject)], admitted[graphrank.SubjectKey(candidate.To.Subject)]) != EdgeVisible {
				continue
			}
			owned := candidate.From
			key := graphrank.SubjectKey(owned.Subject)
			if seen[key] {
				continue // two ownership sources are two edges, one subject
			}
			seen[key] = true
			end := servedEnd(owned)
			out = append(out, FoundSubject{Kind: end.Kind, CanonicalID: end.CanonicalID, Label: end.Label, Match: ""})
		}
		scanned += len(page.Edges)
		if !page.More || len(page.Edges) == 0 {
			return out, false, nil
		}
		if scanned >= MaxFindScanNodes {
			return out, true, nil
		}
		last := page.Edges[len(page.Edges)-1].Key
		after = &last
	}
}

// scanHandle looks the bound handle up with the engine's census (ClickHouse,
// organization-wide), reads the candidate nodes for their labels, and keeps
// only the candidates the subject gate admits.
//
// The census count is NEVER served: it counts rows the caller may not read,
// and "three pull requests #532 exist, you see one" discloses the other two.
// A refused candidate is dropped without a count (a deliberate asymmetry
// with read_relationships' edges_not_visible and read_facts' rows_withheld,
// which count withheld parts of an answer about a subject the caller may
// read). total_known and the ambiguous status therefore count admitted
// candidates only. A census that could not name its satisfiers (over the
// census budget, or a read race) answers truncated, with no number.
func (l *SubjectLookup) scanHandle(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, plan findPlan) ([]FoundSubject, bool, error) {
	if l.census == nil || l.nodes == nil {
		return nil, false, fmt.Errorf("%w: handle", ErrFindModeNotComposed)
	}
	outcome, err := l.census(ctx, principal.OrgID, plan.handle.Kind, plan.handle.Value, true, "", "", false)
	if err != nil {
		return nil, false, err
	}
	if outcome.Count == 0 {
		return nil, false, nil
	}
	var ids []string
	switch {
	case outcome.ClosureMismatch:
	case outcome.Count == 1 && outcome.SatisfierCanonicalID != "":
		ids = []string{outcome.SatisfierCanonicalID}
	case outcome.Count > 1 && !outcome.SatisfierSetClosureMismatch:
		ids = outcome.SatisfierCanonicalIDs
	}
	truncated := len(ids) < outcome.Count
	refs := make([]contextfabric.SubjectRef, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			refs = append(refs, contextfabric.SubjectRef{Kind: plan.handle.Kind, CanonicalID: id})
		}
	}
	if len(refs) == 0 {
		return nil, truncated, nil
	}
	nodes, err := l.nodes.ReadSubjectNodes(ctx, principal, binding, refs)
	if err != nil {
		return nil, false, err
	}
	for index := range nodes {
		nodes[index].Match = MatchProviderKey
	}
	admitted, err := l.gateNodes(ctx, principal, nodes)
	if err != nil {
		return nil, false, err
	}
	return admitted, truncated, nil
}

func (l *SubjectLookup) clock() time.Time {
	if l != nil && l.now != nil {
		return l.now()
	}
	return time.Now()
}
