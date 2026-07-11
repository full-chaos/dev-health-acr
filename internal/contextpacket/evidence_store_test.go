package contextpacket_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type referenceRows struct {
	orgID          string
	record         contextpacket.EvidenceReference
	candidates     []contractsv1.ResolvedScope
	referenceCalls int
}

type bundleRows struct{ evidence []contractsv1.EvidenceRef }

func (r *referenceRows) ResolveEvidenceScope(context.Context, contextpacket.ReadPlan) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}

func (r *referenceRows) EvidenceRows(context.Context, contextpacket.ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	return nil, nil, nil, nil
}

func (r *referenceRows) AuthorizedRepositories(_ context.Context, orgID string, scopes []string) ([]contractsv1.ResolvedScope, error) {
	r.orgID = orgID
	if r.candidates != nil {
		return r.candidates, nil
	}
	if len(scopes) != 1 || scopes[0] != r.record.RepoSlug {
		return nil, storage.ErrNotFound
	}
	return []contractsv1.ResolvedScope{{RepoID: "repo-server-derived", RepoSlug: r.record.RepoSlug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}}}, nil
}

func (r *referenceRows) ResolveEvidenceReference(_ context.Context, orgID string, _ contractsv1.ResolvedScope, _ string) ([]contextpacket.EvidenceReference, error) {
	r.orgID = orgID
	r.referenceCalls++
	return []contextpacket.EvidenceReference{r.record}, nil
}

func (r *bundleRows) ResolveEvidenceScope(_ context.Context, plan contextpacket.ReadPlan) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: plan.RepoSlug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}}, nil
}

func (r *bundleRows) EvidenceRows(context.Context, contextpacket.ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	return r.evidence, nil, nil, nil
}

func TestClickHouseEvidenceStore_expands_authenticated_handle(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:opaque-reference", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.Source.EntityType, evidence.SourceVersion = "ci_pipeline_run", "ci_pipeline_runs.v1"
	codec := fixtureEvidenceCodec(t)
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(&referenceRows{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: evidence, Excerpt: "untrusted content"}}, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	handle, err := codec.Encode("org-fixture", "repo-server-derived", evidence.SourceVersion, evidence.EvidenceRefID)
	if err != nil {
		t.Fatalf("encode handle: %v", err)
	}
	expanded, err := store.ResolveEvidence(context.Background(), fixturePrincipal(), handle)
	if err != nil || expanded.Structured["pipeline_run_id"] != evidence.Source.EntityID || expanded.Evidence.EvidenceRefID != handle {
		t.Fatalf("expanded = %#v, error = %v", expanded, err)
	}
}

func TestClickHouseEvidenceStore_emits_signed_handles(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:raw-id", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.SourceVersion = "ci_pipeline_runs.v1"
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(&bundleRows{evidence: []contractsv1.EvidenceRef{evidence}}, contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-opaque", "", ""))
	if err != nil || len(bundle.Evidence) != 1 || !strings.HasPrefix(bundle.Evidence[0].EvidenceRefID, "ev1_") || strings.Contains(bundle.Evidence[0].EvidenceRefID, evidence.EvidenceRefID) {
		t.Fatalf("bundle = %#v, error = %v", bundle, err)
	}
}

func TestClickHouseEvidenceStore_hides_unknown_boundaries_and_candidate_overflow(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:opaque-reference", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.Source.EntityType, evidence.SourceVersion = "ci_pipeline_run", "ci_pipeline_runs.v1"
	codec := fixtureEvidenceCodec(t)
	handle, err := codec.Encode("org-fixture", "repo-server-derived", evidence.SourceVersion, evidence.EvidenceRefID)
	if err != nil {
		t.Fatalf("encode handle: %v", err)
	}
	overflow := make([]contractsv1.ResolvedScope, 65)
	for index := range overflow {
		overflow[index] = contractsv1.ResolvedScope{RepoID: fmt.Sprintf("repo-%d", index), RepoSlug: "example-org/widget-service"}
	}
	tests := []referenceRows{
		{record: contextpacket.EvidenceReference{RepoSlug: "other-org/other-repo", Evidence: evidence}},
		{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: testEvidence("acr:v1:ci:other-locator", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))}},
		{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: evidence}, candidates: overflow},
	}
	for _, rows := range tests {
		store, createErr := contextpacket.NewClickHouseEvidenceStoreWithOptions(&rows, contextpacket.EvidenceStoreOptions{Codec: codec})
		if createErr != nil {
			t.Fatalf("create evidence store: %v", createErr)
		}
		_, resolveErr := store.ResolveEvidence(context.Background(), storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"example-org/widget-service"}}, handle)
		if !errors.Is(resolveErr, storage.ErrNotFound) || resolveErr.Error() != storage.ErrNotFound.Error() {
			t.Fatalf("resolve error = %v, want generic not found", resolveErr)
		}
	}
}

func TestClickHouseEvidenceStore_accepts_exactly_64_candidates(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:opaque-reference", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.Source.EntityType, evidence.SourceVersion = "ci_pipeline_run", "ci_pipeline_runs.v1"
	codec := fixtureEvidenceCodec(t)
	handle, err := codec.Encode("org-fixture", "repo-server-derived", evidence.SourceVersion, evidence.EvidenceRefID)
	if err != nil {
		t.Fatalf("encode handle: %v", err)
	}
	candidates := make([]contractsv1.ResolvedScope, 64)
	candidates[0] = contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}
	for index := 1; index < len(candidates); index++ {
		candidates[index] = contractsv1.ResolvedScope{RepoID: fmt.Sprintf("repo-%d", index), RepoSlug: "example-org/widget-service"}
	}
	rows := &referenceRows{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: evidence}, candidates: candidates}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	if _, err := store.ResolveEvidence(context.Background(), storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{"*"}}, handle); err != nil {
		t.Fatalf("resolve 64 candidates: %v", err)
	}
	if rows.referenceCalls != 1 {
		t.Fatalf("evidence reference queries = %d, want 1", rows.referenceCalls)
	}
}

func TestClickHouseEvidenceStoreRejectsUnroutableHandleWithoutReferenceQueries(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:opaque-reference", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.Source.EntityType, evidence.SourceVersion = "ci_pipeline_run", "ci_pipeline_runs.v1"
	codec := fixtureEvidenceCodec(t)
	handle, err := codec.Encode("org-fixture", "repo-server-derived", evidence.SourceVersion, evidence.EvidenceRefID)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(handle, "_")
	tagText, macText, found := strings.Cut(parts[3], ".")
	if !found {
		t.Fatal("evidence handle payload separator missing")
	}
	tag, err := base64.RawURLEncoding.DecodeString(tagText)
	if err != nil {
		t.Fatal(err)
	}
	tag[0] ^= 0xff
	parts[3] = base64.RawURLEncoding.EncodeToString(tag) + "." + macText
	rows := &referenceRows{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: evidence}, candidates: []contractsv1.ResolvedScope{{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}}}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ResolveEvidence(context.Background(), fixturePrincipal(), strings.Join(parts, "_"))

	if !errors.Is(err, storage.ErrNotFound) || rows.referenceCalls != 0 {
		t.Fatalf("error = %v, evidence reference queries = %d", err, rows.referenceCalls)
	}
}

func TestClickHouseEvidenceStore_default_constructor_fails_closed(t *testing.T) {
	store := contextpacket.NewClickHouseEvidenceStore(&bundleRows{})
	if _, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-closed", "", "")); err == nil {
		t.Fatal("codec-less store emitted evidence")
	}
}

func TestEvidenceStoreFactory_injects_codec_into_clickhouse_store(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:raw-id", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.SourceVersion = "ci_pipeline_runs.v1"
	store, err := contextpacket.NewEvidenceStoreFactory(fixtureEvidenceCodec(t))(&bundleRows{evidence: []contractsv1.EvidenceRef{evidence}})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-factory", "", ""))
	if err != nil || len(bundle.Evidence) != 1 || !strings.HasPrefix(bundle.Evidence[0].EvidenceRefID, "ev1_") {
		t.Fatalf("factory bundle = %#v, error = %v", bundle, err)
	}
}

func TestObservedEvidenceStoreFactoryInjectsExpansionObserver(t *testing.T) {
	evidence := testEvidence("acr:v1:ci:opaque-reference", "ci", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	evidence.Source.EntityType, evidence.SourceVersion = "ci_pipeline_run", "ci_pipeline_runs.v1"
	codec := fixtureEvidenceCodec(t)
	observer := &expansionObserver{}
	store, err := contextpacket.NewObservedEvidenceStoreFactory(codec, observer, nil)(&referenceRows{record: contextpacket.EvidenceReference{RepoSlug: "example-org/widget-service", Evidence: evidence}})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := codec.Encode("org-fixture", "repo-server-derived", evidence.SourceVersion, evidence.EvidenceRefID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ResolveEvidence(context.Background(), fixturePrincipal(), handle)

	if err != nil || observer.count != 1 || observer.observation.Outcome != contextpacket.OperationSuccess {
		t.Fatalf("resolve error=%v observation=%#v count=%d", err, observer.observation, observer.count)
	}
}

type expansionObserver struct {
	observation contextpacket.EvidenceExpansionObservation
	count       int
}

func (o *expansionObserver) ObserveEvidenceExpansion(_ context.Context, observation contextpacket.EvidenceExpansionObservation) {
	o.observation = observation
	o.count++
}

func fixtureEvidenceCodec(t *testing.T) *contextpacket.EvidenceIDCodec {
	t.Helper()
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatalf("create evidence codec: %v", err)
	}
	return codec
}
