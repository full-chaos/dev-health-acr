package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// canonicalResultOfAboutNineKilobytes is a valid result that measures above the
// smallest byte option a caller may ask for and far below the service ceiling.
func canonicalResultOfAboutNineKilobytes() contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	ready := false
	for i := 0; i < 48; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: "claim_" + strconv.Itoa(1000+i), Kind: contractsv1.ContextFabricFactStatus,
			Subject: subject, Field: "release_ready", Value: contractsv1.ContextFabricScalarValue{Boolean: &ready},
		})
	}
	result.Completeness.ClaimedFactsCount = len(result.ClaimedFacts)
	return result
}

func postInvestigationWithByteOption(t *testing.T, app *App, token string, option int) *httptest.ResponseRecorder {
	t.Helper()
	body := investigationRequestBody()
	body.Options.MaxSerializedBytes = option
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// A caller's max_serialized_bytes below the result's size bounds what is
// served, as the bounded projection that names the stored result; it is not a
// refusal. The service ceiling is the only refusal line.
func TestACallerByteOptionBelowTheResultSizeIsServedAsTheBoundedProjection(t *testing.T) {
	result := canonicalResultOfAboutNineKilobytes()
	store := memoryinvestigation.NewStore()
	if err := store.Save(context.Background(), seedPrincipal(callerOrgID), result, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatal(err)
	}
	app, token := newContextFabricTestAppWithResults(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), store)

	whole := postInvestigationWithByteOption(t, app, token, 262144)
	if whole.Code != http.StatusOK {
		t.Fatalf("option 262144: status = %d", whole.Code)
	}
	canonicalBytes := whole.Body.Len()
	if canonicalBytes <= contractsv1.ContextFabricSerializedBytesMin {
		t.Fatalf("fixture is %d bytes; the row needs a result above the %d byte option", canonicalBytes, contractsv1.ContextFabricSerializedBytesMin)
	}
	if mid := postInvestigationWithByteOption(t, app, token, 65536); mid.Code != http.StatusOK || mid.Body.Len() != canonicalBytes {
		t.Fatalf("option 65536: status = %d, %d bytes, want the canonical body unchanged (%d)", mid.Code, mid.Body.Len(), canonicalBytes)
	}

	option := contractsv1.ContextFabricSerializedBytesMin
	small := postInvestigationWithByteOption(t, app, token, option)
	if small.Code != http.StatusOK {
		t.Fatalf("option %d: status = %d body=%s, want 200", option, small.Code, small.Body.String())
	}
	if small.Body.Len() > option {
		t.Fatalf("served %d bytes against a %d byte option", small.Body.Len(), option)
	}
	var projection contractsv1.ContextFabricAnswerProjection
	if err := json.Unmarshal(small.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.SchemaVersion != contractsv1.ContextFabricAnswerProjectionSchema {
		t.Fatalf("schema_version = %q, want the projection", projection.SchemaVersion)
	}
	if !projection.ProjectionBudget.FullResultOmitted || !projection.ProjectionBudget.Truncated {
		t.Fatalf("projection_budget = %+v, want full_result_omitted and truncated declared", projection.ProjectionBudget)
	}
	if projection.ResultID != result.ResultID {
		t.Fatalf("result_id = %q, want %q", projection.ResultID, result.ResultID)
	}

	fetched := httptest.NewRecorder()
	app.Handler().ServeHTTP(fetched, investigationResultRequest(t, token, projection.ResultID))
	if fetched.Code != http.StatusOK {
		t.Fatalf("GET by result_id: status = %d", fetched.Code)
	}
	var canonical contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(fetched.Body.Bytes(), &canonical); err != nil {
		t.Fatal(err)
	}
	if len(canonical.ClaimedFacts) != 48 || fetched.Body.Len() < canonicalBytes-200 {
		t.Fatalf("GET by result_id served %d claimed facts, %d bytes; want the canonical result (%d bytes)", len(canonical.ClaimedFacts), fetched.Body.Len(), canonicalBytes)
	}
}

// The refusal line is the service's: a result over the ceiling is refused with
// the ceiling named, whatever the caller's option is.
func TestAResultOverTheServiceCeilingIsRefusedNamingTheCeilingNotTheCallersOption(t *testing.T) {
	result := canonicalResultOfAboutNineKilobytes()
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	app.config.MaxSerializedBytes = contractsv1.ContextFabricSerializedBytesMin

	refused := postInvestigationWithByteOption(t, app, token, contractsv1.ContextFabricSerializedBytesMin)
	if refused.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want 413", refused.Code, refused.Body.String())
	}
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, _ := body.Error.Details["max_serialized_bytes"].(float64); int(got) != contractsv1.ContextFabricSerializedBytesMin {
		t.Fatalf("max_serialized_bytes = %v, want the service ceiling %d", body.Error.Details["max_serialized_bytes"], contractsv1.ContextFabricSerializedBytesMin)
	}
}
