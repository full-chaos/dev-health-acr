package devhealthsource

import (
	"context"
	"slices"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// DIMENSION TABLES GO BEFORE THE FACT WALK. A dimension table holds the
// entities other rows refer to (repositories; teams and projects). The
// from-zero read emits a table it got whole with the first batch
// (catch_up.go). A dimension table with more rows than that read asks for
// would wait for the walk, and the sync re-stamps its rows, so they sort last
// for the whole walk. Such a table is read here instead: keyset pages of the
// ordinary page size, table after table, before any fact row.
//
// The position of that read is part of the cursor (cursorState.Dim), with the
// fact position still zero. A batch's cursor is stored only after the backend
// applied the batch, so a failed apply reads the same pages again and a
// restart continues after the last applied page. The phase ends when a read of
// the last table returns every row it has left; only then does the fact walk
// start, from zero, and it emits the same rows again when it reaches them.

// dimensionPosition is where the dimension phase of a from-zero build stands.
type dimensionPosition struct {
	// Tables are the dimension tables of the phase, in read order; At indexes
	// the one being read. At == len(Tables): every table was read whole.
	Tables []string `json:"tables"`
	At     int      `json:"at"`
	// Since and After are the keyset position in Tables[At]; zero at its start.
	Since time.Time `json:"since"`
	After string    `json:"after"`
	// Batches and Rows count what the phase emitted before this position.
	Batches int `json:"batches"`
	Rows    int `json:"rows"`
}

// dimensionPagesPerBatch bounds the pages one dimension batch reads.
const dimensionPagesPerBatch = 5

const (
	dimensionBatchMessage = "devhealthsource from-zero read emits dimension tables before the fact walk"
	dimensionEndedMessage = "devhealthsource dimension phase ended: every dimension table was read whole"
)

// dimensionTablesIn returns the plan's dimension tables among names, in plan
// order.
func (p sourcePlan) dimensionTablesIn(names []string) []string {
	var out []string
	for _, table := range p.tables {
		if table.dimension && slices.Contains(names, table.name) {
			out = append(out, table.name)
		}
	}
	return out
}

func (p sourcePlan) tableNamed(name string) (entityTable, bool) {
	for _, table := range p.tables {
		if table.name == name {
			return table, true
		}
	}
	return entityTable{}, false
}

// withinBatchBounds reports whether the candidates fit one batch.
func withinBatchBounds(all []candidate) bool {
	entities, relationships, tombstones := candidateCounts(all)
	return entities <= contractsv1.ContextFabricProjectionBatchMaxEntities &&
		relationships <= contractsv1.ContextFabricProjectionBatchMaxRelationships &&
		tombstones <= contractsv1.ContextFabricProjectionBatchMaxTombstones
}

// dimensionBatch reads on from at and returns the next batch of dimension
// rows. cursor is the checkpoint's own cursor: "" for the first batch, which
// also carries the seed. When nothing is left to read it starts the fact walk.
func (p sourcePlan) dimensionBatch(ctx context.Context, orgID, cursor string, at dimensionPosition) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	if cursor == "" {
		all = p.seedCandidates(orgID)
	}
	rows, pages := 0, 0
	for at.At < len(at.Tables) && pages < dimensionPagesPerBatch+maxOmittedPageSkips {
		table, known := p.tableNamed(at.Tables[at.At])
		if !known {
			// A table this plan does not read (a cursor of another build of
			// the source): nothing to read for it.
			at.At, at.Since, at.After = at.At+1, time.Time{}, ""
			continue
		}
		from := cursorState{Since: at.Since, After: at.After}
		page, truncated, err := readTable(ctx, table, p.client, orgID, from, incrementalBatchCap)
		if err == nil {
			var bound pageBound
			err = p.boundRead(ctx, orgID, table.name, from, &bound, page, truncated)
		}
		if err != nil {
			logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
			return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
		}
		if len(all) > 0 && !withinBatchBounds(append(all[:len(all):len(all)], page...)) {
			// The page does not fit beside what this batch holds: it opens
			// the next batch. The position stays before it.
			break
		}
		normalizeCandidates(page, p.observeNormalization)
		all = append(all, page...)
		rows += sourceRows(page)
		pages++
		if truncated {
			last := page[len(page)-1]
			at.Since, at.After = last.position(), last.sortKey
		} else {
			at.At, at.Since, at.After = at.At+1, time.Time{}, ""
		}
		if pages >= dimensionPagesPerBatch && carriesPayload(all) {
			break
		}
	}
	items := partitionProjectableCandidates(all, p.quarantineObserver(orgID))
	if !carriesPayload(items) {
		if at.At >= len(at.Tables) {
			return p.walkAfterDimensions(ctx, orgID, cursor, at)
		}
		// Pages were read and hold nothing to project: the position moves
		// without a batch, and the next call reads on.
		p.window.setAhead(p.windowScope, true)
		if next, err := encodeCursorIn(p.cursorSpace(), cursorState{Dim: &at}); err == nil {
			p.noteConsumed(orgID, next)
		}
		p.noteYield()
		return contextfabric.ProjectionBatch{}, false, nil
	}
	at.Batches, at.Rows = at.Batches+1, at.Rows+rows
	next, err := encodeCursorIn(p.cursorSpace(), cursorState{Dim: &at})
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	batch, err := buildBatchTo(orgID, p.source, p.version, cursor, next, items, false, false, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	// The fact walk has not started: rows lie beyond this batch.
	p.window.setAhead(p.windowScope, true)
	p.forgetConsumed(orgID)
	p.logDimensionBatch(ctx, orgID, at, rows)
	return batch, true, nil
}

// walkAfterDimensions starts the fact walk of a from-zero build whose
// dimension tables were read whole: the first page from zero, with the tables
// the from-zero read returns whole (catch_up.go). The tables the phase read
// are not left truncated; they are done.
func (p sourcePlan) walkAfterDimensions(ctx context.Context, orgID, cursor string, at dimensionPosition) (contextfabric.ProjectionBatch, bool, error) {
	_, complete, truncatedTables, err := p.snapshotRead(ctx, orgID)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	p.logDimensionEnded(ctx, orgID, at)
	p.complete = complete
	p.truncatedTables = slices.DeleteFunc(truncatedTables, func(name string) bool { return slices.Contains(at.Tables, name) })
	return p.pagedBatch(ctx, orgID, cursor, cursorState{}, false)
}

func (p sourcePlan) logDimensionBatch(ctx context.Context, orgID string, at dimensionPosition, rows int) {
	if p.logger == nil {
		return
	}
	p.logger.InfoContext(ctx, dimensionBatchMessage,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"dimension_tables", contextfabric.SanitizeLogStrings(at.Tables),
		"tables_read_whole", contextfabric.SanitizeLogStrings(at.Tables[:min(at.At, len(at.Tables))]),
		"phase_complete", at.At >= len(at.Tables),
		"batch_rows", rows, "phase_batches", at.Batches, "phase_rows", at.Rows)
}

func (p sourcePlan) logDimensionEnded(ctx context.Context, orgID string, at dimensionPosition) {
	if p.logger == nil {
		return
	}
	p.logger.InfoContext(ctx, dimensionEndedMessage,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"dimension_tables", contextfabric.SanitizeLogStrings(at.Tables),
		"phase_batches", at.Batches, "phase_rows", at.Rows)
}
