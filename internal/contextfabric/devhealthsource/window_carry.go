package devhealthsource

import "context"

// tableCarry lives for one overlapBatch call. It keeps, per table, the rows a
// COMPLETE read (no row past its statement limit) found after the walk, so the
// next page of that table is served from memory instead of a statement that
// can only return the same rows minus the ones already walked. A truncated
// table is never held: rows lie beyond its last returned row that only a
// statement can read.
type tableCarry struct {
	enabled bool
	held    map[string]carriedRead
	served  int
}

type carriedRead struct {
	from cursorState
	rows []candidate
}

func newTableCarry(enabled bool) *tableCarry {
	return &tableCarry{enabled: enabled, held: map[string]carriedRead{}}
}

// cursorBefore reports whether a sorts before b in keyset order.
func cursorBefore(a, b cursorState) bool {
	if !a.Since.Equal(b.Since) {
		return a.Since.Before(b.Since)
	}
	return a.After < b.After
}

// take serves the table's rows past walk from the held read. It declines when
// the table is not carry-eligible, nothing complete is held, or the walk stands
// before the cursor the held read started from (rows between the two were never
// read).
func (c *tableCarry) take(table entityTable, walk cursorState) ([]candidate, bool) {
	if c == nil || !c.enabled || !table.carry {
		return nil, false
	}
	held, ok := c.held[table.name]
	if !ok || cursorBefore(walk, held.from) {
		return nil, false
	}
	tail := make([]candidate, 0, len(held.rows))
	for _, row := range held.rows {
		if pastEdge(row, walk) {
			tail = append(tail, row)
		}
	}
	c.held[table.name] = carriedRead{from: walk, rows: tail}
	c.served++
	return append([]candidate(nil), tail...), true
}

// note takes the result of a statement the walk just ran at cursor from.
func (c *tableCarry) note(table entityTable, from cursorState, rows []candidate, truncated bool) {
	if c == nil || !c.enabled || !table.carry {
		return
	}
	if truncated {
		delete(c.held, table.name)
		return
	}
	c.held[table.name] = carriedRead{from: from, rows: append([]candidate(nil), rows...)}
}

// reset drops everything held: a new pass restarts the walk behind the old one.
func (c *tableCarry) reset() {
	if c == nil {
		return
	}
	clear(c.held)
}

// windowRead is readTable for the window walk: a carried table is served from
// memory, any other table runs its statement.
func (p sourcePlan) windowRead(ctx context.Context, table entityTable, orgID string, walk cursorState, carry *tableCarry) ([]candidate, bool, error) {
	if rows, ok := carry.take(table, walk); ok {
		return rows, false, nil
	}
	rows, truncated, err := readTable(ctx, table, p.client, orgID, walk, incrementalBatchCap)
	if err == nil {
		carry.note(table, walk, rows, truncated)
	}
	return rows, truncated, err
}
