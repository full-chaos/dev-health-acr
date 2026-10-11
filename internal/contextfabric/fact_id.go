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
// (kind, subject) group, in slice order. It overwrites any id already set,
// because an id minted for a different slice would collide with this one's.
// Run after the slice reaches its final composition and order: the registry
// at the end of a read, and again after a group read is merged in.
func AssignFactIDs(facts []CanonicalFact) {
	ordinals := make(map[string]int, len(facts))
	for i := range facts {
		key := string(facts[i].Kind) + "\x00" + subjectKeyForModel(facts[i].Subject)
		facts[i].ID = MintFactID(facts[i].Kind, facts[i].Subject, ordinals[key])
		ordinals[key]++
	}
}
