package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// scoreLiveTestImage mirrors falkordbImage from adapter_live_integration_test.go
// (whitebox `package falkorgraph` here cannot see identifiers from that
// blackbox `package falkorgraph_test` file, even in the same test binary) --
// see that constant's doc comment for the pin rationale; update both
// together if the pin ever moves.
const scoreLiveTestImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// TestLiveFulltextSearchNormalizesRealRediSearchScores is the live half of
// the AC-3778-0 probe: adapter_live_integration_test.go already proves the
// public contract against a real server, but this test goes one level
// lower, straight at fulltextSearchNodes, to print the RAW RediSearch score
// magnitudes this fix actually normalizes and prove the fix holds on real
// server output, not just on hand-picked numbers in the fake-conn probe
// (score_normalization_test.go).
//
// Three Subject nodes are indexed with the same term ("outage") repeated a
// different number of times in their search_text (entitySearchText joins
// Subject.Label + aliases -- see projection.go), which is exactly the kind
// of thing that makes RediSearch's default scorer produce a materially
// different, > 1 score per hit (docs/design/context-fabric-falkordb-adapter.md
// §6.2 confirms live-observed scores of 4 and 4.5 from ordinary multi-term
// queries; repetition is this test's own lever for the same effect).
func TestLiveFulltextSearchNormalizesRealRediSearchScores(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: scoreLiveTestImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container.Host() error = %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatalf("container.MappedPort() error = %v", err)
	}
	adapter, err := New(Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-score-test", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orgID := "live-score-ladder-" + time.Now().UTC().Format("20060102T150405.000000000")
	observed := time.Now().UTC()

	// The query below is 3 OR-joined terms ("incident|outage|payment" --
	// tokenizeForFulltext splits on space and fulltextSearchNodes joins with
	// "|", per docs/design/context-fabric-falkordb-adapter.md §6.1's
	// verified OR behavior, whose own example -- 'payment|retry' -> d2
	// (4.5, matches both terms), d1 (0.5, matches one) -- is the live-
	// verified basis for this fixture shape). strong matches all 3 terms,
	// mid matches 2, weak matches 1: RediSearch's scorer sums a
	// contribution per matched term, so raw score should climb with match
	// count -- this is the live-observed >1, meaningfully-varying score
	// shape §6.2 calls out, unlike simple in-field repetition of one term
	// (tried first; FalkorDB's scorer did not vary score with that).
	strong := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident_strong", Label: "incident outage payment"}
	mid := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident_mid", Label: "incident outage"}
	weak := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident_weak", Label: "incident"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_live_score_00000001", OrgID: orgID, Source: "live-score-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: strong, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"*"}}, EvidenceRefIDs: []string{"evidence_strong"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: mid, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"*"}}, EvidenceRefIDs: []string{"evidence_mid"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: weak, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"*"}}, EvidenceRefIDs: []string{"evidence_weak"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	key := graphKey(adapter.config.GraphPrefix, orgID)
	candidates, err := adapter.fulltextSearchNodes(ctx, key, orgID, "incident outage payment", 10)
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("fulltextSearchNodes() returned %d candidates, want 3: %#v", len(candidates), candidates)
	}

	byID := make(map[string]graphrank.CandidateNode, 3)
	for _, c := range candidates {
		subject, ok := graphrank.NodeSubject(c)
		if !ok {
			t.Fatalf("candidate %#v did not decode to a subject", c)
		}
		byID[subject.CanonicalID] = c
	}
	got := func(id string) graphrank.CandidateNode {
		c, ok := byID[id]
		if !ok {
			t.Fatalf("fulltextSearchNodes() result missing %s: %#v", id, candidates)
		}
		return c
	}
	strongCandidate, midCandidate, weakCandidate := got("incident_strong"), got("incident_mid"), got("incident_weak")

	rawScore := func(c graphrank.CandidateNode) float64 {
		if c.Score == nil {
			t.Fatalf("candidate %#v has no raw Score", c)
		}
		return *c.Score
	}
	strongScore, midScore, weakScore := rawScore(strongCandidate), rawScore(midCandidate), rawScore(weakCandidate)
	t.Logf("live RediSearch raw scores: strong(3-term match)=%v mid(2-term match)=%v weak(1-term match)=%v", strongScore, midScore, weakScore)
	if !(strongScore >= midScore && midScore >= weakScore) {
		t.Skipf("live RediSearch scoring did not produce strong >= mid >= weak (%v, %v, %v) on this server run -- cannot exercise ordering with this fixture; the fake-conn probe (score_normalization_test.go) already covers the >1 inversion deterministically", strongScore, midScore, weakScore)
	}

	strongConfidence := graphrank.ResultConfidence(strongCandidate.Relevance, strongCandidate.Score)
	midConfidence := graphrank.ResultConfidence(midCandidate.Relevance, midCandidate.Score)
	weakConfidence := graphrank.ResultConfidence(weakCandidate.Relevance, weakCandidate.Score)
	t.Logf("normalized confidence: strong=%v mid=%v weak=%v", strongConfidence, midConfidence, weakConfidence)

	if strongConfidence < midConfidence || midConfidence < weakConfidence {
		t.Fatalf("confidence order did not track real relevance order: strong(score %v)=%v, mid(score %v)=%v, weak(score %v)=%v -- want strong >= mid >= weak",
			strongScore, strongConfidence, midScore, midConfidence, weakScore, weakConfidence)
	}
	if strongConfidence > fulltextRelevanceCeiling || weakConfidence < fulltextRelevanceFloor {
		t.Fatalf("confidence out of documented band [%v, %v]: strong=%v weak=%v", fulltextRelevanceFloor, fulltextRelevanceCeiling, strongConfidence, weakConfidence)
	}
}
