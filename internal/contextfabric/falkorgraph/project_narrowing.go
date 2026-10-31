package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ contextfabric.ProjectNarrowingResolver = (*Adapter)(nil)

// projectNarrowingLookupCypher finds, for each requested raw project id, the
// project nodes of the organization that carry it: a project node's
// authorization_projects is its own raw id (devhealthsource
// projectAuthorizationScope), and its canonical id is its project.v2
// identity. The organization filter is on the node, so another tenant's
// project never resolves.
var projectNarrowingLookupCypher = fmt.Sprintf(
	"UNWIND $ids AS rid MATCH (n:%s {%s:$org, %s:$kind}) WHERE rid IN n.%s RETURN rid, n.%s AS identity",
	labelSubject, propOrgID, propKind, propAuthzProjects, propCanonicalID)

// ResolveProjectNarrowing implements contextfabric.ProjectNarrowingResolver
// (CHAOS-12229): it resolves a request's raw project ids to the canonical
// project.v2 identities of the project nodes that carry them. A raw id that
// resolves to exactly one identity contributes it; one that resolves to none,
// or to more than one (the same raw id under two providers), contributes
// nothing and makes the resolution incomplete, so it is never widened to a
// guess. A requested "*" narrows nothing, and there is nothing to resolve.
// The resolution only lets stated membership decide; it never admits a node
// the request's raw ids and the caller's grants would not.
func (a *Adapter) ResolveProjectNarrowing(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, projectIDs []string) (*contractsv1.ContextFabricProjectNarrowing, error) {
	requested := distinctProjectIDs(projectIDs)
	if len(requested) == 0 {
		return nil, nil
	}
	for _, id := range requested {
		if id == "*" {
			return nil, nil
		}
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return nil, err
	}
	ids := make([]interface{}, 0, len(requested))
	for _, id := range requested {
		ids = append(ids, id)
	}
	rows, err := a.api.query(ctx, key, projectNarrowingLookupCypher, map[string]interface{}{
		"org": orgID, "kind": string(contractsv1.ContextFabricSubjectProject), "ids": ids,
	}, true)
	if err != nil {
		return nil, graphNotProjectedError(safeDependencyError("project narrowing lookup", err))
	}
	resolved := map[string]map[string]bool{}
	for _, row := range rows {
		rid, _ := row["rid"].(string)
		canonical, _ := row["identity"].(string)
		if rid == "" || !canonicalProjectIdentity(canonical) {
			continue
		}
		if resolved[rid] == nil {
			resolved[rid] = map[string]bool{}
		}
		resolved[rid][canonical] = true
	}
	narrowing := &contractsv1.ContextFabricProjectNarrowing{Identities: []string{}, Complete: true}
	seen := map[string]bool{}
	for _, id := range requested {
		identities := resolved[id]
		if len(identities) != 1 {
			narrowing.Complete = false
			continue
		}
		for canonical := range identities {
			if !seen[canonical] {
				seen[canonical] = true
				narrowing.Identities = append(narrowing.Identities, canonical)
			}
		}
	}
	sort.Strings(narrowing.Identities)
	return narrowing, nil
}

// canonicalProjectIdentity reports whether value is a canonical project.v2
// identity: the only form a stated membership holds, so the only form a
// narrowing compares. Review r2: the prefix and the segment count are not
// enough (project.v2::proj-1 has an empty provider); value must be exactly
// what identity.Derive makes of its two non-empty segments. A malformed one
// resolves nothing, so the narrowing is incomplete and never denies on it.
func canonicalProjectIdentity(value string) bool {
	segments, ok := identity.Segments(identity.KindProject, value)
	if !ok || len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return false
	}
	derived, omitted, err := identity.Derive(identity.KindProject, segments, nil)
	return err == nil && !omitted && derived == value
}

// distinctProjectIDs is projectIDs trimmed, without blanks or repeats, in
// order.
func distinctProjectIDs(projectIDs []string) []string {
	out := make([]string, 0, len(projectIDs))
	seen := map[string]bool{}
	for _, id := range projectIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
