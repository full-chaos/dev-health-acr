package hosted

import (
	"context"
	"database/sql"
	"errors"

	postgresmigrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

func checkPostgresRuntime(ctx context.Context, database *sql.DB, runner *postgresmigrations.Runner, episodeWriteback bool) error {
	if err := database.PingContext(ctx); err != nil {
		return errors.New("PostgreSQL runtime is unavailable")
	}
	if runner == nil || runner.VerifyCurrent(ctx, database) != nil {
		return errors.New("PostgreSQL runtime schema is unavailable")
	}
	const query = `
SELECT
  has_table_privilege(current_user, 'acr.schema_migrations', 'SELECT')
    AND has_table_privilege(current_user, 'acr.client_credentials', 'SELECT')
    AND has_table_privilege(current_user, 'acr.client_credentials', 'UPDATE')
    AND has_table_privilege(current_user, 'acr.context_packet_snapshots', 'SELECT')
    AND has_table_privilege(current_user, 'acr.context_packet_snapshots', 'INSERT')
    AND has_table_privilege(current_user, 'acr.context_packet_snapshots', 'DELETE')
    AND has_table_privilege(current_user, 'acr.context_packet_snapshots', 'UPDATE')
    AND has_table_privilege(current_user, 'acr.audit_events', 'INSERT')
    AND (NOT $1
      OR (has_table_privilege(current_user, 'acr.agent_episodes', 'SELECT')
        AND has_table_privilege(current_user, 'acr.agent_episodes', 'INSERT')))`
	var privilegesReady bool
	if err := database.QueryRowContext(ctx, query, episodeWriteback).Scan(&privilegesReady); err != nil || !privilegesReady {
		return errors.New("PostgreSQL runtime schema is unavailable")
	}
	return nil
}
