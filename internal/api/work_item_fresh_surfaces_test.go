package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// Every golden starts with the HTTP result produced by Engine. No result,
// census, claim or projection is hand-assembled to make a golden pass.
func assertFreshTupleSurfaces(t *testing.T, fixture *freshTupleProducerFixture, failure string, result contextfabric.InvestigationResult) {
	t.Helper()
	stored, err := fixture.store.Get(context.Background(), fixture.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SemanticStateRead != contextfabric.SemanticStateReadAvailable || stored.SemanticState == nil || contextfabric.ValidateWorkItemTupleCensus(stored.SemanticState.WorkItemCensus) != contextfabric.WorkItemTupleCensusReadAvailable {
		t.Fatalf("actual codec round trip lost available tuple census: %+v", stored)
	}
	if result.AnswerPlan == nil || result.AnswerPlan.FamilyVersion != "question-family.v3" || stored.SemanticState.FamilyTableVersion != "question-family.v3" {
		t.Fatalf("current producer plan/capture must both use family v3: plan=%+v state=%+v", result.AnswerPlan, stored.SemanticState)
	}
	census := stored.SemanticState.WorkItemCensus
	wantState, wantValue, wantRetained := contextfabric.WorkItemMembershipCensusExact, 1, 1
	if strings.HasPrefix(failure, "floor") {
		wantState, wantValue = contextfabric.WorkItemMembershipCensusFloor, 2000
	}
	if failure == "zero" {
		wantValue, wantRetained = 0, 0
	}
	if failure == "s1" {
		wantState, wantValue, wantRetained = contextfabric.WorkItemMembershipCensusUnmeasured, 0, 0
	}
	if census.State != wantState || census.Value != wantValue || census.Retained != wantRetained {
		t.Fatalf("persisted census = %+v, want %s/%d/%d", census, wantState, wantValue, wantRetained)
	}
	digest, err := contextfabric.WorkItemAuthorizationDigest(fixture.principal, census.RequestedRepositoryScope)
	if err != nil || digest != census.AuthorizationDigest {
		t.Fatalf("persisted current-principal digest = %q, want %q (error %v)", census.AuthorizationDigest, digest, err)
	}
	if !reflect.DeepEqual(stored.Result, result) {
		t.Fatal("HTTP result differs from the actual codec round trip")
	}

	if failure != "s1" {
		sentence := "Counted 1 work item."
		if failure == "zero" {
			sentence = "Counted 0 work items."
		}
		if strings.HasPrefix(failure, "floor") {
			sentence = "Counted at least 2000 work items."
		}
		if !strings.HasSuffix(result.DeterministicAnswer, sentence) {
			t.Fatalf("actual count prose=%q, want suffix %q", result.DeterministicAnswer, sentence)
		}
	}
	projection := answerprojection.Project(result, answerprojection.Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("shared projection rejected actual producer result: %v", err)
	}
	markdown, truncated := sidecar.RenderAnswerProjectionMarkdown(projection, 32768)
	if truncated {
		t.Fatal("small producer golden unexpectedly truncated")
	}
	name := map[string]string{"": "normal", "status": "partial_status", "work": "partial_work", "s1": "unmeasured", "zero": "zero", "floor": "floor", "floor_status": "floor_partial_status"}[failure]
	assertFreshTupleGolden(t, name+".result.json", freshTupleJSON(t, result))
	assertFreshTupleGolden(t, name+".projection.json", freshTupleJSON(t, projection))
	assertFreshTupleGolden(t, name+".projection.md", []byte(markdown))

	assertFreshTupleStoredMCPParity(t, fixture, result)
}

func assertFreshTupleStoredMCPParity(t *testing.T, fixture *freshTupleProducerFixture, result contextfabric.InvestigationResult) {
	t.Helper()
	assertFreshTupleMCPParity(t, fixture, result, false)
}

func assertFreshTupleTerminalMCPParity(t *testing.T, fixture *freshTupleProducerFixture, result contextfabric.InvestigationResult) {
	t.Helper()
	if result.Status != "no_match" && result.Status != "clarification_required" {
		t.Fatalf("terminal parity requires ordinary terminal, got %s", result.Status)
	}
	assertFreshTupleMCPParity(t, fixture, result, true)
}

func assertFreshTupleMCPParity(t *testing.T, fixture *freshTupleProducerFixture, result contextfabric.InvestigationResult, terminalDisclosure bool) {
	t.Helper()
	stored, err := fixture.store.Get(context.Background(), fixture.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	projection := answerprojection.Project(result, answerprojection.Budget{})
	// The real SDK/server reaches the real HTTP endpoint and its codec store.
	// These are controlled backend/model rows, not a deployed-store proof.
	server := httptest.NewTLSServer(fixture.app.InstrumentedHandler(fixture.app.Handler()))
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, fixture.token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("real MCP bootstrap: %v", err)
	}
	mcpResult := callRealMCPInvestigationResult(t, boot, result.ResultID)
	apiResult := getRealAPIResult(t, server, fixture.token, result.ResultID)
	if !bytes.Equal(freshTupleJSON(t, apiResult), freshTupleJSON(t, mcpResult)) {
		t.Fatal("real API and MCP stored tuple responses differ")
	}
	after, err := fixture.store.Get(context.Background(), fixture.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(freshTupleJSON(t, stored), freshTupleJSON(t, after)) {
		t.Fatal("API/MCP serving changed the codec-stored carrier")
	}
	if terminalDisclosure {
		// The existing stored-read fact-scope repair discloses unexpanded
		// evidence for these pre-membership terminals. Pin its exact addition;
		// every other projected field must still equal the fresh producer.
		projection.Limitations = append(slices.Clone(projection.Limitations), contractsv1.ContextFabricFactScopeUnexpandedLimitation)
	}
	if !bytes.Equal(freshTupleJSON(t, projection), freshTupleJSON(t, answerprojection.Project(mcpResult, answerprojection.Budget{}))) {
		t.Fatalf("real MCP result differs through the shared projection: fresh=%s served=%s", freshTupleJSON(t, projection), freshTupleJSON(t, answerprojection.Project(mcpResult, answerprojection.Budget{})))
	}
}

func freshTupleJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func assertFreshTupleGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "work_item_fresh", name)
	if os.Getenv("UPDATE_WORK_ITEM_FRESH_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read producer golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("producer output differs from %s", path)
	}
}
