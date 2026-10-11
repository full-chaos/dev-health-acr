package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const factIDPrefix = "fact_"

// MintFactID is the id of the ordinal-th fact of one (kind, subject) group.
//
// The id deliberately does NOT hash the fact's values or timestamps: those
// move with the clock-resolved window and with aggregation noise, and the
// client input a caller pins by digest must not. A fact's identity inside an
// answer is its place in the registry's deterministic order (see
// sortCanonicalFacts), so the id is a pure function of that place.
func MintFactID(kind FactKind, subject SubjectRef, ordinal int) string {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + string(subject.Kind) + "\x00" + subject.CanonicalID + "\x00" + strconv.Itoa(ordinal)))
	return factIDPrefix + hex.EncodeToString(sum[:12])
}

// AssignFactIDs stamps every fact with the id of its place in its
// (kind, subject) group, in slice order. Run once, on the registry's sorted
// bundle.
func AssignFactIDs(facts []CanonicalFact) {
	AppendFactsWithIDs(nil, facts)
}

// AppendFactsWithIDs returns into followed by a copy of add, where each added
// fact is stamped with the next free ordinal of its (kind, subject) group.
// Facts already in into keep their ids: a bundle another reader already holds
// must not change under it. When into is nil the facts of add are stamped in
// place and returned as-is.
func AppendFactsWithIDs(into, add []CanonicalFact) []CanonicalFact {
	ordinals := make(map[string]int, len(into)+len(add))
	groupKey := func(fact CanonicalFact) string {
		return string(fact.Kind) + "\x00" + subjectKeyForModel(fact.Subject)
	}
	for _, fact := range into {
		ordinals[groupKey(fact)]++
	}
	stamped := add
	if into != nil {
		stamped = append([]CanonicalFact(nil), add...)
	}
	for i := range stamped {
		key := groupKey(stamped[i])
		stamped[i].ID = MintFactID(stamped[i].Kind, stamped[i].Subject, ordinals[key])
		ordinals[key]++
	}
	if into == nil {
		return stamped
	}
	return append(into, stamped...)
}
