package hosted

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The retrieval route decides with the engine's own gate: a composed runtime
// that carries a result store also carries the gate that decides its reads,
// and it is the very gate the engine decides prior-result reads with.
func TestOpen_wiresTheEnginesStoredResultGateToTheRetrievalRoute(t *testing.T) {
	configureGraph(t)
	events := []string{}
	request := testBuildRequest(t, &events, "")
	request.config.EnableContextFabricInvestigations = true
	openPostgres := request.factories.openPostgres
	request.factories.openPostgres = func(ctx context.Context, cfg config.Config, logger *slog.Logger) (postgresComponents, error) {
		components, err := openPostgres(ctx, cfg, logger)
		database := sql.OpenDB(idleConnector{})
		t.Cleanup(func() { _ = database.Close() })
		components.db = database
		return components, err
	}
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	deps := runtime.Dependencies.Runtime
	if deps.Investigator == nil || deps.InvestigationResults == nil {
		t.Fatalf("investigator=%v results=%v; the composition under test was not built", deps.Investigator, deps.InvestigationResults)
	}
	engine, ok := deps.Investigator.(interface {
		StoredResultGate() *contextfabric.StoredResultGate
	})
	if !ok || engine.StoredResultGate() == nil {
		t.Fatalf("investigator %T carries no stored-result gate", deps.Investigator)
	}
	gate, ok := deps.StoredResultGate.(*contextfabric.StoredResultGate)
	if !ok || gate != engine.StoredResultGate() {
		t.Fatalf("route gate = %T %p, want the engine's gate %p", deps.StoredResultGate, gate, engine.StoredResultGate())
	}
}
