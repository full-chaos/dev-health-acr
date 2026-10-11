package contextfabric

import (
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const degradedFactClaimIDPrefix = "server:fact:"

// mintDegradedFactClaims builds the claims a degraded answer carries when the
// model call failed: one per scalar canonical field that was read, in the
// order the facts were read, with the evidence those facts cite. Only the
// model's judgment is missing from such an answer, never the values.
//
// Each claim carries the value of the field it was minted from, so it is
// grounded in those facts by construction; Validate refuses what a claim cannot
// carry (a padded field name, a value that is not finite). The claim count stops at the claimed-fact bound
// and the evidence count at the evidence bound; truncated reports that facts
// were left out.
func mintDegradedFactClaims(facts []CanonicalFact) (claims []ClaimedFact, evidence []string, truncated bool) {
	claims = []ClaimedFact{}
	evidence = []string{}
	seenEvidence := map[string]struct{}{}
	for _, fact := range facts {
		fields := make([]string, 0, len(fact.Fields))
		for field := range fact.Fields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			value, ok := scalarFromFactValue(fact.Fields[field])
			if !ok {
				continue
			}
			claim := ClaimedFact{
				ClaimID: fmt.Sprintf("%s%d", degradedFactClaimIDPrefix, len(claims)),
				Kind:    fact.Kind,
				Subject: fact.Subject,
				Field:   field,
				Value:   value,
			}
			if claim.Validate() != nil {
				continue
			}
			added := make([]string, 0, len(fact.EvidenceRefIDs))
			for _, ref := range fact.EvidenceRefIDs {
				if _, seen := seenEvidence[ref]; !seen {
					added = append(added, ref)
				}
			}
			if len(claims) >= contractsv1.ContextFabricClaimedFactsMaxCount ||
				len(evidence)+len(added) > contractsv1.ContextFabricEvidenceRefIDsMaxCount {
				return claims, evidence, true
			}
			for _, ref := range added {
				seenEvidence[ref] = struct{}{}
				evidence = append(evidence, ref)
			}
			claims = append(claims, claim)
		}
	}
	return claims, evidence, false
}

func scalarFromFactValue(v FactValue) (ScalarValue, bool) {
	switch {
	case v.String != nil:
		return ScalarValue{String: v.String}, true
	case v.Integer != nil:
		return ScalarValue{Integer: v.Integer}, true
	case v.Number != nil:
		return ScalarValue{Number: v.Number}, true
	case v.Boolean != nil:
		return ScalarValue{Boolean: v.Boolean}, true
	case v.Null:
		return ScalarValue{Null: true}, true
	}
	return ScalarValue{}, false
}
