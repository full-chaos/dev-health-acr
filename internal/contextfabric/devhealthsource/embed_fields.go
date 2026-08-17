package devhealthsource

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
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

// repositoryBareNameAlias derives a repository's bare-name alias (CHAOS-3884
// Part A) from its org-qualified canonical slug: the last "/"-delimited
// segment. "full-chaos/dev-health-acr" -> "dev-health-acr". A slug with no
// "/" already IS its own bare name, so this returns "" for it (no
// redundant self-alias -- NodeCandidate's own label check already covers
// that case via subject.Label, and MergeMechanisms/AliasAttributes would
// otherwise carry a value that is byte-identical to the label for every
// single-segment slug this deployment happens to have).
//
// IsASCIIIdentityTerm gate (spot-check item 3a, "monitor the premise, not
// just the equivalence"): NormalizeAliasTerm's EqualFold-equivalence proof
// is scoped to the verified ASCII identity alphabet this org's live data
// uses (graphrank.NormalizeAliasTerm's own doc comment). Threading a
// logger through this producer to FLAG a non-conforming slug would require
// widening queryFunc's shared signature (every registered table query
// function, not just this one) for a monitoring-only concern -- out of
// scope for this slice. Instead the exclusion IS the signal: a
// non-ASCII-alphabet slug derives NO bare-name alias at all, so it can
// never enter an unproven equivalence class, and its absence from the
// graph's aliases property is directly observable (no alias means no
// identity-mechanism commit is possible for it) rather than silently
// trusted.
func repositoryBareNameAlias(slug string) string {
	idx := strings.LastIndex(slug, "/")
	if idx < 0 {
		return ""
	}
	bareName := slug[idx+1:]
	if bareName == "" || !graphrank.IsASCIIIdentityTerm(bareName) {
		return ""
	}
	return bareName
}

// repositoryProviderAlias derives a repository's provider-qualified alias
// (CHAOS-3884 Part A): "<provider>:<slug>", e.g.
// "github:full-chaos/dev-health-acr". This is the ONLY provider-variant
// data available from the `repos` source query today (id, repo, provider,
// last_synced, created_at, tags -- no separate full_name/html_url column);
// if a richer provider-native slug field is added to the source later,
// prefer it over this derived form. "" (no alias) when provider is unset --
// there is nothing to qualify the slug WITH -- or when the composed value
// fails the same ASCII-alphabet gate repositoryBareNameAlias uses.
func repositoryProviderAlias(provider, slug string) string {
	if provider == "" {
		return ""
	}
	value := provider + ":" + slug
	if !graphrank.IsASCIIIdentityTerm(value) {
		return ""
	}
	return value
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
