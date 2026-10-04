package devhealthfacts

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// WorkItemStatusVocabulary returns the closed set of normalized work-item
// status values; contextfabric owns it.
func WorkItemStatusVocabulary() []string { return contextfabric.WorkItemStatusVocabulary() }

const (
	// statusBasisNormalized is the basis of an item whose provider the read
	// could not name: only the vocabulary statement is true for it.
	statusBasisNormalized = "dev_health_normalized"

	statusProvenanceVocabulary = "Status is expected to be one of the sync's normalized vocabulary of eight values (backlog, todo, in_progress, in_review, blocked, done, canceled, unknown), not the provider's own status string; a missing status is null and a value outside the set is served as read, and status_in_vocabulary says which. "

	// statusProvenanceNote is the disclosure for an item whose provider is not
	// known: a statement about the vocabulary, true for every item.
	statusProvenanceNote = statusProvenanceVocabulary +
		"Its basis varies by provider: jira from a status-mapping configuration, github and gitlab from issue labels and open or closed state (a mapping, not a provider fact), linear from the workflow state type. " +
		"The provider of this item is not carried by this read."
)

// statusBasisByProvider is the closed table of status bases. The provider is
// read per item from work_items.provider; a provider outside the table is
// served with statusBasisNormalized and the provider-free note.
var statusBasisByProvider = map[string]struct{ basis, note string }{
	"jira":   {"status_mapping_configuration", "The provider of this item is jira: its status comes from a status-mapping configuration."},
	"github": {"issue_labels_and_state", "The provider of this item is github: its status is mapped from issue labels and open or closed state (a mapping, not a provider fact)."},
	"gitlab": {"issue_labels_and_state", "The provider of this item is gitlab: its status is mapped from issue labels and open or closed state (a mapping, not a provider fact)."},
	"linear": {"workflow_state_type", "The provider of this item is linear: its status comes from the workflow state type."},
}

// statusBasisFor returns the basis and the provenance note of one item from
// its provider, in the case the stored value was written in.
func statusBasisFor(provider string) (basis, note string) {
	if entry, ok := statusBasisByProvider[strings.ToLower(strings.TrimSpace(provider))]; ok {
		return entry.basis, statusProvenanceVocabulary + entry.note
	}
	return statusBasisNormalized, statusProvenanceNote
}

// statusInVocabularyValue reports whether a served status is a member of the
// closed set: null when the status is missing, false when it is outside the
// set (served as read, never mapped).
func statusInVocabularyValue(status string) contextfabric.FactValue {
	if status == "" {
		return contextfabric.NullFactValue()
	}
	return contextfabric.BooleanFactValue(contextfabric.InWorkItemStatusVocabulary(status))
}
