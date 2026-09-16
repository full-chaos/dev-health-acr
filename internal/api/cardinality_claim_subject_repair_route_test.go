package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestByIDRouteRepairsLegacyCardinalityClaimSubject pins the invariant: a
// by-id read reads storage directly and never reaches the pass that mints a
// cardinality claim, so a claim minted under an earlier authority (subject =
// organization for an anchor-bound count) must be repaired on this route's
// own re-read, not served unrepaired. storedWorkItemTupleRouteFixture
// already carries exactly that shape -- an anchor-bound (project) work-item
// tuple whose stored cardinality claim names the organization -- so it is
// reused here rather than built twice.
func TestByIDRouteRepairsLegacyCardinalityClaimSubject(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	// The shared fixture's candidate carries no MatchedTerms -- fine for its
	// own original purpose (work-item census serving, which never reads
	// anchor-bound scope), but a real resolved-and-persisted candidate
	// records which anchor term it matched (count_population_scope.go's
	// anchorBound), which is exactly what this repair's own re-derivation
	// reads to confirm the anchor is bound. Set it to what a live row
	// actually carries rather than weaken the repair to trust an
	// unconfirmed anchor.
	stored.Result.SubjectResolution.Candidates[0].MatchedTerms = []string{"project"}
	legacyClaim := stored.Result.ClaimedFacts[2]
	if legacyClaim.Kind != contractsv1.ContextFabricFactCardinality || legacyClaim.Subject.Kind != contractsv1.ContextFabricSubjectOrganization {
		t.Fatalf("fixture control: ClaimedFacts[2] = %+v, want the legacy organization-subject cardinality claim this test targets", legacyClaim)
	}
	anchor := stored.Result.SubjectResolution.Committed[0]
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var servedClaim *contractsv1.ContextFabricClaimedFact
	count := 0
	for i := range got.ClaimedFacts {
		if got.ClaimedFacts[i].Kind == contractsv1.ContextFabricFactCardinality {
			count++
			servedClaim = &got.ClaimedFacts[i]
		}
	}
	if count != 1 {
		t.Fatalf("served cardinality claims = %d, want exactly 1 (a repair replaces, never duplicates): %#v", count, got.ClaimedFacts)
	}
	if servedClaim.Subject.Kind != anchor.Kind || servedClaim.Subject.CanonicalID != anchor.CanonicalID {
		t.Errorf("served claim subject = %s/%s, want the resolved anchor %s/%s -- the legacy claim was not repaired", servedClaim.Subject.Kind, servedClaim.Subject.CanonicalID, anchor.Kind, anchor.CanonicalID)
	}
	if servedClaim.ClaimID != legacyClaim.ClaimID {
		t.Errorf("served claim id = %q, want the stored claim's own id %q -- a repair must not mint a new id", servedClaim.ClaimID, legacyClaim.ClaimID)
	}
	if !strings.Contains(logs.String(), "context fabric legacy cardinality claim subject repaired") {
		t.Errorf("the repair line was not emitted: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"stored_subject_kind":"organization"`) || !strings.Contains(logs.String(), `"served_subject_kind":"`+string(anchor.Kind)+`"`) {
		t.Errorf("the repair line does not name the before/after subject kinds: %s", logs.String())
	}
}

// TestByIDRouteLeavesAnAlreadyCorrectCardinalityClaimAlone is the
// discriminating half: a row whose claim already agrees with what a fresh
// re-derivation would produce must not be reported as repaired, and must
// not gain a second claim.
func TestByIDRouteLeavesAnAlreadyCorrectCardinalityClaimAlone(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	stored := storedWorkItemTupleRouteFixture(t, principal)
	// A resolved-and-persisted candidate records which anchor term it
	// matched -- see the comment on the sibling test above. Set here too so
	// this test genuinely reaches the repair's own no-op comparison instead
	// of stopping earlier at the "reading cannot re-derive the scope" gate.
	stored.Result.SubjectResolution.Candidates[0].MatchedTerms = []string{"project"}
	anchor := stored.Result.SubjectResolution.Committed[0]
	// A freshly minted claim's Label is the anchor's canonical id, not the
	// anchor's own display label (cardinalityClaimSubject has no display
	// name to draw from) -- match that shape exactly, or this fixture is
	// not actually the "already correct" claim a real mint would produce.
	stored.Result.ClaimedFacts[2].Subject = contractsv1.ContextFabricSubjectRef{Kind: anchor.Kind, CanonicalID: anchor.CanonicalID, Label: anchor.CanonicalID}
	store := &storedWorkItemTupleRouteStore{stored: stored}
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(logs.String(), "context fabric legacy cardinality claim subject repaired") {
		t.Errorf("an already-correct claim was reported as repaired: %s", logs.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	count := 0
	for _, claim := range got.ClaimedFacts {
		if claim.Kind == contractsv1.ContextFabricFactCardinality {
			count++
			if claim.Subject.Kind != anchor.Kind || claim.Subject.CanonicalID != anchor.CanonicalID {
				t.Errorf("served claim subject = %s/%s, want the unchanged anchor %s/%s", claim.Subject.Kind, claim.Subject.CanonicalID, anchor.Kind, anchor.CanonicalID)
			}
		}
	}
	if count != 1 {
		t.Errorf("served cardinality claims = %d, want exactly 1", count)
	}
}
