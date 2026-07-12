package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callToolRequest(t *testing.T, args any) *mcpsdk.CallToolRequest {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: encoded}}
}

func TestHandleContextForTaskSuccess(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "acme/widgets"},
		"scope":      map[string]any{"branch": "main"},
	})

	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a normal tool result, got protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %#v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected rendered markdown content")
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "UNTRUSTED DATA") {
		t.Fatalf("expected UNTRUSTED DATA markdown, got: %#v", result.Content[0])
	}
	if result.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
}

// TestHandleContextForTaskDoesNotFalselyReportTruncationForMarkerTextInHostedContent
// locks result/render truncation provenance end to end: the truncation
// flag comes from RenderContextPacketMarkdown's own byte-budget
// bookkeeping, not from pattern-matching the untrusted rendered markdown,
// so hosted content that happens to contain the renderer's own
// truncation-notice wording must never flip Truncated to true.
func TestHandleContextForTaskDoesNotFalselyReportTruncationForMarkerTextInHostedContent(t *testing.T) {
	fx := newFixtureServer(t)
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		_ = json.NewDecoder(r.Body).Decode(&received)
		packet := validContextPacketFixture(received.RequestID)
		packet.Goal = "prior analyst note: remaining content omitted from the vendor's own dashboard"
		writeJSONFixture(t, w, http.StatusOK, packet)
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "acme/widgets"},
		"scope":      map[string]any{"branch": "main"},
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a normal tool result, got protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %#v", result.Content)
	}
	var response contractsv1.MCPContextForTaskResponse
	if err := json.Unmarshal(result.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if response.RenderedMarkdown.Truncated {
		t.Fatal("expected Truncated=false: the marker text came from untrusted hosted content, not actual truncation")
	}
}

func TestHandleContextForTaskRejectsMalformedArguments(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: []byte("{not json")}}
	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("malformed arguments must be a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for malformed JSON arguments")
	}
}

func TestHandleContextForTaskRejectsExplicitNullGoal(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Arguments: []byte(`{"goal":null}`),
	}}
	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for an explicit JSON null goal")
	}
}

func TestHandleContextForTaskRejectsMissingGoal(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{})
	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for a missing required goal")
	}
}

func TestHandleContextForTaskMapsHostedAPIErrorMatrix(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		code     string
		category string
	}{
		{"auth", http.StatusUnauthorized, "invalid_token", "auth"},
		{"entitlement", http.StatusForbidden, "feature_not_enabled", "entitlement"},
		{"rate_limit", http.StatusTooManyRequests, "rate_limited", "rate_limit"},
		{"validation", http.StatusBadRequest, "invalid_request", "validation"},
		{"unavailable", http.StatusServiceUnavailable, "upstream_unavailable", "unavailable"},
		{"no_data", http.StatusNotFound, "not_found", "no_data"},
		{"version", http.StatusBadRequest, "version_mismatch", "version"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixtureServer(t)
			fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
				writeErrorFixture(t, w, tc.status, tc.code, false)
			}
			boot := newFixtureBootstrap(t, fx)

			req := callToolRequest(t, map[string]any{
				"goal":       "investigate flaky checkout tests",
				"repository": map[string]any{"slug": "acme/widgets"},
				"scope":      map[string]any{"branch": "main"},
			})
			result, err := handleContextForTask(context.Background(), boot, req)
			if err != nil {
				t.Fatalf("hosted API failures must be tool errors, not protocol errors: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected IsError for a hosted API failure")
			}
			text, ok := result.Content[0].(*mcpsdk.TextContent)
			if !ok || !strings.Contains(text.Text, tc.category) {
				t.Fatalf("expected error category %q in result, got: %#v", tc.category, result.Content)
			}
		})
	}
}

func TestHandleContextForTaskHonorsCancellation(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := callToolRequest(t, map[string]any{
		"goal":       "investigate flaky checkout tests",
		"repository": map[string]any{"slug": "acme/widgets"},
		"scope":      map[string]any{"branch": "main"},
	})
	result, err := handleContextForTask(ctx, boot, req)
	if err != nil {
		t.Fatalf("cancellation must be a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError when the context is already cancelled")
	}
}

// TestBudgetOptionsDefaultsAreClampedToHostedLimits locks the
// local-vs-hosted capability semantics gap: this sidecar's own safe
// default budget (max_items=20 etc.) must never exceed what the hosted
// API's capabilities.limits actually advertises, even when the caller
// supplied no budget override at all.
func TestBudgetOptionsDefaultsAreClampedToHostedLimits(t *testing.T) {
	limits := contractsv1.CapabilityLimits{MaxItems: 10, MaxOutputTokens: 600, MaxSerializedBytes: 9000, RequestsPerMinute: 60}
	opts := budgetOptions(nil, limits)
	if opts.MaxItems != 10 || opts.MaxOutputTokens != 600 || opts.MaxSerializedBytes != 9000 {
		t.Fatalf("expected defaults clamped to hosted limits, got: %#v", opts)
	}
}

// TestBudgetOptionsCallerRequestIsClampedToHostedLimits locks the other
// half: an explicit caller-supplied budget override must also never
// exceed the hosted API's advertised capacity, even though it is within
// the MCP contract's own generic 1-50/500-16000/8192-1048576 bounds.
func TestBudgetOptionsCallerRequestIsClampedToHostedLimits(t *testing.T) {
	limits := contractsv1.CapabilityLimits{MaxItems: 10, MaxOutputTokens: 600, MaxSerializedBytes: 9000, RequestsPerMinute: 60}
	budget := &contractsv1.MCPBudget{MaxItems: 50, MaxOutputTokens: 16000, MaxSerializedBytes: 1048576}
	opts := budgetOptions(budget, limits)
	if opts.MaxItems != 10 || opts.MaxOutputTokens != 600 || opts.MaxSerializedBytes != 9000 {
		t.Fatalf("expected caller request clamped to hosted limits, got: %#v", opts)
	}
}

// TestBudgetOptionsUnclampedWhenWithinHostedLimits is the regression
// guard: when hosted limits are generous (the common case), defaults and
// caller overrides behave exactly as before this fix.
func TestBudgetOptionsUnclampedWhenWithinHostedLimits(t *testing.T) {
	limits := contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60}
	if opts := budgetOptions(nil, limits); opts.MaxItems != defaultMaxItems || opts.MaxOutputTokens != defaultMaxOutputTokens || opts.MaxSerializedBytes != defaultMaxSerializedBytes {
		t.Fatalf("expected unclamped defaults, got: %#v", opts)
	}
	budget := &contractsv1.MCPBudget{MaxItems: 5}
	if opts := budgetOptions(budget, limits); opts.MaxItems != 5 {
		t.Fatalf("expected unclamped caller override, got: %#v", opts)
	}
}

// TestHandleContextForTaskRejectsGoalOnlyOutsideDiscoverableWorkspace is
// the CHAOS-2908 rereview regression lock: a goal-only request (no
// explicit repository) whose process cwd is not inside a discoverable Git
// workspace must be rejected with a typed "validation" tool error before
// the hosted context-packets endpoint is ever called -- not silently
// forwarded with an empty repository, which previously fell through
// ContextPacketRequest.Validate()'s generic "slug violates v1 bounds"
// error into classify()'s generic "internal" fallback. This is a
// full-handler test (not just resolveScope) so it also proves the fixture
// server's context-packets handler is never invoked.
func TestHandleContextForTaskRejectsGoalOnlyOutsideDiscoverableWorkspace(t *testing.T) {
	fx := newFixtureServer(t)
	var contextPacketCalls int
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		contextPacketCalls++
		t.Fatal("the hosted context-packets endpoint must not be called for an unresolvable repository")
	}
	boot := newFixtureBootstrap(t, fx)

	dir := t.TempDir() // not a Git repository
	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })

	req := callToolRequest(t, map[string]any{
		"goal": "goal-only, no local workspace",
	})
	result, err := handleContextForTask(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for a goal-only request outside a discoverable workspace")
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "validation") {
		t.Fatalf("expected category %q in result, got: %#v", "validation", result.Content)
	}
	if contextPacketCalls != 0 {
		t.Fatalf("expected the hosted context-packets endpoint to never be called, got %d calls", contextPacketCalls)
	}
}
