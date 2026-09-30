package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	sourceRowGrantedRepoID = "20000000-0000-4000-8000-000000000002"
	sourceRowSecretRepoID  = "30000000-0000-4000-8000-000000000003"
	sourceRowSecretSlug    = "other-org/secret-service"
)

// sourceRowTables is a fake ClickHouse behind the REAL sourcerow resolver:
// repositories by id and catalog rows by (repo, query, locator). Every read
// is recorded with its arguments.
type sourceRowTables struct {
	repos map[string]string
	rows  map[string][]contextpacket.EvidenceReference
	err   error
	reads []string
	// orgRows are organization-level rows by (query, locator); gate is the
	// subject gate the resolver is built with (nil: none composed).
	orgRows map[string][]contextpacket.OrganizationRowReference
	gate    sourcerow.SubjectGate
}

func (s *sourceRowTables) RepositoryByID(_ context.Context, orgID, repoID string) ([]contractsv1.ResolvedScope, error) {
	s.reads = append(s.reads, "repository_by_id:"+orgID+":"+repoID)
	if s.err != nil {
		return nil, s.err
	}
	if slug, ok := s.repos[repoID]; ok {
		return []contractsv1.ResolvedScope{{RepoID: repoID, RepoSlug: slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}}}, nil
	}
	return []contractsv1.ResolvedScope{}, nil
}

func (s *sourceRowTables) SourceRowRepositories(_ context.Context, orgID string, discovery contextpacket.SourceRowDiscovery, entityID string) ([]contractsv1.ResolvedScope, error) {
	s.reads = append(s.reads, "discover:"+orgID+":"+string(discovery)+":"+entityID)
	return []contractsv1.ResolvedScope{}, s.err
}

func (s *sourceRowTables) DependencyLocators(_ context.Context, orgID, repoID, key string) ([]string, error) {
	s.reads = append(s.reads, "dependency_locators:"+orgID+":"+repoID+":"+key)
	return nil, s.err
}

func (s *sourceRowTables) ResolveOrganizationRow(_ context.Context, orgID string, read contextpacket.SourceRowRead) ([]contextpacket.OrganizationRowReference, error) {
	s.reads = append(s.reads, "org_row:"+orgID+":"+read.QueryID+":"+read.Locator)
	if s.err != nil {
		return nil, s.err
	}
	return s.orgRows[read.QueryID+"|"+read.Locator], nil
}

func (s *sourceRowTables) ResolveSourceRow(_ context.Context, orgID string, scope contractsv1.ResolvedScope, read contextpacket.SourceRowRead) ([]contextpacket.EvidenceReference, error) {
	s.reads = append(s.reads, "row:"+orgID+":"+scope.RepoID+":"+read.QueryID+":"+read.Locator)
	if s.err != nil {
		return nil, s.err
	}
	return s.rows[scope.RepoID+"|"+read.QueryID+"|"+read.Locator], nil
}

// withPullRequest seeds pull request 532 in one repository.
func (s *sourceRowTables) withPullRequest(repoID, slug string) *sourceRowTables {
	s.repos[repoID] = slug
	locator := "acr:v1:pull-request:532"
	s.rows[repoID+"|pull_requests.v1|"+locator] = []contextpacket.EvidenceReference{{RepoSlug: slug, Excerpt: "merged", Evidence: contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: locator, SourceVersion: "pull_requests.v1",
		Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: "pull_request", EntityID: "532", DisplayLabel: "Fix the widget"},
		Provenance: "native", Confidence: 1, Citation: "merged", ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Availability: contractsv1.EvidenceAvailable,
	}}}
	return s
}

func newSourceRowTables() *sourceRowTables {
	return &sourceRowTables{repos: map[string]string{}, rows: map[string][]contextpacket.EvidenceReference{}}
}

// sourceRowApp is the hosted app (restricted to hostedTestRepository) with a
// real sourcerow resolver over tables.
func sourceRowApp(t *testing.T, tables *sourceRowTables, results contextfabric.InvestigationResultStore, logs *bytes.Buffer) (*App, string) {
	t.Helper()
	if results == nil {
		results = memoryinvestigation.NewStore()
	}
	app, token := newParityHostedAppWithLogs(t, nil, results, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	app.runtime.SourceRows = testSourceRows(t, tables)
	return app, token
}

// testSourceRows is the real resolver over the fake tables.
func testSourceRows(t *testing.T, tables *sourceRowTables) contextfabric.SourceRowResolver {
	t.Helper()
	resolver, err := sourcerow.New(tables, contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{Now: func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }}), tables.gate)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func scopedEvidenceRequest(t *testing.T, token, ref, resultID string) *http.Request {
	t.Helper()
	request := evidenceRequest(t, token, ref)
	if resultID != "" {
		request.URL.RawQuery = url.Values{"result_id": []string{resultID}}.Encode()
	}
	return request
}

// A caller whose grant holds the row's repository gets the source row,
// labeled as the current row, through the hosted route.
func TestEvidenceRouteServesTheSourceRowToAGrantedCaller(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532")
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository), nil, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s\n%s", rec.Code, rec.Body.String(), logs.String())
	}
	var expanded contractsv1.ExpandedEvidence
	if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
		t.Fatal(err)
	}
	if err := expanded.Validate(); err != nil {
		t.Fatalf("served expansion invalid: %v", err)
	}
	evidence := expanded.Evidence
	if evidence.EvidenceRefID != ref || evidence.Source.System != "dev_health" || evidence.Provenance != "native" || evidence.Source.DisplayLabel != "Fix the widget" {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence.Metadata["record"] != "source_row" || evidence.Metadata["row_state"] != "current" || evidence.Metadata["row_observed_at"] != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("metadata = %v", evidence.Metadata)
	}
	if expanded.Structured["repository"] != hostedTestRepository || expanded.Structured["entity_type"] != "pull-request" {
		t.Fatalf("structured = %v", expanded.Structured)
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionSourceRowServed), "entity_type": "pull-request",
		"source_reason": "served", "source_query": "pull_requests.v1", "source_grammar": "repo_anchored", "source_repositories": 1, "source_admitted": 1, "source_rows": 1,
		"candidate_count": 0, "citing_count": 0, "request_id": rec.Header().Get("X-Request-ID"),
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}

// P1 + P2 through the hosted route: for a restricted caller, a row in a
// repository outside its grant and a row that does not exist give the same
// status, headers and body bytes (one fixed request id on both), after the
// same reads.
func TestEvidenceRouteRefusesAnOutOfGrantRowLikeAnAbsentOne(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowSecretRepoID+":532")
	serve := func(tables *sourceRowTables) (*httptest.ResponseRecorder, *bytes.Buffer) {
		logs := &bytes.Buffer{}
		app, token := sourceRowApp(t, tables, nil, logs)
		logs.Reset()
		rec := httptest.NewRecorder()
		request := evidenceRequest(t, token, ref)
		request.Header.Set("X-Request-ID", "req_6180000000000000000000000000beef")
		app.Handler().ServeHTTP(rec, request)
		return rec, logs
	}
	present := newSourceRowTables().withPullRequest(sourceRowSecretRepoID, sourceRowSecretSlug)
	absent := newSourceRowTables()
	refused, refusedLogs := serve(present)
	missing, _ := serve(absent)
	if refused.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("status: refused %d, absent %d", refused.Code, missing.Code)
	}
	if !bytes.Equal(refused.Body.Bytes(), missing.Body.Bytes()) {
		t.Fatalf("bodies differ:\n refused %s\n absent  %s", refused.Body.String(), missing.Body.String())
	}
	if !reflect.DeepEqual(refused.Header(), missing.Header()) {
		t.Fatalf("headers differ:\n refused %v\n absent  %v", refused.Header(), missing.Header())
	}
	if !reflect.DeepEqual(present.reads, absent.reads) {
		t.Fatalf("reads differ:\n refused %v\n absent  %v", present.reads, absent.reads)
	}
	// The trace, never the wire, tells the two apart.
	parsed, err := certify.Parse(refusedLogs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionNotCited), "source_reason": "no_row", "source_repositories": 1, "source_admitted": 0, "source_rows": 0,
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, refusedLogs.String())
	}
	// The row is real: the same tables serve it to a caller granted its
	// repository.
	granted := newSourceRowTables().withPullRequest(sourceRowSecretRepoID, hostedTestRepository)
	if rec, logs := serve(granted); rec.Code != http.StatusOK {
		t.Fatalf("granted caller: status %d\n%s", rec.Code, logs.String())
	}
}

// P5: no source row, but the caller's own answer cites the ref: the
// persisted record serves it, as before this change.
func TestEvidenceRouteFallsBackToThePersistedRecord(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532")
	store := memoryinvestigation.NewStore()
	seedResult3355(t, store, "org_1", citingStoredResult("result_source_row_fallback", ref))
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, newSourceRowTables(), store, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, scopedEvidenceRequest(t, token, ref, "result_source_row_fallback"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s\n%s", rec.Code, rec.Body.String(), logs.String())
	}
	var expanded contractsv1.ExpandedEvidence
	if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
		t.Fatal(err)
	}
	if expanded.Evidence.Source.System != contextfabric.ContextFabricEvidenceSystem || expanded.Evidence.Provenance != contextfabric.ContextFabricEvidenceProvenance || expanded.Structured["result_id"] != "result_source_row_fallback" {
		t.Fatalf("expanded = %+v", expanded)
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionServed), "source_reason": "no_row", "source_repositories": 0, "source_admitted": 0,
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}

// P6: a failed source read is a retryable 503 even when the caller's answer
// cites the ref: the record is not served in its place.
func TestEvidenceRouteAnswersAFailedSourceReadWithA503(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532")
	store := memoryinvestigation.NewStore()
	seedResult3355(t, store, "org_1", citingStoredResult("result_source_row_503", ref))
	tables := newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository)
	tables.err = errors.New("clickhouse down")
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, tables, store, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, scopedEvidenceRequest(t, token, ref, "result_source_row_503"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionSourceRowUnavailable), "source_reason": "unavailable", "candidate_count": 0, "error_class": "internal",
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}

// Every source-row reason reaches the trace from the hosted route.
func TestEvidenceRouteTracesEverySourceRowReason(t *testing.T) {
	cases := map[contextfabric.SourceRowReason]struct {
		ref    string
		tables func() *sourceRowTables
		status int
	}{
		contextfabric.SourceRowServed: {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532"), func() *sourceRowTables {
			return newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository)
		}, http.StatusOK},
		contextfabric.SourceRowKindOnRecord:  {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProjectTeam, "jira:PROJ-1:team-a"), newSourceRowTables, http.StatusNotFound},
		contextfabric.SourceRowIDMalformed:   {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, "532"), newSourceRowTables, http.StatusNotFound},
		contextfabric.SourceRowNoRow:         {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":9"), newSourceRowTables, http.StatusNotFound},
		contextfabric.SourceRowAmbiguous:     {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532"), func() *sourceRowTables { return ambiguousTables() }, http.StatusNotFound},
		contextfabric.SourceRowUnavailable:   {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532"), func() *sourceRowTables { t := newSourceRowTables(); t.err = errors.New("down"); return t }, http.StatusServiceUnavailable},
		contextfabric.SourceRowInvalid:       {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532"), func() *sourceRowTables { return invalidTables() }, http.StatusNotFound},
		contextfabric.SourceRowBackendAbsent: {contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532"), nil, http.StatusNotFound},
	}
	for _, reason := range contextfabric.SourceRowReasonVocabulary() {
		tc, ok := cases[reason]
		if !ok {
			t.Fatalf("reason %s has no case", reason)
		}
		t.Run(string(reason), func(t *testing.T) {
			logs := &bytes.Buffer{}
			var app *App
			var token string
			if tc.tables == nil {
				app, token = newParityHostedAppWithLogs(t, nil, memoryinvestigation.NewStore(), limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			} else {
				app, token = sourceRowApp(t, tc.tables(), nil, logs)
			}
			logs.Reset()
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, evidenceRequest(t, token, tc.ref))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{"org_id": "org_1", "source_reason": string(reason)}}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
		})
	}
}

// ambiguousTables holds two rows under one catalog evidence id.
func ambiguousTables() *sourceRowTables {
	tables := newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository)
	key := fmt.Sprintf("%s|pull_requests.v1|acr:v1:pull-request:532", sourceRowGrantedRepoID)
	tables.rows[key] = append(tables.rows[key], tables.rows[key][0])
	return tables
}

// invalidTables holds a row whose provenance the contract rejects.
func invalidTables() *sourceRowTables {
	tables := newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository)
	key := fmt.Sprintf("%s|pull_requests.v1|acr:v1:pull-request:532", sourceRowGrantedRepoID)
	tables.rows[key][0].Evidence.Provenance = ""
	return tables
}

// A row the contract rejects is loud: one Warn line with the kind and the
// statement, and no id, beside the trace reason. The route answers from the
// record (here: not found).
func TestEvidenceRouteWarnsOnceOnAnInvalidRow(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532")
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, invalidTables(), nil, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var warns []map[string]any
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		var entry map[string]any
		if json.Unmarshal(line, &entry) == nil && entry["msg"] == contextfabric.SourceRowInvalidLogMessage {
			warns = append(warns, entry)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("warn lines = %d, want 1:\n%s", len(warns), logs.String())
	}
	warn := warns[0]
	if warn["level"] != "WARN" || warn["entity_type"] != "pull-request" || warn["source_query"] != "pull_requests.v1" || warn["org_id"] != "org_1" {
		t.Fatalf("warn = %v", warn)
	}
	line, _ := json.Marshal(warn)
	for _, id := range []string{sourceRowGrantedRepoID, "532", hostedTestRepository} {
		if bytes.Contains(line, []byte(id)) {
			t.Fatalf("warn line carries id %q: %s", id, line)
		}
	}
	// A served row, and a plain no-row, warn nothing.
	for _, tables := range []*sourceRowTables{newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository), newSourceRowTables()} {
		logs := &bytes.Buffer{}
		app, token := sourceRowApp(t, tables, nil, logs)
		logs.Reset()
		app.Handler().ServeHTTP(httptest.NewRecorder(), evidenceRequest(t, token, ref))
		if bytes.Contains(logs.Bytes(), []byte(contextfabric.SourceRowInvalidLogMessage)) {
			t.Fatalf("warned without an invalid row:\n%s", logs.String())
		}
	}
}

// codex r1 P2 (dependency id with trailing relation whitespace): the route
// refuses a ref with surrounding whitespace before either path runs, as it
// did before this change, so no source read happens and the answer is the
// same not-found as an unknown ref. Only a producer that minted such a ref
// could hit it (follow-up: the graph producer's raw relationship type).
func TestEvidenceRouteRefusesASurroundingWhitespaceRefBeforeAnyRead(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, sourceRowGrantedRepoID+":jira:ABC-1:jira:ABC-0:blocks ")
	tables := newSourceRowTables()
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, tables, nil, logs)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
	if rec.Code != http.StatusNotFound || len(tables.reads) != 0 {
		t.Fatalf("status %d, reads %v", rec.Code, tables.reads)
	}
}

// CHAOS-7226 r2 P3: the unscoped (no result_id) deprecation warning names a
// persisted-record risk, so it is written only when that path decided the
// expansion, never for a served source row.
func TestEvidenceRouteWarnsUnscopedOnlyOnThePersistedRecordPath(t *testing.T) {
	const deprecation = "context fabric evidence expansion without result_id is deprecated"
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, sourceRowGrantedRepoID+":532")
	for _, tc := range []struct {
		name   string
		tables *sourceRowTables
		status int
		warns  bool
	}{
		{"served source row", newSourceRowTables().withPullRequest(sourceRowGrantedRepoID, hostedTestRepository), http.StatusOK, false},
		{"no row, record path", newSourceRowTables(), http.StatusNotFound, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := sourceRowApp(t, tc.tables, nil, logs)
			logs.Reset()
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := bytes.Contains(logs.Bytes(), []byte(deprecation)); got != tc.warns {
				t.Fatalf("deprecation warned = %v, want %v:\n%s", got, tc.warns, logs.String())
			}
		})
	}
}

// ownershipGraph is the graph side of the REAL directread.SubjectGate, built
// from ONE ownership data set the way production projects it (#742 r1 P3):
//
//   - owned is each team's CURRENT team_repo_ownership. devhealthsource
//     queryTeams writes it as the team node's authorization_repositories
//     (CHAOS-4390; a team with none carries the no-ownership sentinel), and
//     falkorgraph OwnershipReachedRepositories reads that same list as the
//     team's reach. So a team's node list and its reach are one list.
//   - ownedBy is each project's CURRENT OWNED_BY_TEAM edges. Its reach is
//     the union of its owning teams' lists, and falkorgraph's node check
//     reads a project with that same reach in place of its stored "*"
//     (withProjectReach, CHAOS-7080), so a project's node list is its reach
//     too.
//
// Membership is no input: the gate cannot see it (the producer builds the
// team list from team_repo_ownership, never team_memberships: devhealthsource
// subTeamAuthorizationCarriesCurrentOwnedRepositories). Every call is
// recorded.
type ownershipGraph struct {
	owned   map[string][]string
	ownedBy map[string][]string
	calls   []string
}

// noTeamOwnershipSentinel mirrors devhealthsource's list for a team with no
// ownership rows: it matches no real slug.
const noTeamOwnershipSentinel = "acr-context-fabric:no-team-repository-ownership"

func (g *ownershipGraph) nodeRepositories(canonical string) ([]string, bool) {
	_, team := g.owned[canonical]
	_, project := g.ownedBy[canonical]
	if !team && !project {
		return nil, false
	}
	if repos := g.reachedRepositories(canonical); len(repos) > 0 {
		return repos, true
	}
	return []string{noTeamOwnershipSentinel}, true
}

func (g *ownershipGraph) reachedRepositories(canonical string) []string {
	if repos, ok := g.owned[canonical]; ok {
		return append([]string{}, repos...)
	}
	reached := []string{}
	for _, team := range g.ownedBy[canonical] {
		reached = append(reached, g.owned[team]...)
	}
	return reached
}

func (g *ownershipGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	g.calls = append(g.calls, "binding")
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (g *ownershipGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index, subject := range subjects {
		g.calls = append(g.calls, "authorize:"+subject.CanonicalID)
		repos, found := g.nodeRepositories(subject.CanonicalID)
		switch {
		case !found:
			outcomes[index] = contextfabric.StoredSubjectAbsent
		case graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, map[string]interface{}{"authorization_repositories": repos}):
			outcomes[index] = contextfabric.StoredSubjectAdmitted
		default:
			outcomes[index] = contextfabric.StoredSubjectDenied
		}
	}
	return outcomes, nil
}

func (g *ownershipGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	reached := make([][]string, len(subjects))
	for index, subject := range subjects {
		g.calls = append(g.calls, "reach:"+subject.CanonicalID)
		reached[index] = g.reachedRepositories(subject.CanonicalID)
	}
	return reached, nil
}

// withTeam seeds one team row and gives the tables a real subject gate over
// graph.
func (s *sourceRowTables) withTeam(teamID string, graph *ownershipGraph) *sourceRowTables {
	return s.withOrganizationRow("teams.v1", "team", teamID, "Widget team", graph, teamID)
}

// withProject seeds one project row whose OWN columns are (provider, id) and
// gives the tables a real subject gate over graph. Its evidence id is the
// statement's concatenation, <provider>:<id>.
func (s *sourceRowTables) withProject(provider, projectID string, graph *ownershipGraph) *sourceRowTables {
	return s.withOrganizationRow("projects.v1", "project", provider+":"+projectID, "Widget project", graph, provider, projectID)
}

func (s *sourceRowTables) withOrganizationRow(queryID, kind, entityID, label string, graph *ownershipGraph, key ...string) *sourceRowTables {
	if s.orgRows == nil {
		s.orgRows = map[string][]contextpacket.OrganizationRowReference{}
	}
	locator := contractsv1.ContextFabricEvidenceRefPrefix + kind + ":" + entityID
	s.orgRows[queryID+"|"+locator] = []contextpacket.OrganizationRowReference{{Key: key, Reference: contextpacket.EvidenceReference{Excerpt: "provider=jira, active=1", Evidence: contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: locator, SourceVersion: queryID,
		Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: kind, EntityID: entityID, DisplayLabel: label},
		Provenance: "native", Confidence: 1, Citation: "provider=jira, active=1", ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Availability: contractsv1.EvidenceAvailable,
	}}}}
	s.gate = directread.NewSubjectGate(graph, nil)
	return s
}

// CHAOS-7227: a team row is served when the REAL subject gate admits it by
// ownership: the team owns a repository in the caller's grant.
func TestEvidenceRouteServesAnOwnedTeamRow(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-widget")
	graph := &ownershipGraph{owned: map[string][]string{contextfabric.TeamCanonicalID("team-widget"): {hostedTestRepository}}}
	logs := &bytes.Buffer{}
	app, token := sourceRowApp(t, newSourceRowTables().withTeam("team-widget", graph), nil, logs)
	logs.Reset()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s\n%s", rec.Code, rec.Body.String(), logs.String())
	}
	var expanded contractsv1.ExpandedEvidence
	if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
		t.Fatal(err)
	}
	if expanded.Evidence.EvidenceRefID != ref || expanded.Evidence.Source.System != "dev_health" || expanded.Structured["subject"] != contextfabric.TeamCanonicalID("team-widget") || expanded.Evidence.Metadata["record"] != "source_row" {
		t.Fatalf("expanded = %+v %v", expanded.Evidence, expanded.Structured)
	}
	wantCalls := []string{"binding", "authorize:" + contextfabric.TeamCanonicalID("team-widget"), "reach:" + contextfabric.TeamCanonicalID("team-widget")}
	if !reflect.DeepEqual(graph.calls, wantCalls) {
		t.Fatalf("graph calls = %v, want %v", graph.calls, wantCalls)
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "reason": string(contextfabric.EvidenceExpansionSourceRowServed), "entity_type": "team",
		"source_reason": "served", "source_query": "teams.v1", "source_grammar": "org_keyed", "source_repositories": 1, "source_admitted": 1, "source_rows": 1,
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}

// P7: authorization is ownership, never membership. A team that owns no
// repository of the caller's grant is refused with the same bytes as a team
// that does not exist, after the same graph calls and no row read.
func TestEvidenceRouteRefusesAnUnownedTeamLikeAnAbsentOne(t *testing.T) {
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-secret")
	serve := func(graph *ownershipGraph) (*httptest.ResponseRecorder, *sourceRowTables) {
		tables := newSourceRowTables().withTeam("team-secret", graph)
		app, token := sourceRowApp(t, tables, nil, &bytes.Buffer{})
		rec := httptest.NewRecorder()
		request := evidenceRequest(t, token, ref)
		request.Header.Set("X-Request-ID", "req_7227000000000000000000000000beef")
		app.Handler().ServeHTTP(rec, request)
		return rec, tables
	}
	unowned := &ownershipGraph{owned: map[string][]string{contextfabric.TeamCanonicalID("team-secret"): {sourceRowSecretSlug}}}
	absent := &ownershipGraph{}
	refused, refusedTables := serve(unowned)
	missing, missingTables := serve(absent)
	if refused.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("status: refused %d, absent %d", refused.Code, missing.Code)
	}
	if !bytes.Equal(refused.Body.Bytes(), missing.Body.Bytes()) || !reflect.DeepEqual(refused.Header(), missing.Header()) {
		t.Fatalf("responses differ:\n refused %s %v\n absent  %s %v", refused.Body.String(), refused.Header(), missing.Body.String(), missing.Header())
	}
	if !reflect.DeepEqual(unowned.calls, absent.calls) {
		t.Fatalf("graph calls differ:\n refused %v\n absent  %v", unowned.calls, absent.calls)
	}
	if len(refusedTables.reads) != 0 || len(missingTables.reads) != 0 {
		t.Fatalf("a refused team read its row: %v / %v", refusedTables.reads, missingTables.reads)
	}
}

// CHAOS-7227: a project is decided by the repositories its OWNED_BY_TEAM
// teams own. One whose owning team owns only a repository outside the grant
// is refused and reads no row; one whose owning team owns the caller's
// repository is served. (#742 r1 P3: this replaces a team case whose node
// list and reach came from two data sets, a state production cannot build.)
func TestEvidenceRouteDecidesAProjectByItsOwningTeams(t *testing.T) {
	canonical := projectCanonicalID(t, "jira", "WID")
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "jira:WID")
	for _, tc := range []struct {
		name   string
		owns   string
		status int
		reads  int
		calls  []string
	}{
		// Refused at the node check (its list is the reach); the reach read
		// runs only after node admission.
		{"owning team owns only an out-of-grant repository", sourceRowSecretSlug, http.StatusNotFound, 0, []string{"binding", "authorize:" + canonical}},
		{"owning team owns the caller's repository", hostedTestRepository, http.StatusOK, 1, []string{"binding", "authorize:" + canonical, "reach:" + canonical}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := contextfabric.TeamCanonicalID("team-widget")
			graph := &ownershipGraph{owned: map[string][]string{team: {tc.owns}}, ownedBy: map[string][]string{canonical: {team}}}
			tables := newSourceRowTables().withProject("jira", "WID", graph)
			app, token := sourceRowApp(t, tables, nil, &bytes.Buffer{})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, evidenceRequest(t, token, ref))
			if rec.Code != tc.status || len(tables.reads) != tc.reads {
				t.Fatalf("status %d, reads %v", rec.Code, tables.reads)
			}
			if !reflect.DeepEqual(graph.calls, tc.calls) {
				t.Fatalf("graph calls = %v, want %v", graph.calls, tc.calls)
			}
		})
	}
}

func projectCanonicalID(t *testing.T, provider, projectID string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project: %v %v", err, omitted)
	}
	return canonical
}

// #742 r1 P1 (executed by the reviewer): ref acme:linear:SECRET splits as
// provider "acme", project "linear:SECRET", and the gate admits THAT subject;
// the row its locator matches is the project (provider "acme:linear", id
// "SECRET"), another subject. The route refuses it with the bytes of an absent
// project and logs one Warn line with the kind and statement, no id.
func TestEvidenceRouteRefusesARowOfAnotherSubject(t *testing.T) {
	decoy := projectCanonicalID(t, "acme", "linear:SECRET")
	team := contextfabric.TeamCanonicalID("team-widget")
	graph := &ownershipGraph{owned: map[string][]string{team: {hostedTestRepository}}, ownedBy: map[string][]string{decoy: {team}}}
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "acme:linear:SECRET")
	serve := func(tables *sourceRowTables) (*httptest.ResponseRecorder, *bytes.Buffer) {
		logs := &bytes.Buffer{}
		app, token := sourceRowApp(t, tables, nil, logs)
		logs.Reset()
		rec := httptest.NewRecorder()
		request := evidenceRequest(t, token, ref)
		request.Header.Set("X-Request-ID", "req_7227000000000000000000000000cafe")
		app.Handler().ServeHTTP(rec, request)
		return rec, logs
	}
	mismatched, logs := serve(newSourceRowTables().withProject("acme:linear", "SECRET", graph))
	absent, absentLogs := serve(newSourceRowTables().withProject("acme", "other", graph))
	if mismatched.Code != http.StatusNotFound || !bytes.Equal(mismatched.Body.Bytes(), absent.Body.Bytes()) || !reflect.DeepEqual(mismatched.Header(), absent.Header()) {
		t.Fatalf("mismatch %d %s, absent %d %s", mismatched.Code, mismatched.Body.String(), absent.Code, absent.Body.String())
	}
	var warns []map[string]any
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		var entry map[string]any
		if json.Unmarshal(line, &entry) == nil && entry["msg"] == contextfabric.SourceRowSubjectMismatchLogMessage {
			warns = append(warns, entry)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("warn lines = %d, want 1:\n%s", len(warns), logs.String())
	}
	warn := warns[0]
	if warn["level"] != "WARN" || warn["entity_type"] != "project" || warn["source_query"] != "projects.v1" || warn["org_id"] != "org_1" {
		t.Fatalf("warn = %v", warn)
	}
	line, _ := json.Marshal(warn)
	for _, id := range []string{"SECRET", "acme", "linear"} {
		if bytes.Contains(line, []byte(id)) {
			t.Fatalf("warn line carries id %q: %s", id, line)
		}
	}
	if bytes.Contains(absentLogs.Bytes(), []byte(contextfabric.SourceRowSubjectMismatchLogMessage)) {
		t.Fatalf("an absent row warned:\n%s", absentLogs.String())
	}
	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.EvidenceExpansion, Want: map[string]any{
		"org_id": "org_1", "entity_type": "project", "source_reason": "no_row", "source_query": "projects.v1", "source_admitted": 1, "source_rows": 1,
	}}); err != nil {
		t.Fatalf("certify: %v\n%s", err, logs.String())
	}
}
