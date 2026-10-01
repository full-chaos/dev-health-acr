package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// ingestColumnsRecheck is how long a negative probe stands before the source
// asks ClickHouse again.
const ingestColumnsRecheck = time.Minute

// ingestColumnsStatement counts the ingest columns the switch needs: the two
// ops migrations 099 and 100 add (team_project_ownership.last_synced,
// project_membership_transitions.ingested_at, and the presence view's
// last_synced) and projects.last_synced, which every schema has.
const ingestColumnsStatement = `SELECT count() FROM system.columns
WHERE database = currentDatabase() AND (
  (table = 'team_project_ownership' AND name = 'last_synced') OR
  (table = 'project_membership_transitions' AND name = 'ingested_at') OR
  (table = 'project_membership_presence' AND name = 'last_synced') OR
  (table = 'projects' AND name = 'last_synced'))`

const ingestColumnsWanted = 4

// ingestProbe caches whether the ingest columns exist. A positive answer is
// final for the process; a negative one is re-asked after ingestColumnsRecheck.
type ingestProbe struct {
	mu        sync.Mutex
	known     bool
	available bool
	checkedAt time.Time
}

// availableAt reports whether the teams/projects producers can page on their
// ingest columns. A failed probe keeps the last known answer; with none the
// answer is "no" for this call only, so a flaky probe cannot flip the cursor
// space once it has answered.
func (p *ingestProbe) availableAt(ctx context.Context, client contextpacket.ClickHouseQueryClient, logger *slog.Logger, now time.Time) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known && (p.available || now.Sub(p.checkedAt) < ingestColumnsRecheck) {
		return p.available, nil
	}
	rows, err := client.Query(ctx, ingestColumnsStatement, nil)
	var count uint64
	if err == nil {
		defer rows.Close()
		if !rows.Next() {
			err = fmt.Errorf("devhealthsource: ingest column probe returned no row: %w", rows.Err())
		} else {
			err = rows.Scan(&count)
		}
	}
	if err != nil {
		if !p.known {
			// No answer yet: page on the pre-migration columns, which work on
			// every schema, and ask again next call. Not cached, so a failed
			// probe never pins the source to either space.
			if logger != nil {
				logger.WarnContext(ctx, "ingest_cursor_unavailable: the ingest column probe failed; paging on provider/event time for now",
					"source", contextfabric.SanitizeLogAttr(TeamsProjectsSourceName), "cause_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", err)))
			}
			return false, nil
		}
		return p.available, nil
	}
	p.known, p.available, p.checkedAt = true, count == ingestColumnsWanted, now
	if !p.available && logger != nil {
		logger.WarnContext(ctx, "ingest_cursor_unavailable: team_project_ownership and project_membership_presence keep paging on provider/event time until the ingest columns exist",
			"columns_found", count, "columns_wanted", ingestColumnsWanted, "source", contextfabric.SanitizeLogAttr(TeamsProjectsSourceName))
	}
	return p.available, nil
}
