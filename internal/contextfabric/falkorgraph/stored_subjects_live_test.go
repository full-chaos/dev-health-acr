package falkorgraph_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveStoredSubjectDecisionEqualsTheLiveExactHintDecision is a
// differential oracle on a real FalkorDB: for every projected subject and
// every caller grant, the stored-result decision admits the subject exactly
// when a live turn naming that subject by canonical id commits it. A subject
// never projected, or projected under another organization, is absent.
func TestLiveStoredSubjectDecisionEqualsTheLiveExactHintDecision(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "stored-authz-a-" + stamp
	otherOrgID := "stored-authz-b-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), otherOrgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	entity := func(kind contextfabric.SubjectKind, id string, repos []string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject:    contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: "Label " + id},
			Properties: map[string]contextfabric.ScalarValue{}, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: repos},
			EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, SourceVersion: "v1",
		}
	}
	entities := []contextfabric.EntityProjection{
		entity(contextfabric.SubjectProject, "project_in", []string{"full-chaos/dev-health-acr"}),
		entity(contextfabric.SubjectRepository, "repository_out", []string{"other-org/secret-service"}),
		entity(contextfabric.SubjectPullRequest, "pull_request_in", []string{"full-chaos/dev-health-acr"}),
		entity(contextfabric.SubjectWorkItem, "work_item_repo_less", []string{"acr-context-fabric:no-repository"}),
		entity(contextfabric.SubjectTeam, "team_two_repos", []string{"other-org/secret-service", "full-chaos/dev-health-acr"}),
	}
	batch := func(org string, entities []contextfabric.EntityProjection) contextfabric.ProjectionBatch {
		return contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_stored_" + org[:14], OrgID: org, Source: "stored-authz-test",
			SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
			Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
			Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch(orgID, entities)); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	otherOnly := entity(contextfabric.SubjectProject, "project_other_org_only", []string{"full-chaos/dev-health-acr"})
	if _, err := adapter.ApplyProjectionBatch(ctx, batch(otherOrgID, []contextfabric.EntityProjection{otherOnly})); err != nil {
		t.Fatalf("ApplyProjectionBatch(other org) error = %v", err)
	}

	subjects := make([]contextfabric.SubjectRef, 0, len(entities)+2)
	for _, projected := range entities {
		subjects = append(subjects, projected.Subject)
	}
	neverProjected := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident_never", Label: "Never"}
	subjects = append(subjects, neverProjected, otherOnly.Subject)

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	grants := map[string][]string{
		"repo_in": {"full-chaos/dev-health-acr"}, "repo_out": {"other/repo"}, "owner_wildcard": {"full-chaos/*"}, "universal": {"*"},
	}
	admittedSomewhere, deniedSomewhere := 0, 0
	for name, grant := range grants {
		principal := storage.Principal{OrgID: orgID, RepositoryScopes: grant}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("ResolveInvestigationBinding() error = %v", err)
		}
		outcomes, err := adapter.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
		if err != nil {
			t.Fatalf("%s: AuthorizeStoredSubjects() error = %v", name, err)
		}
		if len(outcomes) != len(subjects) {
			t.Fatalf("%s: %d outcomes for %d subjects", name, len(outcomes), len(subjects))
		}
		// The same subjects named by canonical id alone: each id is carried by
		// exactly one projected node here, so the id-only decision equals the
		// keyed one.
		unkinded := make([]contextfabric.SubjectRef, len(subjects))
		for index, subject := range subjects {
			unkinded[index] = contextfabric.SubjectRef{CanonicalID: subject.CanonicalID}
		}
		byID, err := adapter.AuthorizeStoredSubjects(ctx, principal, binding, unkinded)
		if err != nil {
			t.Fatalf("%s: AuthorizeStoredSubjects(by id) error = %v", name, err)
		}
		for index := range subjects {
			if byID[index] != outcomes[index] {
				t.Errorf("%s/%s: by-id outcome %s, keyed outcome %s", name, subjects[index].CanonicalID, byID[index], outcomes[index])
			}
		}
		for index, subject := range subjects {
			request := liveInvestigationRequest()
			request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "live-test"}}
			resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, binding, nil, nil, nil, "")
			if err != nil {
				t.Fatalf("%s/%s: live ResolveSubjects() error = %v", name, subject.CanonicalID, err)
			}
			live := false
			for _, committed := range resolution.Committed {
				if committed.Kind == subject.Kind && committed.CanonicalID == subject.CanonicalID {
					live = true
				}
			}
			stored := outcomes[index] == contextfabric.StoredSubjectAdmitted
			if stored != live {
				t.Errorf("%s/%s: stored decision %s, live exact-hint committed=%t", name, subject.CanonicalID, outcomes[index], live)
			}
			if stored {
				admittedSomewhere++
			} else {
				deniedSomewhere++
			}
			if (subject == neverProjected || subject == otherOnly.Subject) && outcomes[index] != contextfabric.StoredSubjectAbsent {
				t.Errorf("%s/%s: outcome %s, want absent", name, subject.CanonicalID, outcomes[index])
			}
		}
	}
	// The table is not vacuous: both answers occur.
	if admittedSomewhere == 0 || deniedSomewhere == 0 {
		t.Fatalf("admitted=%d denied=%d; the fixture must produce both", admittedSomewhere, deniedSomewhere)
	}

	// A missing graph is ErrGraphNotProjected, never an empty admission.
	missing := storage.Principal{OrgID: "stored-authz-never-" + stamp, RepositoryScopes: []string{"*"}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, missing)
	if err == nil {
		_, err = adapter.AuthorizeStoredSubjects(ctx, missing, binding, subjects[:1])
	}
	if !errors.Is(err, contextfabric.ErrGraphNotProjected) {
		t.Fatalf("unprojected organization error = %v, want ErrGraphNotProjected", err)
	}
	t.Logf("admitted=%d denied=%d", admittedSomewhere, deniedSomewhere)
}
