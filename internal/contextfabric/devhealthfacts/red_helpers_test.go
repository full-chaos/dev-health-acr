package devhealthfacts_test

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

func unitFactsOf(result contextfabric.FactProviderResult, kind string) []contextfabric.CanonicalFact {
	var out []contextfabric.CanonicalFact
	for _, fact := range result.Facts {
		if value, ok := fact.Fields["unit_kind"]; ok && value.String != nil && *value.String == kind {
			out = append(out, fact)
		}
	}
	return out
}
