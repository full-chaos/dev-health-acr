package contextpacket_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestEvaluationStore_ResolveEvidence_returns_generic_not_found_when_reference_is_not_authorized(t *testing.T) {
	// Given
	store := evidenceFixtureStore(t)
	foreignRepository := storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"other-org/other-repo"}}
	foreignOrganization := storage.Principal{OrgID: "org-foreign", RepositoryScopes: []string{"example-org/widget-service"}}
	authorized := storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"example-org/widget-service"}}
	tests := []struct {
		name      string
		principal storage.Principal
		id        string
	}{
		{name: "foreign repository", principal: foreignRepository, id: "ev-pr-auth-002"},
		{name: "foreign organization", principal: foreignOrganization, id: "ev-pr-auth-002"},
		{name: "unknown reference", principal: authorized, id: "ev-unknown-reference"},
		{name: "malformed reference", principal: authorized, id: "\x00bad"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			_, err := store.ResolveEvidence(context.Background(), test.principal, test.id)

			// Then
			if !errors.Is(err, storage.ErrNotFound) || err.Error() != storage.ErrNotFound.Error() {
				t.Fatalf("error = %v, want generic not found", err)
			}
		})
	}
}

func TestEvaluationStore_ResolveEvidence_expands_authorized_reference_repeatedly(t *testing.T) {
	// Given
	store := evidenceFixtureStore(t)
	principal := storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"example-org/widget-service"}}

	// When
	first, firstErr := store.ResolveEvidence(context.Background(), principal, "ev-pr-auth-002")
	second, secondErr := store.ResolveEvidence(context.Background(), principal, "ev-pr-auth-002")

	// Then
	if firstErr != nil || secondErr != nil {
		t.Fatalf("repeated resolution errors = %v, %v", firstErr, secondErr)
	}
	if first.Excerpt == "" || first.Excerpt != second.Excerpt || first.Structured["pull_request_id"] != "pr-1042" {
		t.Fatalf("unexpected repeated expansion: %#v %#v", first, second)
	}
}

func TestEvaluationStore_ResolveEvidence_expands_every_fixture_source(t *testing.T) {
	// Given
	store := evidenceFixtureStore(t)
	principal := storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"example-org/widget-service"}}
	tests := []struct {
		id            string
		structuredKey string
		entityID      string
	}{
		{id: "ev-pr-auth-002", structuredKey: "pull_request_id", entityID: "pr-1042"},
		{id: "ev-commit-auth-002", structuredKey: "commit_sha", entityID: "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"},
		{id: "ev-ci-checkout-001", structuredKey: "check_run_id", entityID: "checkout-e2e-run-4821"},
		{id: "ev-commit-checkout-001", structuredKey: "commit_sha", entityID: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			// When
			expanded, err := store.ResolveEvidence(context.Background(), principal, test.id)

			// Then
			if err != nil {
				t.Fatalf("expand fixture evidence: %v", err)
			}
			if expanded.Structured[test.structuredKey] != test.entityID || expanded.Excerpt == "" || expanded.Evidence.Source.SafeURI == "" {
				t.Fatalf("unexpected fixture expansion: %#v", expanded)
			}
		})
	}
}

func TestEvidenceResolver_observes_only_safe_latency_metadata(t *testing.T) {
	// Given
	fixedNow := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	observer := &evidenceObserver{}
	resolver := contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{
		Now:      func() time.Time { return fixedNow },
		Observer: observer,
	})

	// When
	_, err := resolver.Expand(context.Background(), contextpacket.EvidenceExpansionInput{
		Evidence: resolverEvidence("ci", "check_run", contractsv1.EvidenceAvailable),
		Excerpt:  "untrusted source content",
	})

	// Then
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if observer.observation.System != "ci" || observer.observation.Availability != contractsv1.EvidenceAvailable || observer.observation.Duration != 0 {
		t.Fatalf("unsafe or unexpected observation: %#v", observer.observation)
	}
}

type evidenceObserver struct {
	observation contextpacket.EvidenceExpansionObservation
}

func (o *evidenceObserver) ObserveEvidenceExpansion(_ context.Context, observation contextpacket.EvidenceExpansionObservation) {
	o.observation = observation
}

func evidenceFixtureStore(t *testing.T) *contextpacket.EvaluationStore {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	corpus, err := evalfixture.VerifyCorpus(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatalf("verify corpus: %v", err)
	}
	store, err := contextpacket.NewEvaluationStore(corpus, "org-fixture")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}
