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
	started := observed.Add(-48 * time.Hour)
	validFromOf := func(kind contextfabric.SubjectKind, at time.Time) *time.Time {
		if kind == contextfabric.SubjectIncident {
			return &at
		}
		return nil
	}
	entities := []contextfabric.EntityProjection{entity(team, "", ""), entity(repository, "", "")}
	var relationships []contextfabric.RelationshipProjection
	for _, s := range stored {
		property := contextfabric.MemberStatusStoredProperty(s.kind)
		subject := contextfabric.SubjectRef{Kind: s.kind, CanonicalID: s.id, Label: s.id}
		entities = append(entities, entity(subject, property, s.value))
		relationType, target := "OWNED_BY_TEAM", team
		if s.kind == contextfabric.SubjectIncident {
			relationType, target = "BELONGS_TO_REPOSITORY", repository
			entities[len(entities)-1].ValidFrom = &started
		}
		relationships = append(relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_" + s.id, Type: contextfabric.RelationshipType(relationType), From: subject, To: target,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + s.id}, ObservedAt: observed, ValidFrom: validFromOf(s.kind, started), SourceVersion: "v1",
		})
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
			if want := (contextfabric.MemberStatusOutsideSet{Kind: tc.kind, Count: tc.outside, Filtered: true, Reached: true}); graphContext.MemberStatusOutsideSet != want {
				t.Fatalf("outside the known set = %+v, want %+v", graphContext.MemberStatusOutsideSet, want)
			}
		})
	}
}

// TestLiveMemberStatusFilterHonoursARestrictedCallersScope projects incidents
// of one repository whose own authorization names a repository the caller
// cannot read, beside readable ones, with edges the caller can see. A caller
// restricted to the readable repository is served only the readable incident
// holding the status, and the outside-set count includes only readable
// incidents: the live adapter fills the node attributes the authorization test
// reads, and the filter reads and counts nothing else.
func TestLiveMemberStatusFilterHonoursARestrictedCallersScope(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-member-status-authz-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	started := observed.Add(-48 * time.Hour)
	readable := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	hidden := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/secret"}}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, status string) contextfabric.EntityProjection {
		properties := map[string]contextfabric.ScalarValue{}
		if status != "" {
			v := status
			properties["status"] = contextfabric.ScalarValue{String: &v}
		}
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: &started, SourceVersion: "v1",
		}
	}
	incidents := []struct {
		id, status string
		scope      contextfabric.AuthorizationScope
	}{
		{"incident:readable-open", "open", readable},
		{"incident:readable-resolved", "resolved", readable},
		{"incident:readable-unmapped", "Investigating", readable},
		{"incident:hidden-open", "open", hidden},
		{"incident:hidden-unmapped", "Paged", hidden},
		{"incident:hidden-no-status", "", hidden},
	}
	entities := []contextfabric.EntityProjection{entity(repository, readable, "")}
	var relationships []contextfabric.RelationshipProjection
	for _, in := range incidents {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: in.id, Label: in.id}
		entities = append(entities, entity(subject, in.scope, in.status))
		relationships = append(relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_" + in.id, Type: "BELONGS_TO_REPOSITORY", From: subject, To: repository,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: readable, EvidenceRefIDs: []string{"evidence_" + in.id}, ObservedAt: observed, ValidFrom: &started, SourceVersion: "v1",
		})
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_member_status_authz_0001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entities ApplyProjectionBatch() error = %v", err)
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_member_status_authz_0002", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationships ApplyProjectionBatch() error = %v", err)
	}

	bases := contextfabric.CommitBasisSet{}
	bases.Record(repository, contextfabric.CommitBasisAuthoritativeIdentity)
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_member_status_authz",
			Question: "which incidents of acme/api are open?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repository}, Candidates: []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_anchor", Subject: repository, State: contextfabric.ResolutionCommitted,
			MatchedTerms: []string{"acme/api"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}}},
		Frame: &contextfabric.QuestionFrame{
			Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{"acme/api"}, MemberKind: contextfabric.SubjectIncident,
				MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "open",
			}},
			Temporal: contextfabric.TemporalIntentCurrent,
			Version:  contextfabric.QuestionFrameVersion,
		},
		ScopeAnchorKind: contextfabric.SubjectRepository,
		Bases:           bases,
	}
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/api"}}
	graphContext, err := adapter.DiscoverContext(ctx, principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := []string{}
	if graphContext.Cohort != nil {
		for _, member := range graphContext.Cohort.Members {
			got = append(got, member.Subject.CanonicalID)
		}
	}
	sort.Strings(got)
	if want := []string{"incident:readable-open"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("served incidents = %v, want %v: an incident outside the caller's scope must not be served", got, want)
	}
	if want := (contextfabric.MemberStatusOutsideSet{Kind: contextfabric.SubjectIncident, Count: 1, Filtered: true, Reached: true}); graphContext.MemberStatusOutsideSet != want {
		t.Fatalf("outside the known set = %+v, want %+v: only the readable unmapped incident counts", graphContext.MemberStatusOutsideSet, want)
	}
}
