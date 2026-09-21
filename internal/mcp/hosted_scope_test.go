package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// hostedRig is one process whose host WOULD resolve a repository for a goal-only
// context_for_task call: the working directory is a Git checkout of
// acme/from-cwd, the connected client advertises that checkout as an MCP root,
// and the local federation index is enabled. Every local discovery path carries
// a recorder, so a test can prove none of them ran rather than infer it from
// the result.
type hostedRig struct {
	boot      *Bootstrap
	session   *mcpsdk.ServerSession
	log       bytes.Buffer
	requests  []contractsv1.ContextPacketRequest
	discovers int
	providers int
	rootsList int
}

func newHostedRig(t *testing.T) *hostedRig {
	t.Helper()
	rig := &hostedRig{}
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	checkout := initTempGitRepo(t, "acme/from-cwd")

	previous := discoverWorkspace
	discoverWorkspace = func(ctx context.Context, opts sidecar.DiscoverOptions) (sidecar.WorkspaceInfo, error) {
		rig.discovers++
		return previous(ctx, opts)
	}
	t.Cleanup(func() { discoverWorkspace = previous })

	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		rig.requests = append(rig.requests, received)
		packet := validContextPacketFixture(received.RequestID)
		packet.Budget = contractsv1.PacketBudget{MaxItems: received.Options.MaxItems, MaxOutputTokens: received.Options.MaxOutputTokens, MaxSerializedBytes: received.Options.MaxSerializedBytes}
		writeJSONFixture(t, w, http.StatusOK, packet)
	}
	rig.boot = federationBootstrap(t, fx, validLocalBundle(now), nil)
	rig.boot.local.providerFactory = func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider {
		rig.providers++
		return federationProvider{bundle: validLocalBundle(now)}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	client.AddRoots(&mcpsdk.Root{URI: "file://" + checkout})
	client.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "roots/list" {
				rig.rootsList++
			}
			return next(ctx, method, req)
		}
	})
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)
	clientSession, err := client.Connect(context.Background(), t2, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	rig.session = serverSession
	return rig
}

// call drives context_for_task the way a transport does, with the connected
// session attached, in hosted or STDIO mode.
func (r *hostedRig) call(t *testing.T, hosted bool, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	cfg, caller := r.boot.split(&r.log)
	cfg.HostedMode = hosted
	req := callToolRequest(t, args)
	req.Session = r.session
	result, err := handleContextForTask(ContextWithCaller(context.Background(), caller), cfg, req)
	require.NoError(t, err)
	return result
}

func (r *hostedRig) localDiscoveryCalls() int { return r.discovers + r.providers + r.rootsList }

func resultText(t *testing.T, result *mcpsdk.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	require.True(t, ok, "expected text content, got %#v", result.Content[0])
	return text.Text
}

func (r *hostedRig) scopeLine(t *testing.T, want map[string]any) certify.Line {
	t.Helper()
	parsed, err := certify.Parse(r.log.Bytes())
	require.NoError(t, err)
	res, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.MCPHostedContextScope, Want: want})
	require.NoError(t, err)
	return res.Line
}

// The rig itself must be able to resolve a scope, or every "nothing ran"
// assertion below would pass on a rig that never could have discovered anything.
func TestHostedRigResolvesFromTheWorkspaceInStdioMode(t *testing.T) {
	rig := newHostedRig(t)

	result := rig.call(t, false, map[string]any{"goal": "inspect the checkout"})

	require.False(t, result.IsError, "%v", result.Content)
	require.Len(t, rig.requests, 1)
	require.Equal(t, "acme/from-cwd", rig.requests[0].Repository.Slug)
	require.Positive(t, rig.discovers, "STDIO must still run workspace discovery")
	require.Positive(t, rig.rootsList, "STDIO must still list the client's roots")
	require.Positive(t, rig.providers, "STDIO must still federate the local index")
	require.NotContains(t, rig.log.String(), eventspec.MCPHostedContextScope.Msg, "STDIO emits no hosted scope event")
}

func TestHostedContextForTaskUsesTheExplicitScopeAndNothingLocal(t *testing.T) {
	rig := newHostedRig(t)
	commit := strings.Repeat("a", 40)

	result := rig.call(t, true, map[string]any{
		"goal":       "inspect the billing retry path",
		"repository": map[string]any{"slug": "acme/billing"},
		"scope": map[string]any{
			"branch":     "release",
			"commit_sha": commit,
			"task_ref":   "TASK-1",
			"files":      []string{"src/retry.go", "src/queue.go"},
		},
	})

	require.False(t, result.IsError, "%v", result.Content)
	require.Len(t, rig.requests, 1)
	sent := rig.requests[0]
	require.Equal(t, "acme/billing", sent.Repository.Slug)
	require.Equal(t, "release", sent.Scope.Branch)
	require.Equal(t, commit, sent.Scope.CommitSHA)
	require.Equal(t, "TASK-1", sent.Scope.TaskRef)
	require.Equal(t, []string{"src/retry.go", "src/queue.go"}, sent.Scope.Files)
	require.Zero(t, rig.discovers, "hosted mode must not run workspace discovery (cwd, Git)")
	require.Zero(t, rig.rootsList, "hosted mode must not list MCP roots")
	require.Zero(t, rig.providers, "hosted mode must not federate the local index")

	var response contractsv1.MCPContextForTaskResponse
	require.NoError(t, json.Unmarshal(result.StructuredContent.(json.RawMessage), &response))
	require.Nil(t, response.LocalContext, "hosted mode never attaches local context")
	require.Nil(t, response.FederatedBudget)

	line := rig.scopeLine(t, map[string]any{"tool": "context_for_task", "scope_source": "explicit_repository", "has_branch": true, "has_commit": true, "file_count": 2})
	require.NotContains(t, line, "repository")
	require.NotContains(t, rig.log.String(), "acme/billing", "the event never carries the repository slug")
	require.NotContains(t, rig.log.String(), "src/retry.go", "the event never carries a file name")
}

// The failing-first pair: the same goal-only call that STDIO resolves from the
// working directory (TestHostedRigResolvesFromTheWorkspaceInStdioMode) is a
// typed refusal in hosted mode, and the hosted API is never reached.
func TestHostedContextForTaskWithoutAScopeIsATypedRefusalEvenWhenTheHostCouldResolveOne(t *testing.T) {
	rig := newHostedRig(t)

	result := rig.call(t, true, map[string]any{"goal": "inspect the checkout"})

	require.True(t, result.IsError)
	text := resultText(t, result)
	require.Equal(t, "validation: "+hostedRepositoryRequiredMessage, text)
	require.Contains(t, text, "repository.slug")
	require.Contains(t, text, "input schema")
	require.Empty(t, rig.requests, "a refused call must not reach the hosted API")
	require.Zero(t, rig.localDiscoveryCalls(), "hosted mode must not touch cwd, Git, roots or the local index")
	rig.scopeLine(t, map[string]any{"tool": "context_for_task", "scope_source": "repository_missing", "has_branch": false, "has_commit": false, "file_count": 0})
}

func TestHostedContextForTaskRefusalsAreTypedAndNameTheInputForEveryShape(t *testing.T) {
	cases := []struct {
		name         string
		args         map[string]any
		wantErr      bool
		wantMessage  string
		wantSource   string
		wantBranch   bool
		wantFilesLen int
	}{
		{name: "goal only", args: map[string]any{"goal": "g"}, wantErr: true, wantMessage: hostedRepositoryRequiredMessage, wantSource: "repository_missing"},
		{name: "scope without repository", args: map[string]any{"goal": "g", "scope": map[string]any{"branch": "main", "files": []string{"a.go"}}}, wantErr: true, wantMessage: hostedRepositoryRequiredMessage, wantSource: "repository_missing", wantBranch: true, wantFilesLen: 1},
		{name: "empty scope object", args: map[string]any{"goal": "g", "scope": map[string]any{}}, wantErr: true, wantMessage: hostedRepositoryRequiredMessage, wantSource: "repository_missing"},
		{name: "changed files requested with repository", args: map[string]any{"goal": "g", "repository": map[string]any{"slug": "acme/billing"}, "scope": map[string]any{"include_changed_files": true}}, wantErr: true, wantMessage: hostedChangedFilesMessage, wantSource: "changed_files_unsupported"},
		{name: "changed files requested without repository", args: map[string]any{"goal": "g", "scope": map[string]any{"include_changed_files": true}}, wantErr: true, wantMessage: hostedChangedFilesMessage, wantSource: "changed_files_unsupported"},
		{name: "changed files declined", args: map[string]any{"goal": "g", "repository": map[string]any{"slug": "acme/billing"}, "scope": map[string]any{"include_changed_files": false}}, wantSource: "explicit_repository"},
		{name: "repository only", args: map[string]any{"goal": "g", "repository": map[string]any{"slug": "acme/billing"}}, wantSource: "explicit_repository"},
		{name: "repository with explicit files", args: map[string]any{"goal": "g", "repository": map[string]any{"slug": "acme/billing"}, "scope": map[string]any{"files": []string{"a.go", "b.go", "c.go"}}}, wantSource: "explicit_repository", wantFilesLen: 3},
	}
	require.NotEmpty(t, cases)

	executed := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newHostedRig(t)

			result := rig.call(t, true, tc.args)
			executed++

			require.Equal(t, tc.wantErr, result.IsError, "%v", result.Content)
			if tc.wantErr {
				require.Equal(t, "validation: "+tc.wantMessage, resultText(t, result))
				require.Empty(t, rig.requests)
			} else {
				require.Len(t, rig.requests, 1)
			}
			require.Zero(t, rig.localDiscoveryCalls())
			line := rig.scopeLine(t, map[string]any{"tool": "context_for_task", "scope_source": tc.wantSource, "has_branch": tc.wantBranch, "has_commit": false, "file_count": tc.wantFilesLen})
			require.Equal(t, tc.wantSource, line["scope_source"])
		})
	}
	require.Equal(t, len(cases), executed)
}

// Whatever the request, hosted resolution is a pure function of the request:
// it takes no session and no context, so it has nothing to list, read or run.
func TestResolveHostedTaskScopeIsAPureFunctionOfTheRequest(t *testing.T) {
	rig := newHostedRig(t)
	req := contractsv1.MCPContextForTaskRequest{Goal: "g", Repository: &contractsv1.MCPRepositoryRef{Slug: "acme/billing"}, Scope: &contractsv1.MCPRequestedScope{Branch: "b"}}

	resolved, source, err := resolveHostedTaskScope(req)

	require.NoError(t, err)
	require.Equal(t, "explicit_repository", source)
	require.Equal(t, "acme/billing", resolved.Repository.Slug)
	require.Equal(t, "b", resolved.Scope.Branch)
	require.Nil(t, resolved.Workspace)
	require.False(t, resolved.LocalEligible)
	require.Zero(t, rig.localDiscoveryCalls())
}

func TestHostedRefusalsAreClassifiedAsValidation(t *testing.T) {
	for _, err := range []error{ErrHostedRepositoryRequired, ErrHostedChangedFilesUnsupported} {
		ce := classify(err)
		require.Equal(t, "validation", ce.category)
		require.NotEmpty(t, ce.message)
	}
}

func TestHostedModeIsProcessConfigurationOnly(t *testing.T) {
	require.False(t, (*ProcessConfig)(nil).hostedMode())
	require.False(t, (&ProcessConfig{}).hostedMode())
	require.True(t, (&ProcessConfig{HostedMode: true}).hostedMode())
}
