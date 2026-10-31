package hosted

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
)

// CHAOS-11952 (lead ruling 2): the read rule for a submission with no thread
// follows the retention switch, the one the retention job runs on: the
// composed runtime's result store carries it, on and off.
func TestOpen_passesTheRetentionSwitchToTheReadStore(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		configureGraph(t)
		events := []string{}
		request := testBuildRequest(t, &events, "")
		request.config.EnableContextFabricInvestigations = true
		request.config.AskDevRetentionEnabled = enabled
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
		store, ok := runtime.Dependencies.Runtime.InvestigationResults.(*pginvestigation.Store)
		if !ok {
			t.Fatalf("result store = %T, want the PostgreSQL store", runtime.Dependencies.Runtime.InvestigationResults)
		}
		if store.UnthreadedRecordRetention() != enabled {
			t.Fatalf("switch %t: the read store's rule = %t", enabled, store.UnthreadedRecordRetention())
		}
		_ = runtime.Close()
	}
}
