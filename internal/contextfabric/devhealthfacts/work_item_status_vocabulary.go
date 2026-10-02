package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// The work-item status Dev Health ops writes into work_items.status is a
// closed set of eight values (work_item_transitions.from_status and
// to_status use the same set). acr reads the column as is: it holds no
// category table and no synonyms.
var workItemStatusVocabulary = [...]string{
	"backlog", "todo", "in_progress", "in_review", "blocked", "done", "canceled", "unknown",
}

// WorkItemStatusVocabulary returns the closed set of normalized work-item
// status values, in the order ops declares them.
func WorkItemStatusVocabulary() []string {
	out := make([]string, len(workItemStatusVocabulary))
	copy(out, workItemStatusVocabulary[:])
	return out
}

const (
	statusBasisNormalized = "dev_health_normalized"

	// statusProvenanceNote is the vocabulary disclosure. The value is a
	// mapping Dev Health applied, not the provider's own status string, and
	// for two providers it is derived from labels.
	statusProvenanceNote = "Dev Health normalizes status into backlog, todo, in_progress, in_review, blocked, done, canceled or unknown. " +
		"github and gitlab values are derived from issue labels and state, so they are a mapping and not a provider fact. " +
		"jira values follow the organization's status mapping. linear values follow the workflow state type."
)

func inWorkItemStatusVocabulary(value string) bool {
	for _, member := range workItemStatusVocabulary {
		if member == value {
			return true
		}
	}
	return false
}

// workItemStatusFields is the fact's field map for one row: the normalized
// value plus the disclosure of what that value is. A value outside the
// closed set is served as read and flagged, never mapped.
func workItemStatusFields(status string) map[string]contextfabric.FactValue {
	inVocabulary := contextfabric.NullFactValue()
	if status != "" {
		inVocabulary = contextfabric.BooleanFactValue(inWorkItemStatusVocabulary(status))
	}
	return map[string]contextfabric.FactValue{
		"status":               stringOrNull(status),
		"status_basis":         contextfabric.StringFactValue(statusBasisNormalized),
		"status_in_vocabulary": inVocabulary,
		"status_provenance":    contextfabric.StringFactValue(statusProvenanceNote),
	}
}
