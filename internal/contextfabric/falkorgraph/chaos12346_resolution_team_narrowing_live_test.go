package falkorgraph_test

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveATeamNarrowingSettlesResolutionTowardTheTeam (CHAOS-12346, lead
// ruling): under a requested team narrowing, subject resolution offers only
// the candidates inside the team when at least one is, and commits the one
// in-team candidate the way a repository narrowing does; with no in-team
// candidate it keeps today's offers, so the conversation never dead-ends.
// acme/payments-api and acme/payments-gateway are owned by team-b,
// acme/payments-worker by team-c; team-x owns nothing.
func TestLiveATeamNarrowingSettlesResolutionTowardTheTeam(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12346-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	created := observed.Add(-48 * time.Hour)
	entity := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", "/", "_").Replace(s.CanonicalID)}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	owned := func(id string, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: id, Type: contractsv1.ContextFabricRelationshipOwnedByTeam, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	team := func(id, label string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: label}
	}
	repository := func(slug string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + slug, Label: slug}
	}
	teamB, teamC, teamX := team("team-b", "Team B"), team("team-c", "Team C"), team("team-x", "Team X")
	api, gateway, worker := repository("acme/payments-api"), repository("acme/payments-gateway"), repository("acme/payments-worker")
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12346_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12346_0002", "cursor-1", "cursor-2"
	for _, t := range []struct {
		team contextfabric.SubjectRef
		id   string
	}{{teamB, "team-b"}, {teamC, "team-c"}, {teamX, "team-x"}} {
		e.Entities = append(e.Entities, entity(t.team, contextfabric.AuthorizationScope{TeamIDs: []string{t.id}}))
	}
	for _, o := range []struct {
		repository contextfabric.SubjectRef
		owner      contextfabric.SubjectRef
		teamID     string
	}{{api, teamB, "team-b"}, {gateway, teamB, "team-b"}, {worker, teamC, "team-c"}} {
		e.Entities = append(e.Entities, entity(o.repository, contextfabric.AuthorizationScope{RepositorySlugs: []string{o.repository.Label}}))
		r.Relationships = append(r.Relationships, owned("rel_"+strings.TrimPrefix(o.repository.Label, "acme/")+"_owner", o.repository, o.owner,
			contextfabric.AuthorizationScope{RepositorySlugs: []string{o.repository.Label}, TeamIDs: []string{o.teamID}}))
	}
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	resolve := func(t *testing.T, term string, teams []string) (committed, offered []string) {
		t.Helper()
		request := liveInvestigationRequest()
		request.Question = "how is " + term + " doing?"
		request.RequestedScope = contextfabric.RequestedScope{TeamIDs: teams}
		interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{term},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: orgID}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("term %q teams %v: ResolveSubjects() error = %v", term, teams, err)
		}
		committed, offered = []string{}, []string{}
		for _, s := range resolution.Committed {
			committed = append(committed, s.Label)
		}
		for _, c := range resolution.Candidates {
			offered = append(offered, c.Subject.Label)
		}
		sort.Strings(committed)
		sort.Strings(offered)
		return committed, offered
	}
	all := []string{api.Label, gateway.Label, worker.Label}
	for _, tc := range []struct {
		name          string
		term          string
		teams         []string
		wantCommitted []string
		wantOffered   []string
	}{
		{"no narrowing: all three are offered", "payments", nil, []string{}, all},
		{"team-b owns two: offered among those two only", "payments", []string{"team-b"}, []string{}, []string{api.Label, gateway.Label}},
		{"team-c owns one: it is committed", "payments", []string{"team-c"}, []string{worker.Label}, []string{worker.Label}},
		{"team-x owns none: today's offers are kept", "payments", []string{"team-x"}, []string{}, all},
		{"a term matching only another team's repository: still offered, no dead end", "payments-worker", []string{"team-b"}, []string{}, []string{worker.Label}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed, offered := resolve(t, tc.term, tc.teams)
			if !reflect.DeepEqual(committed, tc.wantCommitted) || !reflect.DeepEqual(offered, tc.wantOffered) {
				t.Fatalf("term %q teams %v: committed %v offered %v, want committed %v offered %v", tc.term, tc.teams, committed, offered, tc.wantCommitted, tc.wantOffered)
			}
		})
	}
}

// TestLiveATeamNarrowingNeverDropsANamedSubject (CHAOS-12346, Anchor B and the
// cross-class veto residual): under a requested team narrowing, a subject the
// question names by identity stays, inside or outside the team, and a term
// with no in-team match keeps its match. team-b owns acme/payments-api and
// acme/billing; team-c owns acme/payments-worker, acme/ledger-svc and the
// project Invoicing Ops; acme/payments-legacy has no owner. acme/billing and
// Invoicing Ops both carry the alias "invoicing".
func TestLiveATeamNarrowingNeverDropsANamedSubject(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12346-named-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	created := observed.Add(-48 * time.Hour)
	entity := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, aliases ...string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: append([]string{}, aliases...), PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", "/", "_", " ", "_").Replace(s.CanonicalID)}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	teamB := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-b", Label: "Team B"}
	teamC := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-c", Label: "Team C"}
	repository := func(slug string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + slug, Label: slug}
	}
	api, worker, ledger, billing := repository("acme/payments-api"), repository("acme/payments-worker"), repository("acme/ledger-svc"), repository("acme/billing")
	legacy := repository("acme/payments-legacy")
	ops := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:invoicing-ops", Label: "Invoicing Ops"}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12346_named_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12346_named_0002", "cursor-1", "cursor-2"
	e.Entities = append(e.Entities,
		entity(teamB, contextfabric.AuthorizationScope{TeamIDs: []string{"team-b"}}), entity(teamC, contextfabric.AuthorizationScope{TeamIDs: []string{"team-c"}}),
		entity(ops, contextfabric.AuthorizationScope{ProjectIDs: []string{"invoicing-ops"}}, "invoicing"),
		entity(legacy, contextfabric.AuthorizationScope{RepositorySlugs: []string{legacy.Label}}))
	owned := func(id string, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: id, Type: contractsv1.ContextFabricRelationshipOwnedByTeam, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"}
	}
	for _, o := range []struct {
		repository contextfabric.SubjectRef
		owner      contextfabric.SubjectRef
		teamID     string
		aliases    []string
	}{{api, teamB, "team-b", nil}, {billing, teamB, "team-b", []string{"invoicing"}}, {worker, teamC, "team-c", nil}, {ledger, teamC, "team-c", nil}} {
		e.Entities = append(e.Entities, entity(o.repository, contextfabric.AuthorizationScope{RepositorySlugs: []string{o.repository.Label}}, o.aliases...))
		r.Relationships = append(r.Relationships, owned("rel_"+strings.TrimPrefix(o.repository.Label, "acme/")+"_owner", o.repository, o.owner,
			contextfabric.AuthorizationScope{RepositorySlugs: []string{o.repository.Label}, TeamIDs: []string{o.teamID}}))
	}
	r.Relationships = append(r.Relationships, owned("rel_invoicing_ops_owner", ops, teamC, contextfabric.AuthorizationScope{ProjectIDs: []string{"invoicing-ops"}, TeamIDs: []string{"team-c"}}))
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	resolve := func(t *testing.T, terms []string, teams []string) (committed, offered []string) {
		t.Helper()
		request := liveInvestigationRequest()
		request.Question = "how are " + strings.Join(terms, " and ") + " doing?"
		request.RequestedScope = contextfabric.RequestedScope{TeamIDs: teams}
		interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: terms,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: orgID}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("terms %v teams %v: ResolveSubjects() error = %v", terms, teams, err)
		}
		committed, offered = []string{}, []string{}
		for _, s := range resolution.Committed {
			committed = append(committed, s.Label)
		}
		for _, c := range resolution.Candidates {
			offered = append(offered, c.Subject.Label)
		}
		sort.Strings(committed)
		sort.Strings(offered)
		return committed, offered
	}
	t.Run("an exact name outside the team is committed", func(t *testing.T) {
		if committed, offered := resolve(t, []string{worker.Label}, []string{"team-b"}); !reflect.DeepEqual(committed, []string{worker.Label}) {
			t.Fatalf("committed %v offered %v, want %s committed: the question names it", committed, offered, worker.Label)
		}
	})
	t.Run("an alias held by an in-team repository and an out-of-team project stays ambiguous between both", func(t *testing.T) {
		committed, offered := resolve(t, []string{"invoicing"}, []string{"team-b"})
		if len(committed) != 0 || !reflect.DeepEqual(offered, []string{ops.Label, billing.Label}) {
			t.Fatalf("committed %v offered %v, want none committed and both offered: both are named by the alias, so the team narrowing does not settle it", committed, offered)
		}
	})
	t.Run("a second term with no in-team match keeps its match; an ownerless match is never dropped", func(t *testing.T) {
		committed, offered := resolve(t, []string{"payments", "ledger"}, []string{"team-b"})
		all := append(append([]string{}, committed...), offered...)
		for _, want := range []string{api.Label, legacy.Label, ledger.Label} {
			if !slices.Contains(all, want) {
				t.Fatalf("committed %v offered %v, want %s kept", committed, offered, want)
			}
		}
		if slices.Contains(all, worker.Label) {
			t.Fatalf("committed %v offered %v, want %s left out: its only term has an in-team match", committed, offered, worker.Label)
		}
	})
}
