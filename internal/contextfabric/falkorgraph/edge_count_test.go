package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestEdgeCountCypherPerRelationAndArm(t *testing.T) {
	t.Parallel()
	project := contractsv1.ContextFabricRelationshipBelongsToProject
	for name, tc := range map[string]struct {
		key     contextfabric.EdgeCountKey
		want    []string
		notWant []string
		wantErr string
	}{
		"relation without arms": {
			key:     contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest},
			want:    []string{"MATCH (a:" + labelSubject + " {" + propOrgID + ":$org})-[r:" + labelRelation + "]->()", "r." + propRelationType + " = $rel", "count(DISTINCT r." + propRelationshipID + ")"},
			notWant: []string{propValidFromNs},
		},
		"column arm has no validity start": {
			key:  contextfabric.EdgeCountKey{Relation: project, Arm: contextfabric.EdgeArmWorkItemColumn},
			want: []string{"r." + propValidFromNs + " IS NULL"},
		},
		"transition arm has a validity start": {
			key:     contextfabric.EdgeCountKey{Relation: project, Arm: contextfabric.EdgeArmTransition},
			want:    []string{"r." + propValidFromNs + " IS NOT NULL"},
			notWant: []string{propValidFromNs + " IS NULL"},
		},
		"repository arm filters on the source node kind": {
			key:  contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "incident"},
			want: []string{"a." + propKind + " = $arm"},
		},
		"link arm filters on the stored tier": {
			key:  contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: contextfabric.EdgeArmLinkNative},
			want: []string{"r." + propPropertyPrefix + "link_provenance = $arm"},
		},
		"owned by team arm filters on the source node kind": {
			key:  contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: "project"},
			want: []string{"a." + propKind + " = $arm"},
		},
		"an unknown owned by team arm is refused": {key: contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: "incident"}, wantErr: `has no arm "incident"`},
		"an unknown repository arm is refused":    {key: contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "mystery"}, wantErr: `has no arm "mystery"`},
		"an unknown link tier is refused":         {key: contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: "mystery"}, wantErr: `has no arm "mystery"`},
		"unknown arm is refused":                  {key: contextfabric.EdgeCountKey{Relation: project, Arm: "mystery"}, wantErr: `has no arm "mystery"`},
		"an arm on a relation without arms":       {key: contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest, Arm: contextfabric.EdgeArmTransition}, wantErr: "has no arm"},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cypher, err := edgeCountCypher(tc.key)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error %q, got %q, %v", tc.wantErr, cypher, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(cypher, want) {
					t.Fatalf("cypher lacks %q:\n%s", want, cypher)
				}
			}
			for _, banned := range tc.notWant {
				if strings.Contains(cypher, banned) {
					t.Fatalf("cypher must not contain %q:\n%s", banned, cypher)
				}
			}
		})
	}
}

func TestCountEdgesBindsOrgAndNormalizedRelationAndReadsOneScalar(t *testing.T) {
	t.Parallel()
	var params map[string]interface{}
	var cypher string
	var readOnly bool
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, p map[string]interface{}, ro bool) ([]row, error) {
		if strings.Contains(q, "relation_type") {
			cypher, params, readOnly = q, p, ro
		}
		return []row{{"total": int64(1997)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	got, err := adapter.CountEdges(context.Background(), "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition})
	if err != nil || got != 1997 {
		t.Fatalf("CountEdges = %d, %v; want 1997", got, err)
	}
	if _, armed := params["arm"]; armed {
		t.Fatalf("an arm-less count binds no arm: %v", params)
	}
	if !readOnly || params["org"] != "org-1" || params["rel"] == "" || strings.Contains(cypher, "org-1") || strings.Contains(cypher, "BELONGS_TO_PROJECT") {
		t.Fatalf("values are bound, never interpolated; read only: ro=%v params=%v\n%s", readOnly, params, cypher)
	}
}

func TestCountEdgesFailsLoudlyOnAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, rows := range map[string][]row{
		"no row":       {},
		"two rows":     {{"total": int64(1)}, {"total": int64(2)}},
		"non numeric":  {{"total": "many"}},
		"missing cell": {{"other": int64(1)}},
	} {
		rows := rows
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
				if strings.Contains(q, "relation_type") {
					return rows, nil
				}
				return []row{{"total": int64(0)}}, nil
			}}
			got, err := newFakeAdapter(t, fake).CountEdges(context.Background(), "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest})
			if err == nil || got != 0 {
				t.Fatalf("an unreadable answer is an error, never a zero: %d, %v", got, err)
			}
		})
	}
	if _, err := newFakeAdapter(t, &fakeConn{}).CountEdges(context.Background(), " ", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject}); err == nil {
		t.Fatal("a blank organization is refused")
	}
}

// The count read carries its TIMEOUT policy; the attempt's argument is what is
// left of a short deadline less the margin, or the cap.
func TestCountEdgesSendsAServerTimeoutInsideTheCallersBudget(t *testing.T) {
	t.Parallel()
	key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipRelatedTo}
	for name, tc := range map[string]struct {
		deadline time.Duration
		min, max int64
	}{
		"no deadline sends the cap":           {0, edgeCountTimeoutCap.Milliseconds(), edgeCountTimeoutCap.Milliseconds()},
		"a long deadline sends the cap":       {time.Minute, edgeCountTimeoutCap.Milliseconds(), edgeCountTimeoutCap.Milliseconds()},
		"a short deadline sends what is left": {2 * time.Second, 1000, (2*time.Second - edgeCountTimeoutMargin).Milliseconds()},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var sent int64 = -1
			fake := &fakeConn{queryFunc: func(ctx context.Context, g, q string, _ map[string]interface{}, ro bool) ([]row, error) {
				if strings.Contains(q, "relation_type") {
					args, err := queryArgs(ctx, time.Now(), "GRAPH.RO_QUERY", g, q, ro)
					if err == nil && len(args) == 6 {
						sent, _ = args[5].(int64)
					}
				}
				return []row{{"total": int64(3)}}, nil
			}}
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			got, err := newFakeAdapter(t, fake).CountEdges(ctx, "org-1", key)
			if err != nil || got != 3 || sent < tc.min || sent > tc.max {
				t.Fatalf("CountEdges = %d, %v; sent TIMEOUT %d ms, want %d..%d", got, err, sent, tc.min, tc.max)
			}
		})
	}
}

// A caller that has ended is refused with its end (canceled) before any budget
// question; a live caller with no time left beyond the margin is refused as
// over budget, and no read is sent.
func TestCountEdgesAdmitsAReadOnlyInsideALiveBudget(t *testing.T) {
	t.Parallel()
	now := time.Now()
	live, cancelLive := context.WithDeadline(context.Background(), now.Add(time.Hour))
	defer cancelLive()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx   context.Context
		at    time.Time
		class string
	}{
		"a deadline already passed, the context not yet marked": {live, now.Add(2 * time.Hour), "canceled"},
		"a deadline exactly now":                                {live, now.Add(time.Hour), "canceled"},
		"a cancelled caller":                                    {cancelled, now, "canceled"},
		"a live caller with the margin left":                    {live, now.Add(time.Hour - edgeCountTimeoutMargin), "query_budget_exceeded"},
		"a live caller with under the margin left":              {live, now.Add(time.Hour - edgeCountTimeoutMargin/2), "query_budget_exceeded"},
		"a live caller with time left":                          {live, now.Add(time.Hour - time.Second), ""},
	} {
		err := edgeCountAdmit(tc.ctx, tc.at)
		if got := projectionrun.ClassifyFailure(err); got != tc.class {
			t.Errorf("%s: edgeCountAdmit = %v (class %q), want class %q", name, err, got, tc.class)
		}
	}
	sent := false
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(q, "relation_type") {
			sent = true
		}
		return []row{{"total": int64(3)}}, nil
	}}
	short, cancelShort := context.WithTimeout(context.Background(), edgeCountTimeoutMargin/2)
	defer cancelShort()
	_, err := newFakeAdapter(t, fake).CountEdges(short, "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipRelatedTo})
	if sent || !errors.Is(err, contextfabric.ErrQueryBudgetExceeded) || projectionrun.ClassifyFailure(err) != "query_budget_exceeded" {
		t.Fatalf("under the margin: sent %v, err %v (class %q); want no read and query_budget_exceeded", sent, err, projectionrun.ClassifyFailure(err))
	}
}

// Only FalkorDB's own "Query timed out" under a live caller is a count that did
// not fit (query_budget_exceeded). The typed error reads exactly as
// context.DeadlineExceeded for every other caller. A caller whose context ended
// stays canceled, and a deadline from a lower layer (a dial) keeps its class.
func TestCountEdgesNamesOnlyTheServerTimeoutAQueryBudget(t *testing.T) {
	t.Parallel()
	server := classifyFalkorError("query context graph", errors.New("Query timed out"))
	if server.Error() != "query context graph: context deadline exceeded" || !errors.Is(server, context.DeadlineExceeded) {
		t.Fatalf("the server timeout must read as before: %q, Is(DeadlineExceeded)=%v", server.Error(), errors.Is(server, context.DeadlineExceeded))
	}
	if flat := safeDependencyError("op", server); flat.Error() != server.Error() || !errors.Is(flat, context.DeadlineExceeded) || projectionrun.ClassifyFailure(flat) != "canceled" {
		t.Fatalf("an old caller of the server timeout must see what it saw before: %q, class %q", flat.Error(), projectionrun.ClassifyFailure(flat))
	}
	key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipRelatedTo}
	failing := func(err error) *fakeConn {
		return &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
			if strings.Contains(q, "relation_type") {
				return nil, err
			}
			return []row{{"total": int64(0)}}, nil
		}}
	}
	live, cancelLive := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelLive()
	ended, endCancel := context.WithCancel(context.Background())
	endCancel()
	for name, tc := range map[string]struct {
		ctx   context.Context
		err   error
		class string
	}{
		"the server timeout under a live caller":     {live, server, "query_budget_exceeded"},
		"the server timeout after the caller ended":  {ended, server, "canceled"},
		"a dial deadline under a live caller":        {live, fmt.Errorf("dial tcp: %w", context.DeadlineExceeded), "canceled"},
		"a bare deadline under a live caller":        {live, context.DeadlineExceeded, "canceled"},
		"a dependency failure under a live caller":   {live, classifyFalkorError("query context graph", errors.New("EOF")), "dependency_unavailable"},
		"a spent budget after a retry, caller live":  {live, errServerBudgetSpent, "query_budget_exceeded"},
		"a spent budget after a retry, caller ended": {ended, errServerBudgetSpent, "canceled"},
	} {
		_, err := newFakeAdapter(t, failing(tc.err)).CountEdges(tc.ctx, "org-1", key)
		if got := projectionrun.ClassifyFailure(err); got != tc.class {
			t.Errorf("%s: err %v, class %q; want %q", name, err, got, tc.class)
		}
	}
}

// Only a read whose context carries withServerTimeout sends the TIMEOUT
// argument: every other command is byte-identical to the one sent before, so
// other reads keep the server's default bound. The argument is computed at the
// attempt, from what is left of the deadline.
func TestQueryArgsAddATimeoutOnlyToAReadThatCarriesOne(t *testing.T) {
	t.Parallel()
	now := time.Now()
	plain := context.Background()
	timed := withServerTimeout(plain, 5*time.Second, 250*time.Millisecond)
	short, cancel := context.WithDeadline(timed, now.Add(2*time.Second))
	defer cancel()
	for name, tc := range map[string]struct {
		ctx      context.Context
		at       time.Time
		command  string
		readOnly bool
		want     []interface{}
		err      error
	}{
		"a read with no value":                 {plain, now, "GRAPH.RO_QUERY", true, []interface{}{"GRAPH.RO_QUERY", "g", "MATCH (n) RETURN n", "--compact"}, nil},
		"a write with no value":                {plain, now, "GRAPH.QUERY", false, []interface{}{"GRAPH.QUERY", "g", "MATCH (n) RETURN n", "--compact"}, nil},
		"a write never gets a value":           {short, now, "GRAPH.QUERY", false, []interface{}{"GRAPH.QUERY", "g", "MATCH (n) RETURN n", "--compact"}, nil},
		"a read with no deadline gets the cap": {timed, now, "GRAPH.RO_QUERY", true, []interface{}{"GRAPH.RO_QUERY", "g", "MATCH (n) RETURN n", "--compact", "TIMEOUT", int64(5000)}, nil},
		"a read gets what is left":             {short, now, "GRAPH.RO_QUERY", true, []interface{}{"GRAPH.RO_QUERY", "g", "MATCH (n) RETURN n", "--compact", "TIMEOUT", int64(1750)}, nil},
		"a later attempt gets less":            {short, now.Add(500 * time.Millisecond), "GRAPH.RO_QUERY", true, []interface{}{"GRAPH.RO_QUERY", "g", "MATCH (n) RETURN n", "--compact", "TIMEOUT", int64(1250)}, nil},
		"under 1 ms left sends 1":              {short, now.Add(2*time.Second - 250*time.Millisecond - time.Microsecond), "GRAPH.RO_QUERY", true, []interface{}{"GRAPH.RO_QUERY", "g", "MATCH (n) RETURN n", "--compact", "TIMEOUT", int64(1)}, nil},
		"inside the margin is not sent":        {short, now.Add(2*time.Second - 250*time.Millisecond), "GRAPH.RO_QUERY", true, nil, errServerBudgetSpent},
	} {
		got, err := queryArgs(tc.ctx, tc.at, tc.command, "g", "MATCH (n) RETURN n", tc.readOnly)
		if !errors.Is(err, tc.err) || err == nil && tc.err != nil || fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", tc.want) {
			t.Errorf("%s: queryArgs = %#v, %v; want %#v, %v", name, got, err, tc.want, tc.err)
		}
	}
}
