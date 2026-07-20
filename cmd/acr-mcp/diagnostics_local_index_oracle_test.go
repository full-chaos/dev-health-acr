package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestDoctorLocalIndexClassifiesStrictStaleQueryFailure(t *testing.T) {
	status := strings.Replace(typedStatus("1.2.0"), `"reindexRecommended":false`, `"reindexRecommended":true`, 1)
	config, info := typedDoctorFixture(t, status, "[]")
	config.StalePolicy = sidecar.LocalIndexStaleStrict
	err := strictDoctorQueryError(t, config, info)
	report := doctorReportFromQueryError(t, config, info, err)
	assertStrictDoctorReport(t, report, false)
}

func TestDoctorLocalIndexClassifiesStrictStaleCapabilityFailure(t *testing.T) {
	status := strings.Replace(typedStatus("1.2.0"), `"reindexRecommended":false`, `"reindexRecommended":true`, 1)
	config, info := typedDoctorFixture(t, status, "[]")
	config.StalePolicy = sidecar.LocalIndexStaleStrict
	withDoctorLocalProbe(t, config, info, sidecar.NewWorkspaceLocalIndexProvider(config, mustDoctorSnapshot(t, info)))
	report := probeLocalIndex()
	assertStrictCapabilityReport(t, report, false)
}

func TestDoctorLocalIndexClassifiesStrictDirtyWorktreeMismatchCapabilityFailure(t *testing.T) {
	status := strings.Replace(typedStatus("1.2.0"), `"worktreeMismatch":null`, `"worktreeMismatch":"different worktree"`, 1)
	status = strings.Replace(status, `"added":0`, `"added":1`, 1)
	config, info := typedDoctorFixture(t, status, "[]")
	config.StalePolicy = sidecar.LocalIndexStaleStrict
	withDoctorLocalProbe(t, config, info, sidecar.NewWorkspaceLocalIndexProvider(config, mustDoctorSnapshot(t, info)))
	report := probeLocalIndex()
	assertStrictCapabilityReport(t, report, true)
}

func TestDoctorLocalIndexClassifiesStrictDirtyWorktreeMismatchQueryFailure(t *testing.T) {
	status := strings.Replace(typedStatus("1.2.0"), `"worktreeMismatch":null`, `"worktreeMismatch":"different worktree"`, 1)
	status = strings.Replace(status, `"added":0`, `"added":1`, 1)
	config, info := typedDoctorFixture(t, status, "[]")
	config.StalePolicy = sidecar.LocalIndexStaleStrict
	err := strictDoctorQueryError(t, config, info)
	report := doctorReportFromQueryError(t, config, info, err)
	assertStrictDoctorReport(t, report, true)
}

func TestDoctorLocalIndexReportsGracefulStaleMismatchAsDegraded(t *testing.T) {
	caps := sidecar.LocalIndexCapabilities{ProviderID: "codegraph", ProviderVersion: "1.2.0", Available: true, MaxItems: 5, MaxOutputTokens: 1000, Status: sidecar.LocalIndexStatusDegraded, Freshness: sidecar.LocalIndexFreshnessStale}
	bundle := sidecar.LocalEvidenceBundle{ProviderID: "codegraph", ProviderVersion: "1.2.0", QueryID: "query", QueryVersion: "codegraph-json-contract-v1", Status: sidecar.LocalIndexStatusDegraded, Freshness: sidecar.LocalIndexFreshnessStale, Warnings: []string{"local_worktree_mismatch", "local_index_stale", "indexed_commit_unknown"}}
	if err := sidecar.ValidateLocalIndexCapabilities(caps); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.ValidateLocalEvidenceBundle(bundle); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := sidecar.LocalIndexConfig{Provider: sidecar.LocalIndexProviderCodeGraph, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000}
	withDoctorLocalProbe(t, config, validDoctorWorkspace(root), doctorLocalProvider{caps: caps, bundle: bundle})
	report := probeLocalIndex()
	if !report.Available || report.Status != "degraded" || report.Freshness != "stale" || !report.QueryChecked || !report.QuerySucceeded || !report.WorktreeMismatchChecked || !report.WorktreeMismatchDetected || report.ErrorCode != "" || report.IndexedCommitStatus != "indexed_commit_unknown" || report.ResultCount != 0 {
		t.Fatalf("unexpected graceful report: %#v", report)
	}
}

func strictDoctorQueryError(t *testing.T, config sidecar.LocalIndexConfig, info sidecar.WorkspaceInfo) error {
	t.Helper()
	provider := sidecar.NewWorkspaceLocalIndexProvider(config, mustDoctorSnapshot(t, info))
	request := sidecar.LocalContextRequest{TaskID: "acr-mcp-doctor-local-index", Goal: "local index diagnostic", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryState}, Workspace: ptrDoctorSnapshot(t, info), MaxItems: 1, MaxOutputTokens: 125}
	_, err := provider.ContextForTask(context.Background(), request)
	var typed *sidecar.LocalIndexError
	if !errors.As(err, &typed) {
		t.Fatalf("expected typed error, got %v", err)
	}
	return err
}

func doctorReportFromQueryError(t *testing.T, config sidecar.LocalIndexConfig, info sidecar.WorkspaceInfo, err error) localIndexReport {
	t.Helper()
	caps := sidecar.LocalIndexCapabilities{ProviderID: "codegraph", ProviderVersion: "1.2.0", Available: true, MaxItems: 5, MaxOutputTokens: 1000, Status: sidecar.LocalIndexStatusAvailable, Freshness: sidecar.LocalIndexFreshnessFresh}
	withDoctorLocalProbe(t, config, info, doctorLocalProvider{caps: caps, queryErr: err})
	return probeLocalIndex()
}

func assertStrictDoctorReport(t *testing.T, report localIndexReport, mismatch bool) {
	t.Helper()
	if report.Available || report.Status != "unavailable" || report.Freshness != "stale" || report.ErrorCode != "local_index_stale" || !report.QueryChecked || report.QuerySucceeded || !report.IndexChecked || !report.IndexReadable || !report.VersionChecked || !report.VersionCompatible || !report.WorktreeMismatchChecked || report.WorktreeMismatchDetected != mismatch || report.ProviderVersion != "1.2.0" || report.IndexedCommitStatus != "indexed_commit_unknown" {
		t.Fatalf("unexpected strict report: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"different worktree", "local_workspace_dirty", "full-chaos/acr", "0123456789abcdef0123456789abcdef01234567"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
}

func assertStrictCapabilityReport(t *testing.T, report localIndexReport, mismatch bool) {
	t.Helper()
	if report.Available || report.Status != "unavailable" || report.Freshness != "stale" || report.ErrorCode != "local_index_stale" || report.QueryChecked || !report.IndexChecked || !report.IndexReadable || !report.VersionChecked || !report.VersionCompatible || !report.WorktreeMismatchChecked || report.WorktreeMismatchDetected != mismatch || report.IndexedCommitStatus != "indexed_commit_unknown" {
		t.Fatalf("unexpected strict capability report: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"different worktree", "local_workspace_dirty", "full-chaos/acr", "0123456789abcdef0123456789abcdef01234567"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
}

func ptrDoctorSnapshot(t *testing.T, info sidecar.WorkspaceInfo) *sidecar.LocalWorkspaceSnapshot {
	snapshot := mustDoctorSnapshot(t, info)
	return &snapshot
}
