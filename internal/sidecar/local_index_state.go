package sidecar

var localBundleWarningOrder = []string{
	"local_worktree_mismatch",
	"local_index_stale",
	"local_workspace_dirty",
	"changed_files_truncated",
	"local_query_budget_exhausted",
	"indexed_commit_unknown",
}

func validUsableLocalIndexState(status LocalIndexStatus, freshness LocalIndexFreshness) bool {
	if status == "" && freshness == "" {
		return true
	}
	return status == LocalIndexStatusAvailable && (freshness == LocalIndexFreshnessFresh || freshness == LocalIndexFreshnessUnknown) ||
		status == LocalIndexStatusDegraded && (freshness == LocalIndexFreshnessStale || freshness == LocalIndexFreshnessUnknown)
}

func validBundleWarnings(bundle LocalEvidenceBundle) bool {
	position := -1
	foundBudget := false
	foundUnknown := false
	for _, warning := range bundle.Warnings {
		index := -1
		for candidate, allowed := range localBundleWarningOrder {
			if warning == allowed {
				index = candidate
				break
			}
		}
		if index < 0 || index <= position {
			return false
		}
		position = index
		foundBudget = foundBudget || warning == string(LocalIndexErrorQueryBudgetExhausted)
		foundUnknown = foundUnknown || warning == string(LocalIndexErrorIndexedCommitUnknown)
	}
	if foundBudget != bundle.Truncated {
		return false
	}
	if bundle.Status != "" && foundUnknown != (bundle.IndexedCommit == "") {
		return false
	}
	if bundle.Status != "" && bundle.IndexedCommit == "" && (len(bundle.Warnings) == 0 || bundle.Warnings[len(bundle.Warnings)-1] != string(LocalIndexErrorIndexedCommitUnknown)) {
		return false
	}
	for _, warning := range bundle.Warnings {
		if warning == "local_worktree_mismatch" || warning == "local_index_stale" || warning == "local_workspace_dirty" || warning == "changed_files_truncated" {
			if bundle.Status != LocalIndexStatusDegraded || bundle.Freshness != LocalIndexFreshnessStale {
				return false
			}
		}
	}
	return true
}

func canonicalBundleWarnings(warnings []string, truncated bool, indexedCommit string) []string {
	found := make(map[string]bool, len(warnings))
	for _, warning := range warnings {
		found[warning] = true
	}
	found[string(LocalIndexErrorQueryBudgetExhausted)] = truncated
	found[string(LocalIndexErrorIndexedCommitUnknown)] = indexedCommit == ""
	output := make([]string, 0, len(localBundleWarningOrder))
	for _, warning := range localBundleWarningOrder {
		if found[warning] {
			output = append(output, warning)
		}
	}
	return output
}
