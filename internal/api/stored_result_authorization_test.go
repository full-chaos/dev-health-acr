package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// The graph node a stored subject resolves to, by its authorization attribute.
type storedNodeClass string

const (
	nodeGrantedRepo      storedNodeClass = "granted_repo"    // ["example-org/widget-service"]
	nodeOtherRepo        storedNodeClass = "other_repo"      // ["other-org/secret-service"]
	nodeNoRepository     storedNodeClass = "no_repository"   // the projection's repo-less sentinel
	nodeUniversalAttr    storedNodeClass = "universal_attr"  // the string "*"
	nodeAbsent           storedNodeClass = "absent"          // no node under the identity
	nodeCallerOrg        storedNodeClass = "caller_org"      // organization subject, caller's org
	nodeOtherOrg         storedNodeClass = "other_org"       // organization subject, another org
	storedScopeRepoIn                    = "repo_granted_in" // [example-org/widget-service]
	storedScopeOrgWide                   = "org_wide"        // [example-org/*]
	storedScopeRepoOut                   = "repo_granted_out"
	storedScopeUniversal                 = "universal"
)

var storedScopeGrants = map[string][]string{
	storedScopeRepoIn:    {hostedTestRepository},
	storedScopeOrgWide:   {"example-org/*"},
	storedScopeRepoOut:   {"other/repo"},
	storedScopeUniversal: {"*"},
}

// storedResultOracle is the expected decision, written out by hand from the
// grant semantics (exact slug, owner wildcard, universal) and the projection's
// attribute convention -- never computed by the code under test.
var storedResultOracle = map[storedNodeClass]map[string]bool{
	nodeGrantedRepo:   {storedScopeRepoIn: true, storedScopeOrgWide: true, storedScopeRepoOut: false, storedScopeUniversal: true},
	nodeOtherRepo:     {storedScopeRepoIn: false, storedScopeOrgWide: false, storedScopeRepoOut: false, storedScopeUniversal: true},
	nodeNoRepository:  {storedScopeRepoIn: false, storedScopeOrgWide: false, storedScopeRepoOut: false, storedScopeUniversal: true},
	nodeUniversalAttr: {storedScopeRepoIn: true, storedScopeOrgWide: true, storedScopeRepoOut: true, storedScopeUniversal: true},
	nodeAbsent:        {storedScopeRepoIn: false, storedScopeOrgWide: false, storedScopeRepoOut: false, storedScopeUniversal: false},
	nodeCallerOrg:     {storedScopeRepoIn: true, storedScopeOrgWide: true, storedScopeRepoOut: true, storedScopeUniversal: true},
	nodeOtherOrg:      {storedScopeRepoIn: false, storedScopeOrgWide: false, storedScopeRepoOut: false, storedScopeUniversal: false},
}

func storedNodeAttributes(class storedNodeClass) (map[string]interface{}, bool) {
	switch class {
	case nodeGrantedRepo:
		return map[string]interface{}{"authorization_repositories": []string{hostedTestRepository}}, true
	case nodeOtherRepo:
		return map[string]interface{}{"authorization_repositories": []string{"other-org/secret-service"}}, true
	case nodeNoRepository:
		return map[string]interface{}{"authorization_repositories": []string{"acr-context-fabric:no-repository"}}, true
	case nodeUniversalAttr:
		return map[string]interface{}{"authorization_repositories": "*"}, true
	default:
		return nil, false
	}
}

type storedAuthzCell struct {
	kind    contractsv1.ContextFabricSubjectKind
	class   storedNodeClass
	subject contractsv1.ContextFabricSubjectRef
	result  contractsv1.ContextFabricInvestigationResult
}

// TestStoredResultIsServedOnlyWhenTheLiveGrantAdmitsEverySubject enumerates
// {caller grant} x {subject kind} x {graph node class} x {surface} through the
// real composed server: the real route, the real in-memory result store, the
// real MCP tool over the sidecar client, and the real graphrank predicate
// behind the gate.
func TestStoredResultIsServedOnlyWhenTheLiveGrantAdmitsEverySubject(t *testing.T) {
	kinds := contractsv1.ContextFabricSubjectKindVocabulary()
	graphClasses := []storedNodeClass{nodeGrantedRepo, nodeOtherRepo, nodeNoRepository, nodeUniversalAttr, nodeAbsent}

	store := memoryinvestigation.NewStore()
	nodes := map[string]map[string]interface{}{}
	var cells []storedAuthzCell
	for _, kind := range kinds {
		classes := graphClasses
		if kind == contractsv1.ContextFabricSubjectOrganization {
			classes = []storedNodeClass{nodeCallerOrg, nodeOtherOrg}
		}
		for _, class := range classes {
			id := fmt.Sprintf("%s:%s", kind, class)
			switch class {
			case nodeCallerOrg:
				id = "organization:org_1"
			case nodeOtherOrg:
				id = "organization:org_2"
			}
			subject := contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: id, Label: fmt.Sprintf("LABEL-%s-%s", kind, class)}
			if attributes, ok := storedNodeAttributes(class); ok {
				nodes[contextfabric.SubjectMapKey(subject)] = attributes
			}
			result := validContextFabricInvestigationResult()
			result.ResultID = fmt.Sprintf("result_authz_%s_%s", kind, class)
			result.DirectJudgment = fmt.Sprintf("JUDGMENT-%s-%s", kind, class)
			result.SubjectResolution.Committed = []contractsv1.ContextFabricSubjectRef{subject}
			result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{}
			seedResult3355(t, store, "org_1", result)
			cells = append(cells, storedAuthzCell{kind: kind, class: class, subject: subject, result: result})
		}
	}
	// The domain is generated: every kind of the closed vocabulary is a row.
	if want := len(kinds)*len(graphClasses) - len(graphClasses) + 2; len(cells) != want {
		t.Fatalf("cells = %d, want %d", len(cells), want)
	}

	logs := &bytes.Buffer{}
	app, _ := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(subjectNodeGraph{nodes: nodes})
	// The enumeration makes far more requests than the default per-minute
	// allowance; the limiter is not what this test measures.
	manager, err := limits.NewManager(limits.Options{Now: time.Now, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100_000},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100_000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100_000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	app.limits = manager

	executed := 0
	for _, scope := range []string{storedScopeRepoIn, storedScopeOrgWide, storedScopeRepoOut, storedScopeUniversal} {
		token := storedServingCredential(t, app, storedScopeGrants[scope])
		for _, cell := range cells {
			want := storedResultOracle[cell.class][scope]
			name := fmt.Sprintf("%s/%s/%s", scope, cell.kind, cell.class)
			for _, view := range []string{"", "view=projection"} {
				logs.Reset()
				req := investigationResultRequest(t, token, cell.result.ResultID)
				req.URL.RawQuery = view
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				assertStoredAuthzServed(t, name+"/http"+view, want, rec.Code, rec.Body.Bytes(), cell)
				assertStoredAuthzTrace(t, name+"/http"+view, want, logs.String())
				executed++
			}
			called, raw := callStoredServingMCP(t, app, token, cell.result.ResultID)
			status := http.StatusOK
			if called.IsError {
				status = http.StatusNotFound
			}
			assertStoredAuthzServed(t, name+"/mcp", want, status, raw, cell)
			executed++
		}
	}
	if want := 4 * len(cells) * 3; executed != want {
		t.Fatalf("executed %d cells, want %d", executed, want)
	}
}

func assertStoredAuthzServed(t *testing.T, name string, want bool, status int, body []byte, cell storedAuthzCell) {
	t.Helper()
	served := status == http.StatusOK
	if served != want {
		t.Errorf("%s: served=%t (status %d) want served=%t", name, served, status, want)
		return
	}
	if !want {
		if status != http.StatusNotFound {
			t.Errorf("%s: denial status %d, want the not-found answer an unknown id gets", name, status)
		}
		for _, secret := range []string{cell.result.DirectJudgment, cell.subject.Label, cell.subject.CanonicalID} {
			if bytes.Contains(body, []byte(secret)) {
				t.Errorf("%s: denial exposed %q", name, secret)
			}
		}
		return
	}
	if !bytes.Contains(body, []byte(cell.result.DirectJudgment)) && !bytes.Contains(body, []byte(cell.subject.Label)) {
		t.Errorf("%s: served body carries neither the judgment nor the subject", name)
	}
}

// assertStoredAuthzTrace: the decision is rebuildable from the trace alone --
// one Info line with the surface, the principal's scope class, the decision
// and its reason.
func assertStoredAuthzTrace(t *testing.T, name string, want bool, logs string) {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logs), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == contextfabric.StoredResultAuthorizationLogMessage {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Errorf("%s: %d stored-result authorization lines, want 1", name, len(lines))
		return
	}
	line := lines[0]
	wantDecision := string(contextfabric.StoredResultDenied)
	if want {
		wantDecision = string(contextfabric.StoredResultAdmitted)
	}
	if line["level"] != "INFO" || line["decision"] != wantDecision || line["surface"] != string(contextfabric.StoredResultSurfaceResultByID) || line["org_id"] != "org_1" || line["reason"] == "" || line["principal_scope"] == "" {
		t.Errorf("%s: trace line = %v", name, line)
	}
}

// The route's decision line, certified against its eventspec declaration from
// the bytes the production slog handler wrote. The three scenarios differ in
// every counted field.
func TestStoredResultAuthorizationLineCertifiesAgainstItsSpecification(t *testing.T) {
	granted := map[string]interface{}{"authorization_repositories": []string{hostedTestRepository}}
	other := map[string]interface{}{"authorization_repositories": []string{"other-org/secret-service"}}
	subject := func(id string) contractsv1.ContextFabricSubjectRef {
		return contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: id, Label: "Label " + id}
	}
	for _, tc := range []struct {
		name     string
		subjects []contractsv1.ContextFabricSubjectRef
		graph    subjectNodeGraph
		status   int
		want     map[string]any
	}{
		{"admitted", []contractsv1.ContextFabricSubjectRef{subject("pa"), subject("pb")},
			subjectNodeGraph{nodes: map[string]map[string]interface{}{"project\x00pa": granted, "project\x00pb": granted}}, http.StatusOK,
			map[string]any{"decision": "admitted", "reason": "subjects_admitted", "subject_count": 2, "graph_subject_count": 2, "admitted_count": 2, "denied_count": 0, "absent_count": 0, "refused_kinds": []any{}}},
		{"denied", []contractsv1.ContextFabricSubjectRef{subject("pa"), subject("pc"), subject("pd")},
			subjectNodeGraph{nodes: map[string]map[string]interface{}{"project\x00pa": granted, "project\x00pc": other}}, http.StatusNotFound,
			map[string]any{"decision": "denied", "reason": "subject_denied", "subject_count": 3, "graph_subject_count": 3, "admitted_count": 1, "denied_count": 1, "absent_count": 1, "refused_kinds": []any{"project"}}},
		{"unavailable", []contractsv1.ContextFabricSubjectRef{subject("pe")},
			subjectNodeGraph{readErr: errors.New("graph down")}, http.StatusServiceUnavailable,
			map[string]any{"decision": "unavailable", "reason": "graph_read_failed", "subject_count": 1, "graph_subject_count": 1, "admitted_count": 0, "denied_count": 0, "absent_count": 0, "refused_kinds": []any{}, "error_class": "graph_error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			result := validContextFabricInvestigationResult()
			result.ResultID = "result_certify_" + tc.name
			result.SubjectResolution.Committed = tc.subjects
			result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{}
			seedResult3355(t, store, "org_1", result)
			logs := &bytes.Buffer{}
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			app.runtime.StoredResultGate = contextfabric.NewStoredResultGate(tc.graph)
			logs.Reset()
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, investigationResultRequest(t, token, result.ResultID))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			want := map[string]any{
				"org_id": "org_1", "surface": "result_by_id", "principal_scope": "restricted", "repository_scope_count": 1,
				"organization_subject_count": 0, "organization_mismatch_count": 0, "group_count": 0, "group_unproven_count": 0,
				"request_id": rec.Header().Get("X-Request-ID"),
			}
			for key, value := range tc.want {
				want[key] = value
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.StoredResultAuthorization, Want: want}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
		})
	}
}
