package contextfabric_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THREE AUTHORITIES STATE THE SAME BOUND, AND THEY MUST AGREE.
//
// A stored parent_result_id is bounded in three places, and each was written
// independently:
//
//	the wire contract   utf8.RuneCountInString   (validation_helpers.go)
//	the Go store rule   ValidateStoredParentResultID
//	PostgreSQL          char_length              (migration 0037)
//
// `char_length` counts CHARACTERS and `RuneCountInString` counts RUNES, which
// agree for every UTF-8 input. A Go check measuring BYTES agrees with neither
// the moment the id is not ASCII — and that is what it did: 256 `é` is 256
// characters and 512 bytes, so a request the wire contract accepted and
// Postgres would have stored was rejected at Save; 4 `é` is 4 characters and 8
// bytes, so the store accepted what Postgres rejects.
//
// WHY THE ASCII CASES COULD NOT SEE IT: for ASCII, bytes == runes ==
// characters, so every measurement agrees and a suite built from ASCII
// boundaries reads green while the rule is wrong for every non-ASCII id. This
// file pins the agreement at the boundary in the one encoding where the three
// definitions can diverge.
func TestParentIDBound_AllThreeAuthoritiesAgree(t *testing.T) {
	t.Parallel()

	const resultID = "result_bound_agreement_1"

	for _, tc := range []struct {
		name string
		id   string
		// wantAccepted is what ALL THREE must say. There is no case where a
		// disagreement is tolerable: the store rule exists precisely to state
		// the database's rule in Go.
		wantAccepted bool
	}{
		{name: "256 runes: at the upper bound", id: strings.Repeat("é", 256), wantAccepted: true},
		{name: "257 runes: past the upper bound", id: strings.Repeat("é", 257), wantAccepted: false},
		{name: "8 runes: at the lower bound", id: strings.Repeat("é", 8), wantAccepted: true},
		{name: "4 runes: below the lower bound", id: strings.Repeat("é", 4), wantAccepted: false},
		{name: "256 ASCII: the case that already agreed", id: strings.Repeat("y", 256), wantAccepted: true},
		{name: "4 ASCII: the case that already agreed", id: strings.Repeat("y", 4), wantAccepted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runes := utf8.RuneCountInString(tc.id)
			bytes := len(tc.id)

			// AUTHORITY 1: the Go store rule, which is what both adapters call
			// and what mirrors the database.
			storeErr := contextfabric.ValidateStoredParentResultID(resultID, tc.id)
			if accepted := storeErr == nil; accepted != tc.wantAccepted {
				t.Errorf("the Go store rule accepted=%v, want %v for %d runes / %d bytes (err: %v).\n"+
					"PostgreSQL measures char_length, i.e. %d here; a rule measuring bytes disagrees with the database for every non-ASCII id.",
					accepted, tc.wantAccepted, runes, bytes, storeErr, runes)
			}

			// AUTHORITY 2: the wire contract, driven through the REAL request
			// validator rather than a re-implementation of its bound.
			//
			// The base request must be otherwise VALID or every case is
			// rejected for an unrelated reason and the ones expecting a
			// rejection pass vacuously. The first version of this test did
			// exactly that (a missing time_context rejected all six inputs),
			// which is the "an oracle that accepts any error proves nothing
			// about the reason" trap. Two guards now: a control asserting the
			// base validates, and an attribution check requiring the rejection
			// to NAME parent_result_id.
			request := validBoundAgreementRequest()
			request.ParentResultID = tc.id
			err := request.Validate()
			if accepted := err == nil; accepted != tc.wantAccepted {
				t.Errorf("the wire contract accepted=%v, want %v for %d runes / %d bytes (err: %v) -- the contract and the store rule must not disagree, or a request that validates cannot be stored",
					accepted, tc.wantAccepted, runes, bytes, err)
			}
			if err != nil && !strings.Contains(err.Error(), "parent_result_id") {
				t.Errorf("the wire contract rejected this request with %q, which does not name parent_result_id: the rejection is not attributable to the bound under test", err)
			}

			// AUTHORITY 3 is PostgreSQL's CHECK, which cannot run here. What
			// CAN be asserted is the quantity it measures: char_length equals
			// the rune count for UTF-8. Asserting that the Go rule agrees with
			// the RUNE count is therefore asserting it agrees with Postgres,
			// and it is stated rather than left implicit.
			if tc.wantAccepted != (runes >= 8 && runes <= 256) {
				t.Fatalf("this case's expectation (%v) does not match the rune count %d against the documented 8..256 bound -- the fixture, not the code, is wrong", tc.wantAccepted, runes)
			}
		})
	}
}

// validBoundAgreementRequest is a request that validates on every field EXCEPT
// the one under test, so a rejection is attributable to parent_result_id. The
// control below proves it validates with no parent at all.
func validBoundAgreementRequest() contractsv1.ContextFabricInvestigationRequest {
	return contractsv1.ContextFabricInvestigationRequest{
		SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:     "request_bound_agreement01",
		Question:      "do the three authorities agree on this bound?",
		TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

// TestParentIDBound_BaseRequestIsOtherwiseValid is the CONTROL that makes every
// rejection above attributable. Without it, a fixture invalid for an unrelated
// reason turns each "must be refused" case into a pass that proves nothing --
// which is precisely how the first draft of this file behaved.
func TestParentIDBound_BaseRequestIsOtherwiseValid(t *testing.T) {
	t.Parallel()
	if err := validBoundAgreementRequest().Validate(); err != nil {
		t.Fatalf("the base request does not validate (%v), so every rejection in this file would be unattributable", err)
	}
}

// TestParentIDBound_MeasuresRunesNotBytes states the failure directly, so a
// future edit back to len() fails with a message naming the reason rather than
// only a boundary number.
func TestParentIDBound_MeasuresRunesNotBytes(t *testing.T) {
	t.Parallel()

	// 256 runes, 512 bytes: the two measurements maximally disagree here.
	id := strings.Repeat("é", 256)
	if utf8.RuneCountInString(id) == len(id) {
		t.Fatal("fixture is ASCII, so it cannot distinguish a rune count from a byte count")
	}
	if err := contextfabric.ValidateStoredParentResultID("result_bound_agreement_1", id); err != nil {
		t.Errorf("a 256-rune parent was rejected (%v): the bound is stated in characters by the migration and in runes by the wire contract, so measuring bytes rejects ids both authorities accept", err)
	}
}
