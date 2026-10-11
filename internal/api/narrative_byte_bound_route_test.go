package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A stored row at the write maximum for limitations and warnings must still
// be servable as a projection under the service's byte ceiling, whatever the
// caller's item budget: the caller cannot shrink free text by asking for
// fewer drivers.
func TestStoredProjectionOfMaximalNarrativesFitsTheServedByteCeiling(t *testing.T) {
	const ceiling = 131072
	result := parityInvestigationResult()
	result.Limitations, result.Warnings = nil, nil
	for i := 0; i < contractsv1.ContextFabricProjectedNarrativeMaxCount; i++ {
		result.Limitations = append(result.Limitations, "limitation "+strconv.Itoa(i)+" "+strings.Repeat("x", 1900))
		result.Warnings = append(result.Warnings, "warning "+strconv.Itoa(i)+" "+strings.Repeat("y", 1900))
	}
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("fixture is not a readable stored row: %v", err)
	}
	investigator := investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	})
	app, token := newParityHostedAppWithBudget(t, investigator, legacyResultStore{result: result},
		limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20})
	app.config.RequestTimeout = time.Minute
	app.config.MaxSerializedBytes = ceiling
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/context-fabric/investigations/"+result.ResultID+
		"?view=projection&max_drivers=1&max_cohort_members=1&max_evidence_refs=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%d bytes), want 200 within the %d byte ceiling", response.StatusCode, len(body), ceiling)
	}
	if len(body) > ceiling {
		t.Errorf("served %d bytes over the %d byte ceiling", len(body), ceiling)
	}
	var projection contractsv1.ContextFabricAnswerProjection
	if err := json.Unmarshal(body, &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Limitations) == 0 || len(projection.Warnings) == 0 {
		t.Fatalf("a non-empty list was emptied: %d limitations, %d warnings", len(projection.Limitations), len(projection.Warnings))
	}
	if projection.ProjectionBudget.LimitationsOmitted == 0 || projection.ProjectionBudget.WarningsOmitted == 0 {
		t.Errorf("cut lists reported omitted %d/%d", projection.ProjectionBudget.LimitationsOmitted, projection.ProjectionBudget.WarningsOmitted)
	}
}
