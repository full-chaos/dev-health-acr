package falkorgraph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12456 ruling 1. The adapter sets the truncation sentinel before any
// authorization (runFulltextQuery has no principal), so rows a restricted caller
// cannot see fill the page and decide whether its census round runs. These
// turns run the real adapter over the real search statement's row contract.

type triggerRow struct {
	id, repo string
	number   int
}

func (r triggerRow) node() *node {
	return &node{Properties: map[string]interface{}{
		propKind: string(contextfabric.SubjectPullRequest), propCanonicalID: r.id, propLabel: fmt.Sprintf("PR #%d", r.number),
		propAuthzRepos: []string{r.repo},
	}}
}

// triggerTurn is one turn of a restricted caller granted one repository.
type triggerTurn struct {
	committed []string
	census    int
	rounds    []string
}

type triggerTracer struct {
	mu     sync.Mutex
	rounds []string
}

func (t *triggerTracer) Trace(event graphrank.ResolutionTraceEvent) {
	if event.Stage == "evidence_round" {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.rounds = append(t.rounds, event.ShadowOutcome)
	}
}

func runTriggerTurn(t *testing.T, inside triggerRow, page []triggerRow) triggerTurn {
	t.Helper()
	return runTriggerTurnFor(t, inside, page, userprincipal.Narrowed(t, "org_1", []string{inside.repo}, []string{inside.repo}), fmt.Sprintf("is pull request %d ready to merge?", inside.number))
}

func runTriggerTurnFor(t *testing.T, inside triggerRow, page []triggerRow, principal storage.Principal, question string) triggerTurn {
	t.Helper()
	nodes := map[string]*node{inside.id: inside.node()}
	rows := make([]row, 0, len(page))
	for _, r := range page {
		n := r.node()
		nodes[r.id] = n
		rows = append(rows, row{"node": n, "score": 1.0})
	}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return rows, nil
		case strings.Contains(cypher, propCanonicalID+":$id"):
			id, _ := params["id"].(string)
			if n, ok := nodes[id]; ok {
				return []row{{"n": n}}, nil
			}
		}
		return nil, nil
	}}
	var turn triggerTurn
	tracer := &triggerTracer{}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true,
		ResolutionTracer: tracer,
		CensusFunc: func(ctx context.Context, _ string, kind graphrank.CensusKind, value string, handleBound bool, _ contextfabric.SubjectKind, _ string, _ bool) (graphrank.CensusOutcome, error) {
			turn.census++
			grant, bound := contextfabric.CensusGrantFrom(ctx)
			outcome := graphrank.CensusOutcome{CensusReadAt: time.Now().UTC(), GrantApplied: bound}
			if kind == contextfabric.SubjectPullRequest && handleBound && value == fmt.Sprint(inside.number) && slices.Contains(grant, inside.repo) {
				outcome.Count, outcome.SatisfierCanonicalID = 1, inside.id
			}
			return outcome, nil
		},
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678",
		Question: question, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{fmt.Sprintf("pull request %d", inside.number)},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, subject := range resolution.Committed {
		turn.committed = append(turn.committed, subject.CanonicalID)
	}
	turn.rounds = tracer.rounds
	return turn
}

// The same inside subject, the same question. Only rows the caller cannot see
// differ, and the turn must not change.
func TestHiddenRowsInAFullPageDoNotDecideARestrictedRound(t *testing.T) {
	t.Parallel()
	inside := triggerRow{id: "pull_request:r-inside:5063", repo: "acme/alpha", number: 5063}
	var hidden []triggerRow
	for i := range 30 {
		hidden = append(hidden, triggerRow{id: fmt.Sprintf("pull_request:r-hidden-%02d:5063", i), repo: "other/secret", number: 5063})
	}
	for name, page := range map[string][]triggerRow{
		"inside first in a full page": append([]triggerRow{inside}, hidden...),
		"inside cut off a full page":  hidden,
		"a part page beside the rows": append([]triggerRow{inside}, hidden[:3]...),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			alone := runTriggerTurn(t, inside, []triggerRow{inside})
			crowded := runTriggerTurn(t, inside, page)
			if !slices.Equal(alone.committed, crowded.committed) || alone.census != crowded.census || !slices.Equal(alone.rounds, crowded.rounds) {
				t.Fatalf("hidden rows changed the turn: alone %+v, beside hidden rows %+v", alone, crowded)
			}
			if len(alone.committed) != 1 || alone.committed[0] != inside.id {
				t.Fatalf("committed = %v, want the inside pull request", alone.committed)
			}
		})
	}
}

// Ruling 1 widens nothing else: an unrestricted caller's round stays on the
// truncation, and a restricted question that binds no handle has no round to run.
func TestOnlyARestrictedBoundHandleLeavesTheTruncationRule(t *testing.T) {
	t.Parallel()
	inside := triggerRow{id: "pull_request:r-inside:5063", repo: "acme/alpha", number: 5063}
	unrestricted := runTriggerTurnFor(t, inside, []triggerRow{inside}, userprincipal.Authenticated(t, "org_1", nil), "is pull request 5063 ready to merge?")
	if unrestricted.census != 0 || len(unrestricted.rounds) != 0 {
		t.Fatalf("unrestricted, no truncation: %+v, want no round", unrestricted)
	}
	principal := userprincipal.Narrowed(t, "org_1", []string{inside.repo}, []string{inside.repo})
	noHandle := runTriggerTurnFor(t, inside, []triggerRow{inside}, principal, "what is ready to merge?")
	if noHandle.census != 0 || len(noHandle.rounds) != 0 {
		t.Fatalf("restricted, no handle: %+v, want no round", noHandle)
	}
}
