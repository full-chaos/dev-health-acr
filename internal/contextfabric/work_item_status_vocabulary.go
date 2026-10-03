package contextfabric

// workItemStatusVocabulary is the closed set of normalized work-item status
// values Dev Health ops writes into work_items.status
// (work_item_transitions.from_status and to_status use the same set). acr
// holds no category table and no synonyms.
var workItemStatusVocabulary = [...]string{
	"backlog", "todo", "in_progress", "in_review", "blocked", "done", "canceled", "unknown",
}

// WorkItemStatusVocabulary returns the closed set, in the order ops
// declares it.
func WorkItemStatusVocabulary() []string {
	out := make([]string, len(workItemStatusVocabulary))
	copy(out, workItemStatusVocabulary[:])
	return out
}

// InWorkItemStatusVocabulary reports whether value is a member of the set.
func InWorkItemStatusVocabulary(value string) bool {
	for _, member := range workItemStatusVocabulary {
		if member == value {
			return true
		}
	}
	return false
}
