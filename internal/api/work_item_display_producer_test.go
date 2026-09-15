package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestWorkItemDisplayProducerFloor(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	f.client.rowsByPhase = map[string][][]any{"s1": {{f.client.memberID, "repo-1", "work-1", hostedTestRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)}}}
	body := investigationRequestBody()
	body.Options.MaxCohortMembers = 1
	body.Question = "What is the state and count of Project Alpha work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	result := serveFreshTupleRequest(t, f, body)
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	result = stored.Result
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	displayProducerArtifact(t, "floor.generated.json", raw)
	defer displayProducerTransports(t, f, stored, "floor")
	projection := answerprojection.Project(result, answerprojection.Budget{})
	markdown, truncated := sidecar.RenderAnswerProjectionMarkdown(projection, 24000)
	t.Logf("HTTP=200 phases=%v codec store round trip passed; canonical answer=%q", f.client.phases, result.DeterministicAnswer)
	for _, line := range strings.Split(markdown, "\n") {
		if strings.Contains(line, "2000") || strings.Contains(line, "## Cohort") {
			t.Log(line)
		}
	}
	t.Logf("markdown_truncated=%v lower_bound_disclosed=%v", truncated, strings.Contains(markdown, "at least"))
	if !strings.Contains(markdown, "at least") {
		t.Fatal("capped population renders as an exact cardinality; lower bound is missing")
	}
}

func TestWorkItemDisplayProducerStatuses(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	var info bytes.Buffer
	f.dependencies.Telemetry = contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&info, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = engine
	f.model.statusOnly = true
	f.client.rowsByPhase = map[string][][]any{}
	for i := 0; i < 12; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", workID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.client.rowsByPhase["s1"] = append(f.client.rowsByPhase["s1"], []any{id, "repo-1", workID, hostedTestRepository, uint8(1), uint64(12), uint64(12), uint64(0), uint64(0), uint64(0)})
		status := "open"
		if i == 9 {
			status = "waiting"
		}
		f.client.rowsByPhase["status"] = append(f.client.rowsByPhase["status"], []any{workID, status, "repo-1"})
		f.client.rowsByPhase["work"] = append(f.client.rowsByPhase["work"], []any{workID, strings.Repeat("Long descriptive title ", 21) + workID, "repo-1"})
	}
	body := investigationRequestBody()
	body.Options.MaxCohortMembers = 12
	body.Question = "What is the state and count of Project Alpha work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	result := serveFreshTupleRequest(t, f, body)
	stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(stored.Result)
	if err != nil {
		t.Fatal(err)
	}
	displayProducerArtifact(t, "statuses.generated.json", canonical)
	defer displayProducerTransports(t, f, stored, "statuses")
	beforeRender := info.Len()
	projection := answerprojection.Project(stored.Result, answerprojection.Budget{})
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	statuses := 0
	for _, fact := range result.ClaimedFacts {
		if fact.Kind == contextfabric.FactStatus {
			statuses++
		}
	}
	projected := 0
	for _, fact := range projection.KeyFacts {
		if fact.Kind == contextfabric.FactStatus {
			projected++
		}
	}
	t.Logf("HTTP=200 canonical_status_claims=%d projected_status_claims=%d cohort=%d facts_omitted=%d truncated=%v waiting_visible=%v current_state_chars=%d", statuses, projected, len(projection.Cohort.Members), projection.ProjectionBudget.FactsOmitted, projection.ProjectionBudget.Truncated, strings.Contains(string(raw), "waiting"), len(projection.CurrentState))
	t.Logf("Info log unchanged by projection=%v (%d bytes)", beforeRender == info.Len(), info.Len())
	displayProducerArtifact(t, "status-info.jsonl", info.Bytes())
	if projected != statuses || projected != 12 || !strings.Contains(string(raw), "waiting") {
		t.Fatal("waiting member status disappears from saved-result projection without an omission count")
	}
}

func displayProducerArtifact(t *testing.T, name string, data []byte) {
	t.Helper()
	if dir := os.Getenv("WORK_ITEM_DISPLAY_ARTIFACT_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Real bootstrap, protected HTTP, SDK dispatch and response validation run on
// the producer's actual stored carrier. Only reuse lookup is controlled.
func displayProducerTransports(t *testing.T, f *freshTupleProducerFixture, stored contextfabric.StoredInvestigationResult, name string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "display-info-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	f.app.logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo}))
	f.dependencies.Telemetry = contextfabric.NewSlogEngineTelemetry(f.app.logger)
	var outgoing string
	original := f.app.runtime.Investigator
	f.app.runtime.Investigator = investigatorFunc(func(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		outgoing = r.RequestID
		return original.Investigate(ctx, p, r)
	})
	server := httptest.NewTLSServer(f.app.InstrumentedHandler(f.app.Handler()))
	defer server.Close()
	configureSidecarEnvironment(t, server, f.token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatal(err)
	}
	apiProjection := getRealAPIProjection(t, server, f.token, stored.Result.ResultID, 10, 25, 100)
	displayProducerLeaves(t, stored.Result, apiProjection)
	if name == "statuses" {
		tight := getRealAPIProjection(t, server, f.token, stored.Result.ResultID, 10, 2, 100)
		if tight.Cohort == nil || len(tight.Cohort.Members) != 2 || len(tight.KeyFacts) != 3 || tight.ProjectionBudget.CohortMembersOmitted != 10 || tight.ProjectionBudget.FactsOmitted != 10 {
			t.Fatalf("actual API tight projection lost honest closure: %+v", tight.ProjectionBudget)
		}
		displayProducerArtifact(t, name+".tight-api.projection.json", mustRawJSON(t, tight))
	}
	mcpServer := acrmcp.NewServerWithDiagnostics(boot, "test-version", file)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "display-proof", Version: "0.0.1"}, nil)
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := mcpServer.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	input := v1.MCPInvestigateQuestionRequest{Question: stored.Result.Question}
	args, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	f.dependencies.ReuseGate = lifetimeTupleReuseGate{stored: stored}
	f.engine, err = contextfabric.NewEngine(f.dependencies, f.engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.client.phases)
	syntheses := 0
	f.model.observe = func(contextfabric.SynthesisInput) { syntheses++ }
	called, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "investigate_question", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	if called.IsError {
		raw, _ := os.ReadFile(file.Name())
		displayProducerArtifact(t, name+".mcp-failure-info.jsonl", raw)
		t.Fatalf("real MCP error: %s; Engine error=%v", mustRawJSON(t, called.Content), f.engineErr)
	}
	var response v1.MCPInvestigateQuestionResponse
	if err = json.Unmarshal(mustRawJSON(t, called.StructuredContent), &response); err != nil {
		t.Fatal(err)
	}
	if err = response.Validate(); err != nil {
		t.Fatal(err)
	}
	if !response.Structured.Reused {
		t.Error("MCP failed to accept the actual producer carrier")
	}
	rawDiagnostic, _ := os.ReadFile(file.Name())
	displayProducerArtifact(t, name+".mcp-diagnostic.jsonl", rawDiagnostic)
	displayProducerLeaves(t, stored.Result, response.Structured)
	if response.RenderedMarkdown.Truncated || len(response.RenderedMarkdown.Markdown) > 24000 {
		t.Errorf("default production Markdown clipped: bytes=%d truncated=%t", len(response.RenderedMarkdown.Markdown), response.RenderedMarkdown.Truncated)
	}
	if name == "floor" && !strings.Contains(response.RenderedMarkdown.Markdown, "Work-item count: at least 2000.") {
		t.Error("MCP lost floor qualification")
	}
	if name == "statuses" {
		for _, fact := range stored.Result.ClaimedFacts {
			if fact.Kind != contextfabric.FactStatus {
				continue
			}
			want := *fact.Value.String
			found := false
			for _, line := range strings.Split(response.RenderedMarkdown.Markdown, "\n") {
				if strings.HasPrefix(line, "- Work item:") && strings.Contains(line, fact.Subject.Label) && strings.Contains(line, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("MCP member/status mismatch for %s", fact.Subject.CanonicalID)
			}
		}
	}
	if syntheses != 0 || !reflect.DeepEqual(f.client.phases[before:], []string{"s1"}) {
		t.Errorf("reuse did fresh content/synthesis: phases=%v syntheses=%d", f.client.phases[before:], syntheses)
	}
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := certify.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	basis := "recorded"
	if name == "floor" {
		basis = "floor"
	}
	_, err = certify.Certify(parsed, certify.Assertion{Event: eventspec.AnswerDisplay, Want: map[string]any{"request_id": outgoing, "surface": "investigate_question", "canonical_members": len(stored.Result.Cohort.Members), "projected_members": len(stored.Result.Cohort.Members), "canonical_eligible_facts": len(stored.Result.ClaimedFacts), "projected_facts": len(stored.Result.ClaimedFacts), "max_facts": 50, "max_members": 20, "max_evidence": 25, "facts_omitted": 0, "members_omitted": 0, "evidence_omitted": 0, "count_basis": basis, "markdown_rendered": true, "markdown_truncated": false}})
	if err != nil {
		t.Fatal(err)
	}
	apiEvents := 0
	for _, line := range parsed.LinesWithMsg(eventspec.AnswerDisplay.Msg) {
		if line["surface"] == "result_by_id" {
			apiEvents++
			want := map[string]any{"request_id": line["request_id"], "surface": "result_by_id", "canonical_members": len(stored.Result.Cohort.Members), "canonical_eligible_facts": len(stored.Result.ClaimedFacts), "max_facts": 50, "max_evidence": 100, "count_basis": basis, "markdown_rendered": false, "markdown_truncated": false}
			if line["max_members"] == float64(2) {
				want["projected_members"], want["projected_facts"], want["members_omitted"], want["facts_omitted"] = 2, 3, 10, 10
				want["projection_truncated"], want["evidence_omitted"] = true, 10
			} else {
				want["projected_members"], want["projected_facts"], want["members_omitted"], want["facts_omitted"] = len(stored.Result.Cohort.Members), len(stored.Result.ClaimedFacts), 0, 0
				want["max_members"], want["evidence_omitted"] = 25, 0
			}
			_, err = certify.Certify(parsed, certify.Assertion{Event: eventspec.AnswerDisplay, Want: want})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	wantAPIEvents := 1
	if name == "statuses" {
		wantAPIEvents = 2
	}
	if apiEvents != wantAPIEvents {
		t.Errorf("actual projected API observations=%d want%d", apiEvents, wantAPIEvents)
	}
	if outgoing == stored.Result.RequestID {
		t.Error("reuse did not mint a distinct current request correlation")
	}
	displayProducerArtifact(t, name+".reuse.mcp.json", mustRawJSON(t, response))
	displayProducerArtifact(t, name+".reuse.md", []byte(response.RenderedMarkdown.Markdown))
	displayProducerArtifact(t, name+".caller-info.jsonl", raw)
}

func displayProducerLeaves(t *testing.T, r contextfabric.InvestigationResult, p v1.ContextFabricAnswerProjection) {
	t.Helper()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Cohort == nil || len(p.Cohort.Members) != len(r.Cohort.Members) {
		t.Fatalf("producer members lost: want=%d projection=%s", len(r.Cohort.Members), mustRawJSON(t, p))
	}
	for i, m := range p.Cohort.Members {
		if !reflect.DeepEqual(m.Subject, r.Cohort.Members[i].Subject) {
			t.Error("member identity/order changed")
		}
		if m.RankingComputed || m.AttentionRank != 0 || m.Score != nil {
			t.Error("invented ranking")
		}
		for _, ref := range m.EvidenceRefIDs {
			if !slices.Contains(p.EvidenceRefIDs, ref) {
				t.Error("lost required evidence")
			}
		}
	}
	for _, want := range r.ClaimedFacts {
		if want.Kind != contextfabric.FactStatus && want.Kind != contextfabric.FactWork && want.Kind != v1.ContextFabricFactCardinality {
			continue
		}
		found := false
		for _, got := range p.KeyFacts {
			if got.ClaimID == want.ClaimID {
				found = true
				if got.Subject.Kind != want.Subject.Kind || got.Subject.CanonicalID != want.Subject.CanonicalID || !reflect.DeepEqual(got.Value, want.Value) {
					t.Errorf("producer leaf changed: %s", want.ClaimID)
				}
			}
		}
		if !found {
			t.Errorf("producer claim lost: %s", want.ClaimID)
		}
	}
	if len(p.PrincipalDrivers) != 0 || len(p.Cohort.RankingTable) != 0 {
		t.Error("invented judgment")
	}
}

func TestWorkItemDisplayProducerControls(t *testing.T) {
	for _, mode := range []string{"status", "work", "zero", "s1", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			failure := mode
			if mode == "unknown" {
				failure = ""
			}
			f := newFreshTupleProducerFixture(t, failure)
			if mode == "unknown" {
				f.client.rowsByPhase = map[string][][]any{"status": {{"work-1", "unknown", "repo-1"}}}
			}
			body := investigationRequestBody()
			body.Question = "What is the state and count of Project Alpha work items?"
			body.Options.MaxCohortMembers = 1
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			r := serveFreshTupleRequest(t, f, body)
			logFile, err := os.CreateTemp(t.TempDir(), "control-info-*.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			f.app.logger = slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
			server := httptest.NewTLSServer(f.app.InstrumentedHandler(f.app.Handler()))
			defer server.Close()
			p := getRealAPIProjection(t, server, f.token, r.ResultID, 10, 25, 100)
			if err := p.Validate(); err != nil {
				t.Fatal(err)
			}
			md, cut := sidecar.RenderAnswerProjectionMarkdown(p, 24000)
			if cut {
				t.Fatal("small control clipped")
			}
			claims := 0
			for _, fact := range p.KeyFacts {
				if fact.Kind == v1.ContextFabricFactCardinality {
					claims++
				}
			}
			if mode == "s1" {
				if claims != 0 || strings.Contains(md, "count:") {
					t.Error("unmeasured gained count")
				}
			} else if claims != 1 {
				t.Error("measured count lost")
			}
			if mode == "status" && !strings.Contains(md, "No status evidence in this answer") {
				t.Error("missing status was concealed")
			}
			if mode == "unknown" && !strings.Contains(md, "unknown") {
				t.Error("explicit unknown was lost")
			}
			if mode == "zero" && !strings.Contains(md, "Recorded work-item count: 0.") {
				t.Error("measured zero lost")
			}
			if err := logFile.Sync(); err != nil {
				t.Fatal(err)
			}
			rawLog, err := os.ReadFile(logFile.Name())
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := certify.Parse(rawLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := parsed.LinesWithMsg(eventspec.AnswerDisplay.Msg)
			if len(lines) != 1 {
				t.Fatalf("control display Info count=%d want1", len(lines))
			}
			basis, members := "recorded", 1
			if mode == "s1" {
				basis, members = "absent", 0
			}
			if mode == "zero" {
				members = 0
			}
			_, err = certify.Certify(parsed, certify.Assertion{Event: eventspec.AnswerDisplay, Want: map[string]any{
				"request_id": lines[0]["request_id"], "surface": "result_by_id", "canonical_members": members, "projected_members": members,
				"count_basis": basis, "floor_counts": 0, "recorded_counts": claims, "facts_omitted": 0, "members_omitted": 0, "markdown_rendered": false, "markdown_truncated": false,
			}})
			if err != nil {
				t.Fatal(err)
			}
			displayProducerArtifact(t, mode+".caller-info.jsonl", rawLog)
			displayProducerArtifact(t, mode+".generated.json", mustRawJSON(t, r))
			displayProducerArtifact(t, mode+".projection.json", mustRawJSON(t, p))
			displayProducerArtifact(t, mode+".md", []byte(md))
		})
	}
}

func TestWorkItemDisplayUnavailableCensusDoesNotInferFloor(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	f.client.rowsByPhase = map[string][][]any{"s1": {{f.client.memberID, "repo-1", "work-1", hostedTestRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)}}}
	body := investigationRequestBody()
	body.Options.MaxCohortMembers = 1
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	r := serveFreshTupleRequest(t, f, body)
	stored, err := f.store.Get(context.Background(), f.principal, r.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	before := portableStoredCarrierBytes(t, stored)
	for _, mode := range []string{"absent", "malformed", "unsupported", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			copy := stored
			state := *stored.SemanticState
			copy.SemanticState = &state
			census := *state.WorkItemCensus
			state.WorkItemCensus = &census
			switch mode {
			case "absent":
				state.WorkItemCensus = nil
			case "malformed":
				census.AuthorizationDigest = "invalid"
			case "unsupported":
				census.Version = "unavailable-version"
			case "unreadable":
				copy.SemanticState = nil
				copy.SemanticStateRead = contextfabric.SemanticStateReadMalformed
			}
			principal := f.principal
			principal.RepositoryScopes = []string{"*"}
			served := contextfabric.ServeStoredWorkItemTuple(copy.Result, copy.SemanticState, copy.SemanticStateRead, principal)
			p := answerprojection.Project(served.Result, answerprojection.Budget{})
			for _, d := range p.CoverageDetails {
				if d.Kind == v1.ContextFabricSubjectWorkItem && d.Code == v1.ContextFabricCoverageDetailKindCensusTruncated {
					t.Error("unavailable census regained D47")
				}
			}
			md, cut := sidecar.RenderAnswerProjectionMarkdown(p, 24000)
			if cut || !strings.Contains(md, "Recorded work-item count: 2000. This view does not establish whether the population count is exact.") || strings.Contains(md, "Work-item count: at least") {
				t.Errorf("metadata fallback count: %s", md)
			}
			if len(p.Limitations) == 0 {
				t.Error("unmeasured limitation disappeared")
			}
			displayProducerArtifact(t, "metadata-"+mode+".projection.json", mustRawJSON(t, p))
		})
	}
	assertPortableStoredCarrierUnchanged(t, "display fallback", before, stored)
}
