package falkorgraph_test

// CHAOS-5517 (clauses 3+6, resolution seam): certifies eventspec.IdentityUniverse
// -- the ticket's own named cross-package producer -- through a REAL
// falkordb.Adapter (live testcontainer), a REAL NewSlogResolutionTracer +
// slog.JSONHandler, and certify.Certify/CertifyAbsent against the real
// emitted JSON. Reuses chaos3884_identity_lookup_live_test.go's own proven
// fixture (newLiveAdapterWithIdentityUniverseAndTracer,
// TestLiveAliasIdentityUniverseCompletenessIsTracedThroughTheWiredComposition's
// shape) but swaps its capture tracer (fakeResolutionTracer) for a real one.

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestLiveIdentityUniverseCertifiesThroughTheRealWiredComposition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		complete bool
	}{
		{name: "complete", complete: true},
		{name: "truncated", complete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			stamp := time.Now().UTC().Format("20060102T150405.000000000")
			orgID := "live-identity-universe-certify-" + tc.name + "-" + stamp
			repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:live-repo-certify-" + tc.name, Label: "full-chaos/certify-" + tc.name}

			identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
				if calledOrgID != orgID {
					return nil, time.Time{}, false, nil
				}
				return []graphrank.IdentityRow{{
					Kind: repo.Kind, CanonicalID: repo.CanonicalID, Label: repo.Label,
					Aliases: []string{"certify-" + tc.name}, ObservedAt: time.Now().UTC(),
				}}, time.Now().UTC(), tc.complete, nil
			}
			var buf bytes.Buffer
			tracer := graphrank.NewSlogResolutionTracer(
				slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			adapter := newLiveAdapterWithIdentityUniverseAndTracer(t, ctx, identityUniverse, falkorgraph.NoopTelemetry{}, tracer)
			t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

			batch := contextfabric.ProjectionBatch{
				SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_universe_certify", OrgID: orgID, Source: "live-identity-test",
				SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
				Entities: []contextfabric.EntityProjection{{
					Subject: repo, Aliases: []string{"certify-" + tc.name},
					Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repo.Label}},
					EvidenceRefIDs: []string{"evidence_repo_certify_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
				}},
				Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
				Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
			}
			if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
				t.Fatalf("ApplyProjectionBatch() error = %v", err)
			}

			principal := storage.Principal{OrgID: orgID}
			request := liveInvestigationRequest()
			interpreted := contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"certify-" + tc.name},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			}
			if _, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, ""); err != nil {
				t.Fatalf("ResolveSubjects() error = %v", err)
			}

			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse() on real production output error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.IdentityUniverse,
				Want:  map[string]any{"request_id": request.RequestID, "complete": tc.complete},
			}); err != nil {
				t.Fatalf("Certify(IdentityUniverse) error = %v", err)
			}
		})
	}
}

// TestLiveIdentityUniverseCertifiesAbsentOnTheHistoricalAxis drives the SAME
// wired composition on a HISTORICAL axis question -- HIGH-6's own
// "temporal authority stays with the graph" rule skips the identity
// mechanism entirely, so identity_universe must certify absent, not merely
// go unchecked.
func TestLiveIdentityUniverseCertifiesAbsentOnTheHistoricalAxis(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-identity-universe-certify-historical-" + stamp
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:live-repo-certify-historical", Label: "full-chaos/certify-historical"}

	identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return []graphrank.IdentityRow{{
			Kind: repo.Kind, CanonicalID: repo.CanonicalID, Label: repo.Label,
			Aliases: []string{"certify-historical"}, ObservedAt: time.Now().UTC(),
		}}, time.Now().UTC(), true, nil
	}
	var buf bytes.Buffer
	tracer := graphrank.NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	adapter := newLiveAdapterWithIdentityUniverseAndTracer(t, ctx, identityUniverse, falkorgraph.NoopTelemetry{}, tracer)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_universe_historical", OrgID: orgID, Source: "live-identity-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
		Entities: []contextfabric.EntityProjection{{
			Subject: repo, Aliases: []string{"certify-historical"},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repo.Label}},
			EvidenceRefIDs: []string{"evidence_repo_certify_historical"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	asOf := time.Now().UTC().Add(-24 * time.Hour)
	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"certify-historical"},
		// TemporalValidTime + a real AsOf makes newTemporalFilter's own
		// `active` true (falkorgraph/temporal.go) -- HIGH-6's own trigger
		// to skip the identity mechanism entirely.
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	if _, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, ""); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.IdentityUniverse, map[string]any{"request_id": request.RequestID}); err != nil {
		t.Fatalf("CertifyAbsent(IdentityUniverse) error = %v -- a historical-axis question must skip the identity mechanism entirely", err)
	}
}
