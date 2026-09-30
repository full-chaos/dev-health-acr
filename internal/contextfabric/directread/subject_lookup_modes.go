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

// ErrFindScopeRequired: handle mode cannot serve this repository-restricted
// caller within its bound (the grant holds more than MaxHandleGrantRepositories
// readable repositories, or the grant listing is incomplete). A typed
// refusal that carries no count; the route answers invalid_request with
// reason scope_required.
var ErrFindScopeRequired = errors.New("find_subjects: scope_required")

// MaxHandleGrantRepositories bounds the per-repository census a
// repository-restricted handle lookup runs (one census per granted
// repository; the census takes one anchor, not a set). Past it the lookup is
// refused (ErrFindScopeRequired), never widened to the organization.
const MaxHandleGrantRepositories = 50

// CensusAnchorSupport reports whether the census can scope kind to one
// subject of anchorKind (devhealthsource.CensusAnchorSupported is the
// production one).
type CensusAnchorSupport func(kind graphrank.CensusKind, anchorKind contextfabric.SubjectKind) bool

// WithCensusAnchorSupport lets a repository-restricted handle lookup decide,
// from the handle's kind alone and before any grant listing or census,
// whether it can be served inside a repository grant (CHAOS-7160 r1). It
// returns l.
func (l *SubjectLookup) WithCensusAnchorSupport(support CensusAnchorSupport) *SubjectLookup {
	if l != nil {
		l.anchorSupport = support
	}
	return l
}

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

// errFindHandleBoundCount marks the "handle must name exactly one ..." shape
// failure. Internal: Find maps it per caller class (CHAOS-7200).
var errFindHandleBoundCount = errors.New("find_subjects: handle bound count")

func planHandle(plan findPlan, handle string) (findPlan, error) {
	plan.mode = FindModeHandle
	if len(plan.kinds) > 0 {
		return plan, fmt.Errorf("%w: handle mode takes no kind; the handle names it", ErrFindInvalidRequest)
	}
	if len([]rune(handle)) > MaxFindQueryRunes {
		return plan, fmt.Errorf("%w: handle is too long", ErrFindInvalidRequest)
	}
	bound := graphrank.BindHandles(handle)
	if len(bound) == 0 {
		// CHAOS-7159: one whole-text key of any prefix; the org census
		// decides existence (registry and free-text grammar unchanged).
		if key, ok := graphrank.BindWorkItemKey(handle); ok {
			bound = []graphrank.BoundHandle{key}
		}
	}
	// Key SHAPE, any prefix (one grammar: graphrank). Find refuses a
	// repository-restricted caller on it before any binding or upstream call.
	plan.hasKeyToken = graphrank.HasWorkItemKeyToken(handle)
	if len(bound) != 1 {
		return plan, fmt.Errorf("%w: %w: handle must name exactly one pull request number, work item key or CI run id", ErrFindInvalidRequest, errFindHandleBoundCount)
	}
	plan.handle = bound[0]
	plan.kinds = []string{string(bound[0].Kind)}
	return plan, nil
}

// scanOwnedBy reads the team's OWNED_BY_TEAM in-edges whose owned end is a
// repository or project, through the same gates as read_relationships: the
// team takes a fresh subject-gate decision (spent here); every end node of
// every examined edge takes one; every edge passes EdgeGate. A refused team,
// like a missing one, gives an empty answer. The scan has no edge cap: it is
// bounded by the team's own repository and project OWNED_BY_TEAM edges (the
// end-kind filter), and a cap that counted withheld edges would make the
// status depend on edges the caller may not see (CHAOS-7126 r1 P1).
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
		if !page.More || len(page.Edges) == 0 {
			return out, false, nil
		}
		last := page.Edges[len(page.Edges)-1].Key
		if after != nil && !after.Less(last) {
			return nil, false, fmt.Errorf("owned_by edge page did not advance")
		}
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
	// A repository-restricted caller never reaches the organization-wide
	// census (CHAOS-7126 r1 P1): its status, truncation and even its timing
	// would depend on rows the caller may not read. It gets one census per
	// repository it may read, anchored on that repository, so every row the
	// census counts is in the caller's own grant. Unrestricted and universal
	// callers keep the one organization-wide census.
	anchors := []contextfabric.SubjectRef{{}}
	anchorRefused := false
	if plan.anchor != nil {
		// CHAOS-7158: one census, bound to the anchor. The anchor takes a
		// subject-gate decision like any candidate; a refused, unreadable or
		// missing anchor answers exactly like an anchor with no match (empty,
		// no count, no reason) and runs the same census. A restricted caller needs one census, not one
		// per granted repository; its candidates are still gated below.
		proof, decision := l.gate.Authorize(ctx, principal, []contextfabric.SubjectRef{*plan.anchor})
		if decision.Decision == DecisionUnavailable {
			return nil, false, fmt.Errorf("subject gate unavailable: %w", gateError(decision))
		}
		anchorRefused = proof.Len() == 0
		if plan.anchorDecision != nil {
			*plan.anchorDecision = "admitted"
			if anchorRefused {
				*plan.anchorDecision = "refused"
			}
		}
		if !anchorRefused {
			if err := proof.consume(ctx, principal, l.clock()); err != nil {
				return nil, false, fmt.Errorf("%w: %w", ErrUngatedRead, err)
			}
		}
		anchors = []contextfabric.SubjectRef{*plan.anchor}
	} else if ClassifyPrincipal(principal) == ClassRestricted {
		// Decided from the kind, before the grant listing and before any
		// census: the refusal cannot depend on how many repositories the
		// grant holds (r1 #701: an empty grant skipped the census and
		// answered empty). The in-loop check below stays as the backstop
		// for a lookup composed without this support.
		if l.anchorSupport != nil && !l.anchorSupport(plan.handle.Kind, contractsv1.ContextFabricSubjectRepository) {
			return nil, false, fmt.Errorf("%w: %s handles cannot be looked up inside a repository grant", ErrFindScopeRequired, plan.handle.Kind)
		}
		granted, err := NewGrantedRepositories(&SubjectLookup{graph: l.graph, gate: l.gate, now: l.now}).GrantedRepositories(ctx, principal)
		switch {
		case errors.Is(err, ErrGrantedRepositoriesIncomplete):
			return nil, false, fmt.Errorf("%w: the grant listing is incomplete", ErrFindScopeRequired)
		case err != nil:
			return nil, false, err
		case len(granted) > MaxHandleGrantRepositories:
			return nil, false, fmt.Errorf("%w: the grant holds more than %d repositories", ErrFindScopeRequired, MaxHandleGrantRepositories)
		}
		anchors = granted
	}
	var ids []string
	truncated := false
	for _, anchor := range anchors {
		outcome, err := l.census(ctx, principal.OrgID, plan.handle.Kind, plan.handle.Value, true, anchor.Kind, anchor.CanonicalID, anchor.CanonicalID != "")
		if plan.anchor != nil && errors.Is(err, graphrank.ErrCensusAnchorUnsupported) {
			return nil, false, fmt.Errorf("%w: a %s handle cannot be anchored on a %s", ErrFindInvalidRequest, plan.handle.Kind, plan.anchor.Kind)
		}
		if anchor.CanonicalID != "" && errors.Is(err, graphrank.ErrCensusAnchorUnsupported) {
			// The census cannot scope this handle kind to a repository (a
			// work item: Linear work items carry no repository). A
			// restricted credential is refused by type, never answered from
			// the organization-wide census and never as an outage.
			return nil, false, fmt.Errorf("%w: %s handles cannot be looked up inside a repository grant", ErrFindScopeRequired, plan.handle.Kind)
		}
		if err != nil {
			return nil, false, err
		}
		if anchorRefused {
			// A refused or missing anchor runs the same census as a readable
			// one and discards only the OUTCOME: a census error above answers
			// the same unavailable for all three anchor states (codex r2 P1),
			// and no response, status or timing seam tells them apart.
			return nil, false, nil
		}
		if outcome.Count == 0 {
			continue
		}
		var named []string
		switch {
		case outcome.ClosureMismatch:
		case outcome.Count == 1 && outcome.SatisfierCanonicalID != "":
			named = []string{outcome.SatisfierCanonicalID}
		case outcome.Count > 1 && !outcome.SatisfierSetClosureMismatch:
			named = outcome.SatisfierCanonicalIDs
		}
		if len(named) < outcome.Count {
			truncated = true
		}
		ids = append(ids, named...)
	}
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
