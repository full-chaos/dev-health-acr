package contextpacket_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestDeploymentsSourceQuery_usesTruthfulCitationWhenReleaseReferenceIsEmpty(t *testing.T) {
	// Given
	var deploymentQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "deployments.v1" {
			deploymentQuery = query
			break
		}
	}

	// When
	const expectedCitation = "if(d.release_ref != '', concat('release_ref=', d.release_ref), concat('deployment_id=', d.deployment_id)) citation"
	citationIsTruthy := strings.Contains(deploymentQuery.Statement, expectedCitation)
	citationUsesNullableStatus := strings.Contains(deploymentQuery.Statement, "ifNull(d.status, '') citation")

	// Then
	if !citationIsTruthy || citationUsesNullableStatus {
		t.Fatal("deployments.v1 must cite its release reference or deployment identity, never nullable status")
	}
}
