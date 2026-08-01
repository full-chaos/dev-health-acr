package clickhouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestIntegrationClient_matches_exact_evidence_digest(t *testing.T) {
	client, _ := integrationClient(t)
	digest := sha256.Sum256([]byte("acr:v1:ci:fixture"))
	rows, err := client.Query(context.Background(), `SELECT evidence_ref_id FROM (SELECT 'acr:v1:ci:fixture' evidence_ref_id) WHERE lower(hex(SHA256(evidence_ref_id))) = {evidence_locator_hash:String} LIMIT 2`, []contextpacket.ClickHouseBinding{{Name: "evidence_locator_hash", Value: hex.EncodeToString(digest[:])}})
	if err != nil {
		t.Fatalf("query exact evidence digest: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("exact evidence digest returned no rows: %v", rows.Err())
	}
	var locator string
	if err := rows.Scan(&locator); err != nil || locator != "acr:v1:ci:fixture" {
		t.Fatalf("exact evidence digest locator = %q, error = %v", locator, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("exact evidence digest returned multiple rows: %v", rows.Err())
	}
}

func TestIntegrationClient_queries_read_only_ClickHouse(t *testing.T) {
	// Given
	client, options := integrationClient(t)

	// When
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer rows.Close()

	// Then
	if !rows.Next() {
		t.Fatalf("SELECT 1 returned no rows: %v", rows.Err())
	}
	var value uint8
	if err := rows.Scan(&value); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if value != 1 {
		t.Fatalf("SELECT 1 value = %d", value)
	}
	if _, err := client.Query(context.Background(), "INSERT INTO evidence VALUES ('forbidden')", nil); !errors.Is(err, ErrUnsafeStatement) {
		t.Fatalf("INSERT error = %v, want ErrUnsafeStatement", err)
	}
	if _, err := client.Query(context.Background(), "SELECT 1; SELECT 2", nil); !errors.Is(err, ErrUnsafeStatement) {
		t.Fatalf("multi-statement error = %v, want ErrUnsafeStatement", err)
	}
	assertIntegrationParameterEncoding(t, client)
	assertIntegrationExecutionLimit(t, options)
	assertIntegrationEvidenceLookup(t, client)
	assertIntegrationServerRejectsMutation(t, options)
}

func TestIntegrationClient_native_readonly_fixture_is_not_skipped(t *testing.T) {
	// Given
	client, options := integrationClient(t)

	// When
	err := client.Ping(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	assertIntegrationServerRejectsMutation(t, options)
}

func assertIntegrationExecutionLimit(t *testing.T, options Options) {
	t.Helper()

	// Given
	limited, err := NewClickHouseQueryClientWithOptions(Options{DSN: options.DSN, TLS: options.TLS, MaxExecutionTime: 1, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("create execution-limited client: %v", err)
	}
	t.Cleanup(func() { _ = limited.Close() })

	// When
	rows, err := limited.Query(context.Background(), "SELECT getSetting('max_execution_time')", nil)
	if err != nil {
		t.Fatalf("query max_execution_time: %v", err)
	}
	t.Cleanup(func() { _ = rows.Close() })

	// Then
	if !rows.Next() {
		t.Fatalf("max_execution_time returned no rows: %v", rows.Err())
	}
	var limit float64
	if err := rows.Scan(&limit); err != nil || limit != 1 {
		t.Fatalf("max_execution_time = %v, %v; want 1, nil", limit, err)
	}
	started := time.Now()
	timeoutRows, err := limited.Query(context.Background(), "SELECT sleepEachRow(1) FROM numbers(3) SETTINGS max_block_size = 1", nil)
	if err == nil {
		for timeoutRows.Next() {
		}
		err = timeoutRows.Err()
		if closeErr := timeoutRows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if err == nil {
		t.Fatal("execution-limited query unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("execution-limited query took %v, want at most 2s", elapsed)
	}
}

func assertIntegrationParameterEncoding(t *testing.T, client *Client) {
	t.Helper()

	// Given
	ctx := context.Background()

	// When
	nilRows, err := client.Query(ctx, "SELECT {as_of:Nullable(DateTime64(3, 'UTC'))} IS NULL", []contextpacket.ClickHouseBinding{{Name: "as_of", Value: nil}})
	if err != nil {
		t.Fatalf("query nil DateTime64: %v", err)
	}
	t.Cleanup(func() { _ = nilRows.Close() })
	timeRows, err := client.Query(ctx, "SELECT toString({as_of:Nullable(DateTime64(3, 'UTC'))})", []contextpacket.ClickHouseBinding{{Name: "as_of", Value: time.Date(2026, time.July, 10, 11, 0, 0, 123_456_789, time.UTC)}})
	if err != nil {
		t.Fatalf("query DateTime64: %v", err)
	}
	t.Cleanup(func() { _ = timeRows.Close() })

	// Then
	if !nilRows.Next() {
		t.Fatalf("nil DateTime64 returned no rows: %v", nilRows.Err())
	}
	var isNull uint8
	if err := nilRows.Scan(&isNull); err != nil || isNull != 1 {
		t.Fatalf("nil DateTime64 result = %d, %v; want 1, nil", isNull, err)
	}
	if !timeRows.Next() {
		t.Fatalf("DateTime64 returned no rows: %v", timeRows.Err())
	}
	var formatted string
	if err := timeRows.Scan(&formatted); err != nil || formatted != "2026-07-10 11:00:00.123" {
		t.Fatalf("DateTime64 result = %q, %v; want UTC milliseconds", formatted, err)
	}
}

func assertIntegrationEvidenceLookup(t *testing.T, client *Client) {
	t.Helper()

	// Given
	const (
		orgID    = "acr-integration-org"
		repoID   = "00000000-0000-0000-0000-000000000001"
		repoSlug = "full-chaos/acr-integration"
	)
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{repoSlug}}
	request := contractsv1.ContextPacketRequest{Repository: contractsv1.RepositoryRef{Slug: repoSlug}, Scope: contractsv1.RequestedScope{Branch: "main"}}
	rows := contextpacket.NewCatalogClickHouseRows(client)
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatalf("create evidence codec: %v", err)
	}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}

	// When
	scope, err := store.ResolveScope(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("query evidence catalog: %v", err)
	}
	if len(bundle.Evidence) == 0 {
		t.Fatal("seeded catalog returned no evidence")
	}
	var ciEvidence *contractsv1.EvidenceRef
	for index := range bundle.Evidence {
		if bundle.Evidence[index].SourceVersion == "ci_pipeline_runs.v1" {
			ciEvidence = &bundle.Evidence[index]
			break
		}
	}
	if ciEvidence == nil {
		t.Fatalf("seeded CI evidence missing from %#v", bundle.Evidence)
	}
	expanded, err := store.ResolveEvidence(context.Background(), principal, ciEvidence.EvidenceRefID)

	// Then
	if scope.RepoID != repoID || scope.RepoSlug != repoSlug || scope.Resolution != contractsv1.ScopeBranchFiltered {
		t.Fatalf("resolved scope = %#v", scope)
	}
	if ciEvidence.EvidenceRefID[:4] != "ev2_" {
		t.Fatalf("seeded evidence = %#v", ciEvidence)
	}
	if err != nil || expanded.Evidence.SourceVersion != "ci_pipeline_runs.v1" {
		t.Fatalf("resolve seeded evidence = %#v, %v", expanded, err)
	}
}
