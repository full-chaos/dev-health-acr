package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// historicalParityResult is the parity fixture moved onto a historical
// axis, with an effective time DELIBERATELY earlier than the requested one
// -- the day-grain narrowing CHAOS-3781's Tier A rollups actually produce.
func historicalParityResult(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	requested := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	effective := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)

	result := parityInvestigationResult()
	result.Interpretation.TimeContext = contractsv1.ContextFabricTimeContext{
		Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &requested,
	}
	result.Temporal = &contractsv1.ContextFabricTemporalLabel{
		Requested:        result.Interpretation.TimeContext,
		Effective:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &effective},
		Grain:            contractsv1.ContextFabricGrainDay,
		CoverageComplete: false,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the historical parity fixture is not a valid canonical result: %v", err)
	}
	return result
}

// TestHistoricalAnswerReachesBothRealSurfacesLabelled is the end-to-end
// acceptance for exposing CHAOS-3781's time axis through the CHAOS-3746
// answer surface.
//
// The unit tests prove Project copies the label and the renderer prints it.
// Neither proves it SURVIVES the two real transports: the MCP tool encodes
// the projection through its own self-contained response schema, and the
// hosted route serializes it over HTTP. A field the Go type carries but
// either wire shape drops would pass every unit test and still leave an
// agent reading a March answer as today's.
//
// So this drives a real MCP tool call and a real HTTP request against the
// same stored historical result, and requires both to carry the label AND
// to agree with each other byte for byte.
func TestHistoricalAnswerReachesBothRealSurfacesLabelled(t *testing.T) {
	result := historicalParityResult(t)

	store := memoryinvestigation.NewStore()
	if err := store.Save(context.Background(), storage.Principal{OrgID: callerOrgID}, result,
		contextfabric.SourceWatermarkSnapshot{}, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: result.Temporal.Effective.AsOf}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}); err != nil {
		t.Fatalf("seed historical result: %v", err)
	}
	investigator := investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	})

	app, token := newParityHostedApp(t, investigator, store)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)

	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("sidecar bootstrap against the real hosted API failed: %v", err)
	}

	mcpProjection := callRealMCPInvestigateQuestion(t, boot, result.Question, 3, 1, 10)
	apiProjection := getRealAPIProjection(t, server, token, result.ResultID, 3, 1, 10)

	for name, projection := range map[string]contractsv1.ContextFabricAnswerProjection{
		"MCP": mcpProjection,
		"API": apiProjection,
	} {
		if projection.Temporal == nil {
			t.Fatalf("the %s surface served a historical answer with no temporal label", name)
		}
		if got := projection.Temporal.Effective.AsOf; got == nil || !got.Equal(*result.Temporal.Effective.AsOf) {
			t.Errorf("%s effective time = %v, want the canonical %s", name, got, result.Temporal.Effective.AsOf)
		}
		if projection.Temporal.CoverageComplete {
			t.Errorf("%s reported complete temporal coverage for an answer that had none", name)
		}
	}

	mcpEncoded, err := json.Marshal(mcpProjection)
	if err != nil {
		t.Fatal(err)
	}
	apiEncoded, err := json.Marshal(apiProjection)
	if err != nil {
		t.Fatal(err)
	}
	if string(mcpEncoded) != string(apiEncoded) {
		t.Fatalf("the two surfaces disagree on a historical answer.\n MCP = %s\n API = %s", mcpEncoded, apiEncoded)
	}
}
