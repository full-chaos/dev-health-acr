package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Text summaries of the direct data tools (CHAOS-7072, design G "Text
// content: a bounded summary and first rows, not a copy of the structured
// JSON"). The structured content of each tool is the hosted API JSON
// verbatim; the text content is what a client that shows only text to its
// model still needs: the terminal states first (call, completeness, result,
// status), the counts, a refusal's code and reason, then the first rows.
//
// Rules this file keeps, each pinned by a test:
//
//   - The text never exceeds DataTextMaxBytes. Lines and blocks are added
//     whole while they fit; nothing is cut in the middle of an untrusted
//     block, so a fence is never left open.
//   - Text that came from a provider or from the caller (subject labels and
//     ids, row values) sits only inside an untrustedBlock, and any other
//     hosted string is neutralised inline (safeInline). The first line names
//     the whole text untrusted data.
//   - A summary that cannot be built says so in fixed text; it never echoes
//     a raw body.
//   - No model is called and nothing is read but the JSON passed in.

// DataTextMaxBytes is the largest text content of a direct data tool.
const DataTextMaxBytes = 4096

const (
	dataTextReserve  = 96
	dataTextRowBytes = 300
	dataTextMaxRows  = 5
)

const dataTextTruncatedNote = "[text is bounded; the structured content holds the full answer]"

const dataTextUnreadable = "The answer could not be summarised. Read the structured content."

// dataText builds a bounded text. add refuses a piece that would not fit,
// keeping a reserve for the closing note.
type dataText struct {
	max       int
	b         strings.Builder
	truncated bool
}

func newDataText(max int) *dataText {
	if max <= dataTextReserve*2 {
		max = DataTextMaxBytes
	}
	t := &dataText{max: max}
	t.b.WriteString(untrustedDataHeader + "\n")
	return t
}

func (t *dataText) fits(piece string) bool {
	return t.b.Len()+len(piece)+1+dataTextReserve <= t.max
}

// line adds one line when it fits; it reports whether it did.
func (t *dataText) line(text string) bool {
	if !t.fits(text) {
		t.truncated = true
		return false
	}
	t.b.WriteString(text)
	t.b.WriteString("\n")
	return true
}

// block adds several lines as one unit, or none of them.
func (t *dataText) block(lines []string) bool {
	joined := strings.Join(lines, "\n")
	if !t.fits(joined) {
		t.truncated = true
		return false
	}
	t.b.WriteString(joined)
	t.b.WriteString("\n")
	return true
}

func (t *dataText) String() string {
	if t.truncated {
		t.b.WriteString(dataTextTruncatedNote + "\n")
	}
	out := t.b.String()
	if len(out) > t.max {
		// Unreachable by construction (the reserve holds the note); a hard
		// guard so the bound never depends on the arithmetic above.
		out = truncateToValidUTF8(out, t.max)
	}
	return out
}

func unreadableDataText() string {
	t := newDataText(DataTextMaxBytes)
	t.line(dataTextUnreadable)
	return t.String()
}

// plainToken renders a short hosted token (a status, a code, a name, a
// closed-vocabulary word or a fixed reason sentence) as it is when every rune
// is plain text, and neutralised (safeInline) otherwise. Plain text here means
// letters, digits, space and _ . , : ; - / ( ) ' and the token stays under 200
// bytes: nothing that can open a link, an element or a table, so codes such as
// basis_dependent_shape stay readable and searchable.
func plainToken(value string) string {
	if len(value) > 200 {
		return safeInline(value)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(" _.,:;-/()'", r):
		default:
			return safeInline(value)
		}
	}
	return value
}

// compactRow renders one decoded JSON value as a single bounded line.
func compactRow(v any, limit int) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "(row could not be rendered)"
	}
	text := string(encoded)
	if len(text) <= limit {
		return text
	}
	cut := truncateToValidUTF8(text, limit)
	return cut + "..."
}

// RenderDataCatalogSummary summarises a data_catalog answer.
func RenderDataCatalogSummary(raw json.RawMessage, max int) string {
	var view struct {
		ContractVersion string   `json:"contract_version"`
		Sections        []string `json:"sections"`
		Operations      *struct {
			CallerClass string `json:"caller_class"`
			Available   bool   `json:"available"`
			Reason      string `json:"reason"`
			Operations  []struct {
				Name      string `json:"name"`
				Purpose   string `json:"purpose"`
				Available bool   `json:"available"`
			} `json:"operations"`
			NotServed     []json.RawMessage `json:"not_served"`
			RefusedShapes []json.RawMessage `json:"refused_shapes"`
		} `json:"operations"`
		Facts *struct {
			Served bool `json:"served"`
		} `json:"facts"`
		Subjects *struct {
			Kinds []json.RawMessage `json:"kinds"`
		} `json:"subjects"`
		Relationships *struct {
			Types []json.RawMessage `json:"types"`
		} `json:"relationships"`
		Limits *struct {
			MaxBytesDefault int `json:"max_bytes_default"`
			MaxBytesCap     int `json:"max_bytes_cap"`
			FindPageMax     int `json:"find_page_max"`
		} `json:"limits"`
		Caller struct {
			Scopes     []string `json:"scopes"`
			GrantClass string   `json:"grant_class"`
		} `json:"caller"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return unreadableDataText()
	}
	t := newDataText(max)
	t.line(fmt.Sprintf("data_catalog (%s): sections %s.", plainToken(view.ContractVersion), plainToken(strings.Join(view.Sections, ", "))))
	scopes := make([]string, 0, len(view.Caller.Scopes))
	for _, scope := range view.Caller.Scopes {
		scopes = append(scopes, plainToken(scope))
	}
	t.line(fmt.Sprintf("Your grant class: %s. Scopes: %s.", plainToken(view.Caller.GrantClass), strings.Join(scopes, ", ")))
	if ops := view.Operations; ops != nil {
		state := "yes"
		if !ops.Available {
			state = "no (" + plainToken(ops.Reason) + ")"
		}
		t.line(fmt.Sprintf("run_operation available to you now: %s. Operations served to you: %d. Not served: %d. Refused variable shapes: %d.", state, len(ops.Operations), len(ops.NotServed), len(ops.RefusedShapes)))
		shown := 0
		for _, op := range ops.Operations {
			purpose := op.Purpose
			if len(purpose) > 90 {
				purpose = truncateToValidUTF8(purpose, 90) + "..."
			}
			if !t.line("- " + plainToken(op.Name) + ": " + plainToken(purpose)) {
				break
			}
			shown++
		}
		if shown < len(ops.Operations) {
			t.line(fmt.Sprintf("(%d more operations in the structured content)", len(ops.Operations)-shown))
		}
	}
	if view.Facts != nil && !view.Facts.Served {
		t.line("Facts: not served in this release.")
	}
	if view.Subjects != nil {
		t.line(fmt.Sprintf("Subject kinds: %d.", len(view.Subjects.Kinds)))
	}
	if view.Relationships != nil {
		t.line(fmt.Sprintf("Relationship types: %d. An edge shows a relation, not a cause.", len(view.Relationships.Types)))
	}
	if l := view.Limits; l != nil {
		t.line(fmt.Sprintf("Limits: run_operation max_bytes default %d, cap %d; find_subjects page up to %d.", l.MaxBytesDefault, l.MaxBytesCap, l.FindPageMax))
	}
	t.line("Missing is not healthy and not zero. No person data is served.")
	return t.String()
}

// RenderFindSubjectsSummary summarises a find_subjects answer and lists the
// first subjects that fit.
func RenderFindSubjectsSummary(raw json.RawMessage, max int) string {
	var view struct {
		Status   string `json:"status"`
		Subjects []struct {
			Kind        string `json:"kind"`
			CanonicalID string `json:"canonical_id"`
			Label       string `json:"label"`
			Match       string `json:"match"`
		} `json:"subjects"`
		Population struct {
			Returned   int  `json:"returned"`
			TotalKnown int  `json:"total_known"`
			Truncated  bool `json:"truncated"`
		} `json:"population"`
		Page struct {
			Returned   int    `json:"returned"`
			Complete   bool   `json:"complete"`
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
		SearchedKinds []string `json:"searched_kinds"`
		Request       struct {
			Mode string `json:"mode"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return unreadableDataText()
	}
	t := newDataText(max)
	t.line(fmt.Sprintf("find_subjects: status=%s; mode=%s.", plainToken(view.Status), plainToken(view.Request.Mode)))
	bound := ""
	if view.Population.Truncated {
		bound = " (a lower bound: the read hit a bound)"
	}
	t.line(fmt.Sprintf("Returned %d; %d readable subjects known%s.", len(view.Subjects), view.Population.TotalKnown, bound))
	next := "none"
	if view.Page.NextCursor != "" {
		next = "present: pass it as cursor for the next page"
	}
	t.line(fmt.Sprintf("Page complete: %t; next_cursor: %s.", view.Page.Complete, next))
	switch view.Status {
	case "empty":
		kinds := make([]string, 0, len(view.SearchedKinds))
		for _, kind := range view.SearchedKinds {
			kinds = append(kinds, plainToken(kind))
		}
		t.line("Searched kinds: " + strings.Join(kinds, ", ") + ". An empty answer is not proof that no such subject exists.")
	case "ambiguous":
		t.line("The name matched more than one subject. Choose by canonical_id.")
	}
	count := len(view.Subjects)
	if count > 0 {
		for k := min(count, 50); k > 0; k-- {
			lines := []string{"Subjects (first " + fmt.Sprint(k) + " of " + fmt.Sprint(count) + "): kind | canonical_id | label | match"}
			rows := make([]string, 0, k)
			for _, s := range view.Subjects[:k] {
				rows = append(rows, s.Kind+" | "+s.CanonicalID+" | "+s.Label+" | "+s.Match)
			}
			lines = append(lines, untrustedBlock("subject ids and labels", strings.Join(rows, "\n"))...)
			if t.block(lines) {
				break
			}
		}
	}
	t.line("Take ids from here or from a response; never build one.")
	return t.String()
}

// RenderOperationSummary summarises a run_operation answer: the terminal
// states first, a refusal's code and reason, the size, then the first rows of
// the first list in the data.
func RenderOperationSummary(raw json.RawMessage, max int) string {
	var view struct {
		Call         string `json:"call"`
		Completeness string `json:"completeness"`
		Result       string `json:"result"`
		Operation    string `json:"operation"`
		Refusal      *struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
			Path   string `json:"path"`
		} `json:"refusal"`
		EffectiveScope *struct {
			RepoIDs       []string `json:"repo_ids"`
			ForcedByGrant bool     `json:"forced_by_grant"`
		} `json:"effective_scope"`
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Class string `json:"class"`
		} `json:"errors"`
		Page struct {
			ReturnedBytes int `json:"returned_bytes"`
			MaxBytes      int `json:"max_bytes"`
		} `json:"page"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return unreadableDataText()
	}
	t := newDataText(max)
	result := view.Result
	if result == "" {
		result = "none"
	}
	t.line(fmt.Sprintf("run_operation %s: call=%s; completeness=%s; result=%s.", plainToken(view.Operation), plainToken(view.Call), plainToken(view.Completeness), plainToken(result)))
	if view.Completeness == "unknown" {
		t.line("Completeness unknown means unknown: do not call this complete.")
	}
	switch view.Result {
	case "empty_unverified":
		t.line("Empty and unverified: this is not proof of no data, and not healthy.")
	case "empty_declared":
		t.line("Empty, and the payload declared itself complete.")
	}
	if view.Refusal != nil {
		line := fmt.Sprintf("Refused: code=%s; reason: %s", plainToken(view.Refusal.Code), plainToken(view.Refusal.Reason))
		if view.Refusal.Path != "" {
			line += " (path " + plainToken(view.Refusal.Path) + ")"
		}
		t.line(line)
		t.line("A refusal is terminal and typed: change the request, do not retry it unchanged.")
	}
	if len(view.Errors) > 0 {
		classes := make([]string, 0, len(view.Errors))
		for _, e := range view.Errors {
			classes = append(classes, plainToken(e.Class))
		}
		t.line("Upstream error classes: " + strings.Join(classes, ", ") + ".")
	}
	if view.EffectiveScope != nil {
		t.line(fmt.Sprintf("Scope: %d repositories; forced by your grant: %t.", len(view.EffectiveScope.RepoIDs), view.EffectiveScope.ForcedByGrant))
	}
	t.line(fmt.Sprintf("Data size: %d bytes of at most %d.", view.Page.ReturnedBytes, view.Page.MaxBytes))
	if len(view.Data) > 0 && !bytes.Equal(bytes.TrimSpace(view.Data), []byte("null")) {
		renderOperationData(t, view.Data)
	}
	return t.String()
}

// renderOperationData adds the shape of data and its first rows.
func renderOperationData(t *dataText, data json.RawMessage) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.line("Data could not be read for the summary.")
		return
	}
	var arrays []string
	var first []any
	firstPath := ""
	var walk func(path string, v any, depth int)
	walk = func(path string, v any, depth int) {
		if depth > 4 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				child := k
				if path != "" {
					child = path + "." + k
				}
				walk(child, x[k], depth+1)
			}
		case []any:
			arrays = append(arrays, fmt.Sprintf("%s (%d)", plainToken(path), len(x)))
			if first == nil && len(x) > 0 {
				if _, isObject := x[0].(map[string]any); isObject {
					first, firstPath = x, path
				}
			}
		}
	}
	walk("", decoded, 0)
	if len(arrays) > 0 {
		if len(arrays) > 8 {
			arrays = append(arrays[:8], "...")
		}
		t.line("Lists in data: " + strings.Join(arrays, ", ") + ".")
	}
	if first != nil {
		count := len(first)
		for k := min(count, dataTextMaxRows); k > 0; k-- {
			rows := make([]string, 0, k)
			for _, row := range first[:k] {
				rows = append(rows, compactRow(row, dataTextRowBytes))
			}
			lines := []string{fmt.Sprintf("First rows of %s (%d of %d):", plainToken(firstPath), k, count)}
			lines = append(lines, untrustedBlock("rows", strings.Join(rows, "\n"))...)
			if t.block(lines) {
				return
			}
		}
		return
	}
	if len(arrays) == 0 {
		t.block(append([]string{"Data (bounded):"}, untrustedBlock("data", compactRow(decoded, 600))...))
	}
}

// DataTextWithinBound reports whether text respects the direct data text
// bound and is valid UTF-8. Tests use it as the oracle.
func DataTextWithinBound(text string) bool {
	return len(text) <= DataTextMaxBytes && utf8.ValidString(text)
}
