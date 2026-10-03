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

	// statusProvenanceNote is the vocabulary disclosure. It is a statement
	// about the vocabulary, true for every item: it never says which provider
	// this item came from, because the read does not carry the provider.
	statusProvenanceNote = "Status is the sync's normalized vocabulary of eight values (backlog, todo, in_progress, in_review, blocked, done, canceled, unknown), not the provider's own status string. " +
		"Its basis varies by provider: jira from a status-mapping configuration, github and gitlab from issue labels and open or closed state (a mapping, not a provider fact), linear from the workflow state type. " +
		"The provider of this item is not carried by this read."
)

func inWorkItemStatusVocabulary(value string) bool {
	for _, member := range workItemStatusVocabulary {
		if member == value {
			return true
		}
	}
	return false
}

// statusInVocabularyValue reports whether a served status is a member of the
// closed set: null when the status is missing, false when it is outside the
// set (served as read, never mapped).
func statusInVocabularyValue(status string) contextfabric.FactValue {
	if status == "" {
		return contextfabric.NullFactValue()
	}
	return contextfabric.BooleanFactValue(inWorkItemStatusVocabulary(status))
}
