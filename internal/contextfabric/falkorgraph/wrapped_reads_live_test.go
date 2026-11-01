package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// wrappedReadLog counts the reads a turn makes under a link reach, each with the
// shape of its statement, and whether a row held a whole node.
type wrappedReadLog struct {
	conn
	mu     sync.Mutex
	shapes map[string]int
	nodes  int
	total  int
}

var wrappedReadDigits = regexp.MustCompile(`\d+`)

func (l *wrappedReadLog) query(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
	rows, err := l.conn.query(ctx, key, cypher, params, readOnly)
	if !readOnly || ctx.Value(linkReachContextKey{}) == nil {
		return rows, err
	}
	shape := wrappedReadDigits.ReplaceAllString(strings.Join(strings.Fields(cypher), " "), "N")
	if len(shape) > 110 {
		shape = shape[:110]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.shapes[shape]++
	l.total++
	for _, r := range rows {
		held := false
		for _, value := range r {
			if _, ok := value.(*node); ok {
				held = true
			}
		}
		if held {
			l.nodes++
			break
		}
	}
	return rows, err
}

func (l *wrappedReadLog) reset() (int, int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := make([]string, 0, len(l.shapes))
	for shape, count := range l.shapes {
		lines = append(lines, fmt.Sprintf("%3d %s", count, shape))
	}
	sort.Strings(lines)
	total, nodes := l.total, l.nodes
	l.shapes, l.total, l.nodes = map[string]int{}, 0, 0
	return total, nodes, lines
}

// TestLiveWrappedReadsOfATurn counts what one real Engine turn reads on the
// graph under a link reach, on a repository, a team and a work-item question.
func TestLiveWrappedReadsOfATurn(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	log := &wrappedReadLog{conn: adapter.api, shapes: map[string]int{}}
	adapter.api = log

	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	routeOrg := "live-wrapped-reads-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), routeOrg) })
	fixture := newLiveRouteFixture(routeOrg, time.Now().UTC())
	for _, batch := range []contextfabric.ProjectionBatch{fixture.entities, fixture.relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	teamOrg := "live-wrapped-reads-team-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), teamOrg) })
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	entities, relationships := newLiveMemberFixture(teamOrg, time.Now().UTC(), func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		add(team, contextfabric.AuthorizationScope{TeamIDs: []string{"platform"}, RepositorySlugs: []string{"acme/api"}}, "")
		r := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/api", Label: "acme/api"}
		add(r, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}, "")
		relate("api_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, r, team, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}, TeamIDs: []string{"platform"}}, "")
	}, nil)
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}

	turn := func(t *testing.T, name string, interpreter contextfabric.QuestionInterpreter, principal storage.Principal, question string) {
		t.Helper()
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: interpreter, Graph: adapter, Facts: emptyFactReader{},
			Synthesizer: synthesizeFunc(func(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, errors.New("no model in this count")
			}),
			Results:     discardingResultStore{}, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_12456001" }})
		if err != nil {
			t.Fatal(err)
		}
		runCtx, _ := contextfabric.WithSynthesisInputCollector(ctx)
		log.reset()
		result, err := engine.Investigate(runCtx, principal, contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12456001",
			Question: question,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
		// A turn with no model ends at synthesis, after its graph reads; the count
		// is what this test is for.
		total, nodes, shapes := log.reset()
		t.Logf("%s: status=%s err=%v committed=%d wrapped reads=%d, with a whole node=%d\n%s", name, result.Status, err, len(result.SubjectResolution.Committed), total, nodes, strings.Join(shapes, "\n"))
	}
	open := func(term string) contextfabric.QuestionInterpreter {
		return fixedInterpreter{interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{term},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}}
	}
	billing := userprincipal.Narrowed(t, routeOrg, []string{"acme/billing"}, []string{"acme/billing"})
	turn(t, "repository", open("acme/billing"), billing, "how is acme/billing doing?")
	turn(t, "work item", open("Fix invoice rounding"), billing, "why is Fix invoice rounding blocked?")
	turn(t, "team", cohortQuestionInterpreter{member: contextfabric.SubjectRepository}, userprincipal.Narrowed(t, teamOrg, []string{"acme/api"}, []string{"acme/api"}), "what does team Platform own")
}
