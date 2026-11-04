package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// withServedDetailTableLimitation replaces a model limitation that cites a
// detail table the answer SERVES with the service's own sentence (CHAOS-12887).
// The model is given a table's row count and never its rows, so it can write
// that the rows were "not supplied" beside a table the page draws. Only a
// limitation that names a served table, by its field name, its display noun or
// its row count, is replaced. A table the size cut withheld has no rows left,
// so it is not served: the model's sentence and the service's withheld sentence
// stand as they are, and no "shown beside" sentence is added. The sentence is
// idempotent: finalizeResult runs again on a cut document, and it is removed
// again when no served table remains; the model sentence it replaced is not
// restored then, because the withheld sentence already says the table is out.
// The status is never changed here.
func withServedDetailTableLimitation(result InvestigationResult, facts []CanonicalFact) InvestigationResult {
	served := servedDetailTableClaims(result.ClaimedFacts, facts)
	hadSentence := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricDetailTableServedLimitation {
			hadSentence = true
		}
	}
	if len(served) == 0 {
		if hadSentence {
			result.Limitations = withoutLimitation(result.Limitations, contractsv1.ContextFabricDetailTableServedLimitation)
		}
		return result
	}
	kept := make([]string, 0, len(result.Limitations))
	replaced := 0
	for _, limitation := range result.Limitations {
		if limitation != contractsv1.ContextFabricDetailTableServedLimitation && limitationCitesServedTable(limitation, served) {
			replaced++
			continue
		}
		kept = append(kept, limitation)
	}
	if replaced == 0 {
		return result
	}
	composed, displaced := appendBoundedLimitations(kept, []string{contractsv1.ContextFabricDetailTableServedLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}

// servedDetailTableClaims are the claims that carry a declared detail table
// with rows left on them.
func servedDetailTableClaims(claims []ClaimedFact, facts []CanonicalFact) []ClaimedFact {
	var served []ClaimedFact
	for _, claim := range claims {
		if claim.Table == nil || len(claim.Rows) == 0 {
			continue
		}
		fact, ok := claimSourceFact(facts, claim)
		if !ok || !detailTableField(fact, claim.Table.Field) {
			continue
		}
		served = append(served, claim)
	}
	return served
}

func limitationCitesServedTable(limitation string, served []ClaimedFact) bool {
	text := strings.ToLower(limitation)
	for _, claim := range served {
		field := claim.Table.Field
		if strings.Contains(text, field) || strings.Contains(text, strings.ToLower(contractsv1.ContextFabricFactFieldNoun(field))) {
			return true
		}
	}
	return false
}
