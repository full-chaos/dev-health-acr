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

var _ directread.InactiveTeamAuthority = (*Adapter)(nil)

// maxTwinCandidates bounds the active teams read to find the twin of an
// inactive team; more than one match is ambiguous and names none.
const maxTwinCandidates = 2

// InactiveTeams implements directread.InactiveTeamAuthority. For each team
// subject it reads the keyed node; a node the source marks inactive that the
// shared node predicate would admit this caller to is reported inactive, with
// the one active team of the same organization that carries the same name as
// its twin when that twin is admitted to the caller too. Any other subject,
// including one the caller may not read, is reported with the zero value.
func (a *Adapter) InactiveTeams(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]directread.InactiveTeam, error) {
	out := make([]directread.InactiveTeam, len(subjects))
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
	keyed := fmt.Sprintf("UNWIND $targets AS t MATCH (n:%s {%s:$org, %s:t.kind, %s:t.id}) RETURN n",
		labelSubject, propOrgID, propKind, propCanonicalID)
	now := a.now()
	current := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
	for start := 0; start < len(subjects); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(subjects))
		targets := make([]interface{}, 0, end-start)
		for _, subject := range subjects[start:end] {
			targets = append(targets, map[string]interface{}{"kind": string(contractsv1.ContextFabricSubjectTeam), "id": subject.CanonicalID})
		}
		rows, err := a.api.query(ctx, key, keyed, map[string]interface{}{"org": orgID, "targets": targets}, true)
		if err != nil {
			return nil, graphNotProjectedError(safeDependencyError("inactive team lookup", err))
		}
		byID := map[string]*node{}
		for _, r := range rows {
			if n, ok := r["n"].(*node); ok && n != nil && inactiveTeamNode(n) {
				byID[propStringValue(n.Properties[propCanonicalID])] = n
			}
		}
		for offset, subject := range subjects[start:end] {
			n := byID[subject.CanonicalID]
			if n == nil {
				continue
			}
			candidate := toCandidateNode(n)
			decided := graphrank.AuthorizeStoredSubjectNodes(principal, []contextfabric.SubjectRef{subject}, map[string][]graphrank.CandidateNode{graphrank.SubjectKey(subject): {candidate}})
			if len(decided) != 1 || decided[0] != contextfabric.StoredSubjectAdmitted {
				continue
			}
			result := directread.InactiveTeam{Inactive: true}
			twin, err := a.activeTwin(ctx, principal, key, orgID, candidate, current)
			if err != nil {
				return nil, err
			}
			result.ActiveTwinID = twin
			out[start+offset] = result
		}
	}
	return out, nil
}

// activeTwin is the canonical id of the one active team that carries the
// inactive team's name and that the caller may read, or "" when there is none
// or more than one.
func (a *Adapter) activeTwin(ctx context.Context, principal storage.Principal, key, orgID string, inactive graphrank.CandidateNode, temporal temporalFilter) (string, error) {
	term := strings.TrimSpace(inactive.Name)
	if term == "" {
		term = strings.TrimSpace(graphrank.StringAttribute(inactive.Attributes, propLabel))
	}
	if term == "" {
		return "", nil
	}
	rows, err := a.exactNameKindPage(ctx, key, orgID, string(contractsv1.ContextFabricSubjectTeam), term, "", maxTwinCandidates, temporal)
	if err != nil {
		return "", err
	}
	twins := make([]string, 0, maxTwinCandidates)
	for _, r := range rows {
		n, ok := r["n"].(*node)
		if !ok || n == nil || inactiveTeamNode(n) ||
			propStringValue(n.Properties[propOrgID]) != orgID ||
			propStringValue(n.Properties[propKind]) != string(contractsv1.ContextFabricSubjectTeam) {
			continue
		}
		candidate := toCandidateNode(n)
		if exactNameMatchClass(term, candidate) == "" {
			continue
		}
		subject := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: propStringValue(n.Properties[propCanonicalID])}
		decided := graphrank.AuthorizeStoredSubjectNodes(principal, []contextfabric.SubjectRef{subject}, map[string][]graphrank.CandidateNode{graphrank.SubjectKey(subject): {candidate}})
		if len(decided) == 1 && decided[0] == contextfabric.StoredSubjectAdmitted {
			twins = append(twins, subject.CanonicalID)
		}
	}
	if len(twins) != 1 {
		return "", nil
	}
	return twins[0], nil
}
