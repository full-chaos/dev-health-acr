package falkorgraph_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestLiveKindCensus runs CHAOS-5654's kind-scoped census on a real FalkorDB:
// a term-free survey whose frame declares a servable member kind outside the
// exact-name census reaches that kind's projected population, in canonical-id
// order, scoped to the caller's organization, and the emitted census line
// carries the population the engine returned.
func TestLiveKindCensus(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: falkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err, "start FalkorDB container")
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	var logs bytes.Buffer
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-kind-census", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
		Telemetry: falkorgraph.SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))},
	})
	require.NoError(t, err, "construct falkorgraph.Adapter")

	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-kind-census-a-" + stamp
	otherOrgID := "live-kind-census-b-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), otherOrgID) })

	const memberKind = contextfabric.SubjectIncident
	// Projected out of canonical-id order, so an ordered result is the query's
	// doing, not the projection's.
	order := []int{4, 1, 3, 0, 2}
	project := func(org string, ids []int, prefix string) {
		observed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		entities := []contextfabric.EntityProjection{}
		for _, i := range ids {
			entities = append(entities, contextfabric.EntityProjection{
				Subject:        contextfabric.SubjectRef{Kind: memberKind, CanonicalID: fmt.Sprintf("%s_%02d", prefix, i), Label: fmt.Sprintf("Outage %d", i)},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
				EvidenceRefIDs: []string{fmt.Sprintf("evidence_%s_%02d", prefix, i)},
				ObservedAt:     observed, SourceVersion: "v1",
			})
		}
		entities = append(entities, contextfabric.EntityProjection{
			Subject:        contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: prefix + "_team", Label: "Platform"},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_" + prefix + "_team"},
			ObservedAt:     observed, SourceVersion: "v1",
		})
		batch := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_kind_census_" + prefix, OrgID: org, Source: "live-kind-census",
			SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
			Entities: entities, Relationships: []contextfabric.RelationshipProjection{},
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
			Tombstones: []contextfabric.ProjectionTombstone{},
		}
		_, applyErr := adapter.ApplyProjectionBatch(ctx, batch)
		require.NoError(t, applyErr, "ApplyProjectionBatch(%s)", org)
	}
	project(orgID, order, "incident_a")
	project(otherOrgID, []int{0, 1}, "incident_b")

	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_00000054",
		Question: "Which ones need a look?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 3, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: request,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "attention",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		Frame: &contextfabric.QuestionFrame{
			Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: contextfabric.SubjectExpression{
				Kind:       contextfabric.SubjectExpressionDiscoveredKind,
				Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: memberKind},
			},
			Temporal: contextfabric.TemporalIntentCurrent,
			Version:  contextfabric.QuestionFrameVersion,
		},
	}

	graphContext, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, discovery)
	require.NoError(t, err, "DiscoverContext")
	require.NotNil(t, graphContext.Cohort, "a term-free %s survey reached no cohort", memberKind)
	require.Equal(t, memberKind, graphContext.Cohort.Kind)
	got := make([]string, 0, len(graphContext.Cohort.Members))
	for _, member := range graphContext.Cohort.Members {
		got = append(got, member.Subject.CanonicalID)
	}
	require.Equal(t, []string{"incident_a_00", "incident_a_01", "incident_a_02"}, got, "members in canonical-id order, cut at the member cap, from this organization only")
	require.Equal(t, 5, graphContext.CohortPopulation, "the whole projected population of this organization")
	require.True(t, graphContext.Cohort.Truncated, "cut at the member cap")

	var census map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		record := map[string]any{}
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "context_fabric: cohort kind census" {
			require.Nil(t, census, "more than one kind census line for one call")
			census = record
		}
	}
	require.NotNil(t, census, "no kind census line in %s", logs.String())
	require.Equal(t, "ran", census["decision"])
	require.Equal(t, string(memberKind), census["kinds"])
	require.Equal(t, float64(5), census["pool_size"])
	require.Equal(t, false, census["truncated"])
}
