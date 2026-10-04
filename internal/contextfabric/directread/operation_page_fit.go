package directread

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// cutList is one row list of an answer that was cut.
type cutList struct {
	path         string
	rowsReturned int
	rowsRead     int
	hasPageInfo  bool
	hasTotal     bool
}

// pageCut describes an answer cut to the largest whole-row pages that fit
// the response budget.
type pageCut struct {
	data  json.RawMessage
	lists []cutList
	// fullBytes is the serialized size of the uncut data.
	fullBytes int
}

func (c pageCut) rowsReturned() int {
	n := 0
	for _, l := range c.lists {
		n += l.rowsReturned
	}
	return n
}

func (c pageCut) rowsRead() int {
	n := 0
	for _, l := range c.lists {
		n += l.rowsRead
	}
	return n
}

type listRef struct {
	path   string
	parent map[string]any
	key    string
	rows   []any
}

// collectLists finds every row list of the answer: each array of more than
// one element reached through objects only (never through another list).
func collectLists(prefix string, obj map[string]any, out *[]listRef) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := obj[key].(type) {
		case []any:
			if len(v) > 1 {
				*out = append(*out, listRef{path: path, parent: obj, key: key, rows: v})
			}
		case map[string]any:
			collectLists(path, v, out)
		}
	}
}

// fitListPage cuts the row lists of an over-budget answer, the longest first,
// to whole-row prefixes: the longest list is cut to the largest prefix that
// makes the serialized data fit maxBytes; when even one row of it is not
// enough it is cut to one row and the next list is cut the same way. It
// reports false when the answer has no list or does not fit with one row of
// each list: such an answer stays refused.
func fitListPage(data json.RawMessage, maxBytes int) (pageCut, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return pageCut{}, false
	}
	var refs []listRef
	collectLists("", root, &refs)
	if len(refs) == 0 {
		return pageCut{}, false
	}
	sort.SliceStable(refs, func(i, j int) bool { return len(refs[i].rows) > len(refs[j].rows) })

	failed := false
	size := func() int {
		out, err := json.Marshal(root)
		if err != nil {
			failed = true
			return 0
		}
		return len(out)
	}
	cutTo := func(r listRef, n int) {
		r.parent[r.key] = r.rows[:n]
		describeReturnedPage(r.parent, r.rows[:n])
	}

	var cuts []cutList
	fits := false
	for _, r := range refs {
		total := len(r.rows)
		cutTo(r, 1)
		if size() > maxBytes {
			cuts = append(cuts, cutListOf(r, 1))
			continue
		}
		// Largest n in [1,total-1] that fits. The size is increasing in n
		// except for the end cursor, whose length varies by at most spread
		// bytes, so a search can stop up to spread/2 rows short: scan on.
		lo, hi := 1, total-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			cutTo(r, mid)
			if size() <= maxBytes {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		for n := lo + 1; n < total && n <= lo+cursorSpread(r.rows)+1; n++ {
			cutTo(r, n)
			if size() <= maxBytes {
				lo = n
			}
		}
		cutTo(r, lo)
		cuts = append(cuts, cutListOf(r, lo))
		fits = true
		break
	}
	if failed || !fits {
		return pageCut{}, false
	}
	out, err := json.Marshal(root)
	if err != nil || len(out) > maxBytes {
		return pageCut{}, false
	}
	return pageCut{data: out, lists: cuts, fullBytes: len(data)}, true
}

func cutListOf(r listRef, n int) cutList {
	l := cutList{path: r.path, rowsReturned: n, rowsRead: len(r.rows)}
	_, l.hasPageInfo = r.parent["pageInfo"].(map[string]any)
	_, l.hasTotal = r.parent["totalCount"]
	return l
}

// cursorSpread is the largest difference in length between the cursors of
// the rows of one list.
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
		if lo < 0 || len(c) < lo {
			lo = len(c)
		}
		if len(c) > hi {
			hi = len(c)
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
	parts := make([]string, 0, len(c.lists))
	pageInfo, total := false, false
	for _, l := range c.lists {
		parts = append(parts, fmt.Sprintf("%s %d of %d", l.path, l.rowsReturned, l.rowsRead))
		pageInfo = pageInfo || l.hasPageInfo
		total = total || l.hasTotal
	}
	out := fmt.Sprintf("Returned %d of %d rows read (%s): the answer was cut to whole rows to fit max_bytes (%d); the rest were left out.", c.rowsReturned(), c.rowsRead(), strings.Join(parts, "; "), maxBytes)
	switch {
	case c.fullBytes <= MaxOperationMaxBytes:
		out += fmt.Sprintf(" Repeat the same call with max_bytes of at least %d to receive every row.", c.fullBytes)
	case op.hasClientVariable():
		out += fmt.Sprintf(" The whole answer does not fit the largest max_bytes (%d): narrow the call with the operation's variables.", MaxOperationMaxBytes)
	default:
		out += fmt.Sprintf(" The whole answer does not fit the largest max_bytes (%d) and this operation has no variable to narrow it.", MaxOperationMaxBytes)
	}
	if pageInfo {
		out += " pageInfo describes this page: more rows exist."
	}
	if total {
		out += " totalCount is the count of the full read, not of this page."
	}
	return out
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
