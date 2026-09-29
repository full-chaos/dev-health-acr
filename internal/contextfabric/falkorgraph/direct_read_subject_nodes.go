package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ directread.SubjectNodeReader = (*Adapter)(nil)

// ReadSubjectNodes reads the stored Subject nodes for subjects, in the
// caller's own organization graph, at most storedSubjectBatch per statement
// (CHAOS-7126: labels for find_subjects handle candidates). A subject with
// no node is simply absent from the result. It applies no authorization:
// the caller gates every node.
func (a *Adapter) ReadSubjectNodes(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]directread.LookupNode, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	if len(subjects) == 0 {
		return nil, nil
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return nil, err
	}
	cypher := fmt.Sprintf("UNWIND $refs AS s MATCH (n:%s {%s:$org, %s:s.k, %s:s.i}) RETURN n",
		labelSubject, propOrgID, propKind, propCanonicalID)
	var out []directread.LookupNode
	for start := 0; start < len(subjects); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(subjects))
		refs := make([]interface{}, 0, end-start)
		for _, subject := range subjects[start:end] {
			refs = append(refs, map[string]interface{}{"k": string(subject.Kind), "i": subject.CanonicalID})
		}
		rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "refs": refs}, true)
		if err != nil {
			return nil, safeDependencyError("read subject nodes", err)
		}
		for _, row := range rows {
			n, ok := row["n"].(*node)
			if !ok || n == nil {
				continue
			}
			out = append(out, directread.LookupNode{
				Kind: propStringValue(n.Properties[propKind]), CanonicalID: propStringValue(n.Properties[propCanonicalID]),
				Label: propStringValue(n.Properties[propLabel]), Attributes: copyProperties(n.Properties),
			})
		}
	}
	return out, nil
}
