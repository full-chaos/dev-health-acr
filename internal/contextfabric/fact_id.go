package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const factIDPrefix = "fact_"

// MintFactID returns the deterministic id of a canonical fact: a pure
// function of its kind, subject, timestamps and field values, so the same
// observation carries the same id on every read. Provenance (source, source
// version, evidence refs) is deliberately outside the digest: the id names
// the observation, not who reported it.
func MintFactID(fact CanonicalFact) string {
	payload := struct {
		Kind       FactKind             `json:"kind"`
		Subject    SubjectRef           `json:"subject"`
		ObservedAt *time.Time           `json:"observed_at,omitempty"`
		EventAt    *time.Time           `json:"event_at,omitempty"`
		Fields     map[string]FactValue `json:"fields"`
	}{fact.Kind, fact.Subject, fact.ObservedAt, fact.EventAt, fact.Fields}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return factIDPrefix + hex.EncodeToString(sum[:12])
}
