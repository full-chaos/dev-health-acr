package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type servedPayloadLedger struct {
	t         *testing.T
	validated map[string]int
}

func (l *servedPayloadLedger) check(tool, schemaFile, label string, result *mcpsdk.CallToolResult) {
	l.t.Helper()
	if result == nil || result.IsError {
		l.t.Errorf("%s/%s: no successful tool result", tool, label)
		return
	}
	if result.StructuredContent == nil {
		l.t.Errorf("%s/%s: no structured content", tool, label)
		return
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		l.t.Fatal(err)
	}
	if string(encoded) == "{}" || string(encoded) == "null" {
		l.t.Errorf("%s/%s: empty structured content", tool, label)
		return
	}
	if err := validateAgainstEmbedded(l.t, schemaFile, encoded); err != nil {
		l.t.Errorf("%s/%s: served payload violates its own output schema: %v", tool, label, err)
		return
	}
	l.validated[tool]++
}

func mutatedExample(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestServedPayloadsValidateAgainstTheirOwnOutputSchemas(t *testing.T) {
	ledger := &servedPayloadLedger{t: t, validated: map[string]int{}}
	ctx := context.Background()

	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	cft, err := invokeContextForTask(ctx, boot, callToolRequest(t, map[string]any{
		"goal": "investigate flaky checkout tests", "repository": map[string]any{"slug": "acme/widgets"}, "scope": map[string]any{"branch": "main"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolContextForTask, contextForTaskResponseSchemaFile, "fixture", cft)
	se, err := invokeSourceEvidence(ctx, boot, callToolRequest(t, map[string]any{"evidence_ref_id": "ev_abc123"}))
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolSourceEvidence, sourceEvidenceResponseSchemaFile, "fixture", se)

	h := newDTHosted(t)
	dt := h.boot(t, append([]string{toolGraphQLQuery}, dtAllTools...)...)
	// The catalog schema is closed (additionalProperties false); that is a separate forward-compat gap from open vocabularies.
	catalogBody := mutatedExample(t, []byte(dtCatalogBody), func(doc map[string]any) { delete(doc, "future_member") })
	h.catalog = func(w http.ResponseWriter, r *http.Request) { dtRaw(w, http.StatusOK, string(catalogBody)) }
	ledger.check(toolDataCatalog, dataCatalogResponseSchemaFile, "catalog", dtCall(t, dt, toolDataCatalog, `{"sections":["operations","limits"]}`))
	ledger.check(toolFindSubjects, findSubjectsResponseSchemaFile, "list", dtCall(t, dt, toolFindSubjects, `{"kind":"repository"}`))
	ledger.check(toolRunOperation, runOperationResponseSchemaFile, "served", dtCall(t, dt, toolRunOperation, `{"operation":"hotspots"}`))
	ledger.check(toolGraphQLQuery, graphqlQueryResponseSchemaFile, "served", dtCall(t, dt, toolGraphQLQuery, `{"query":"{ hotspots { rows { filePath } } }"}`))
	h.operate = func(w http.ResponseWriter, r *http.Request) { dtRaw(w, http.StatusOK, dtRefusedBody) }
	ledger.check(toolRunOperation, runOperationResponseSchemaFile, "refused", dtCall(t, dt, toolRunOperation, `{"operation":"investmentBreakdown"}`))

	root := filepath.Join("..", "..", "contracts", "examples", "v1")
	factsExample, err := os.ReadFile(filepath.Join(root, "mcp_read_facts_response.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"fact_served", "read_no_fact", "measured_zero", "not_applicable", "unavailable", "stale", "truncated", "withheld_response_budget"} {
		body := mutatedExample(t, factsExample, func(doc map[string]any) {
			rows, _ := doc["coverage"].([]any)
			for _, row := range rows {
				row.(map[string]any)["outcome"] = outcome
			}
		})
		hosted := &readFactsHosted{respond: body}
		ledger.check(toolReadFacts, readFactsResponseSchemaFile, outcome, callReadFacts(t, newReadFactsBootstrap(t, hosted, true), readFactsArgs))
	}

	relExample, err := os.ReadFile(filepath.Join(root, "mcp_read_relationships_response.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, edgeType := range []string{"BELONGS_TO_PROJECT", "BELONGS_TO_PULL_REQUEST", "BELONGS_TO_REPOSITORY", "BLOCKS", "CORRELATED_WITH_INCIDENT", "DOCUMENTED_BY", "DUPLICATES", "HAS_EPISODE", "LINKS_PULL_REQUEST", "OWNED_BY_TEAM", "PART_OF", "RELATED_TO"} {
		body := mutatedExample(t, relExample, func(doc map[string]any) {
			edges, _ := doc["edges"].([]any)
			for _, edge := range edges {
				edge.(map[string]any)["type"] = edgeType
			}
		})
		hosted := &readRelationshipsHosted{respond: body}
		ledger.check(toolReadRelationships, readRelationshipsResponseSchemaFile, edgeType, callReadRelationships(t, newReadRelationshipsBootstrap(t, hosted, true), readRelationshipsArgs))
	}

	result := parityResult()
	answerBoot := withInterpretationTool(answerFixtureBootstrap(t, result, nil))
	args, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})
	if err != nil {
		t.Fatal(err)
	}
	iq, err := invokeInvestigateQuestion(ctx, answerBoot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolInvestigateQuestion, investigateQuestionResponseSchemaFile, "degraded", iq)
	iwi, err := invokeInvestigateWithInterpretation(answerBoot, interpretationArgs(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolInvestigateWithInterpretation, investigateQuestionResponseSchemaFile, "supplied", iwi)
	resultArgs, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	ir, err := invokeInvestigationResult(ctx, answerBoot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: resultArgs}})
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolInvestigationResult, investigationResultResponseSchemaFile, "whole", ir)

	wfx := newFixtureServer(t)
	wfx.EpisodeHandler = func(w http.ResponseWriter, r *http.Request) {
		var created contractsv1.AgentEpisodeCreate
		if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
			t.Error(err)
		}
		recorded := validAgentEpisodeFixture(created, false)
		recorded.SchemaVersion = contractsv1.AgentEpisodeSchema
		writeJSONFixture(t, w, http.StatusCreated, recorded)
	}
	client, closeFn := connectedClient(t, newWritebackFixtureBootstrap(t, wfx))
	defer closeFn()
	recorded, err := client.CallTool(ctx, &mcpsdk.CallToolParams{Name: toolRecordEpisode, Arguments: recordEpisodeArguments("summary", "transcript", "default_90d")})
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolRecordEpisode, recordEpisodeResponseSchemaFile, "recorded", recorded)
	nfx := newFixtureServer(t)
	nfx.EpisodeHandler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	nclient, ncloseFn := connectedClient(t, newWritebackFixtureBootstrap(t, nfx))
	defer ncloseFn()
	noPersist, err := nclient.CallTool(ctx, &mcpsdk.CallToolParams{Name: toolRecordEpisode, Arguments: recordEpisodeArguments("summary", "transcript", "no_persist")})
	if err != nil {
		t.Fatal(err)
	}
	ledger.check(toolRecordEpisode, recordEpisodeResponseSchemaFile, "no_persist", noPersist)

	for _, tool := range []string{toolContextForTask, toolSourceEvidence, toolDataCatalog, toolFindSubjects, toolRunOperation, toolGraphQLQuery, toolReadFacts, toolReadRelationships, toolInvestigateQuestion, toolInvestigateWithInterpretation, toolInvestigationResult, toolRecordEpisode} {
		if ledger.validated[tool] == 0 {
			t.Errorf("%s: no served payload was validated; the measurement did not happen", tool)
		}
	}
}
