package sidecar

import "slices"

// LocalIndexStatus reports whether local evidence is usable under its policy.
type LocalIndexStatus string

const (
	LocalIndexStatusAvailable   LocalIndexStatus = "available"
	LocalIndexStatusDegraded    LocalIndexStatus = "degraded"
	LocalIndexStatusUnavailable LocalIndexStatus = "unavailable"
)

// LocalIndexFreshness reports the index relationship to its workspace.
type LocalIndexFreshness string

const (
	LocalIndexFreshnessFresh   LocalIndexFreshness = "fresh"
	LocalIndexFreshnessStale   LocalIndexFreshness = "stale"
	LocalIndexFreshnessUnknown LocalIndexFreshness = "unknown"
)

type localIndexClassification struct {
	Status    LocalIndexStatus
	Freshness LocalIndexFreshness
	Warnings  []string
}

func (c localIndexClassification) omit(policy LocalIndexStalePolicy) bool {
	return policy == LocalIndexStaleStrict && (slices.Contains(c.Warnings, "local_index_stale") || slices.Contains(c.Warnings, "local_worktree_mismatch") || slices.Contains(c.Warnings, "local_workspace_dirty"))
}

func classifyCodeGraphWorkspace(workspace LocalWorkspaceSnapshot) localIndexClassification {
	if workspace.ChangedFilesState == LocalChangedFilesTruncated {
		return localIndexClassification{Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessStale, Warnings: []string{"changed_files_truncated"}}
	}
	return localIndexClassification{Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessUnknown}
}

func classifyCodeGraphStatus(status codeGraphStatus) localIndexClassification {
	warnings := make([]string, 0, 4)
	if status.WorktreeMismatch {
		warnings = append(warnings, "local_worktree_mismatch")
	}
	if status.ReindexRecommended || status.ExtractionMismatch || status.PendingChanges > 0 {
		warnings = append(warnings, "local_index_stale")
	}
	if status.PendingChanges > 0 {
		warnings = append(warnings, "local_workspace_dirty")
	}
	if status.IndexedCommit == "" {
		warnings = append(warnings, "indexed_commit_unknown")
	}
	freshness := LocalIndexFreshnessFresh
	if slices.Contains(warnings, "local_index_stale") || slices.Contains(warnings, "local_worktree_mismatch") || slices.Contains(warnings, "local_workspace_dirty") {
		freshness = LocalIndexFreshnessStale
	}
	state := LocalIndexStatusAvailable
	if freshness == LocalIndexFreshnessStale {
		state = LocalIndexStatusDegraded
	}
	return localIndexClassification{Status: state, Freshness: freshness, Warnings: warnings}
}
