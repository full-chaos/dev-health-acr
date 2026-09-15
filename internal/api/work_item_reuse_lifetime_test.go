package api

import (
	"context"
	"encoding/json"
	"errors"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type lifetimeTupleReuseGate struct {
	stored contextfabric.StoredInvestigationResult
}

func (g lifetimeTupleReuseGate) FindReusable(context.Context, storage.Principal, contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	return g.stored, true, "", nil
}

type lifetimeTupleGraph struct{ calls int }

func (g *lifetimeTupleGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}
func (g *lifetimeTupleGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	g.calls++
	return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, nil, nil, errors.New("tuple reuse reached ResolveSubjects")
}
func (g *lifetimeTupleGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	g.calls++
	return contextfabric.GraphContext{}, errors.New("tuple reuse reached DiscoverContext")
}

type lifetimeTupleClient struct {
	id, repo, work string
	count          uint64
	calls          int
	panicQuery     bool
}

func (c *lifetimeTupleClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls++
	if c.panicQuery {
		panic("S1 panic immediately after admission")
	}
	return &lifetimeTupleRows{client: c}, nil
}

type lifetimeTupleRows struct {
	client *lifetimeTupleClient
	index  int
}

func (r *lifetimeTupleRows) Next() bool { return r.index < 2 }
func (r *lifetimeTupleRows) Scan(dest ...any) error {
	// Successful S1 has one member and one identity-free resolved sentinel.
	values := []any{r.client.id, r.client.repo, r.client.work, hostedTestRepository, uint8(1), r.client.count, r.client.count, uint64(0), uint64(0), uint64(0), uint8(0), uint8(1)}
	if r.index == 1 {
		values = []any{"", "", "", "", uint8(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint8(1), uint8(1)}
	}
	if len(dest) != len(values) {
		return errors.New("unexpected S1 scan width")
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = values[i].(string)
		case *uint8:
			*p = values[i].(uint8)
		case *uint64:
			*p = values[i].(uint64)
		default:
			return errors.New("unexpected S1 scan type")
		}
	}
	r.index++
	return nil
}
func (r *lifetimeTupleRows) Err() error   { return nil }
func (r *lifetimeTupleRows) Close() error { return nil }

// This drives the actual Engine and Begin reader through the full hosted
// middleware/route. Only the database query transport supplies fixed rows;
// permit registration, S1 decode, reuse, response fitting and writing execute.
func TestWorkItemTupleEngineS1PermitSurvivesHTTPWrite(t *testing.T) {
	for _, name := range []string{"hit", "s1_panic", "budget_rejection", "writer_error", "writer_partial", "cancel_during_write", "reuse_miss_fresh_continuation"} {
		panics := name == "s1_panic"
		t.Run(name, func(t *testing.T) {
			principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{hostedTestRepository}}
			result, state := lifetimeTupleFixture(t, principal)
			segments, ok := identity.Segments(identity.KindWorkItem, result.Cohort.Members[0].Subject.CanonicalID)
			if !ok || len(segments) != 2 {
				t.Fatal("invalid fixture member")
			}
			client := &lifetimeTupleClient{id: result.Cohort.Members[0].Subject.CanonicalID, repo: segments[0], work: segments[1], count: 1, panicQuery: panics}
			if name == "reuse_miss_fresh_continuation" {
				client.count = 2
			}
			gate := newResponseOwnerAPITestGate(t)
			reader, err := devhealthfacts.NewWorkItemMembershipReader(client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
			if err != nil {
				t.Fatal(err)
			}
			graph := &lifetimeTupleGraph{}
			anchors, freshCalls := 0, 0
			byteLimit := int64(0)
			if name == "budget_rejection" {
				byteLimit = 1
			}
			interpreter := lifetimeTupleInterpreter(func(c context.Context, p storage.Principal, request contextfabric.InvestigationRequest) error {
				freshCalls++
				if name != "reuse_miss_fresh_continuation" {
					return errors.New("unexpected fresh interpretation")
				}
				// Fresh admission is not activated by this PR. This continuation
				// fixture uses the actual Begin port to measure whether a reuse
				// miss removed its lease before normal fresh work can proceed.
				_, _, err := reader.BeginWorkItemMembership(c, p, contextfabric.WorkItemMembershipRequest{Anchor: contextfabric.WorkItemMembershipAnchor{Subject: result.SubjectResolution.Committed[0]}, RequestedRepositoryScope: request.RequestedScope.RepositorySlugs})
				if err != nil {
					return err
				}
				return errors.New("fresh continuation after acquired S1")
			})
			engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
				Interpreter: interpreter, Graph: graph, Facts: surfaceFacts{t}, Synthesizer: surfaceSynthesizer{t},
				ReuseGate: lifetimeTupleReuseGate{contextfabric.StoredInvestigationResult{Result: result, SemanticState: state, SemanticStateRead: contextfabric.SemanticStateReadAvailable}},
				CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
					anchors++
					return true, contextfabric.CandidateVerificationValid
				},
				WorkItemMembership: reader,
			}, contextfabric.EngineOptions{ServiceVersion: "test", MaxSerializedBytes: byteLimit, NewResultID: func() string { return "result_fresh_unused" }})
			if err != nil {
				t.Fatal(err)
			}
			app, token := newContextFabricTestApp(t, engine)
			app.config.RequestTimeout = 5 * time.Second
			writer := newResponseOwnerBlockingWriter()
			if name == "writer_error" || name == "writer_partial" {
				writer.writeErr = errors.New("client disconnected")
			}
			if name == "writer_partial" {
				writer.partialN = 4
			}
			request := investigationRequest(t, token)
			requestContext, cancel := context.WithCancel(request.Context())
			defer cancel()
			request = request.WithContext(requestContext)
			done := startResponseOwnerHandler(app.InstrumentedHandler(app.Handler()), writer, request)
			defer func() {
				select {
				case <-writer.unblock:
				default:
					close(writer.unblock)
				}
			}()
			waitForResponseOwnerWrite(t, writer)
			assertResponseOwnerGateHeld(t, gate)
			wantS1, wantFresh := 1, 0
			if name == "reuse_miss_fresh_continuation" {
				wantS1, wantFresh = 2, 1
			}
			if client.calls != wantS1 || anchors != 1 || graph.calls != 0 || freshCalls != wantFresh {
				t.Fatalf("S1=%d anchors=%d forbidden graph calls=%d", client.calls, anchors, graph.calls)
			}
			if name == "cancel_during_write" {
				cancel()
				assertResponseOwnerGateHeld(t, gate)
			}
			close(writer.unblock)
			if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
				t.Fatalf("handler escaped panic=%v", recovered)
			}
			assertResponseOwnerGateFree(t, gate)
			if panics || name == "reuse_miss_fresh_continuation" {
				if writer.status != http.StatusInternalServerError {
					t.Fatalf("panic response=%d body=%s", writer.status, writer.body.String())
				}
				return
			}
			if name == "budget_rejection" {
				if writer.status != http.StatusRequestEntityTooLarge {
					t.Fatalf("budget status=%d body=%s", writer.status, writer.body.String())
				}
				return
			}
			if name == "writer_error" || name == "writer_partial" {
				if writer.lastN != writer.partialN {
					t.Fatal("writer failure was not exercised")
				}
				return
			}
			if writer.status != http.StatusOK {
				t.Fatalf("hit response=%d body=%s", writer.status, writer.body.String())
			}
			var served contextfabric.InvestigationResult
			if err := json.Unmarshal(writer.body.Bytes(), &served); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(served.ClaimedFacts, result.ClaimedFacts) {
				t.Fatal("HTTP reuse lost retained-member status/title")
			}
			if !served.Reused || served.ResultID != result.ResultID || len(served.Cohort.Members) != 1 {
				t.Fatalf("not stored tuple hit: %+v", served)
			}

		})
	}
}

func lifetimeTupleFixture(t *testing.T, principal storage.Principal) (contextfabric.InvestigationResult, *contextfabric.PersistedSemanticState) {
	t.Helper()
	result := validContextFabricInvestigationResult()
	anchorID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", "P1"}, nil)
	if err != nil || omitted {
		t.Fatalf("project fixture identity: %v", err)
	}
	memberID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-1"}, nil)
	if err != nil || omitted {
		t.Fatalf("member fixture identity: %v", err)
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: anchorID, Label: "Project Alpha"}
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: memberID, Label: "Work item"}
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-1:work-1")
	result.SubjectResolution = contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{{ReceiptID: "receipt-project", Subject: anchor, State: contextfabric.ResolutionCommitted, MatchReasons: []string{"exact"}}}, Committed: []contextfabric.SubjectRef{anchor}}
	result.Cohort = &contextfabric.Cohort{Kind: contextfabric.SubjectWorkItem, Rationale: "project members", Complete: true, Members: []contextfabric.CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"project_membership"}, EvidenceRefIDs: []string{ref}}}}
	result.EvidenceRefIDs = []string{ref}
	result.EvidenceRefLabels = map[string]string{ref: "work item"}
	status, title := "open", "Implement the thing"
	result.ClaimedFacts = []contextfabric.ClaimedFact{
		{ClaimID: "claim-status", Kind: contextfabric.FactStatus, Subject: member, Field: "status", Value: contextfabric.ScalarValue{String: &status}},
		{ClaimID: "claim-title", Kind: contextfabric.FactWork, Subject: member, Field: "title", Value: contextfabric.ScalarValue{String: &title}},
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contextfabric.ValidateResult(result); err != nil {
		t.Fatal(err)
	}
	if err := contextfabric.ValidateWorkItemTuplePayload(result, principal); err != nil {
		t.Fatal(err)
	}
	validation := contextfabric.ValidateFrame(contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: contextfabric.SubjectWorkItem}}, Temporal: contextfabric.TemporalIntentCurrent}, []contextfabric.AnswerObligation{contextfabric.ObligationState}, contextfabric.ShapeDiscoveredCohort)
	frame := validation.Frame
	digest, err := contextfabric.WorkItemAuthorizationDigest(principal, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := contextfabric.BuildSemanticState(contextfabric.SemanticStateInput{Outcome: contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel, Frame: &frame, Gate: contextfabric.DecideFrameGate(validation, true), WinningSample: contextfabric.FamilySample{ModelFamily: contextfabric.QuestionFamilyScopedCohortStatus, ScopeAnchorKind: contextfabric.SubjectProject, ScopeAnchorTerm: "Project Alpha"}}, EmittedShape: contextfabric.ShapeDiscoveredCohort, FamilyVersion: contextfabric.QuestionFamilyTableVersion, WorkItemCensus: &contextfabric.WorkItemTupleCensus{Version: contextfabric.WorkItemTupleCensusVersion, State: contextfabric.WorkItemMembershipCensusExact, Value: 1, Retained: 1, RequestedRepositoryScope: []string{}, AuthorizationDigest: digest}})
	encoded, err := contextfabric.EncodeSemanticState(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, readStatus := contextfabric.DecodeSemanticState(encoded)
	if readStatus != contextfabric.SemanticStateReadAvailable {
		t.Fatal(readStatus)
	}
	return result, decoded
}

type lifetimeTupleInterpreter func(context.Context, storage.Principal, contextfabric.InvestigationRequest) error

func (f lifetimeTupleInterpreter) Interpret(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.QuestionFamilyOutcome{}, f(ctx, p, r)
}
