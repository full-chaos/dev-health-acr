package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// WorkItemStatusVocabulary returns the closed set of normalized work-item
// status values; contextfabric owns it.
func WorkItemStatusVocabulary() []string { return contextfabric.WorkItemStatusVocabulary() }

const (
	statusBasisNormalized = "dev_health_normalized"

	// statusProvenanceNote is the vocabulary disclosure. It is a statement
	// about the vocabulary, true for every item: it never says which provider
	// this item came from, because the read does not carry the provider.
	statusProvenanceNote = "Status is expected to be one of the sync's normalized vocabulary of eight values (backlog, todo, in_progress, in_review, blocked, done, canceled, unknown), not the provider's own status string; a missing status is null and a value outside the set is served as read, and status_in_vocabulary says which. " +
		"Its basis varies by provider: jira from a status-mapping configuration, github and gitlab from issue labels and open or closed state (a mapping, not a provider fact), linear from the workflow state type. " +
		"The provider of this item is not carried by this read."
)

// statusInVocabularyValue reports whether a served status is a member of the
// closed set: null when the status is missing, false when it is outside the
// set (served as read, never mapped).
func statusInVocabularyValue(status string) contextfabric.FactValue {
	if status == "" {
		return contextfabric.NullFactValue()
	}
	return contextfabric.BooleanFactValue(contextfabric.InWorkItemStatusVocabulary(status))
}
