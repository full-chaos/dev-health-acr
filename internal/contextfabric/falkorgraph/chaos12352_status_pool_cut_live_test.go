package falkorgraph_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveATeamNarrowingStatesTheStatusPoolCutInsideTheNarrowing (CHAOS-12352):
// team alpha owns acme/big with 250 open pull requests and no review, so
// "acme/big's pull requests awaiting review" is decided page by page and the
// read is cut at its page cap. The anchor is the repository (its team list is
// "*", so a team narrowing keeps it as the subject; a team anchor outside the
// narrowing is dropped by its own team list, before any status read). The pool cut disclosure counts the members the read checked; under a
// team narrowing it counts only those inside the narrowing, and the pool (the
// anchor's count, never the requested team's: Anchor B) is not stated.
func TestLiveATeamNarrowingStatesTheStatusPoolCutInsideTheNarrowing(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12352-pool-cut-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	created := observed.Add(-48 * time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/big"}}
	open := "open"
	entity := func(s contextfabric.SubjectRef, props map[string]contextfabric.ScalarValue) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: props,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + s.CanonicalID}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	rel := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: id, Type: typ, From: from, To: to, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-big", Label: "acme/big"}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12352_pool_cut_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12352_pool_cut_0002", "cursor-1", "cursor-2"
	teamEntity := entity(team, nil)
	teamEntity.Authorization = contextfabric.AuthorizationScope{TeamIDs: []string{"alpha"}}
	e.Entities = append(e.Entities, teamEntity, entity(repo, nil))
	// Alpha owns acme/big since 12 hours ago; beta owned it before. The
	// narrowing reads the CURRENT owner on every axis.
	switched := observed.Add(-12 * time.Hour)
	former := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:beta", Label: "Beta"}
	formerEntity := entity(former, nil)
	formerEntity.Authorization = contextfabric.AuthorizationScope{TeamIDs: []string{"beta"}}
	e.Entities = append(e.Entities, formerEntity)
	owner := rel("rel_repo_team", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, team)
	owner.Authorization = contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/big"}, TeamIDs: []string{"alpha"}}
	owner.ValidFrom = &switched
	formerOwner := rel("rel_repo_team_beta", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, former)
	formerOwner.Authorization = contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/big"}, TeamIDs: []string{"beta"}}
	formerOwner.ValidTo = &switched
	r.Relationships = append(r.Relationships, owner, formerOwner)
	for i := 1; i <= 250; i++ {
		pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:big:%03d", i), Label: fmt.Sprintf("PR #%d", i)}
		e.Entities = append(e.Entities, entity(pr, map[string]contextfabric.ScalarValue{"state": {String: &open}}))
		r.Relationships = append(r.Relationships, rel(fmt.Sprintf("rel_pr_%03d_repo", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pr, repo))
	}
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	dayAgo := observed.Add(-24 * time.Hour)
	historical := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &dayAgo}
	for _, tc := range []struct {
		name  string
		teams []string
		want  contextfabric.MemberStatusPoolCut
		time  contextfabric.TimeContext
	}{
		{"no narrowing: the first 26 of the 250 read", nil, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 26, Pool: 250}, contextfabric.TimeContext{}},
		{"a narrowing that reaches none: a cut read that reached none of its members", []string{"team-other"}, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 0, Pool: 0}, contextfabric.TimeContext{}},
		{"the owning team (rule (i) holds): its 26 read of its 250", []string{"alpha"}, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 26, Pool: 250}, contextfabric.TimeContext{}},
		{"a day ago, the current owner: its 26 read of its 250", []string{"alpha"}, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 26, Pool: 250}, historical},
		{"a day ago, the former owner: no pool stated", []string{"beta"}, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 0, Pool: 0}, historical},
	} {
		teams := tc.teams
		timeContext := tc.time
		if timeContext.Axis == "" {
			timeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		}
		scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{repo.Label}, MemberKind: contextfabric.SubjectPullRequest,
			MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "awaiting_review"}
		bases := contextfabric.CommitBasisSet{}
		bases.Record(repo, contextfabric.CommitBasisAuthoritativeIdentity)
		request := contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12352b",
				Question: "which pull requests of acme/big await review?", TimeContext: timeContext,
				RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
				Options:        contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144},
				Consumer:       contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			},
			Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: timeContext},
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repo}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_anchor", Subject: repo, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{repo.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}},
			Frame: &contextfabric.QuestionFrame{
				Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
				Temporal:          contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
			},
			ScopeAnchorKind: repo.Kind, Bases: bases,
		}
		gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
		if err != nil {
			t.Fatalf("DiscoverContext error = %v", err)
		}
		if gc.MemberStatusPoolCut != tc.want {
			t.Errorf("%s: pool cut %+v, want %+v", tc.name, gc.MemberStatusPoolCut, tc.want)
		}
		if !gc.CohortPopulationLowerBound {
			t.Errorf("%s: the read was cut, want the population a lower bound", tc.name)
		}
	}
}
