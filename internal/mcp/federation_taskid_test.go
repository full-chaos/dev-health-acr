package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/stretchr/testify/require"
)

type taskIDCapturingProvider struct {
	request sidecar.LocalContextRequest
}

func (p *taskIDCapturingProvider) Capabilities(context.Context) (sidecar.LocalIndexCapabilities, error) {
	return sidecar.LocalIndexCapabilities{}, nil
}

func (p *taskIDCapturingProvider) ContextForTask(_ context.Context, request sidecar.LocalContextRequest) (sidecar.LocalEvidenceBundle, error) {
	p.request = request
	return validLocalBundle(time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)), nil
}

func (p *taskIDCapturingProvider) ResolveEvidence(context.Context, string) (sidecar.LocalExpandedEvidence, error) {
	return sidecar.LocalExpandedEvidence{}, sidecar.ErrLocalEvidenceNotFound
}

func TestFederation_LocalTaskID_isCanonicalAndBudgetInvariant(t *testing.T) {
	asOf := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	scope := resolvedTaskScope{Repository: contractsv1.RepositoryRef{Slug: "Acme/Widgets"}, Scope: contractsv1.RequestedScope{Branch: "main", CommitSHA: "ABC", TaskRef: "CHAOS-3007", Files: []string{"b.go", "a.go"}, AsOf: &asOf, TimeWindowDays: 7}, Workspace: &sidecar.LocalWorkspaceSnapshot{ChangedFilesState: sidecar.LocalChangedFilesComplete}}
	input := contractsv1.MCPContextForTaskRequest{Goal: "inspect", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryEvidence, contractsv1.CategoryState}}
	first := localTaskID(scope, input, contractsv1.PacketOptions{MaxItems: 1})
	scope.Scope.Files = []string{"a.go", "b.go"}
	input.RequestedCategories = []contractsv1.PacketCategory{contractsv1.CategoryState, contractsv1.CategoryEvidence}
	second := localTaskID(scope, input, contractsv1.PacketOptions{MaxItems: 50, MaxOutputTokens: 16000, MaxSerializedBytes: 1048576})
	require.Equal(t, first, second)
	require.Len(t, first, 78)
	require.True(t, strings.HasPrefix(first, "local-task:v1:"))
	require.Regexp(t, `^local-task:v1:[0-9a-f]{64}$`, first)
}

func TestFederation_LocalTaskID_changesForEverySemanticInput(t *testing.T) {
	// Given
	asOf := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	baseScope := resolvedTaskScope{Repository: contractsv1.RepositoryRef{Slug: "acme/widgets"}, Scope: contractsv1.RequestedScope{Branch: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TaskRef: "CHAOS-3007", Files: []string{"a.go"}, AsOf: &asOf, TimeWindowDays: 7}, Workspace: &sidecar.LocalWorkspaceSnapshot{ChangedFilesState: sidecar.LocalChangedFilesComplete}}
	baseInput := contractsv1.MCPContextForTaskRequest{Goal: "inspect", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryEvidence}}
	options := contractsv1.PacketOptions{MaxItems: 20, MaxOutputTokens: 4000, MaxSerializedBytes: 262144}
	base := localTaskID(baseScope, baseInput, options)

	// When
	variants := []struct {
		name    string
		scope   resolvedTaskScope
		input   contractsv1.MCPContextForTaskRequest
		options contractsv1.PacketOptions
	}{
		{"repository", resolvedTaskScope{Repository: contractsv1.RepositoryRef{Slug: "acme/other"}, Scope: baseScope.Scope, Workspace: baseScope.Workspace}, baseInput, options},
		{"goal", baseScope, contractsv1.MCPContextForTaskRequest{Goal: "other", RequestedCategories: baseInput.RequestedCategories}, options},
		{"branch", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: "release", CommitSHA: baseScope.Scope.CommitSHA, TaskRef: baseScope.Scope.TaskRef, Files: baseScope.Scope.Files, AsOf: baseScope.Scope.AsOf, TimeWindowDays: baseScope.Scope.TimeWindowDays}, Workspace: baseScope.Workspace}, baseInput, options},
		{"commit", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: baseScope.Scope.Branch, CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TaskRef: baseScope.Scope.TaskRef, Files: baseScope.Scope.Files, AsOf: baseScope.Scope.AsOf, TimeWindowDays: baseScope.Scope.TimeWindowDays}, Workspace: baseScope.Workspace}, baseInput, options},
		{"taskref", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: baseScope.Scope.Branch, CommitSHA: baseScope.Scope.CommitSHA, TaskRef: "CHAOS-3008", Files: baseScope.Scope.Files, AsOf: baseScope.Scope.AsOf, TimeWindowDays: baseScope.Scope.TimeWindowDays}, Workspace: baseScope.Workspace}, baseInput, options},
		{"files", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: baseScope.Scope.Branch, CommitSHA: baseScope.Scope.CommitSHA, TaskRef: baseScope.Scope.TaskRef, Files: []string{"b.go"}, AsOf: baseScope.Scope.AsOf, TimeWindowDays: baseScope.Scope.TimeWindowDays}, Workspace: baseScope.Workspace}, baseInput, options},
		{"state", resolvedTaskScope{Repository: baseScope.Repository, Scope: baseScope.Scope, Workspace: &sidecar.LocalWorkspaceSnapshot{ChangedFilesState: sidecar.LocalChangedFilesTruncated}}, baseInput, options},
		{"as_of", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: baseScope.Scope.Branch, CommitSHA: baseScope.Scope.CommitSHA, TaskRef: baseScope.Scope.TaskRef, Files: baseScope.Scope.Files, TimeWindowDays: baseScope.Scope.TimeWindowDays}, Workspace: baseScope.Workspace}, baseInput, options},
		{"time_window", resolvedTaskScope{Repository: baseScope.Repository, Scope: contractsv1.RequestedScope{Branch: baseScope.Scope.Branch, CommitSHA: baseScope.Scope.CommitSHA, TaskRef: baseScope.Scope.TaskRef, Files: baseScope.Scope.Files, AsOf: baseScope.Scope.AsOf, TimeWindowDays: 8}, Workspace: baseScope.Workspace}, baseInput, options},
		{"categories", baseScope, baseInput, contractsv1.PacketOptions{MaxItems: options.MaxItems, MaxOutputTokens: options.MaxOutputTokens, MaxSerializedBytes: options.MaxSerializedBytes, RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryState}}},
	}

	// Then
	require.Equal(t, "local-task:v1:6f9670c9d6f395cb5502fe775b995c18f157f36a2df4a7901cab70e789817dc8", base)
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			require.NotEqual(t, base, localTaskID(variant.scope, variant.input, variant.options))
			require.Equal(t, base, localTaskID(baseScope, baseInput, contractsv1.PacketOptions{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}))
		})
	}
}

func TestFederation_LocalTaskID_isForwardedToProvider(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	provider := &taskIDCapturingProvider{}
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{MaxItems: 5, MaxOutputTokens: 1000, MaxSerializedBytes: 65536, Timeout: time.Second}, func() time.Time { return now }, nil)
	runtime.providerFactory = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		return provider
	}
	scope := resolvedTaskScope{Repository: contractsv1.RepositoryRef{Slug: "acme/widgets"}, Scope: contractsv1.RequestedScope{TaskRef: "CHAOS-3007"}, Workspace: &sidecar.LocalWorkspaceSnapshot{}}
	input := contractsv1.MCPContextForTaskRequest{Goal: "inspect"}
	options := contractsv1.PacketOptions{MaxItems: 20, MaxOutputTokens: 4000, MaxSerializedBytes: 262144}

	// When
	_, err := runtime.bundle(context.Background(), scope, input, options)

	// Then
	require.NoError(t, err)
	require.Equal(t, localTaskID(scope, input, options), provider.request.TaskID)
}

func TestFederation_MapBundle_rejectsDuplicateProviderIDsBeforeHosted(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	bundle := validLocalBundle(now)
	bundle.Evidence = append(bundle.Evidence, bundle.Evidence[0])
	runtime := newLocalFederationRuntime(sidecar.LocalIndexConfig{}, func() time.Time { return now }, nil)
	_, err := runtime.mapLocalBundle("acme/widgets", bundle, map[string]struct{}{})
	require.Error(t, err)
}

func TestFederation_HostedRouteCache_expiresAndEvicts(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	cache := newHostedRouteCache(1, time.Minute, func() time.Time { return now })
	cache.put(localEvidencePrefix + "one")
	require.True(t, cache.has(localEvidencePrefix+"one"))
	cache.put(localEvidencePrefix + "two")
	require.False(t, cache.has(localEvidencePrefix+"one"))
	now = now.Add(time.Minute)
	require.False(t, cache.has(localEvidencePrefix+"two"))
}
