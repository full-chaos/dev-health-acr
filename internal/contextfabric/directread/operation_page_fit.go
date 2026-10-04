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
		set  func(rows []any)
		rows []any
	}
	var best *listRef
	consider := func(rows []any, set func([]any)) {
		if len(rows) > 1 && (best == nil || len(rows) > len(best.rows)) {
			best = &listRef{set: set, rows: rows}
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
			consider(v, func(rows []any) { root[field] = rows })
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if rows, ok := v[key].([]any); ok {
					v, key := v, key
					consider(rows, func(rows []any) { v[key] = rows })
				}
			}
		}
	}
	if best == nil {
		return pageCut{}, false
	}
	total := len(best.rows)
	encode := func(n int) (json.RawMessage, bool) {
		best.set(best.rows[:n])
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
	return pageCut{data: fitted, rowsReturned: lo, rowsRead: total}, true
}

// cutStatement is the plain statement of a cut. It names only what the
// request really has to get the rest: a narrower scope and a larger
// max_bytes.
func (c pageCut) statement(maxBytes int) string {
	return fmt.Sprintf("Returned %d of %d rows read: the answer was cut to the largest whole-row page that fits max_bytes (%d). The rest were left out. Narrow the scope or the filters, or raise max_bytes (up to %d), to see more.", c.rowsReturned, c.rowsRead, maxBytes, MaxOperationMaxBytes)
}
