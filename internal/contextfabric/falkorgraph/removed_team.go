package falkorgraph

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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

// removedCohortTeams are the team members of the served cohort whose node,
// already read for the cohort, carries gone_at: a team served as a member is as
// much a team the answer speaks about as a committed one, and its node needs no
// second read.
func removedCohortTeams(cohort *contextfabric.Cohort, nodes []graphrank.CandidateNode) []contextfabric.SubjectRef {
	if cohort == nil || cohort.Kind != contextfabric.SubjectTeam {
		return nil
	}
	served := make(map[contextfabric.SubjectRef]bool, len(cohort.Members))
	for _, member := range cohort.Members {
		served[member.Subject] = true
	}
	var removed []contextfabric.SubjectRef
	for _, n := range nodes {
		subject, ok := graphrank.NodeSubject(n)
		if !ok || subject.Kind != contextfabric.SubjectTeam || strings.TrimSpace(propStringValue(n.Attributes[removedTeamProperty])) == "" {
			continue
		}
		for member := range served {
			if member.Kind == subject.Kind && member.CanonicalID == subject.CanonicalID && !slices.Contains(removed, member) {
				removed = append(removed, member)
			}
		}
	}
	return removed
}

// removedCommittedTeams returns the team subjects given whose node carries
// gone_at and that the caller may read, in the order given. A removed team stays
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

var _ contextfabric.RemovedTeamReader = (*Adapter)(nil)

// RemovedTeams implements contextfabric.RemovedTeamReader: the bounded keyed
// read behind a reuse hit, with the same authorization as DiscoverContext's.
func (a *Adapter) RemovedTeams(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.SubjectRef, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return nil, fmt.Errorf("%w: authenticated organization is required", contextfabric.ErrUnavailable)
	}
	key, err := a.effectiveKey(ctx, orgID, binding)
	if err != nil {
		return nil, err
	}
	ctx, err = a.withProjectReach(ctx, key, principal)
	if err != nil {
		return nil, err
	}
	removed, failed := a.removedCommittedTeams(ctx, key, principal, subjects)
	if failed {
		return nil, graphNotProjectedError(fmt.Errorf("removed team lookup failed"))
	}
	return removed, nil
}
