package directread

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// An operation has a primary list when the catalogue advertises a row-limit
// variable for it and its served outputs hold exactly one list reached from
// the root field through objects only. Only such an operation is cut to fit
// the response budget; every other shape (two or more top-level lists, no
// row-limit variable, lists inside rows) keeps the response_budget refusal.

// PrimaryList is the output path of the operation's one primary list, for
// example "workGraphEdges.edges", or false when the operation has none.
func (op *OperationPolicy) PrimaryList() (string, bool) {
	limited := false
	for _, v := range op.Variables {
		if v.Allowed && v.Source == SourceClient && v.Kind != "object" && (v.Path == "limit" || strings.HasSuffix(v.Path, ".limit")) {
			limited = true
			break
		}
	}
	if !limited {
		return "", false
	}
	lists := map[string]bool{}
	for _, out := range op.Outputs {
		if out.BeyondDocument {
			continue
		}
		if i := strings.Index(out.Path, "[*]"); i >= 0 {
			lists[out.Path[:i]] = true
		}
	}
	if len(lists) != 1 {
		return "", false
	}
	for path := range lists {
		return path, true
	}
	return "", false
}

// hasClientVariable reports whether the client can set a scalar variable.
func (op *OperationPolicy) hasClientVariable() bool {
	for _, v := range op.Variables {
		if v.Allowed && v.Source == SourceClient && v.Kind != "object" {
			return true
		}
	}
	return false
}

// pageCut describes an answer cut to the largest whole-row page of its
// primary list that fits the response budget.
type pageCut struct {
	data         json.RawMessage
	path         string
	rowsReturned int
	rowsRead     int
	fullBytes    int
	hasPageInfo  bool
	hasTotal     bool
}

// fitListPage cuts the primary list at listPath (an output path such as
// "workGraphEdges.edges") to the largest whole-row prefix whose serialized
// data fits maxBytes. It reports false when the path holds no list of more
// than one row, or when not even one row fits: such an answer stays refused.
func fitListPage(data json.RawMessage, listPath string, maxBytes int) (pageCut, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return pageCut{}, false
	}
	segs := strings.Split(listPath, ".")
	parent := root
	for _, seg := range segs[:len(segs)-1] {
		next, ok := parent[seg].(map[string]any)
		if !ok {
			return pageCut{}, false
		}
		parent = next
	}
	key := segs[len(segs)-1]
	rows, ok := parent[key].([]any)
	if !ok || len(rows) < 2 {
		return pageCut{}, false
	}
	total := len(rows)
	failed := false
	size := func(n int) int {
		parent[key] = rows[:n]
		describeReturnedPage(parent, rows[:n])
		out, err := json.Marshal(root)
		if err != nil {
			failed = true
		}
		return len(out)
	}
	if size(1) > maxBytes {
		return pageCut{}, false
	}
	// Largest n in [1,total-1] that fits. The size grows with n except for
	// the end cursor, whose length varies by at most the spread of the row
	// cursors, so the search can stop up to spread/2 rows short: scan on.
	lo, hi := 1, total-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if size(mid) <= maxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for n := lo + 1; n < total && n <= lo+cursorSpread(rows)+1; n++ {
		if size(n) <= maxBytes {
			lo = n
		}
	}
	out := size(lo)
	if failed || out > maxBytes {
		return pageCut{}, false
	}
	final, err := json.Marshal(root)
	if err != nil {
		return pageCut{}, false
	}
	cut := pageCut{data: final, path: listPath, rowsReturned: lo, rowsRead: total, fullBytes: len(data)}
	_, cut.hasPageInfo = parent["pageInfo"].(map[string]any)
	_, cut.hasTotal = parent["totalCount"]
	return cut, true
}

// cursorSpread is the largest difference in serialized length (as the size
// check measures it, with escapes) between the cursors of the rows of one list.
func cursorSpread(rows []any) int {
	lo, hi := -1, -1
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		c, ok := m["cursor"].(string)
		if !ok {
			continue
		}
		enc, err := json.Marshal(c)
		if err != nil {
			continue
		}
		if lo < 0 || len(enc) < lo {
			lo = len(enc)
		}
		if len(enc) > hi {
			hi = len(enc)
		}
	}
	if lo < 0 {
		return 0
	}
	return hi - lo
}

// describeReturnedPage makes the page position fields of a cut answer
// describe the returned page, not the upstream page: more rows exist, and
// the end position is the cursor of the last returned row when the rows
// carry one, otherwise unknown (null). The start position is the first row's
// and stays. totalCount is not touched: it counts the full read.
func describeReturnedPage(parent map[string]any, rows []any) {
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

// statement is the plain statement of a cut. It offers only what exists: the
// same call with a larger max_bytes, and a narrower request when the
// operation has a variable the client can set. A continuation is not
// offered: no operation takes an offset or a cursor.
func (c pageCut) statement(maxBytes int, op *OperationPolicy) string {
	out := fmt.Sprintf("Returned %d of %d rows read (%s): the answer was cut to whole rows to fit max_bytes (%d); the rest were left out.", c.rowsReturned, c.rowsRead, c.path, maxBytes)
	switch {
	case c.fullBytes <= MaxOperationMaxBytes:
		out += fmt.Sprintf(" Repeat the same call with max_bytes of at least %d to receive every row.", c.fullBytes)
	case op.hasClientVariable():
		out += fmt.Sprintf(" The whole answer does not fit the largest max_bytes (%d): narrow the call with the operation's variables.", MaxOperationMaxBytes)
	default:
		out += fmt.Sprintf(" The whole answer does not fit the largest max_bytes (%d) and this operation has no variable to narrow it.", MaxOperationMaxBytes)
	}
	if c.hasPageInfo {
		out += " pageInfo describes this page: more rows exist."
	}
	if c.hasTotal {
		out += " totalCount is the count of the full read, not of this page."
	}
	return out
}
