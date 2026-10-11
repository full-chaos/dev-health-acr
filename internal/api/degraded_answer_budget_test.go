package api

import (
	"bytes"
	"fmt"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A degraded answer for a failed model call holds to the same item ceiling
// every answer holds to: with more facts read than the ceiling admits as
// claims, it serves as many as fit and says so, never a refusal.
func TestADegradedAnswerHoldsToTheItemCeilingWithManyFactsRead(t *testing.T) {
	logs := &bytes.Buffer{}
	model := scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelOutput), receipt: true}
	app, token := teamSynthesisApp(t, model, nil, teamExpander{targets: 10, complete: true, workItemsOnly: true}, logs)

	response := postTeamQuestion(t, app, token)

	result := decodeTeamAnswer(t, response, logs)
	items := contractsv1.CountContextFabricResultItems(result).Budgeted()
	if result.Status != cf.InvestigationDegraded || len(result.ClaimedFacts) == 0 || items > 50 {
		t.Fatalf("status = %q claims = %d budgeted items = %d, want a degraded answer with its facts inside the 50-item ceiling", result.Status, len(result.ClaimedFacts), items)
	}
	if !hasLimitation(result, contractsv1.ContextFabricSynthesisFailedFactsTruncatedLimitation) {
		t.Fatalf("limitations = %q, want the truncated disclosure", result.Limitations)
	}
}
