package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// One approver's review id on two pull requests of one repository: each
// review.v2 ref expands, through the hosted route, to exactly its own row.
func TestEvidenceRouteServesEachReviewOfARepeatedReviewID(t *testing.T) {
	tables := newSourceRowTables()
	tables.repos[sourceRowGrantedRepoID] = hostedTestRepository
	want := map[string]string{}
	for _, review := range []struct{ number, state string }{{"1042", "APPROVED"}, {"1043", "CHANGES_REQUESTED"}} {
		ref, fits := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityReviewV2, sourceRowGrantedRepoID, review.number, "approval-42")
		if !fits {
			t.Fatalf("ref %q does not fit", ref)
		}
		id := strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(contractsv1.ContextFabricEvidenceEntityReviewV2)+":")
		tables.rows[sourceRowGrantedRepoID+"|pull_request_reviews.v2|"+ref] = []contextpacket.EvidenceReference{{RepoSlug: hostedTestRepository, Excerpt: review.state, Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: ref, SourceVersion: "pull_request_reviews.v2",
			Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: "pull_request_review", EntityID: id, DisplayLabel: "PR #" + review.number + " review"},
			Provenance: "native", Confidence: 1, Citation: review.state, ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Availability: contractsv1.EvidenceAvailable,
		}}}
		want[ref] = review.state
	}
	app, token := sourceRowApp(t, tables, nil, &bytes.Buffer{})
	for ref, state := range want {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", ref, rec.Code, rec.Body.String())
		}
		var expanded contractsv1.ExpandedEvidence
		if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
			t.Fatal(err)
		}
		if expanded.Evidence.EvidenceRefID != ref || expanded.Evidence.Citation != state || expanded.Evidence.Metadata["record"] != "source_row" {
			t.Fatalf("%s served %+v, want its own row (%s)", ref, expanded.Evidence, state)
		}
	}
}
