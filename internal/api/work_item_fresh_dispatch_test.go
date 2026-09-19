package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// These tests use the actual candidate, S1 adapter, status/work providers,
// registry, interpreter and synthesis adapters, engine, store codec and HTTP
// encoder. Backend query rows and model drafts are controlled; no deployed
// graph or ClickHouse measurement is claimed here.
type freshTupleQueryClient struct {
	memberID    string
	phases      []string
	fail        string
	rowsByPhase map[string][][]any
	queries     []freshTupleQuery
	scanned     map[string]int
}

type freshTupleQuery struct {
	phase    string
	sql      string
	bindings []contextpacket.ClickHouseBinding
}

func (c *freshTupleQueryClient) Query(_ context.Context, sql string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	phase := "s1"
	rows := [][]any{{c.memberID, "repo-1", "work-1", hostedTestRepository, uint8(1), uint64(1), uint64(1), uint64(0), uint64(0), uint64(0)}}
	if strings.Contains(sql, "w.status") {
		phase = "status"
		rows = [][]any{{"work-1", "open", "repo-1"}}
	}
	if strings.Contains(sql, "w.title") {
		phase = "work"
		rows = [][]any{{"work-1", "Implement the thing", "repo-1"}}
	}
	if configured, ok := c.rowsByPhase[phase]; ok {
		rows = configured
	}
	c.phases = append(c.phases, phase)
	c.queries = append(c.queries, freshTupleQuery{phase: phase, sql: sql, bindings: append([]contextpacket.ClickHouseBinding(nil), bindings...)})
	if c.fail == phase {
		return nil, errors.New("controlled content read failure")
	}
	if c.fail == "zero" {
		rows = nil
	}
	if phase == "s1" {
		var err error
		rows, err = freshTupleResolvedS1Rows(rows)
		if err != nil {
			return nil, err
		}
	}
	var rowErr error
	if c.fail == phase+"_partial" {
		rowErr = errors.New("controlled failure after scanned content rows")
	}
	if c.scanned == nil {
		c.scanned = map[string]int{}
	}
	return &freshTupleQueryRows{rows: rows, rowErr: rowErr, scanned: func() { c.scanned[phase]++ }}, nil
}

// These scenario rows describe ten member fields, not complete S1 wire rows.
// Encode only successful resolved-anchor fixtures; protocol-negative tests use
// the membership package's explicit rows. Clone before appending so later reuse
// reads see the same scenario payload, including slices with spare capacity.
func freshTupleResolvedS1Rows(members [][]any) ([][]any, error) {
	rows := make([][]any, 0, len(members)+1)
	for _, member := range members {
		if len(member) != 10 {
			return nil, fmt.Errorf("resolved S1 fixture member width %d != 10", len(member))
		}
		// The eight census fields, the nine per-path census columns (four
		// authorization paths, repo-less, repo-less denied, denied
		// project-less, two excluded link kinds), the two counters, then the
		// row kind and the anchor resolution.
		row := append(append(append([]any{}, member[:8]...), s1PathZeros()...), member[8:]...)
		row = append(row, uint8(0), uint8(1))
		rows = append(rows, row)
	}
	sentinel := append(append([]any{"", "", "", "", uint8(0), uint64(0), uint64(0), uint64(0)}, s1PathZeros()...), uint64(0), uint64(0), uint8(1), uint8(1))
	return append(rows, sentinel), nil
}

func s1PathZeros() []any {
	return []any{uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0)}
}

type freshTupleQueryRows struct {
	rows    [][]any
	index   int
	rowErr  error
	scanned func()
}

func (r *freshTupleQueryRows) Next() bool { return r.index < len(r.rows) }
func (r *freshTupleQueryRows) Scan(dest ...any) error {
	row := r.rows[r.index]
	if len(row) != len(dest) {
		return fmt.Errorf("scan width %d != %d", len(row), len(dest))
	}
	for i, target := range dest {
		reflect.ValueOf(target).Elem().Set(reflect.ValueOf(row[i]))
	}
	r.index++
	if r.scanned != nil {
		r.scanned()
	}
	return nil
}
func (r *freshTupleQueryRows) Err() error { return r.rowErr }
func (*freshTupleQueryRows) Close() error { return nil }

type freshTupleGraph struct {
	t                   *testing.T
	projectID           string
	resolve, discover   int
	allowNoCandidate    bool
	ambiguousCandidates bool
}

func (*freshTupleGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "tuple-producer", Epoch: 1}, nil
}
func (g *freshTupleGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	g.resolve++
	if g.ambiguousCandidates {
		pool := map[string]contextfabric.SubjectCandidate{}
		for _, projectKey := range []string{"P1", "P2"} {
			id, _, err := identity.Derive(identity.KindProject, []string{"linear", projectKey}, nil)
			if err != nil {
				g.t.Fatal(err)
			}
			candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Project Alpha", graphrank.CandidateNode{UUID: "node-" + projectKey, Name: "Project Alpha", Attributes: map[string]interface{}{"subject_kind": "project", "canonical_id": id, "label": "Project Alpha", "authorization_repositories": []string{hostedTestRepository}}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
			if !ok {
				g.t.Fatal("authorized ambiguous candidate rejected")
			}
			pool[id] = candidate
		}
		return graphrank.ResolveFromMergedCandidates(pool, nil, nil, r.Options.MaxSubjectCandidates, r.Options.AllowClarification, false, nil, 0, false, 0, 0, false), contextfabric.StructureOfferMaterial{}, nil, nil, nil
	}
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Project Alpha", graphrank.CandidateNode{UUID: "project-node", Name: "Project Alpha", Attributes: map[string]interface{}{"subject_kind": "project", "canonical_id": g.projectID, "label": "Project Alpha", "evidence_refs": []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-other:foreign")}, "authorization_repositories": []string{hostedTestRepository}}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		if g.allowNoCandidate {
			return graphrank.ResolveFromMergedCandidates(nil, nil, nil, r.Options.MaxSubjectCandidates, r.Options.AllowClarification, false, nil, 0, false, 0, 0, false), contextfabric.StructureOfferMaterial{}, nil, nil, nil
		}
		g.t.Fatal("actual candidate producer rejected project fixture")
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_tuple_project"
	// CommitBasisSet/CommitDecisionDigestSet: this fixture presents a real,
	// authorized, uniquely resolved candidate exactly as identity_fast_path
	// would. anchorBound (count_population_scope.go) requires the live basis
	// to bind a term match; a later turn naming this one's saved document as
	// its reuse candidate binds only through the PERSISTED digest twin
	// (CommitBasisSetFromDigests, chaos4085_commit_basis.go), since
	// CommitBasis itself is never persisted.
	bases := contextfabric.CommitBasisSet{}
	bases.Record(candidate.Subject, contextfabric.CommitBasisAuthoritativeIdentity)
	digests := contextfabric.CommitDecisionDigestSet{}
	digests.Record(candidate.Subject, contextfabric.CommitDecisionDigest{CommitGate: "identity_fast_path", IdentityProven: true})
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, bases, digests, nil
}
func (g *freshTupleGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	g.discover++
	return contextfabric.GraphContext{}, errors.New("tuple reached graph discovery")
}

type freshTupleModel struct {
	frame      contextfabric.QuestionFrame
	statusOnly bool
	observe    func(contextfabric.SynthesisInput)
}

func (m freshTupleModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := validRouteTestModelReceipt(contextfabric.ModelOperationInterpret)
	receipt.Outcome = "success"
	receipt.QuestionFrame = &m.frame
	receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
	receipt.ScopeAnchorKind = contextfabric.SubjectProject
	receipt.ScopeAnchorTerm = "Project Alpha"
	receipt.RequestedSubjectKind = contextfabric.SubjectWorkItem
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Project Alpha"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}}}, receipt, nil
}
func (m freshTupleModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	if m.observe != nil {
		m.observe(input)
	}
	draft := validRouteTestSynthesisDraft()
	draft.Drivers = []contextfabric.DriverJudgment{}
	draft.DirectJudgment = "Available work item evidence."
	draft.CurrentState = "Available work item evidence."
	draft.DeterministicAnswer = "Available work item evidence."
	draft.ClaimedFacts = []contextfabric.ClaimedFact{}
	// Use the current producer-owned display subject after work/title reads.
	// The fact still supplies the claim kind, value and evidence.
	subjects := map[string]contextfabric.SubjectRef{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			subjects[member.Subject.CanonicalID] = member.Subject
		}
	}
	refs := map[string]bool{}
	for index, fact := range input.Facts.Facts {
		if m.statusOnly && fact.Kind == contextfabric.FactWork {
			continue
		}
		field := "status"
		if fact.Kind == contextfabric.FactWork {
			field = "title"
		}
		if value := fact.Fields[field].String; value != nil {
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: fmt.Sprintf("claim_tuple_%d", index), Kind: fact.Kind, Subject: subjects[fact.Subject.CanonicalID], Field: field, Value: contextfabric.ScalarValue{String: value}})
			for _, ref := range fact.EvidenceRefIDs {
				if !refs[ref] {
					refs[ref] = true
					draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, ref)
				}
			}
		}
	}
	receipt := validRouteTestModelReceipt(contextfabric.ModelOperationSynthesize)
	receipt.Outcome = "success"
	return draft, receipt, nil
}

func TestWorkItemFreshHTTPUsesActualProducers(t *testing.T) {
	for _, failure := range []string{"", "status", "work", "s1", "zero"} {
		t.Run("read_"+failure, func(t *testing.T) {
			fixture := newFreshTupleProducerFixture(t, failure)
			client, graph, gate := fixture.client, fixture.graph, fixture.gate
			app, token, memberID := fixture.app, fixture.token, fixture.client.memberID
			body := investigationRequestBody()
			body.Question = "What is the state and count of this project's work items?"
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(raw))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Request-ID", "req_00000000000000000000000000000001")
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			recorder := httptest.NewRecorder()
			app.InstrumentedHandler(app.Handler()).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s; phases=%v engine=%v", recorder.Code, recorder.Body.String(), client.phases, fixture.engineErr)
			}
			var result contextfabric.InvestigationResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if graph.resolve != 1 || graph.discover != 0 {
				t.Fatalf("graph resolve=%d discover=%d", graph.resolve, graph.discover)
			}
			wantPhases := []string{"s1", "status", "work"}
			if failure == "s1" || failure == "zero" {
				wantPhases = []string{"s1"}
			}
			if !reflect.DeepEqual(client.phases, wantPhases) {
				t.Errorf("phases=%v want=%v response=%s", client.phases, wantPhases, recorder.Body.String())
			}
			if failure == "s1" {
				if result.Cohort != nil {
					t.Fatal("unmeasured S1 manufactured cohort")
				}
			} else if result.Cohort == nil {
				t.Fatal("measured S1 lost cohort")
			}
			if failure != "s1" && failure != "zero" {
				if len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != memberID || result.Cohort.Members[0].RankingComputed {
					t.Fatalf("cohort=%+v", result.Cohort)
				}
			}
			if failure == "work" && result.Cohort.Members[0].Subject.Label != "work-1" {
				t.Errorf("title fallback=%q", result.Cohort.Members[0].Subject.Label)
			}
			if failure == "" || failure == "status" {
				if result.Cohort.Members[0].Subject.Label != "Implement the thing" {
					t.Errorf("producer title lost: %+v", result.Cohort.Members[0].Subject)
				}
			}
			expectedClaims := 2
			if failure == "status" || failure == "work" {
				expectedClaims = 1
			}
			if failure == "s1" || failure == "zero" {
				expectedClaims = 0
			}
			actualClaims := 0
			for _, claim := range result.ClaimedFacts {
				if claim.Kind == contextfabric.FactStatus || claim.Kind == contextfabric.FactWork {
					actualClaims++
				}
			}
			if actualClaims != expectedClaims {
				t.Errorf("content claims=%d want=%d: %+v", actualClaims, expectedClaims, result.ClaimedFacts)
			}
			if len(result.SubjectResolution.Candidates[0].EvidenceRefIDs) != 0 {
				t.Fatal("project candidate retained foreign evidence")
			}
			assertResponseOwnerGateFree(t, gate)
			assertFreshTupleSurfaces(t, fixture, failure, result)
		})
	}
}
