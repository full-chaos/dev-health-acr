package main

import (
	"context"
	"os"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

type doctorLocalProvider struct {
	caps   sidecar.LocalIndexCapabilities
	bundle sidecar.LocalEvidenceBundle
	err    error
}

func (p doctorLocalProvider) Capabilities(context.Context) (sidecar.LocalIndexCapabilities, error) {
	return p.caps, p.err
}
func (p doctorLocalProvider) ContextForTask(_ context.Context, request sidecar.LocalContextRequest) (sidecar.LocalEvidenceBundle, error) {
	if request.TaskID != "acr-mcp-doctor-local-index" || request.Goal != "local index diagnostic" || request.MaxItems != 1 || request.MaxOutputTokens != 125 || len(request.RequestedCategories) != 1 || request.RequestedCategories[0] != contractsv1.CategoryState {
		return sidecar.LocalEvidenceBundle{}, sidecar.ErrInvalidLocalContextRequest
	}
	return p.bundle, p.err
}
func (doctorLocalProvider) ResolveEvidence(context.Context, string) (sidecar.LocalExpandedEvidence, error) {
	return sidecar.LocalExpandedEvidence{}, sidecar.ErrLocalEvidenceNotFound
}

func TestMain(m *testing.M) {
	_ = os.Setenv("ACR_LOCAL_INDEX_PROVIDER", "disabled")
	os.Exit(m.Run())
}

func TestProbeLocalIndexReportsBoundedHealthyResult(t *testing.T) {
	root := t.TempDir()
	config := sidecar.LocalIndexConfig{Provider: sidecar.LocalIndexProviderCodeGraph, Timeout: time.Second, MaxItems: 5, MaxOutputTokens: 1000}
	withDoctorLocalProbe(t, config, sidecar.WorkspaceInfo{GitRoot: root, Remote: &sidecar.RemoteInfo{Host: "github.com", Owner: "full-chaos", Repo: "acr"}, Branch: "main", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}, doctorLocalProvider{caps: sidecar.LocalIndexCapabilities{Available: true, ProviderVersion: "1.2.0", MaxItems: 5, MaxOutputTokens: 1000, Status: sidecar.LocalIndexStatusAvailable, Freshness: sidecar.LocalIndexFreshnessFresh}, bundle: sidecar.LocalEvidenceBundle{Warnings: []string{string(sidecar.LocalIndexErrorIndexedCommitUnknown)}}})
	report := probeLocalIndex()
	if !report.QuerySucceeded || report.ResultCount != 0 || report.ProviderVersion != "1.2.0" || report.IndexedCommitStatus != string(sidecar.LocalIndexErrorIndexedCommitUnknown) || !report.WorkspaceScopeValid {
		t.Fatalf("unexpected healthy local report: %#v", report)
	}
}

func TestProbeLocalIndexDoesNotDiscoverWorkspaceWhenDisabledOrInvalid(t *testing.T) {
	for _, config := range []sidecar.LocalIndexConfig{{Provider: sidecar.LocalIndexProviderDisabled, Timeout: time.Second}, {Provider: sidecar.LocalIndexProviderDisabled, Err: os.ErrInvalid, Timeout: time.Second}} {
		t.Run(string(config.Provider), func(t *testing.T) {
			called := false
			originalConfig, originalDiscover := loadDoctorLocalIndexConfig, discoverDoctorWorkspace
			loadDoctorLocalIndexConfig = func() sidecar.LocalIndexConfig { return config }
			discoverDoctorWorkspace = func(context.Context, sidecar.DiscoverOptions) (sidecar.WorkspaceInfo, error) {
				called = true
				return sidecar.WorkspaceInfo{}, nil
			}
			t.Cleanup(func() { loadDoctorLocalIndexConfig, discoverDoctorWorkspace = originalConfig, originalDiscover })
			report := probeLocalIndex()
			if called || report.ConfigValid == (config.Err != nil) {
				t.Fatalf("unexpected disabled/invalid report: %#v", report)
			}
		})
	}
}

func withDoctorLocalProbe(t *testing.T, config sidecar.LocalIndexConfig, info sidecar.WorkspaceInfo, provider sidecar.LocalIndexProvider) {
	t.Helper()
	originalConfig, originalDiscover, originalProvider := loadDoctorLocalIndexConfig, discoverDoctorWorkspace, newDoctorLocalProvider
	loadDoctorLocalIndexConfig = func() sidecar.LocalIndexConfig { return config }
	discoverDoctorWorkspace = func(context.Context, sidecar.DiscoverOptions) (sidecar.WorkspaceInfo, error) { return info, nil }
	newDoctorLocalProvider = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		return provider
	}
	t.Cleanup(func() {
		loadDoctorLocalIndexConfig, discoverDoctorWorkspace, newDoctorLocalProvider = originalConfig, originalDiscover, originalProvider
	})
}
