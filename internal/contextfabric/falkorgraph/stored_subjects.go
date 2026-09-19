package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// storedSubjectBatch bounds one keyed lookup. A stored result can name a few
// hundred subjects (a full cohort plus its paths); batching keeps each query
// small without changing the decision.
const storedSubjectBatch = 256

var _ contextfabric.StoredSubjectAuthorizer = (*Adapter)(nil)

// AuthorizeStoredSubjects implements contextfabric.StoredSubjectAuthorizer.
// It reads each subject's node by the same org-scoped natural key the live
// exact-hint path reads, with no temporal filter (validity in time is not an
// authorization input), and hands the nodes to
// graphrank.AuthorizeStoredSubjectNodes, which applies the shared predicate.
func (a *Adapter) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]graphrank.CandidateNode, len(subjects))
	cypher := fmt.Sprintf("UNWIND $targets AS t MATCH (n:%s {%s:$org, %s:t.kind, %s:t.id}) RETURN n",
		labelSubject, propOrgID, propKind, propCanonicalID)
	for start := 0; start < len(subjects); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(subjects))
		targets := make([]interface{}, 0, end-start)
		for _, subject := range subjects[start:end] {
			targets = append(targets, map[string]interface{}{"kind": string(subject.Kind), "id": subject.CanonicalID})
		}
		rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "targets": targets}, true)
		if err != nil {
			return nil, graphNotProjectedError(safeDependencyError("authorize stored subjects", err))
		}
		for _, row := range rows {
			n, ok := row["n"].(*node)
			if !ok || n == nil {
				continue
			}
			subject := contextfabric.SubjectRef{
				Kind:        contextfabric.SubjectKind(propStringValue(n.Properties[propKind])),
				CanonicalID: propStringValue(n.Properties[propCanonicalID]),
			}
			nodes[graphrank.SubjectKey(subject)] = toCandidateNode(n)
		}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}
