package v1

import "encoding/json"

// The census row ceiling: how many fact-scope decisions a request's byte
// budget can carry beside the smallest valid answer.
//
// WHY A CEILING EXISTS. The census has one row per requirement/origin cell,
// so its worst-case size is a function of the eligibility table, not of
// anything a caller controls, and it is not charged to the item budget (see
// ContextFabricResultItemCounts). Measured against the SMALLEST budget a
// caller may request (ContextFabricSerializedBytesMin), a table of 25 cells
// no longer fits beside the smallest valid answer. Refusing such an answer
// because of its own diagnostics, or silently cutting the census, are both
// wrong; so the census is the one collection that sheds, and says how many.
//
// SHEDDING HAPPENS WHEN THE CENSUS IS COMPOSED, never by trimming a composed
// answer (see the note at the top of context_fabric_response_budget.go). The
// ceiling is a pure function of the requested byte budget, so the stored and
// the served documents are the same document.

// contextFabricFactScopeCensusRecordWorstBytes is the widest serialized
// census record: every string at the longest member of its own closed
// vocabulary, every number at its maximum, the population present. DERIVED
// from maximalFactScopeCensus, the same fixture the answer bounds measure, so
// a longer token moves it rather than a sentence nobody re-measures.
func contextFabricFactScopeCensusRecordWorstBytes() int {
	worst := 0
	for _, record := range maximalFactScopeCensus() {
		encoded, err := json.Marshal(record)
		if err != nil {
			// A record of strings and ints always marshals; failing closed
			// to a pessimistic width keeps the ceiling safe, never larger.
			return ContextFabricSerializedBytesMin
		}
		if len(encoded) > worst {
			worst = len(encoded)
		}
	}
	return worst
}

// contextFabricFactScopeCensusEnvelopeBytes is what the census costs beyond
// its rows: the key, the array brackets, and the omitted count's key and its
// widest value.
func contextFabricFactScopeCensusEnvelopeBytes() int {
	encoded, err := json.Marshal(map[string]any{
		"fact_scope_census":         []int{},
		"fact_scope_census_omitted": ContextFabricFactScopeCensusMaxCount,
	})
	if err != nil {
		return ContextFabricSerializedBytesMin
	}
	// Plus the separating comma the enclosing object pays for each key.
	return len(encoded) + 2
}

// ContextFabricFactScopeCensusRowCeiling is the number of census rows a
// request whose serialized budget is maxSerializedBytes can carry beside the
// smallest valid answer. A budget that is not positive (no byte budget was
// declared) carries every row.
//
// The result is clamped to [0, ContextFabricFactScopeCensusMaxCount]. At the
// smallest budget a caller may request it is below the table size; at every
// larger budget it is the whole table or close to it, which is why a default
// request never sheds.
func ContextFabricFactScopeCensusRowCeiling(maxSerializedBytes int64) int {
	if maxSerializedBytes <= 0 {
		return ContextFabricFactScopeCensusMaxCount
	}
	headroom := maxSerializedBytes - int64(ContextFabricMinimumAnswerBytes) - int64(contextFabricFactScopeCensusEnvelopeBytes())
	if headroom <= 0 {
		return 0
	}
	// Each row also pays one separating comma.
	rows := headroom / int64(contextFabricFactScopeCensusRecordWorstBytes()+1)
	if rows > int64(ContextFabricFactScopeCensusMaxCount) {
		return ContextFabricFactScopeCensusMaxCount
	}
	return int(rows)
}
