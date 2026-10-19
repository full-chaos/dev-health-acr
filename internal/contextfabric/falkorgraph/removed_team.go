package falkorgraph

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// removedTeamProperty is the node property the team projector writes from
// teams.gone_at: set when a sync proved the team gone at its provider.
// maxLoggedRemovedTeams bounds the ids named on the lookup line.
const maxLoggedRemovedTeams = 10

const removedTeamProperty = propPropertyPrefix + "gone_at"

// removedCommittedTeams returns the committed team subjects whose node carries
// gone_at and that the caller may read, in commit order. A removed team stays
// active and its edges stay served, so the answer flags it. The second result
// is true when the read failed: the flag is then unknown, not absent.
func (a *Adapter) removedCommittedTeams(ctx context.Context, key string, principal storage.Principal, committed []contextfabric.SubjectRef) ([]contextfabric.SubjectRef, bool) {
	var teams []contextfabric.SubjectRef
	for _, subject := range committed {
		if subject.Kind == contextfabric.SubjectTeam {
			teams = append(teams, subject)
		}
	}
	if len(teams) == 0 {
		return nil, false
	}
	var removed []contextfabric.SubjectRef
	keyed := fmt.Sprintf("UNWIND $targets AS t MATCH (n:%s {%s:$org, %s:t.kind, %s:t.id}) RETURN n",
		labelSubject, propOrgID, propKind, propCanonicalID)
	for start := 0; start < len(teams); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(teams))
		targets := make([]interface{}, 0, end-start)
		for _, subject := range teams[start:end] {
			targets = append(targets, map[string]interface{}{"kind": string(contractsv1.ContextFabricSubjectTeam), "id": subject.CanonicalID})
		}
		rows, err := a.api.query(ctx, key, keyed, map[string]interface{}{"org": principal.OrgID, "targets": targets}, true)
		if err != nil {
			slog.Default().WarnContext(ctx, "context_fabric: removed team lookup failed", "teams", len(teams[start:end]), "error_class", "graph_query")
			return nil, true
		}
		gone := map[string]*node{}
		for _, r := range rows {
			if n, ok := r["n"].(*node); ok && n != nil && strings.TrimSpace(propStringValue(n.Properties[removedTeamProperty])) != "" {
				gone[propStringValue(n.Properties[propCanonicalID])] = n
			}
		}
		for _, subject := range teams[start:end] {
			n := gone[subject.CanonicalID]
			if n == nil {
				continue
			}
			decided := graphrank.AuthorizeStoredSubjectNodes(principal, []contextfabric.SubjectRef{subject}, map[string][]graphrank.CandidateNode{graphrank.SubjectKey(subject): {toCandidateNode(n)}})
			if len(decided) == 1 && decided[0] == contextfabric.StoredSubjectAdmitted {
				removed = append(removed, subject)
			}
		}
	}
	removedIDs := make([]string, 0, min(len(removed), maxLoggedRemovedTeams))
	for _, subject := range removed[:min(len(removed), maxLoggedRemovedTeams)] {
		removedIDs = append(removedIDs, subject.CanonicalID)
	}
	slog.Default().InfoContext(ctx, "context_fabric: removed team lookup", "teams", len(teams), "removed", len(removed), "removed_ids", contextfabric.SanitizeLogStrings(removedIDs))
	return removed, false
}
