package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ directread.SubjectGraph = (*Adapter)(nil)

// DefaultExactNameKinds are the kinds a name lookup searches when the caller
// names none: the closed exact-name census set (repository, project, team),
// whose whole population fits one bounded read. A high-population kind must
// be named by the caller and is bounded per kind.
func DefaultExactNameKinds() []string { return append([]string(nil), exactNameKinds...) }

// ListSubjectsByKind implements directread.SubjectGraph for find_subjects
// mode list (CHAOS-7036 C.4, S1a). One keyset page of the caller's own
// organization graph: nodes of one kind, ordered by canonical id, after
// afterCanonicalID. It asks for one row more than pageSize and reports More
// when that row comes back, the same rule cohortKindCensusCandidates uses.
// Only nodes valid now are listed. No embedding model is touched.
func (a *Adapter) ListSubjectsByKind(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, kind, afterCanonicalID string, pageSize int) (directread.LookupPage, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return directread.LookupPage{}, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	if pageSize < 1 || pageSize > directread.MaxLookupPageSize {
		return directread.LookupPage{}, fmt.Errorf("%w: lookup page size %d out of range", contextfabric.ErrUnavailable, pageSize)
	}
	if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
		return directread.LookupPage{}, fmt.Errorf("%w: unknown subject kind", contextfabric.ErrUnavailable)
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return directread.LookupPage{}, err
	}
	now := a.now()
	current := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
	after := ""
	if afterCanonicalID != "" {
		after = fmt.Sprintf(" AND n.%s > $after", propCanonicalID)
	}
	cypher := fmt.Sprintf("MATCH (n:%s) WHERE n.%s = $org AND n.%s = $kind%s%s RETURN n ORDER BY n.%s LIMIT %d",
		labelSubject, propOrgID, propKind, after, current.predicate("n"), propCanonicalID, pageSize+1)
	params := map[string]interface{}{"org": orgID, "kind": kind}
	if afterCanonicalID != "" {
		params["after"] = afterCanonicalID
	}
	rows, err := a.api.query(ctx, key, cypher, current.bind(params), true)
	if err != nil {
		return directread.LookupPage{}, graphNotProjectedError(safeDependencyError("list subjects by kind", err))
	}
	page := directread.LookupPage{More: len(rows) > pageSize}
	if page.More {
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil {
			continue
		}
		// A node of another kind or organization is never returned, even
		// if the store answered one: the read is exact on both keys.
		if propStringValue(n.Properties[propOrgID]) != orgID || propStringValue(n.Properties[propKind]) != kind {
			continue
		}
		page.Nodes = append(page.Nodes, lookupNode(n, ""))
	}
	return page, nil
}

// FindSubjectsByExactName implements directread.SubjectGraph for find_subjects
// mode name. Per requested kind it fetches the kind's nodes under the census
// pool bound (ordered by canonical id, one row more than the bound, the shape
// chaos4348ExactNameCandidates and cohortKindCensusCandidates share), then
// keeps the nodes whose label, alias or provider key equals query. The
// equality rule is graphrank's own (label, alias, provider alias; case
// folded, trimmed), so this arm agrees with the engine's exact-name arm.
// Truncated is true when any kind hit the bound. No embedding model is
// touched: the vector arm is off (K6).
func (a *Adapter) FindSubjectsByExactName(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query string, kinds []string) (directread.LookupPage, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return directread.LookupPage{}, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return directread.LookupPage{}, nil
	}
	if len(kinds) == 0 {
		kinds = DefaultExactNameKinds()
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return directread.LookupPage{}, err
	}
	now := a.now()
	current := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
	var page directread.LookupPage
	seenKinds := map[string]struct{}{}
	for _, kind := range kinds {
		if _, dup := seenKinds[kind]; dup {
			continue
		}
		seenKinds[kind] = struct{}{}
		if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
			return directread.LookupPage{}, fmt.Errorf("%w: unknown subject kind", contextfabric.ErrUnavailable)
		}
		candidates, truncated, err := a.exactNameKindPool(ctx, key, orgID, kind, current)
		if err != nil {
			return directread.LookupPage{}, err
		}
		page.Truncated = page.Truncated || truncated
		for _, candidate := range candidates {
			if match := exactNameMatchClass(query, candidate); match != "" {
				page.Nodes = append(page.Nodes, lookupNodeFromCandidate(candidate, match))
			}
		}
	}
	sort.SliceStable(page.Nodes, func(i, j int) bool {
		if page.Nodes[i].CanonicalID != page.Nodes[j].CanonicalID {
			return page.Nodes[i].CanonicalID < page.Nodes[j].CanonicalID
		}
		return page.Nodes[i].Kind < page.Nodes[j].Kind
	})
	return page, nil
}

func (a *Adapter) exactNameKindPool(ctx context.Context, key, orgID, kind string, temporal temporalFilter) ([]graphrank.CandidateNode, bool, error) {
	cypher := fmt.Sprintf("MATCH (n:%s) WHERE n.%s = $org AND n.%s = $kind%s RETURN n ORDER BY n.%s LIMIT %d",
		labelSubject, propOrgID, propKind, temporal.predicate("n"), propCanonicalID, exactNameCandidateQueryLimit+1)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kind": kind}), true)
	if err != nil {
		return nil, false, graphNotProjectedError(safeDependencyError("find subjects by exact name", err))
	}
	truncated := len(rows) > exactNameCandidateQueryLimit
	if truncated {
		rows = rows[:exactNameCandidateQueryLimit]
	}
	out := make([]graphrank.CandidateNode, 0, len(rows))
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil {
			continue
		}
		if propStringValue(n.Properties[propOrgID]) != orgID || propStringValue(n.Properties[propKind]) != kind {
			continue
		}
		out = append(out, toCandidateNode(n))
	}
	return out, truncated, nil
}

// exactNameMatchClass classifies how query equals the node: exact (label or
// name), alias, provider_key, or "" for no equality. The order and the
// equality (trim, case fold) are graphrank.exactNameMatches' own.
func exactNameMatchClass(query string, node graphrank.CandidateNode) string {
	term := strings.TrimSpace(query)
	if term == "" {
		return ""
	}
	label := strings.TrimSpace(graphrank.StringAttribute(node.Attributes, propLabel))
	if strings.EqualFold(term, node.Name) || strings.EqualFold(term, label) {
		return string(contractsv1.ContextFabricMatchExact)
	}
	for _, alias := range graphrank.AliasAttributes(node.Attributes) {
		if strings.EqualFold(term, alias) {
			return string(contractsv1.ContextFabricMatchAlias)
		}
	}
	for _, alias := range graphrank.ProviderAliasAttributes(node.Attributes) {
		if strings.EqualFold(term, alias) {
			return string(contractsv1.ContextFabricMatchProviderKey)
		}
	}
	return ""
}

func lookupNode(n *node, match string) directread.LookupNode {
	return lookupNodeFromCandidate(toCandidateNode(n), match)
}

func lookupNodeFromCandidate(candidate graphrank.CandidateNode, match string) directread.LookupNode {
	return directread.LookupNode{
		Kind:        propStringValue(candidate.Attributes[propKind]),
		CanonicalID: propStringValue(candidate.Attributes[propCanonicalID]),
		Label:       propStringValue(candidate.Attributes[propLabel]),
		Match:       match,
		Attributes:  candidate.Attributes,
	}
}
