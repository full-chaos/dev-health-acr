package contextpacket_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// lowConfidenceNewestClient models prod deployments: the confidence-filtered
// evidence read only sees an old high-confidence row, while the source itself
// holds newer low-confidence rows (CHAOS-6565).
type lowConfidenceNewestClient struct {
	old, newest time.Time
}

func (c lowConfidenceNewestClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.HasPrefix(statement, "SELECT max(observed_at)") {
		return &rowScanner{rows: [][]any{{c.newest, uint64(48)}}}, nil
	}
	return &rowScanner{rows: [][]any{{"acr:v1:deployment:1", "dev_health", "deployment", "1", "deployment", "", "native", 0.9, "citation", c.old}}}, nil
}

func TestWatermarkUsesSourceLatestNotConfidenceFilteredEvidence(t *testing.T) {
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("freshness", "main", "commit-1"))
	if err != nil {
		t.Fatal(err)
	}
	plan.RepoID = "00000000-0000-0000-0000-000000000001"
	now := time.Now().UTC()
	client := lowConfidenceNewestClient{old: now.Add(-200 * 24 * time.Hour), newest: now.Add(-time.Hour)}
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range result.Watermarks {
		if w.Source != "deployments.v1" {
			continue
		}
		if w.Status != "fresh" || w.LastIngestedAt == nil || !w.LastIngestedAt.Equal(client.newest) {
			t.Fatalf("deployments watermark = %+v, want fresh at the source's newest row %v", w, client.newest)
		}
		return
	}
	t.Fatal("no deployments.v1 watermark")
}
