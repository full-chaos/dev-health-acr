package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func readProjectDeployments(t *testing.T, expander *scriptedExpander, read func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error)) FactsResponse {
	t.Helper()
	provider := perSubjectProvider(contextfabric.FactDeployments, contractsv1.ContextFabricSubjectRepository)
	provider.read = read
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"deployments"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// An expansion that ran to completion and reached nothing is a measured
// absence of reach, not an unavailable read: no fact, and the read is
// complete.
func TestDirectReadCompletedEmptyExpansionIsNoFactNotUnavailable(t *testing.T) {
	expander := &scriptedExpander{expand: expandTo()}
	response := readProjectDeployments(t, expander, func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	})
	row := coverageOf(t, response, "deployments", projectQ)
	if row.Outcome != OutcomeReadNoFact || response.Status != StatusComplete {
		t.Fatalf("row %+v status %q, want read_no_fact and complete", row, response.Status)
	}
}

// A provider that cut its read and kept no fact for the reached subjects does
// not leave the root reading "no fact" in a complete response.
func TestDirectReadTruncatedProviderReadWithoutFactIsNotComplete(t *testing.T) {
	expander := &scriptedExpander{expand: expandTo(labelled(repoA, "acme/a"))}
	response := readProjectDeployments(t, expander, func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceTruncated}, nil
	})
	row := coverageOf(t, response, "deployments", projectQ)
	if row.Outcome == OutcomeReadNoFact || response.Status != StatusPartial {
		t.Fatalf("row %+v status %q, want a cut outcome and partial", row, response.Status)
	}
}

// Every reached subject evaluated with nothing found is a measured zero for
// the root; one never evaluated keeps it "no fact" (missing is not zero).
func TestDirectReadDerivedMeasuredZeroIsNotNoFact(t *testing.T) {
	a, b := labelled(repoA, "acme/a"), labelled(repoB, "acme/b")
	for _, tc := range []struct {
		name      string
		evaluated []contextfabric.SubjectRef
		want      string
	}{
		{"every reached subject evaluated", []contextfabric.SubjectRef{a, b}, OutcomeMeasuredZero},
		{"one reached subject never evaluated", []contextfabric.SubjectRef{a}, OutcomeReadNoFact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expander := &scriptedExpander{expand: expandTo(a, b)}
			response := readProjectDeployments(t, expander, func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
				return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, EvaluatedSubjects: tc.evaluated}, nil
			})
			if row := coverageOf(t, response, "deployments", projectQ); row.Outcome != tc.want {
				t.Fatalf("row %+v, want %s", row, tc.want)
			}
		})
	}
}
