package main

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

var (
	loadDoctorLocalIndexConfig = sidecar.LoadLocalIndexConfig
	discoverDoctorWorkspace    = sidecar.DiscoverWorkspace
	newDoctorLocalProvider     = sidecar.NewWorkspaceLocalIndexProvider
)

type localIndexReport struct {
	ProviderMode                string `json:"provider_mode"`
	ConfigValid                 bool   `json:"config_valid"`
	WorkspaceDiscovered         bool   `json:"workspace_discovered"`
	RepositoryIdentityAvailable bool   `json:"repository_identity_available"`
	WorkspaceScopeValid         bool   `json:"workspace_scope_valid"`
	IndexChecked                bool   `json:"index_checked"`
	IndexReadable               bool   `json:"index_readable"`
	Available                   bool   `json:"available"`
	ProviderVersion             string `json:"provider_version,omitempty"`
	VersionChecked              bool   `json:"version_checked"`
	VersionCompatible           bool   `json:"version_compatible"`
	Status                      string `json:"status"`
	Freshness                   string `json:"freshness"`
	MaxItems                    int    `json:"max_items"`
	MaxOutputTokens             int    `json:"max_output_tokens"`
	WorktreeMismatchChecked     bool   `json:"worktree_mismatch_checked"`
	WorktreeMismatchDetected    bool   `json:"worktree_mismatch_detected"`
	QueryChecked                bool   `json:"query_checked"`
	QuerySucceeded              bool   `json:"query_succeeded"`
	ResultCount                 int    `json:"result_count"`
	IndexedCommitStatus         string `json:"indexed_commit_status,omitempty"`
	ErrorCode                   string `json:"error_code,omitempty"`
}

func probeLocalIndex() localIndexReport {
	config := loadDoctorLocalIndexConfig()
	report := localIndexReport{ProviderMode: string(config.Provider), ConfigValid: config.Err == nil, Status: "unavailable", Freshness: "unknown", MaxItems: config.MaxItems, MaxOutputTokens: config.MaxOutputTokens}
	if config.Err != nil {
		return report
	}
	if config.Provider == sidecar.LocalIndexProviderDisabled {
		report.ErrorCode = string(sidecar.LocalIndexErrorDisabled)
		return report
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	info, err := discoverDoctorWorkspace(ctx, sidecar.DiscoverOptions{IncludeChangedFiles: false})
	if err != nil {
		return report
	}
	report.WorkspaceDiscovered = true
	if info.Remote == nil || info.Remote.Slug() == "" {
		return report
	}
	report.RepositoryIdentityAvailable = true
	snapshot, err := sidecar.NewLocalWorkspaceSnapshot(info, info.Remote.Slug(), false)
	if err != nil {
		return report
	}
	report.WorkspaceScopeValid = true
	provider := newDoctorLocalProvider(config, snapshot)
	caps, err := provider.Capabilities(ctx)
	if err != nil {
		applyCapabilitiesError(&report, err)
		return report
	}
	report.IndexChecked, report.VersionChecked, report.WorktreeMismatchChecked = true, true, true
	report.IndexReadable, report.Available, report.VersionCompatible = caps.Available, caps.Available, true
	report.ProviderVersion, report.Status, report.Freshness = caps.ProviderVersion, string(caps.Status), string(caps.Freshness)
	if !caps.Available {
		return report
	}
	bundle, err := provider.ContextForTask(ctx, sidecar.LocalContextRequest{TaskID: "acr-mcp-doctor-local-index", Goal: "local index diagnostic", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryState}, Workspace: &snapshot, MaxItems: 1, MaxOutputTokens: 125})
	report.QueryChecked = true
	if err != nil {
		report.Available = false
		applyQueryError(&report, err)
		return report
	}
	report.QuerySucceeded, report.ResultCount = true, len(bundle.Evidence)
	applyLocalIndexWarnings(&report, bundle.Warnings)
	return report
}

func applyLocalIndexWarnings(report *localIndexReport, warnings []string) {
	for _, warning := range warnings {
		if warning == string(sidecar.LocalIndexErrorIndexedCommitUnknown) {
			report.IndexedCommitStatus = warning
		}
		if warning == string(sidecar.LocalIndexErrorWorktreeMismatch) {
			report.WorktreeMismatchDetected = true
		}
	}
}

func applyCapabilitiesError(report *localIndexReport, err error) {
	applyLocalIndexError(report, err)
	var localErr *sidecar.LocalIndexError
	if errors.As(err, &localErr) && localErr.Code() != sidecar.LocalIndexErrorExecutableAbsent {
		report.IndexChecked, report.VersionChecked = true, true
	}
}

func applyQueryError(report *localIndexReport, err error) {
	applyLocalIndexError(report, err)
}

func applyLocalIndexError(report *localIndexReport, err error) {
	if err == nil {
		return
	}
	var localErr *sidecar.LocalIndexError
	if !errors.As(err, &localErr) {
		return
	}
	report.ErrorCode, report.Status, report.Freshness = string(localErr.Code()), string(localErr.Status()), string(localErr.Freshness())
	applyLocalIndexWarnings(report, localErr.Warnings())
	if localErr.Code() == sidecar.LocalIndexErrorIncompatibleVersion {
		report.VersionCompatible = false
	}
	if localErr.Code() == sidecar.LocalIndexErrorWorktreeMismatch {
		report.WorktreeMismatchDetected = true
	}
}
