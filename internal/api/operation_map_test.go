package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func TestRequestOperationNamesEveryContextFabricRoute(t *testing.T) {
	cells := []struct {
		method, path string
		want         observability.Operation
	}{
		{http.MethodPost, ContextFabricInvestigationsPath, observability.OperationInvestigation},
		{http.MethodGet, ContextFabricInvestigationsPath + "/res_0123456789abcdef", observability.OperationInvestigationResult},
		{http.MethodGet, ContextFabricDataCatalogPath, observability.OperationDataCatalog},
		{http.MethodPost, ContextFabricDataSubjectsPath, observability.OperationDataSubjects},
		{http.MethodPost, ContextFabricDataFactsPath, observability.OperationDataFacts},
		{http.MethodPost, ContextFabricDataRelationshipsPath, observability.OperationDataRelationships},
		{http.MethodPost, ContextFabricDataOperationsPath, observability.OperationDataOperations},
		{http.MethodPost, ContextFabricDataGraphQLPath, observability.OperationDataGraphQL},
	}
	for _, cell := range cells {
		request := httptest.NewRequest(cell.method, cell.path, nil)
		got := requestOperation(request)
		if got != cell.want {
			t.Errorf("%s %s: operation %q, want %q", cell.method, cell.path, got, cell.want)
		}
	}
}

func TestDataRouteObservationCarriesItsOperation(t *testing.T) {
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	app, _ := newHostedTestApp(t, nil, &hooks, nil, nil, nil)
	request := httptest.NewRequest(http.MethodPost, ContextFabricDataFactsPath, nil)
	app.Handler().ServeHTTP(httptest.NewRecorder(), request)
	if got := sink.only(t).Operation; got != observability.OperationDataFacts {
		t.Fatalf("observation operation %q, want %q", got, observability.OperationDataFacts)
	}
}
