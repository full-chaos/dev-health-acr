package devhealthsource

import (
	"encoding/json"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file holds the CHAOS-3833 (embed-text spec v2 §2) producer-side field
// discipline. Producers emit CANONICAL field values -- array fields already
// deduplicated, sorted, element-capped and joined; free-text heads already
// capped -- so the same source row always yields byte-identical properties,
// and the falkorgraph per-kind composition can treat every property as a
// deterministic scalar. The composition applies its own per-field caps too
// (the authoritative ones, per §0 decision (b)); the producer caps below
// exist to bound what is persisted as a graph property at all.

// ticketKeyAlias derives a work item's ticket-key alias from its
// work_item_id (spec §2, review R2 -- the parse rule is EXACT): the
// substring after the FIRST ':', taken verbatim to the end, so
// "linear:CHAOS-100" -> "CHAOS-100", "jira:ABC-1" -> "ABC-1", and an id
// with additional colons keeps the remainder intact ("a:b:c" -> "b:c").
// An id with NO colon derives no alias (""). Grounded: 100% of live ids
// are "linear:"-prefixed, but the rule never assumes the prefix set.
func ticketKeyAlias(workItemID string) string {
	_, remainder, found := strings.Cut(workItemID, ":")
	if !found {
		return ""
	}
	return remainder
}

// joinedSortedList canonicalizes an array field for embedding text (spec §2
// determinism rule, review R2): trim, drop empties, deduplicate, SORT,
// element-cap, per-element rune-cap, join. An unordered source array must
// never produce two different texts for the same row.
func joinedSortedList(values []string, maxElements, maxElementRunes int, separator string) string {
	seen := make(map[string]struct{}, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = headRunes(strings.TrimSpace(value), maxElementRunes)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	sort.Strings(cleaned)
	if len(cleaned) > maxElements {
		cleaned = cleaned[:maxElements]
	}
	return strings.Join(cleaned, separator)
}

// parsedRepoTags canonicalizes repos.tags, which is a Nullable(String)
// holding a JSON-rendered array (live: `["github","Go"]`) rather than a
// real Array(String) -- see devhealthschema. A value that does not parse
// as a JSON string array yields "" rather than leaking raw JSON into
// search text.
func parsedRepoTags(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return ""
	}
	return joinedSortedList(tags, 10, 40, " ")
}

// headRunes returns the first limit runes of text -- the same semantics as
// embedprovider.TruncateRunes, restated here so a producer does not import
// the embedder-construction package for a string helper. The authoritative
// per-field caps live in the falkorgraph composition; these producer-side
// heads only bound what is stored as a graph property.
func headRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// setStringProperty sets a non-empty, head-capped string property, skipping
// blanks so a template line whose field is absent is skipped rather than
// rendered around an empty value.
func setStringProperty(properties map[string]contractsv1.ContextFabricScalarValue, name, value string, maxRunes int) {
	value = headRunes(strings.TrimSpace(value), maxRunes)
	if value == "" {
		return
	}
	properties[name] = stringScalar(value)
}

func intScalar(value int64) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{Integer: &value}
}
