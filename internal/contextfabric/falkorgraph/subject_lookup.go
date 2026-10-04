package falkorgraph

import (
	"context"
	"fmt"
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
// mode name. For one kind it asks the store for the nodes whose label,
// alias or provider key equals query (the predicate is in the query, see
// exactNameKindPage), one keyset page in canonical-id order after
// afterCanonicalID. The caller pages through the matches and applies the
// subject gate to each page, so the bound on matches examined is the caller's
// and a match is found wherever it sorts. The equality is trim and lower-case
// (strings.ToLower), the same rule in the store query and in the Go check
// here; a name that differs only by a case fold the store maps differently
// (for example a final sigma) matches by exact case only. No embedding model is
// touched: the vector arm is off (K6).
func (a *Adapter) FindSubjectsByExactName(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, query, kind, afterCanonicalID string, pageSize int) (directread.LookupPage, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return directread.LookupPage{}, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	if pageSize < 1 || pageSize > directread.MaxLookupPageSize {
		return directread.LookupPage{}, fmt.Errorf("%w: lookup page size %d out of range", contextfabric.ErrUnavailable, pageSize)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return directread.LookupPage{}, nil
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
	rows, err := a.exactNameKindPage(ctx, key, orgID, kind, query, afterCanonicalID, pageSize, current)
	if err != nil {
		return directread.LookupPage{}, err
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
		if propStringValue(n.Properties[propOrgID]) != orgID || propStringValue(n.Properties[propKind]) != kind {
			continue
		}
		candidate := toCandidateNode(n)
		page.After = propStringValue(n.Properties[propCanonicalID])
		if match := exactNameMatchClass(query, candidate); match != "" {
			page.Nodes = append(page.Nodes, lookupNodeFromCandidate(candidate, match))
		}
	}
	return page, nil
}

// exactNameKindPage reads one keyset page (one row more than pageSize) of the
// nodes of kind whose label, alias or provider alias equals term, ordered by
// canonical id. The equality is pushed into the graph query, so the store
// examines every node of the kinds and the answer does not depend on where a
// match sorts. The predicate carries the raw term (case kept) next to the
// lower-cased one, so an exact-case name always matches itself.
func (a *Adapter) exactNameKindPage(ctx context.Context, key, orgID, kind, term, after string, pageSize int, temporal temporalFilter) ([]row, error) {
	afterClause := ""
	if after != "" {
		afterClause = fmt.Sprintf(" AND n.%s > $after", propCanonicalID)
	}
	cypher := fmt.Sprintf("MATCH (n:%[1]s) WHERE n.%[2]s = $org AND n.%[3]s = $kind%[4]s%[5]s AND (%[6]s) RETURN n ORDER BY n.%[7]s LIMIT %[8]d",
		labelSubject, propOrgID, propKind, temporal.predicate("n"), afterClause, exactNamePredicate("n"), propCanonicalID, pageSize+1)
	params := map[string]interface{}{"org": orgID, "kind": kind, "term": term, "termLower": strings.ToLower(term)}
	if after != "" {
		params["after"] = after
	}
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(params), true)
	if err != nil {
		return nil, graphNotProjectedError(safeDependencyError("find subjects by exact name", err))
	}
	return rows, nil
}

// exactNamePredicate is the label / alias / provider alias equality over node
// variable v, for $term and $termLower.
func exactNamePredicate(v string) string {
	return fmt.Sprintf("%[1]s.%[2]s = $term"+
		" OR ANY(a IN coalesce(%[1]s.%[3]s, []) WHERE a = $term)"+
		" OR ANY(a IN coalesce(%[1]s.%[4]s, []) WHERE a = $term)",
		v, propLabel, propAliases, propProviderAliases)
}

// exactNameMatchClass classifies how query equals the node: exact (label or
// name), alias, provider_key, or "" for no equality. The order and the
// equality (trim, case fold) are graphrank.exactNameMatches' own.
// sameName is the one name equality: trimmed, lower-cased (strings.ToLower),
// the rule the store query applies.
func sameName(term, name string) bool {
	return term == name || strings.ToLower(term) == strings.ToLower(strings.TrimSpace(name))
}

func exactNameMatchClass(query string, node graphrank.CandidateNode) string {
	term := strings.TrimSpace(query)
	if term == "" {
		return ""
	}
	label := strings.TrimSpace(graphrank.StringAttribute(node.Attributes, propLabel))
	if sameName(term, node.Name) || sameName(term, label) {
		return string(contractsv1.ContextFabricMatchExact)
	}
	for _, alias := range graphrank.AliasAttributes(node.Attributes) {
		if sameName(term, alias) {
			return string(contractsv1.ContextFabricMatchAlias)
		}
	}
	for _, alias := range graphrank.ProviderAliasAttributes(node.Attributes) {
		if sameName(term, alias) {
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
