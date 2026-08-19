package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	candidates, truncated, err := adapter.fulltextSearchNodes(ctx, key, orgID, "incident outage payment", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if truncated {
		t.Fatalf("fulltextSearchNodes() reported truncated=true for a 3-row result well under the limit=10 budget")
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

	// Codex P4: this must be a hard assertion, not a t.Skipf escape hatch --
	// a skip on an unexpected live measurement is a PASSING outcome, which
	// silently stops proving anything on the exact axis (real, > 1,
	// meaningfully unbounded RediSearch scores) this fix exists to handle.
	// Fixed-round-2's raw scores are consistently > 1 on the pinned FalkorDB
	// image for this fixture (verified repeatedly against a live
	// container); if that ever stops holding, this test SHOULD fail loudly,
	// not quietly downgrade to a skip.
	if strongScore <= 1 || midScore <= 1 || weakScore <= 1 {
		t.Fatalf("live RediSearch raw scores were not all > 1 (strong=%v mid=%v weak=%v) -- this test requires realistic, unbounded-above scores to exercise the D11 defect's exact range", strongScore, midScore, weakScore)
	}

	strongConfidence := graphrank.ResultConfidence(strongCandidate.Relevance, strongCandidate.Score)
	midConfidence := graphrank.ResultConfidence(midCandidate.Relevance, midCandidate.Score)
	weakConfidence := graphrank.ResultConfidence(weakCandidate.Relevance, weakCandidate.Score)
	t.Logf("normalized confidence: strong=%v mid=%v weak=%v", strongConfidence, midConfidence, weakConfidence)

	// Fix round 2 (Codex P1/P3): confidence is now computed client-side from
	// each candidate's own search_text word coverage (queries.go's
	// fulltextMatchedTermCount), not from the live RediSearch score at all
	// -- so this ordering is guaranteed BY CONSTRUCTION (strong's label
	// contains all 3 query words, mid 2, weak 1), not merely "usually true
	// on this server run". A violation here means fulltextMatchedTermCount
	// itself is broken against a real server's decoded node/attribute
	// shape, not that the live scorer produced an unlucky sample.
	if strongConfidence < midConfidence || midConfidence < weakConfidence {
		t.Fatalf("confidence order did not track real word-coverage order: strong(score %v)=%v, mid(score %v)=%v, weak(score %v)=%v -- want strong >= mid >= weak",
			strongScore, strongConfidence, midScore, midConfidence, weakScore, weakConfidence)
	}
	if strongConfidence != fulltextRelevanceCeiling {
		t.Fatalf("strong (3-of-3 term match) confidence = %v, want exactly the ceiling %v", strongConfidence, fulltextRelevanceCeiling)
	}
	if weakConfidence < fulltextRelevanceFloor || weakConfidence > fulltextRelevanceCeiling {
		t.Fatalf("confidence out of documented band [%v, %v]: weak=%v", fulltextRelevanceFloor, fulltextRelevanceCeiling, weakConfidence)
	}
}

// TestLiveResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit is the live
// counterpart of TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit
// (score_normalization_round2_test.go's fake-conn probe): against a real
// FalkorDB server, a lone fulltext hit whose own indexed text matches only
// 1 of 4 query terms must not auto-commit (Codex P1 / AC-3778-4: a weak
// single lexical match must not silently read as a confident answer).
func TestLiveResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit(t *testing.T) {
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
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-weak-lone-test", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orgID := "live-weak-lone-" + time.Now().UTC().Format("20060102T150405.000000000")
	observed := time.Now().UTC()
	// Only "outage" from the 4-term question below appears in this
	// project's label -- a genuinely weak, 1-of-4 lexical match, and the
	// ONLY subject projected into this organization's graph (so it is a
	// real "lone hit", not an artifact of truncation).
	weak := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_weak", Label: "Unrelated Outage Tracker"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_live_weak_00000001", OrgID: orgID, Source: "live-score-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: weak, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"*"}}, EvidenceRefIDs: []string{"evidence_weak"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	principal := storage.Principal{OrgID: orgID}
	request := contextfabric.InvestigationRequest{
		Question: "incident outage payment gateway",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"incident outage payment gateway"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}
	resolution, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects() committed %#v against a real FalkorDB server for a lone hit matching only 1 of 4 query terms -- want no auto-commit", resolution.Committed)
	}
	// Codex R2-3: asserting only "nothing committed" passes vacuously if
	// the search found nothing at all (e.g. a query/index regression that
	// silently drops every candidate) -- that is not the scenario this test
	// exists to prove. The planted weak hit must actually be PRESENT, as a
	// candidate, at a confidence below the lone-candidate gate -- proving
	// the weak match was found and correctly demoted, not merely absent.
	var weakCandidate *contextfabric.SubjectCandidate
	for i := range resolution.Candidates {
		if resolution.Candidates[i].Subject.CanonicalID == weak.CanonicalID {
			weakCandidate = &resolution.Candidates[i]
			break
		}
	}
	if weakCandidate == nil {
		t.Fatalf("ResolveSubjects() candidates = %#v, want the planted weak hit (%s) present -- \"nothing committed\" must not pass vacuously because nothing was found at all", resolution.Candidates, weak.CanonicalID)
	}
	// Read live, not a hand-duplicated literal (CHAOS-3857 un-staling sweep,
	// chris 2026-08-17): this used to hardcode 0.72, which drifted the
	// moment LoneFloor moved to 0.68 and would have kept silently passing
	// against a threshold production no longer uses.
	loneFloor := graphrank.DefaultCommitGatePolicy().LoneFloor
	if weakCandidate.Confidence >= loneFloor {
		t.Fatalf("planted weak hit confidence = %v, want < %v (the lone-candidate auto-commit gate) for a 1-of-4-term match", weakCandidate.Confidence, loneFloor)
	}
}
