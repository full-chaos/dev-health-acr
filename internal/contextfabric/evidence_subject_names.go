package contextfabric

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// evidenceSubjectNames is the name the answer already holds for a repository, a
// project or an organization an evidence ref names by its raw id (CHAOS-12620):
// from the committed subjects, the candidates, the cohort's members, the path nodes
// and the claimed facts' subjects. A label that is the subject's own raw id is no
// name. Nothing is read: without a name in the answer the label is the noun alone.
func evidenceSubjectNames(result InvestigationResult) contractsv1.ContextFabricEvidenceSubjectName {
	names := map[string]string{}
	keep := func(subject SubjectRef) {
		label := strings.TrimSpace(subject.Label)
		if subject.CanonicalID == "" || label == "" || label == subject.CanonicalID {
			return
		}
		if _, held := names[subject.CanonicalID]; !held {
			names[subject.CanonicalID] = label
		}
	}
	for _, subject := range result.SubjectResolution.Committed {
		keep(subject)
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		keep(candidate.Subject)
	}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			keep(member.Subject)
		}
	}
	for _, path := range result.Paths {
		for _, node := range path.Nodes {
			keep(node)
		}
	}
	for _, fact := range result.ClaimedFacts {
		keep(fact.Subject)
	}
	return func(kind contractsv1.ContextFabricSubjectKind, rawID string) (string, bool) {
		var canonicalID string
		switch kind {
		case SubjectRepository:
			canonicalID = RepositoryCanonicalID(rawID)
		case SubjectOrganization:
			canonicalID = OrganizationCanonicalID(rawID)
		case SubjectProject:
			provider, id, found := strings.Cut(rawID, ":")
			if !found {
				return "", false
			}
			derived, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
			if err != nil || omitted {
				return "", false
			}
			canonicalID = derived
		default:
			return "", false
		}
		name, ok := names[canonicalID]
		if !ok || name == rawID {
			return "", false
		}
		return name, true
	}
}

// readerEvidenceRefLabels is a result's evidence labels for a reader: a label
// saved before CHAOS-12620 is restated from its ref with the names the result
// holds, and any other label stands (contractsv1.ContextFabricEvidenceRefStoredLabel).
// The result's own map is not changed; a nil map stays nil, the ruled "this result
// predates labels" case.
func readerEvidenceRefLabels(result InvestigationResult) map[string]string {
	if result.EvidenceRefLabels == nil {
		return nil
	}
	names := evidenceSubjectNames(result)
	labels := make(map[string]string, len(result.EvidenceRefLabels))
	for ref, label := range result.EvidenceRefLabels {
		labels[ref] = contractsv1.ContextFabricEvidenceRefStoredLabel(ref, label, names)
	}
	return labels
}
