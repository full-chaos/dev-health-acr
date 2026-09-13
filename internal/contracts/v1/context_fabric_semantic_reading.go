package v1

import "fmt"

// CHAOS-5672 (chris, D49): the disclosure a stored result carries on a read
// when the accepted reading it was produced under cannot be loaded and the
// read needed it.
//
// WHY IT IS ON THE WIRE. A stored clarification is judged answerable or not
// against the reading persisted beside it. A row with no loadable reading --
// saved before readings were persisted, or saved without one -- is served as
// stored, because a read by id must never run a new investigation. Without
// this field a consumer cannot tell that row from one whose clarification the
// server checked and found answerable; with it, the consumer is told the check
// could not be made and why.
//
// ADDITIVE AND OPTIONAL. Absent means the read did not need the reading or had
// it; present means the read needed it and could not have it. Fresh composition
// never sets it -- a composing turn always has its own reading.

// ContextFabricSemanticReadingStatus is the closed status of the reading on a
// read. One member: a present field only ever says the reading was unavailable.
type ContextFabricSemanticReadingStatus string

const (
	// ContextFabricSemanticReadingUnavailable: the read needed the stored
	// reading and could not load it.
	ContextFabricSemanticReadingUnavailable ContextFabricSemanticReadingStatus = "unavailable"
)

// ContextFabricSemanticReadingStatuses returns the closed status vocabulary.
func ContextFabricSemanticReadingStatuses() []ContextFabricSemanticReadingStatus {
	return []ContextFabricSemanticReadingStatus{ContextFabricSemanticReadingUnavailable}
}

// ContextFabricSemanticReadingReason is the closed reason the reading was
// unavailable.
type ContextFabricSemanticReadingReason string

const (
	// ContextFabricSemanticReadingStateAbsent: the row carries no reading.
	// That is every row saved before readings were persisted, and every row
	// whose turn captured none; the store keeps no record of which, so the
	// reason says only what is true of both.
	ContextFabricSemanticReadingStateAbsent ContextFabricSemanticReadingReason = "semantic_state_absent"
	// ContextFabricSemanticReadingStateUnreadable: the row carries a reading
	// this build cannot read -- it does not decode, exceeds a bound, or names
	// a format this build does not read.
	ContextFabricSemanticReadingStateUnreadable ContextFabricSemanticReadingReason = "semantic_state_unreadable"
)

// ContextFabricSemanticReadingReasons returns the closed reason vocabulary.
func ContextFabricSemanticReadingReasons() []ContextFabricSemanticReadingReason {
	return []ContextFabricSemanticReadingReason{ContextFabricSemanticReadingStateAbsent, ContextFabricSemanticReadingStateUnreadable}
}

// ContextFabricSemanticReading is the disclosure itself.
type ContextFabricSemanticReading struct {
	Status ContextFabricSemanticReadingStatus `json:"status"`
	Reason ContextFabricSemanticReadingReason `json:"reason"`
}

// Validate admits exactly the closed members, both required.
func (s ContextFabricSemanticReading) Validate() error {
	validStatus := false
	for _, member := range ContextFabricSemanticReadingStatuses() {
		if s.Status == member {
			validStatus = true
		}
	}
	if !validStatus {
		return fmt.Errorf("semantic_reading status %q is not a vocabulary member", s.Status)
	}
	for _, member := range ContextFabricSemanticReadingReasons() {
		if s.Reason == member {
			return nil
		}
	}
	return fmt.Errorf("semantic_reading reason %q is not a vocabulary member", s.Reason)
}

// validateSemanticReadingOnResult holds the disclosure to the one document it
// describes: a clarification, whose answerability needed the reading.
func validateSemanticReadingOnResult(reading *ContextFabricSemanticReading, status ContextFabricInvestigationStatus) error {
	if reading == nil {
		return nil
	}
	if err := reading.Validate(); err != nil {
		return err
	}
	if status != ContextFabricInvestigationClarificationRequired {
		return fmt.Errorf("semantic_reading cannot accompany status %q: only a clarification's answerability needs the stored reading", status)
	}
	return nil
}
