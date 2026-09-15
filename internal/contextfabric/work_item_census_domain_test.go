package contextfabric

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWorkItemTupleCensusFiniteDomain(t *testing.T) {
	// Independent boundary table from the persisted census contract. Exact
	// populations span 0..C; floor names C; unmeasured names no population.
	states := []struct {
		state          WorkItemMembershipCensusState
		low, high, cap int
	}{
		{WorkItemMembershipCensusExact, 0, 2000, 200},
		{WorkItemMembershipCensusFloor, 2000, 2000, 200},
		{WorkItemMembershipCensusUnmeasured, 0, 0, 0},
		{"unrecognized", 1, 0, 0},
	}
	for _, spec := range states {
		for _, value := range []int{-1, 0, 1, 199, 200, 201, 1999, 2000, 2001} {
			for _, retained := range []int{-1, 0, 1, 199, 200, 201} {
				name := fmt.Sprintf("%s/value%d/retained%d", spec.state, value, retained)
				t.Run(name, func(t *testing.T) {
					census := testWorkItemTupleCensus(spec.state, value, retained, strings.Repeat("a", 64))
					limit := spec.cap
					if spec.state == WorkItemMembershipCensusExact && value < limit {
						limit = value
					}
					want := value >= spec.low && value <= spec.high && retained >= 0 && retained <= limit
					if got := validWorkItemTupleCensus(*census); got != want {
						t.Fatalf("census valid=%t want=%t", got, want)
					}
				})
			}
		}
	}
	for _, entry := range []struct {
		name, version, digest string
		want                  bool
	}{
		{"valid", WorkItemTupleCensusVersion, strings.Repeat("a", 64), true},
		{"future_version", "future", strings.Repeat("a", 64), false},
		{"short_digest", WorkItemTupleCensusVersion, strings.Repeat("a", 62), false},
		{"odd_digest", WorkItemTupleCensusVersion, strings.Repeat("a", 63), false},
		{"uppercase_digest", WorkItemTupleCensusVersion, strings.Repeat("A", 64), false},
		{"nonhex_digest", WorkItemTupleCensusVersion, strings.Repeat("g", 64), false},
	} {
		t.Run(entry.name, func(t *testing.T) {
			c := testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, entry.digest)
			c.Version = entry.version
			if got := validWorkItemTupleCensus(*c); got != entry.want {
				t.Fatalf("valid=%t want=%t", got, entry.want)
			}
		})
	}
}

func TestWorkItemTupleCensusDecodedFieldsRemainBoundToRaw(t *testing.T) {
	for _, name := range []string{"version", "state", "value", "retained", "scope", "digest"} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(testWorkItemTupleCensus(WorkItemMembershipCensusExact, 7, 1, strings.Repeat("a", 64)))
			if err != nil {
				t.Fatal(err)
			}
			var census WorkItemTupleCensus
			if err := json.Unmarshal(encoded, &census); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "version":
				census.Version = "future"
			case "state":
				census.State = WorkItemMembershipCensusFloor
			case "value":
				census.Value = 8
			case "retained":
				census.Retained = 2
			case "scope":
				census.RequestedRepositoryScope = []string{"other"}
			case "digest":
				census.AuthorizationDigest = strings.Repeat("b", 64)
			}
			if got := ValidateWorkItemTupleCensus(&census); got != WorkItemTupleCensusReadMalformed {
				t.Fatalf("altered %s census read=%s", name, got)
			}
			marshaled, err := census.MarshalJSON()
			if err != nil || !bytes.Equal(marshaled, encoded) {
				t.Fatalf("altered %s census rewrote captured unavailable value: error=%v", name, err)
			}
		})
	}
}

func TestWorkItemTupleCensusRequiresEachWireField(t *testing.T) {
	encoded, err := json.Marshal(testWorkItemTupleCensus(WorkItemMembershipCensusExact, 1, 1, strings.Repeat("a", 64)))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "state", "value", "retained", "requested_repository_scope", "authorization_digest"} {
		t.Run(key, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, key)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var census WorkItemTupleCensus
			if err := json.Unmarshal(raw, &census); err != nil {
				t.Fatal(err)
			}
			if got := ValidateWorkItemTupleCensus(&census); got != WorkItemTupleCensusReadMalformed {
				t.Fatalf("missing %s read=%s", key, got)
			}
		})
	}
}

func TestWorkItemTupleCensusEncodeRejectsUnavailable(t *testing.T) {
	for _, name := range []string{"absent", "valid", "malformed", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			state := validWorkItemTupleSemanticState(t)
			state.WorkItemCensus = testWorkItemTupleCensus(WorkItemMembershipCensusExact, 7, 1, strings.Repeat("a", 64))
			switch name {
			case "absent":
				state.WorkItemCensus = nil
			case "malformed":
				state.WorkItemCensus.Value = -1
			case "unsupported":
				state.WorkItemCensus.Version = "future"
			}
			_, err := EncodeSemanticState(state)
			wantError := name == "malformed" || name == "unsupported"
			if (err != nil) != wantError {
				t.Fatalf("encode unavailable census error=%v wantError=%t", err, wantError)
			}
		})
	}
}
