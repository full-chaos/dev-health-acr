package directread

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7159: handle mode takes one whole work-item key of ANY prefix; the
// org census (not a prefix list) decides existence. Free-text grammar is not
// widened.
func TestChaos7159_HandleModeAcceptsAnyPrefixKeyAndTheCensusDecidesExistence(t *testing.T) {
	wi := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item:acme:PAY-42")
	newGraph := func() *modesGraph {
		g := handleGraph()
		g.nodes[graphrank.SubjectKey(wi)] = repos("acme/a")
		return g
	}
	found := graphrank.CensusOutcome{Count: 1, SatisfierCanonicalID: wi.CanonicalID}
	for _, tc := range []struct {
		name, handle string
		outcome      graphrank.CensusOutcome
		wantStatus   FindStatus
		wantCensus   string // value the census was asked for; "" = no census call
		wantInvalid  bool
	}{
		{"unknown-prefix key that exists", "PAY-42", found, FindComplete, "PAY-42", false},
		{"unknown-prefix key that is missing", "PAY-9999", graphrank.CensusOutcome{}, FindEmpty, "PAY-9999", false},
		{"registered prefix still works", "CHAOS-4322", graphrank.CensusOutcome{}, FindEmpty, "CHAOS-4322", false},
		{"mixed-case prefix is asked as canonical upper case", "Pay2-7", graphrank.CensusOutcome{}, FindEmpty, "PAY2-7", false},
		{"key embedded in text is not an exact key", "please inspect PAY-42", found, "", "", true},
		{"prefix too long", "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFG-1", found, "", "", true},
		{"no digits", "PAY-abc", found, "", "", true},
		{"digit-first prefix", "9PAY-1", found, "", "", true},
		{"underscore prefix", "PAY_X-1", found, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []censusCall
			response, err := newModesLookup(newGraph(), fixedCensus(tc.outcome, nil, &calls)).Find(relCtx("7159-"+tc.name), unrestricted, FindRequest{Handle: tc.handle})
			if tc.wantInvalid {
				if err == nil || len(calls) != 0 {
					t.Fatalf("want a shape refusal with no census call, got err=%v calls=%+v", err, calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", response.Status, tc.wantStatus)
			}
			if len(calls) != 1 || calls[0].value != tc.wantCensus || calls[0].kind != contextfabric.SubjectWorkItem {
				t.Fatalf("census calls = %+v, want one work_item census for %q", calls, tc.wantCensus)
			}
			if tc.wantStatus == FindComplete && foundIDs(response) != wi.CanonicalID {
				t.Fatalf("found = %s", foundIDs(response))
			}
		})
	}
}

// codex r1 P2: the census compares the stored key exactly, and trackers treat
// keys case-insensitively, so a lower-case handle must reach the canonical
// upper-case stored key. The census here is exact-match on the stored key.
func TestChaos7159_LowerCaseHandleFindsTheCanonicalStoredKey(t *testing.T) {
	wi := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item:acme:PAY-42")
	g := handleGraph()
	g.nodes[graphrank.SubjectKey(wi)] = repos("acme/a")
	census := func(_ context.Context, _ string, _ graphrank.CensusKind, value string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (graphrank.CensusOutcome, error) {
		if value == "PAY-42" {
			return graphrank.CensusOutcome{Count: 1, SatisfierCanonicalID: wi.CanonicalID}, nil
		}
		return graphrank.CensusOutcome{}, nil
	}
	for _, handle := range []string{"PAY-42", "pay-42", "Pay-42"} {
		response, err := newModesLookup(g, census).Find(relCtx("case-"+handle), unrestricted, FindRequest{Handle: handle})
		if err != nil || response.Status != FindComplete || foundIDs(response) != wi.CanonicalID {
			t.Fatalf("%q = %v %+v", handle, err, response)
		}
	}
}
