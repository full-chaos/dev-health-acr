package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5654: a discovered cohort whose frame declares a servable member kind
// the exact-name census does not fetch reaches that kind's population with no
// matching term. Every DiscoverContext pin below drives the reader against a
// fake store that answers a `$kinds` query ONLY for the kinds the query binds,
// only up to the LIMIT the query itself states, and in DESCENDING canonical-id
// order. The fake does not apply ORDER BY, so the order the reader imposes is
// observable; TestLiveKindCensus in kind_census_live_test.go runs the query on
// a real FalkorDB, where ORDER BY and LIMIT are applied by the engine.

// kindCensusStore is a fake graph holding a population per subject kind.
type kindCensusStore struct {
	population map[string]int
	// deniedKinds marks kinds whose rows no repository-scoped principal may
	// read; every other row carries the wildcard scope.
	deniedKinds map[string]bool
	fulltext    []row
	kindQueries []kindCensusQuery
	// malformedFor and failFor act only on a query that binds exactly that
	// one kind: the first appends a row that is not a node, the second fails
	// the query.
	malformedFor string
	failFor      string
}

type kindCensusQuery struct {
	cypher   string
	kinds    []string
	params   map[string]interface{}
	readOnly bool
}

var kindCensusLimitPattern = regexp.MustCompile(`LIMIT (\d+)\s*$`)

func kindCensusMemberID(kind string, i int) string { return fmt.Sprintf("%s_%05d", kind, i) }

func kindCensusScope(r row, key string, denied bool) row {
	if denied {
		r[key].(*node).Properties["authorization_repositories"] = []string{"repo-denied"}
	} else {
		r[key].(*node).Properties["authorization_repositories"] = "*"
	}
	return r
}

func (s *kindCensusStore) conn(t *testing.T) *fakeConn {
	t.Helper()
	return &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return s.fulltext, nil
		case strings.Contains(cypher, "$kinds"):
			kinds, ok := params["kinds"].([]string)
			if !ok {
				t.Fatalf("a $kinds query bound kinds of type %T, want []string", params["kinds"])
			}
			s.kindQueries = append(s.kindQueries, kindCensusQuery{cypher: cypher, kinds: append([]string(nil), kinds...), params: params, readOnly: readOnly})
			match := kindCensusLimitPattern.FindStringSubmatch(cypher)
			if match == nil {
				t.Fatalf("a $kinds query states no LIMIT: %s", cypher)
			}
			limit, _ := strconv.Atoi(match[1])
			single := len(kinds) == 1
			if single && kinds[0] == s.failFor {
				return nil, errKindCensusStore
			}
			rows := make([]row, 0)
			if single && kinds[0] == s.malformedFor {
				rows = append(rows, row{"n": "not a node"})
			}
			for _, kind := range kinds {
				for i := s.population[kind] - 1; i >= 0; i-- {
					if len(rows) >= limit {
						return rows, nil
					}
					r := fakeSubjectNodeRow(kind, kindCensusMemberID(kind, i), fmt.Sprintf("%s %d", kind, i))
					rows = append(rows, kindCensusScope(r, "n", s.deniedKinds[kind]))
				}
			}
			return rows, nil
		default:
			return nil, nil
		}
	}}
}

var errKindCensusStore = errors.New("kind census store unavailable")

// singleKindQueries returns the `$kinds` queries that bound exactly [kind].
func (s *kindCensusStore) singleKindQueries(kind contextfabric.SubjectKind) []kindCensusQuery {
	var out []kindCensusQuery
	for _, q := range s.kindQueries {
		if len(q.kinds) == 1 && q.kinds[0] == string(kind) {
			out = append(out, q)
		}
	}
	return out
}

// uncensusedServableKinds is every kind the seam serves that the exact-name
// census does not fetch, read from the two authorities rather than listed.
func uncensusedServableKinds(t *testing.T) []contextfabric.SubjectKind {
	t.Helper()
	censused := map[string]bool{}
	for _, kind := range exactNameKinds {
		censused[kind] = true
	}
	var out []contextfabric.SubjectKind
	for _, kind := range contextfabric.ServableCohortKindsForAudit() {
		if !censused[string(kind)] {
			out = append(out, kind)
		}
	}
	if len(out) == 0 {
		t.Fatal("every servable kind is in the exact-name census, so no kind exercises the kind-scoped census")
	}
	return out
}

// unservablePublishedKind is a published subject kind the seam refuses.
func unservablePublishedKind(t *testing.T) contextfabric.SubjectKind {
	t.Helper()
	servable := map[contextfabric.SubjectKind]bool{}
	for _, kind := range contextfabric.ServableCohortKindsForAudit() {
		servable[kind] = true
	}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		if !servable[contextfabric.SubjectKind(kind)] {
			return contextfabric.SubjectKind(kind)
		}
	}
	t.Fatal("every published subject kind is servable, so no fixture can declare an unservable kind")
	return ""
}

func kindCensusSurveyRequest(kind contextfabric.SubjectKind, maxMembers int) contextfabric.GraphDiscoveryRequest {
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Frame.SubjectExpression.Discovered.MemberKind = kind
	request.Request.Options.MaxCohortMembers = maxMembers
	return request
}

func memberIDs(cohort *contextfabric.Cohort) []string {
	if cohort == nil {
		return nil
	}
	ids := make([]string, 0, len(cohort.Members))
	for _, m := range cohort.Members {
		ids = append(ids, m.Subject.CanonicalID)
	}
	return ids
}

// TestDiscoverContextTermFreeSurveyReachesTheDeclaredKindsPopulation is the
// user path: no row matches a term, the frame declares a servable kind outside
// the exact-name census, and the cohort is that kind's population.
func TestDiscoverContextTermFreeSurveyReachesTheDeclaredKindsPopulation(t *testing.T) {
	t.Parallel()
	for _, kind := range uncensusedServableKinds(t) {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			store := &kindCensusStore{population: map[string]int{string(kind): 3, "team": 2, "repository": 1}}
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)

			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, kindCensusSurveyRequest(kind, 2))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if result.Cohort == nil {
				t.Fatalf("Cohort = nil for a term-free %s survey; kind queries %+v", kind, store.kindQueries)
			}
			if result.Cohort.Kind != kind {
				t.Errorf("Cohort.Kind = %q, want %q", result.Cohort.Kind, kind)
			}
			want := []string{kindCensusMemberID(string(kind), 0), kindCensusMemberID(string(kind), 1)}
			if got := memberIDs(result.Cohort); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("members = %v, want %v: the population in subject-key order, cut at the member cap", got, want)
			}
			if result.CohortPopulation != 3 {
				t.Errorf("CohortPopulation = %d, want 3: every fetched member is counted past the member cap", result.CohortPopulation)
			}

			queries := store.singleKindQueries(kind)
			if len(queries) != 1 {
				t.Fatalf("%d queries bound exactly [%s], want 1 (all $kinds queries: %+v)", len(queries), kind, store.kindQueries)
			}
			q := queries[0]
			if !q.readOnly {
				t.Error("the kind census query must be read-only")
			}
			if org, _ := q.params["org"].(string); org != "org-1" {
				t.Errorf("kind census bound org %q, want %q", org, "org-1")
			}
			if want := fmt.Sprintf("ORDER BY n.%s LIMIT %d", propCanonicalID, exactNameCandidateQueryLimit+1); !strings.HasSuffix(q.cypher, want) {
				t.Errorf("kind census cypher %q does not end with %q", q.cypher, want)
			}
			for _, other := range store.kindQueries {
				if len(other.kinds) > 1 {
					for _, bound := range other.kinds {
						if bound == string(kind) {
							t.Errorf("a shared census bound %v, which includes %q -- the kind must be fetched on its own", other.kinds, kind)
						}
					}
				}
			}

			if len(telemetry.cohortKindCensuses) != 1 {
				t.Fatalf("kind census lines = %+v, want exactly 1", telemetry.cohortKindCensuses)
			}
			line := telemetry.cohortKindCensuses[0]
			if line.decision != CohortKindCensusRan || line.memberKind != kind || strings.Join(line.kinds, ",") != string(kind) ||
				line.poolSize != 3 || line.poolBound != exactNameCandidateQueryLimit || line.truncated {
				t.Errorf("kind census line = %+v, want decision=%q member_kind=%q kinds=[%s] pool_size=3 pool_bound=%d truncated=false",
					line, CohortKindCensusRan, kind, kind, exactNameCandidateQueryLimit)
			}
			if len(telemetry.cohortKindBases) != 1 || !telemetry.cohortKindBases[0].discovered || telemetry.cohortKindBases[0].poolTruncation != CohortPoolTruncationNone {
				t.Errorf("cohort kind basis = %+v, want one discovered line with pool_truncation=%q", telemetry.cohortKindBases, CohortPoolTruncationNone)
			}

			wantFacts := graphrank.CohortFactRequirements(kind)
			if len(wantFacts) == 0 {
				t.Fatalf("no cohort fact requirement is declared for %q, so the cohort cannot be answered", kind)
			}
			for _, fact := range wantFacts {
				found := false
				for _, requirement := range result.FactRequirements {
					if requirement.Kind == fact {
						found = true
					}
				}
				if !found {
					t.Errorf("fact requirement %q for the %s cohort is absent from %+v", fact, kind, result.FactRequirements)
				}
			}
		})
	}
}

// TestDiscoverContextKindCensusPastItsBoundTruncatesTheCohort pins the bound
// and its boundary: a population one row past the bound is cut and says so, a
// population exactly at the bound is whole.
func TestDiscoverContextKindCensusPastItsBoundTruncatesTheCohort(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	for _, tc := range []struct {
		name       string
		population int
		truncated  bool
	}{
		{name: "one_past_the_bound", population: exactNameCandidateQueryLimit + 1, truncated: true},
		{name: "exactly_the_bound", population: exactNameCandidateQueryLimit, truncated: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &kindCensusStore{population: map[string]int{string(kind): tc.population, "team": 1}}
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)
			// The member cap sits above the bound, so only the pool can make
			// this cohort incomplete.
			request := kindCensusSurveyRequest(kind, exactNameCandidateQueryLimit+2)

			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if len(telemetry.cohortKindCensuses) != 1 {
				t.Fatalf("kind census lines = %+v, want exactly 1", telemetry.cohortKindCensuses)
			}
			line := telemetry.cohortKindCensuses[0]
			if line.truncated != tc.truncated || line.poolSize != exactNameCandidateQueryLimit {
				t.Errorf("kind census line truncated=%v pool_size=%d, want truncated=%v pool_size=%d", line.truncated, line.poolSize, tc.truncated, exactNameCandidateQueryLimit)
			}
			if result.Cohort == nil || len(result.Cohort.Members) != exactNameCandidateQueryLimit {
				t.Fatalf("cohort members = %d, want %d", len(memberIDs(result.Cohort)), exactNameCandidateQueryLimit)
			}
			basis := telemetry.cohortKindBases[0]
			wantBasis, wantArms := CohortPoolTruncationNone, ""
			if tc.truncated {
				wantBasis, wantArms = CohortPoolTruncationTruncated, string(CohortPoolTruncationArmKindCensus)
			}
			if basis.poolTruncation != wantBasis || formatCohortPoolTruncationArms(basis.poolTruncationArms) != wantArms {
				t.Errorf("pool truncation = %q arms %q, want %q arms %q", basis.poolTruncation, formatCohortPoolTruncationArms(basis.poolTruncationArms), wantBasis, wantArms)
			}
			if result.Cohort.Truncated != tc.truncated || result.Cohort.Complete == tc.truncated {
				t.Errorf("Complete=%v Truncated=%v, want Truncated=%v", result.Cohort.Complete, result.Cohort.Truncated, tc.truncated)
			}
		})
	}
}

// kindCensusClippedLexicalRows is a full-text result one row past the collect
// budget whose first two rows are members of kind that the store also holds.
func kindCensusClippedLexicalRows(kind contextfabric.SubjectKind) []row {
	rows := make([]row, 0, poolTruncationFulltextCollectLimit+1)
	for i := 0; i < 2; i++ {
		r := fulltextRow(string(kind), kindCensusMemberID(string(kind), i), fmt.Sprintf("%s %d", kind, i), "teams struggling", nil)
		rows = append(rows, kindCensusScope(r, "node", false))
	}
	for i := 0; len(rows) < poolTruncationFulltextCollectLimit+1; i++ {
		r := fulltextRow("repository", fmt.Sprintf("lexical_repo_%d", i), fmt.Sprintf("Repo %d", i), "teams struggling", nil)
		rows = append(rows, kindCensusScope(r, "node", false))
	}
	return rows
}

// TestDiscoverContextKindCensusCoversAClippedLexicalArm: a completed,
// non-empty kind census holds what the clipped lexical arm dropped, so the
// cohort keeps its completeness claim. The empty-census control is below.
func TestDiscoverContextKindCensusCoversAClippedLexicalArm(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	for _, tc := range []struct {
		name        string
		population  int
		wantBasis   CohortPoolTruncationBasis
		wantMembers int
	}{
		{name: "census_holds_the_population", population: 3, wantBasis: CohortPoolTruncationCoveredByCensus, wantMembers: 3},
		{name: "census_is_empty", population: 0, wantBasis: CohortPoolTruncationTruncated, wantMembers: 2},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &kindCensusStore{population: map[string]int{string(kind): tc.population, "team": 1}, fulltext: kindCensusClippedLexicalRows(kind)}
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)

			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, kindCensusSurveyRequest(kind, 50))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if len(telemetry.cohortKindCensuses) != 1 || telemetry.cohortKindCensuses[0].decision != CohortKindCensusRan || telemetry.cohortKindCensuses[0].poolSize != tc.population {
				t.Fatalf("kind census lines = %+v, want one %q with pool_size %d", telemetry.cohortKindCensuses, CohortKindCensusRan, tc.population)
			}
			basis := telemetry.cohortKindBases[0]
			if got := formatCohortPoolTruncationArms(basis.poolTruncationArms); got != string(CohortPoolTruncationArmFulltext) {
				t.Fatalf("cut arms = %q, want %q -- the lexical arm must be the only cut arm", got, CohortPoolTruncationArmFulltext)
			}
			if basis.poolTruncation != tc.wantBasis {
				t.Errorf("pool truncation = %q, want %q", basis.poolTruncation, tc.wantBasis)
			}
			if result.Cohort == nil || len(result.Cohort.Members) != tc.wantMembers {
				t.Fatalf("members = %v, want %d", memberIDs(result.Cohort), tc.wantMembers)
			}
			wantTruncated := tc.wantBasis == CohortPoolTruncationTruncated
			if result.Cohort.Truncated != wantTruncated || result.Cohort.Complete == wantTruncated {
				t.Errorf("Complete=%v Truncated=%v, want Truncated=%v", result.Cohort.Complete, result.Cohort.Truncated, wantTruncated)
			}
		})
	}
}

// TestKindCensusDecisionIsReachedThroughDiscoverContext drives every decision
// through the reader and checks the fetch runs exactly when the decision says.
func TestKindCensusDecisionIsReachedThroughDiscoverContext(t *testing.T) {
	t.Parallel()
	uncensused := uncensusedServableKinds(t)[0]
	censused := contextfabric.SubjectKind(exactNameKinds[0])
	unservable := unservablePublishedKind(t)
	cases := []struct {
		name       string
		build      func() contextfabric.GraphDiscoveryRequest
		decision   CohortKindCensusDecision
		memberKind contextfabric.SubjectKind
		fetched    contextfabric.SubjectKind
	}{
		{
			name:     "uncensused_servable_kind",
			build:    func() contextfabric.GraphDiscoveryRequest { return kindCensusSurveyRequest(uncensused, 10) },
			decision: CohortKindCensusRan, memberKind: uncensused, fetched: uncensused,
		},
		{
			name:     "kind_the_exact_name_census_fetches",
			build:    func() contextfabric.GraphDiscoveryRequest { return kindCensusSurveyRequest(censused, 10) },
			decision: CohortKindCensusKindInExactNameCensus, memberKind: censused, fetched: "",
		},
		{
			name:     "unservable_kind",
			build:    func() contextfabric.GraphDiscoveryRequest { return kindCensusSurveyRequest(unservable, 10) },
			decision: CohortKindCensusNoServableMemberKind, memberKind: "", fetched: "",
		},
		{
			name: "named_members_anchor_resolved",
			build: func() contextfabric.GraphDiscoveryRequest {
				request := cohortDiscoveryRequestWithScopeAnchor(contextfabric.ShapeExplicitCohort, true)
				request.Frame.SubjectExpression.Scoped.MemberKind = uncensused
				return request
			},
			decision: CohortKindCensusNotAdmitted, memberKind: uncensused, fetched: "",
		},
		{
			name: "subject_already_committed",
			build: func() contextfabric.GraphDiscoveryRequest {
				request := kindCensusSurveyRequest(uncensused, 10)
				request.Resolution.Committed = []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository_committed", Label: "committed"}}
				return request
			},
			decision: CohortKindCensusNotAdmitted, memberKind: uncensused, fetched: "",
		},
		{
			name: "frame_absent",
			build: func() contextfabric.GraphDiscoveryRequest {
				request := kindCensusSurveyRequest(uncensused, 10)
				request.Frame = nil
				return request
			},
			decision: CohortKindCensusNotAdmitted, memberKind: "", fetched: "",
		},
	}
	seen := map[CohortKindCensusDecision]bool{}
	for _, tc := range cases {
		store := &kindCensusStore{population: map[string]int{string(uncensused): 2, string(censused): 2, string(unservable): 2}}
		telemetry := &recordingTelemetry{}
		adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)
		if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, tc.build()); err != nil {
			t.Fatalf("%s: DiscoverContext() error = %v", tc.name, err)
		}
		if len(telemetry.cohortKindCensuses) != 1 {
			t.Fatalf("%s: kind census lines = %+v, want exactly 1", tc.name, telemetry.cohortKindCensuses)
		}
		line := telemetry.cohortKindCensuses[0]
		if line.decision != tc.decision || line.memberKind != tc.memberKind {
			t.Errorf("%s: decision=%q member_kind=%q, want %q/%q", tc.name, line.decision, line.memberKind, tc.decision, tc.memberKind)
		}
		for _, kind := range []contextfabric.SubjectKind{uncensused, censused, unservable} {
			got := len(store.singleKindQueries(kind))
			want := 0
			if kind == tc.fetched {
				want = 1
			}
			if got != want {
				t.Errorf("%s: %d single-kind queries for %q, want %d", tc.name, got, kind, want)
			}
		}
		seen[line.decision] = true
	}
	for _, decision := range CohortKindCensusDecisionVocabulary() {
		if !seen[decision] {
			t.Errorf("decision %q is never reached through DiscoverContext", decision)
		}
	}
}

// TestKindCensusDecisionFollowsTheSeamAndTheExactNameCensus quantifies the
// pure decision over every servable kind, reading the census's kind list
// directly rather than the predicate the decision calls.
func TestKindCensusDecisionFollowsTheSeamAndTheExactNameCensus(t *testing.T) {
	t.Parallel()
	censused := map[string]bool{}
	for _, kind := range exactNameKinds {
		censused[kind] = true
	}
	servable := contextfabric.ServableCohortKindsForAudit()
	if len(servable) == 0 {
		t.Fatal("no servable kind, so nothing below can fail")
	}
	for _, kind := range servable {
		want := CohortKindCensusRan
		if censused[string(kind)] {
			want = CohortKindCensusKindInExactNameCensus
		}
		if got := cohortKindCensusDecision(true, kind); got != want {
			t.Errorf("cohortKindCensusDecision(true, %q) = %q, want %q", kind, got, want)
		}
		if got := cohortKindCensusDecision(false, kind); got != CohortKindCensusNotAdmitted {
			t.Errorf("cohortKindCensusDecision(false, %q) = %q, want %q", kind, got, CohortKindCensusNotAdmitted)
		}
	}
	if got := cohortKindCensusDecision(true, ""); got != CohortKindCensusNoServableMemberKind {
		t.Errorf("cohortKindCensusDecision(true, \"\") = %q, want %q", got, CohortKindCensusNoServableMemberKind)
	}
}

// TestDiscoverContextKindCensusBindsTheTemporalWindow: on a historical axis
// the kind census reads only subjects valid at the requested time, the same
// predicate every other read in the call applies.
func TestDiscoverContextKindCensusBindsTheTemporalWindow(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{population: map[string]int{string(kind): 2}}
	adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})
	request := kindCensusSurveyRequest(kind, 10)
	asOf := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	request.Interpretation.TimeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}

	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	queries := store.singleKindQueries(kind)
	if len(queries) != 1 {
		t.Fatalf("%d single-kind queries, want 1", len(queries))
	}
	filter := newTemporalFilter(request.Interpretation.TimeContext)
	if !strings.Contains(queries[0].cypher, filter.predicate("n")) {
		t.Errorf("kind census cypher %q lacks the temporal predicate %q", queries[0].cypher, filter.predicate("n"))
	}
	if queries[0].params[temporalParamStart] != filter.startNs || queries[0].params[temporalParamEnd] != filter.endNs {
		t.Errorf("kind census params %v do not bind the window %d..%d", queries[0].params, filter.startNs, filter.endNs)
	}
}

// TestDiscoverContextWhollyDeniedClaimNeedsTheCohortKindsOwnCensus pins the
// "every member was denied" disclosure to a census of the cohort's own kind
// that was not cut.
func TestDiscoverContextWhollyDeniedClaimNeedsTheCohortKindsOwnCensus(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	for _, tc := range []struct {
		name               string
		kindPopulation     int
		teamPopulation     int
		wantDeniedReported int
	}{
		{name: "kind_census_whole", kindPopulation: 3, teamPopulation: 1, wantDeniedReported: 3},
		{name: "kind_census_cut", kindPopulation: exactNameCandidateQueryLimit + 1, teamPopulation: 1, wantDeniedReported: 0},
		{name: "kind_census_whole_exact_name_census_cut", kindPopulation: 3, teamPopulation: exactNameCandidateQueryLimit + 1, wantDeniedReported: 3},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The lexical arm also returns a denied member the kind census
			// returns, so a member two arms return must count once. The fake
			// returns the highest ids first, so the highest id is in the fetch
			// whether or not the fetch is cut.
			lexical := fulltextRow(string(kind), kindCensusMemberID(string(kind), tc.kindPopulation-1), "lexical", "teams struggling", nil)
			store := &kindCensusStore{
				population:  map[string]int{string(kind): tc.kindPopulation, "team": tc.teamPopulation},
				deniedKinds: map[string]bool{string(kind): true},
				fulltext:    []row{kindCensusScope(lexical, "node", true)},
			}
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)
			principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"repo-allowed"}}

			result, err := adapter.DiscoverContext(context.Background(), principal, kindCensusSurveyRequest(kind, 10))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if result.Cohort != nil {
				t.Fatalf("Cohort = %v, want nil: every %s member is denied", memberIDs(result.Cohort), kind)
			}
			wantDropped := tc.kindPopulation
			if wantDropped > exactNameCandidateQueryLimit {
				wantDropped = exactNameCandidateQueryLimit
			}
			if telemetry.cohortMembersAuthzDropped != wantDropped {
				t.Errorf("cohort members authz dropped = %d, want %d", telemetry.cohortMembersAuthzDropped, wantDropped)
			}
			if telemetry.cohortDeniedByAuthorization != tc.wantDeniedReported {
				t.Errorf("cohort denied by authorization = %d, want %d", telemetry.cohortDeniedByAuthorization, tc.wantDeniedReported)
			}
			deniedReason := fmt.Sprintf("cohort_denied_by_authorization:%d", wantDropped)
			hasReason := false
			for _, reason := range result.Coverage.DegradedReasons {
				if reason == deniedReason {
					hasReason = true
				}
			}
			if hasReason != (tc.wantDeniedReported > 0) {
				t.Errorf("degraded reasons %v, want %q present=%v", result.Coverage.DegradedReasons, deniedReason, tc.wantDeniedReported > 0)
			}
		})
	}
}

func captureCohortKindCensusLines(t *testing.T, level slog.Level, emit func(SlogTelemetry)) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	emit(SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: level}))})
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("telemetry emitted a line that is not valid JSON: %v (%q)", err, line)
		}
		records = append(records, record)
	}
	return records
}

const kindCensusLineMsg = "context_fabric: cohort kind census"

// TestCohortKindCensusLineCarriesTheFetchOnARanDecision asserts the emitted
// line through the production sink at the production level, with values that
// differ from one another and from their zero values.
func TestCohortKindCensusLineCarriesTheFetchOnARanDecision(t *testing.T) {
	t.Parallel()
	const canonicalRequestID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), canonicalRequestID)
	records := captureCohortKindCensusLines(t, productionSinkLevel, func(sink SlogTelemetry) {
		sink.RecordCohortKindCensus(ctx, "org_sink_test", CohortKindCensusRan, contextfabric.SubjectIncident, []string{"alpha_kind", "beta_kind"}, 37, 2000, true)
	})
	if len(records) != 1 {
		t.Fatalf("got %d lines, want 1", len(records))
	}
	record := records[0]
	want := map[string]any{
		"level": "INFO", "msg": kindCensusLineMsg, "org_id": "org_sink_test", "request_id": canonicalRequestID,
		"decision": "ran", "member_kind": "incident", "kinds": "alpha_kind,beta_kind",
		"pool_size": float64(37), "pool_bound": float64(2000), "truncated": true,
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %#v, want %#v", key, record[key], value)
		}
	}
	for key := range record {
		if _, ok := want[key]; !ok && key != "time" {
			t.Errorf("unexpected key %q on the kind census line", key)
		}
	}
}

// TestCohortKindCensusLineOmitsFetchFieldsWhenTheCensusDidNotRun: a census
// that did not run carries no pool size, even when the caller hands one over,
// so non-execution never reads as a measured zero.
func TestCohortKindCensusLineOmitsFetchFieldsWhenTheCensusDidNotRun(t *testing.T) {
	t.Parallel()
	notRun := 0
	for _, decision := range CohortKindCensusDecisionVocabulary() {
		if decision == CohortKindCensusRan {
			continue
		}
		notRun++
		records := captureCohortKindCensusLines(t, productionSinkLevel, func(sink SlogTelemetry) {
			sink.RecordCohortKindCensus(context.Background(), "org_sink_test", decision, contextfabric.SubjectTeam, []string{"team"}, 9, 2000, true)
		})
		if len(records) != 1 {
			t.Fatalf("%s: got %d lines, want 1", decision, len(records))
		}
		if records[0]["decision"] != string(decision) || records[0]["member_kind"] != "team" {
			t.Errorf("%s: decision=%v member_kind=%v", decision, records[0]["decision"], records[0]["member_kind"])
		}
		for _, key := range []string{"kinds", "pool_size", "pool_bound", "truncated"} {
			if _, present := records[0][key]; present {
				t.Errorf("%s: key %q present on a census that did not run", decision, key)
			}
		}
	}
	if notRun == 0 {
		t.Fatal("no non-ran decision is declared, so nothing above can fail")
	}
}

// TestCohortKindCensusLineIsEmittedAtTheProductionLevel observes the handler
// admit the line at Info and suppress it at Error.
func TestCohortKindCensusLineIsEmittedAtTheProductionLevel(t *testing.T) {
	t.Parallel()
	emit := func(sink SlogTelemetry) {
		sink.RecordCohortKindCensus(context.Background(), "org_sink_test", CohortKindCensusRan, contextfabric.SubjectIncident, []string{"incident"}, 3, 2000, false)
	}
	if got := len(captureCohortKindCensusLines(t, productionSinkLevel, emit)); got != 1 {
		t.Errorf("an Info handler admitted %d lines, want 1", got)
	}
	if got := len(captureCohortKindCensusLines(t, slog.LevelError, emit)); got != 0 {
		t.Fatalf("an Error handler admitted %d lines, want 0", got)
	}
}

// TestCohortKindCensusDecisionVocabularyIsClosed guards the vocabulary itself.
func TestCohortKindCensusDecisionVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	vocabulary := CohortKindCensusDecisionVocabulary()
	if len(vocabulary) == 0 {
		t.Fatal("empty vocabulary")
	}
	seen := map[CohortKindCensusDecision]bool{}
	for _, decision := range vocabulary {
		if strings.TrimSpace(string(decision)) == "" {
			t.Error("a decision is blank")
		}
		if seen[decision] {
			t.Errorf("decision %q is declared twice", decision)
		}
		seen[decision] = true
	}
}

// TestDiscoverContextEmitsTheKindCensusLineThroughTheProductionSink drives the
// reader with the production telemetry and reads the decision graph back from
// the emitted lines alone: the census decision and fetch, then the pool
// truncation and the discovered cohort.
func TestDiscoverContextEmitsTheKindCensusLineThroughTheProductionSink(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{population: map[string]int{string(kind): 3, "team": 2}}
	records := captureCohortKindCensusLines(t, productionSinkLevel, func(sink SlogTelemetry) {
		adapter := newFakeAdapterWithTelemetry(t, store.conn(t), sink)
		if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, kindCensusSurveyRequest(kind, 2)); err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
	})
	var census, basis map[string]any
	for _, record := range records {
		switch record["msg"] {
		case kindCensusLineMsg:
			if census != nil {
				t.Fatal("more than one kind census line for one call")
			}
			census = record
		case "context_fabric: cohort kind basis":
			basis = record
		}
	}
	if census == nil || basis == nil {
		t.Fatalf("lines = %v, want a kind census line and a cohort kind basis line", records)
	}
	want := map[string]any{"decision": "ran", "member_kind": string(kind), "kinds": string(kind), "pool_size": float64(3), "pool_bound": float64(exactNameCandidateQueryLimit), "truncated": false}
	for key, value := range want {
		if census[key] != value {
			t.Errorf("kind census %s = %#v, want %#v", key, census[key], value)
		}
	}
	if basis["pool_truncation"] != string(CohortPoolTruncationNone) || basis["discovered"] != true || basis["member_kind"] != string(kind) {
		t.Errorf("cohort kind basis line = %v, want pool_truncation=none discovered=true member_kind=%s", basis, kind)
	}
}

// TestDiscoverContextKindCensusSkipsARowThatIsNotANode: a row the store returns
// in a shape the reader cannot read is neither a member nor part of the pool
// size the census reports.
func TestDiscoverContextKindCensusSkipsARowThatIsNotANode(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{population: map[string]int{string(kind): 2}, malformedFor: string(kind)}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, kindCensusSurveyRequest(kind, 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortKindCensuses) != 1 || telemetry.cohortKindCensuses[0].poolSize != 2 {
		t.Fatalf("kind census lines = %+v, want one with pool_size 2 (the unreadable row excluded)", telemetry.cohortKindCensuses)
	}
	if got := memberIDs(result.Cohort); len(got) != 2 {
		t.Errorf("members = %v, want the 2 readable rows", got)
	}
}

// TestDiscoverContextKindCensusReadFailureFailsTheCall: a failed census read is
// an error, never an empty population that reads as "no such members".
func TestDiscoverContextKindCensusReadFailureFailsTheCall(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{population: map[string]int{string(kind): 2, "team": 1}, failFor: string(kind)}
	adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})

	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, kindCensusSurveyRequest(kind, 10))
	if err == nil {
		t.Fatalf("DiscoverContext() error = nil with cohort %v, want the census read failure", memberIDs(result.Cohort))
	}
	if !strings.Contains(err.Error(), "read kind census candidates") {
		t.Errorf("error = %v, want it to name the kind census read", err)
	}
}

// TestDiscoverContextEmitsNoKindCensusLineForANamedSubject: a question about
// one named subject has no census to decide, so the call carries no census
// line and runs no kind-scoped fetch.
func TestDiscoverContextEmitsNoKindCensusLineForANamedSubject(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{population: map[string]int{string(kind): 2}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, store.conn(t), telemetry)
	request := kindCensusSurveyRequest(kind, 10)
	request.Frame.SubjectExpression = contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"outage"}, ExpectedKind: &kind},
	}

	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.cohortKindCensuses) != 0 {
		t.Errorf("kind census lines = %+v, want none for a named subject", telemetry.cohortKindCensuses)
	}
	if got := len(store.singleKindQueries(kind)); got != 0 {
		t.Errorf("%d kind-scoped queries for a named subject, want 0", got)
	}
}
