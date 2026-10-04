package directread

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// pageCut describes a list answer cut to the largest whole-row page that
// fits the response budget.
type pageCut struct {
	data         json.RawMessage
	rowsReturned int
	rowsRead     int
	// fullBytes is the serialized size of the uncut data.
	fullBytes int
	// hasTotal is set when the answer carries a totalCount of the full read.
	hasTotal bool
}

// fitListPage cuts the longest row list of an over-budget answer to the
// largest whole-row prefix whose serialized data fits maxBytes. The lists
// considered are the arrays directly under a root field's object, or the
// root field itself when it is an array. It reports false when the answer
// has no list, or when not even one row fits: such an answer stays refused.
func fitListPage(data json.RawMessage, maxBytes int) (pageCut, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return pageCut{}, false
	}
	type listRef struct {
		set    func(rows []any)
		rows   []any
		parent map[string]any
	}
	var best *listRef
	consider := func(rows []any, parent map[string]any, set func([]any)) {
		if len(rows) > 1 && (best == nil || len(rows) > len(best.rows)) {
			best = &listRef{set: set, rows: rows, parent: parent}
		}
	}
	fields := make([]string, 0, len(root))
	for k := range root {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	for _, field := range fields {
		switch v := root[field].(type) {
		case []any:
			field := field
			consider(v, nil, func(rows []any) { root[field] = rows })
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if rows, ok := v[key].([]any); ok {
					v, key := v, key
					consider(rows, v, func(rows []any) { v[key] = rows })
				}
			}
		}
	}
	if best == nil {
		return pageCut{}, false
	}
	total := len(best.rows)
	hasTotal := false
	if best.parent != nil {
		_, hasTotal = best.parent["totalCount"]
	}
	encode := func(n int) (json.RawMessage, bool) {
		best.set(best.rows[:n])
		describeReturnedPage(best.parent, best.rows[:n])
		out, err := json.Marshal(root)
		return out, err == nil
	}
	lo, hi := 0, total-1
	var fitted json.RawMessage
	for lo < hi {
		mid := (lo + hi + 1) / 2
		out, ok := encode(mid)
		if !ok {
			return pageCut{}, false
		}
		if len(out) <= maxBytes {
			lo, fitted = mid, out
		} else {
			hi = mid - 1
		}
	}
	if lo < 1 {
		return pageCut{}, false
	}
	out, ok := encode(lo)
	if !ok || len(out) > maxBytes {
		return pageCut{}, false
	}
	fitted = out
	return pageCut{data: fitted, rowsReturned: lo, rowsRead: total, fullBytes: len(data), hasTotal: hasTotal}, true
}

// describeReturnedPage makes the page position fields of a cut answer
// describe the returned page, not the upstream page: more rows exist, and
// the end position is the cursor of the last returned row when the rows
// carry one, otherwise unknown (null). The start position is the first row's
// and stays. totalCount is not touched: it counts the full read.
func describeReturnedPage(parent map[string]any, rows []any) {
	if parent == nil {
		return
	}
	info, ok := parent["pageInfo"].(map[string]any)
	if !ok {
		return
	}
	if _, has := info["hasNextPage"]; has {
		info["hasNextPage"] = true
	}
	if _, has := info["endCursor"]; has {
		info["endCursor"] = nil
		if last, ok := rows[len(rows)-1].(map[string]any); ok {
			if cursor, ok := last["cursor"].(string); ok {
				info["endCursor"] = cursor
			}
		}
	}
}

// statement is the plain statement of a cut. No operation has an offset or
// a cursor variable, so the one way to the rest is the same call with a
// larger max_bytes (the size of the uncut data, when it is within the
// maximum), or a narrower scope or filters.
func (c pageCut) statement(maxBytes int) string {
	out := fmt.Sprintf("Returned %d of %d rows read: the answer was cut to the largest whole-row page that fits max_bytes (%d); the rest were left out.", c.rowsReturned, c.rowsRead, maxBytes)
	if c.fullBytes <= MaxOperationMaxBytes {
		out += fmt.Sprintf(" Repeat the same call with max_bytes of at least %d to receive all %d rows.", c.fullBytes, c.rowsRead)
	} else {
		out += fmt.Sprintf(" All %d rows do not fit the largest max_bytes (%d): narrow the scope or the filters.", c.rowsRead, MaxOperationMaxBytes)
	}
	out += " This operation has no offset or cursor, so continuing from the end of this page is not possible; pageInfo describes this page (more rows exist)."
	if c.hasTotal {
		out += " totalCount is the count of the full read, not of this page."
	}
	return out
}
