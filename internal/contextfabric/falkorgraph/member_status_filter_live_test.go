package falkorgraph_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveProjectAndIncidentStatusFilterSelectsByStoredStatus projects a
// team's projects and an organization's incidents into a real FalkorDB with
// the property names the producers write (state, status), some inside the
// kind's closed set and some outside it or absent, then asks for each status
// through the production reader: the stored-property read runs as written, the
// served members are exactly the ones holding the status, and the members
// outside the set are counted.
func TestLiveProjectAndIncidentStatusFilterSelectsByStoredStatus(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-member-status-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:acme", Label: "Acme"}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	entity := func(subject contextfabric.SubjectRef, property, value string) contextfabric.EntityProjection {
		properties := map[string]contextfabric.ScalarValue{}
		if value != "" {
			v := value
			properties[property] = contextfabric.ScalarValue{String: &v}
		}
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, SourceVersion: "v1",
		}
	}
	stored := []struct {
		kind  contextfabric.SubjectKind
		id    string
		value string
	}{
		{contextfabric.SubjectProject, "project:linear-started", "started"},
		{contextfabric.SubjectProject, "project:linear-completed", "completed"},
		{contextfabric.SubjectProject, "project:jira-free-text", "In Progress"},
		{contextfabric.SubjectProject, "project:github-free-text", "OPEN"},
		{contextfabric.SubjectProject, "project:no-state", ""},
		{contextfabric.SubjectIncident, "incident:open", "open"},
		{contextfabric.SubjectIncident, "incident:resolved", "resolved"},
		{contextfabric.SubjectIncident, "incident:raw", "Investigating"},
		{contextfabric.SubjectIncident, "incident:no-status", ""},
	}
	entities := []contextfabric.EntityProjection{entity(team, "", ""), entity(repository, "", "")}
	var relationships []contextfabric.RelationshipProjection
	for _, s := range stored {
		property := contextfabric.MemberStatusStoredProperty(s.kind)
		subject := contextfabric.SubjectRef{Kind: s.kind, CanonicalID: s.id, Label: s.id}
		entities = append(entities, entity(subject, property, s.value))
		if s.kind == contextfabric.SubjectProject {
			relationships = append(relationships, contextfabric.RelationshipProjection{
				RelationshipID: "relationship_" + s.id + "_owned", Type: "OWNED_BY_TEAM", From: subject, To: team,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization: scope, EvidenceRefIDs: []string{"evidence_" + s.id}, ObservedAt: observed, SourceVersion: "v1",
			})
		}
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_member_status_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entities ApplyProjectionBatch() error = %v", err)
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_member_status_00000002", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationships ApplyProjectionBatch() error = %v", err)
	}

	cases := []struct {
		kind    contextfabric.SubjectKind
		anchor  contextfabric.SubjectRef
		status  string
		want    []string
		outside int
	}{
		{contextfabric.SubjectProject, team, "started", []string{"project:linear-started"}, 3},
		{contextfabric.SubjectProject, team, "completed", []string{"project:linear-completed"}, 3},
		{contextfabric.SubjectProject, team, "paused", []string{}, 3},
		{contextfabric.SubjectIncident, repository, "open", []string{"incident:open"}, 2},
		{contextfabric.SubjectIncident, repository, "resolved", []string{"incident:resolved"}, 2},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"="+tc.status, func(t *testing.T) {
			bases := contextfabric.CommitBasisSet{}
			bases.Record(tc.anchor, contextfabric.CommitBasisAuthoritativeIdentity)
			request := contextfabric.GraphDiscoveryRequest{
				Request: contextfabric.InvestigationRequest{
					SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_member_status",
					Question: "which members are in that state?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Options: contextfabric.InvestigationOptions{
						MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
						MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
					},
					Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
				},
				Interpretation: contextfabric.InterpretedQuestion{
					Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				},
				Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{tc.anchor}, Candidates: []contextfabric.SubjectCandidate{{
					ReceiptID: "receipt_anchor", Subject: tc.anchor, State: contextfabric.ResolutionCommitted,
					MatchedTerms: []string{tc.anchor.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
				}}},
				Frame: &contextfabric.QuestionFrame{
					Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
					SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{
						AnchorTerms: []string{tc.anchor.Label}, MemberKind: tc.kind,
						MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: tc.status,
					}},
					Temporal: contextfabric.TemporalIntentCurrent,
					Version:  contextfabric.QuestionFrameVersion,
				},
				ScopeAnchorKind: tc.anchor.Kind,
				Bases:           bases,
			}
			graphContext, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			got := []string{}
			if graphContext.Cohort != nil {
				for _, member := range graphContext.Cohort.Members {
					if member.Subject.Kind == tc.kind {
						got = append(got, member.Subject.CanonicalID)
					}
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("served %s = %v, want %v", tc.kind, got, tc.want)
			}
			if want := (contextfabric.MemberStatusOutsideSet{Kind: tc.kind, Count: tc.outside}); graphContext.MemberStatusOutsideSet != want {
				t.Fatalf("outside the known set = %+v, want %+v", graphContext.MemberStatusOutsideSet, want)
			}
		})
	}
}
