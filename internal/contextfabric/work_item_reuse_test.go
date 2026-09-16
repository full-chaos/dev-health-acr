package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type tupleReuseGate struct{ stored StoredInvestigationResult }

func (g tupleReuseGate) FindReusable(context.Context, storage.Principal, ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	return g.stored, true, "", nil
}

type tupleMembershipFunc func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error)

func (f tupleMembershipFunc) BeginWorkItemMembership(ctx context.Context, p storage.Principal, r WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
	return f(ctx, p, r)
}

func tupleReuseFixture(t *testing.T) (storage.Principal, InvestigationRequest, StoredInvestigationResult, WorkItemMembershipResult) {
	t.Helper()
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api", "acme/web"}}
	request := validInvestigationRequest()
	request.RequestedScope = RequestedScope{RepositorySlugs: []string{"acme/api"}, ProjectIDs: []string{"project-1"}, TeamIDs: []string{"team-a"}}
	result := workItemTuplePayloadFixture(t)
	result.Coverage.Sources = []SourceObservation{}
	result.ResultID = "result_tuple_reused"
	result.RequestID = "request_tuple_original"
	result.Interpretation.TimeContext.Axis = TemporalCurrent
	state := workItemTupleSemanticStateFixture()
	digest, err := WorkItemAuthorizationDigest(principal, request.RequestedScope.RepositorySlugs)
	if err != nil {
		t.Fatal(err)
	}
	state.WorkItemCensus = &WorkItemTupleCensus{Version: WorkItemTupleCensusVersion, State: WorkItemMembershipCensusExact, Value: 7, Retained: 1, RequestedRepositoryScope: request.RequestedScope.RepositorySlugs, AuthorizationDigest: digest}
	current := WorkItemMembershipResult{Members: []WorkItemMembershipMember{{CanonicalID: result.Cohort.Members[0].Subject.CanonicalID}}, Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 7, ServedMembers: 1}}
	return principal, request, StoredInvestigationResult{Result: result, SemanticState: state, SemanticStateRead: SemanticStateReadAvailable}, current
}

func TestWorkItemTupleReuseFiniteDecisions(t *testing.T) {
	for _, name := range []string{"hit", "floor", "zero", "digest", "census_absent", "census_malformed", "census_unsupported", "reading_absent", "reading_malformed", "payload", "project_excluded", "anchor_unverifiable", "anchor_absent", "team_change", "state_changed", "value_changed", "identity_changed", "retained_changed", "unmeasured", "full_gate", "port_error_with_lease", "late_cancel", "repository_changed", "anchor_cancel", "nil_lease", "nil_verifier", "nil_membership", "no_owner", "permuted_members", "stored_permuted_members"} {
		t.Run(name, func(t *testing.T) {
			principal, request, stored, current := tupleReuseFixture(t)
			expectedHit := name == "hit" || name == "floor" || name == "zero" || name == "team_change" || name == "permuted_members" || name == "stored_permuted_members"
			switch name {
			case "permuted_members", "stored_permuted_members":
				secondID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-2"}, nil)
				if err != nil || omitted {
					t.Fatalf("second canonical identity: err=%v omitted=%t", err, omitted)
				}
				second := SubjectRef{Kind: SubjectWorkItem, CanonicalID: secondID, Label: "Second work item"}
				stored.Result.Cohort.Members = append(stored.Result.Cohort.Members, CohortMember{Subject: second})
				stored.SemanticState.WorkItemCensus.Retained = 2
				current.Members = append(current.Members, WorkItemMembershipMember{CanonicalID: secondID})
				current.Census.ServedMembers = 2
				if name == "permuted_members" {
					current.Members[0], current.Members[1] = current.Members[1], current.Members[0]
				} else {
					stored.Result.Cohort.Members[0], stored.Result.Cohort.Members[1] = stored.Result.Cohort.Members[1], stored.Result.Cohort.Members[0]
				}
			case "floor":
				stored.SemanticState.WorkItemCensus.State = WorkItemMembershipCensusFloor
				stored.SemanticState.WorkItemCensus.Value = WorkItemMembershipCensusLimit
				current.Census.State = WorkItemMembershipCensusFloor
				current.Census.AuthorizedPopulation = WorkItemMembershipCensusLimit + 1
			case "zero":
				stored.SemanticState.WorkItemCensus.Value = 0
				stored.SemanticState.WorkItemCensus.Retained = 0
				stored.Result.Cohort.Members = []CohortMember{}
				stored.Result.ClaimedFacts = nil
				stored.Result.RemainingWork = nil
				stored.Result.EvidenceRefIDs = nil
				stored.Result.EvidenceRefLabels = nil
				current.Members = nil
				current.Census.AuthorizedPopulation = 0
			case "repository_changed":
				request.RequestedScope.RepositorySlugs = []string{"acme/web"}
			case "digest":
				principal.RepositoryScopes = []string{"acme/other"}
			case "census_absent":
				stored.SemanticState.WorkItemCensus = nil
			case "census_malformed":
				stored.SemanticState.WorkItemCensus.Value = -1
			case "census_unsupported":
				stored.SemanticState.WorkItemCensus.Version = "future"
			case "reading_absent":
				stored.SemanticStateRead = SemanticStateReadAbsent
			case "reading_malformed":
				stored.SemanticStateRead = SemanticStateReadMalformed
			case "payload":
				stored.Result.EvidenceRefIDs = append(stored.Result.EvidenceRefIDs, "foreign")
			case "project_excluded":
				request.RequestedScope.ProjectIDs = []string{"other-project"}
			case "team_change":
				request.RequestedScope.TeamIDs = []string{"different-team"}
			case "state_changed":
				stored.SemanticState.WorkItemCensus.Value = WorkItemMembershipCensusLimit
				current.Census.State = WorkItemMembershipCensusFloor
				current.Census.AuthorizedPopulation = WorkItemMembershipCensusLimit + 1
			case "value_changed":
				current.Census.AuthorizedPopulation = 8
			case "identity_changed":
				current.Members[0].CanonicalID = "another-member"
			case "retained_changed":
				current.Members = nil
			case "unmeasured":
				current.Census.PopulationMeasured = false
			}
			gate, err := NewWorkItemMembershipGate(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			var blocked *WorkItemMembershipLease
			if name == "full_gate" {
				blocked, err = gate.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer blocked.Release()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, owner := NewWorkItemResponseOwnerContext(ctx)
			defer owner.Complete()
			anchors, reads := 0, 0
			engine := mustReuseTestEngine(t, EngineDependencies{ReuseGate: tupleReuseGate{stored}, CandidateVerifier: func(_ context.Context, p storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind SubjectKind, id string) (bool, CandidateVerificationReason) {
				anchors++
				if !reflect.DeepEqual(scope, request.RequestedScope) || !reflect.DeepEqual(p, principal) || binding.Epoch != 12 || kind != SubjectProject || id != "project-1" {
					t.Fatalf("live anchor inputs lost: p=%+v scope=%+v binding=%+v kind=%s id=%s", p, scope, binding, kind, id)
				}
				if name == "anchor_cancel" {
					cancel()
				}
				if name == "anchor_unverifiable" {
					return false, CandidateVerificationGraphUnverifiable
				}
				if name == "anchor_absent" || !reflect.DeepEqual(scope.ProjectIDs, []string{"project-1"}) {
					return false, CandidateVerificationClaimLost
				}
				return true, CandidateVerificationValid
			}, WorkItemMembership: tupleMembershipFunc(func(c context.Context, p storage.Principal, r WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
				reads++
				if !reflect.DeepEqual(r.RequestedRepositoryScope, request.RequestedScope.RepositorySlugs) || !reflect.DeepEqual(p, principal) || r.PlanMaxMembers != stored.Result.AnswerPlan.Budget.MaxMembers || r.RequestMaxMembers != request.Options.MaxCohortMembers {
					t.Fatal("S1 lost current authorization inputs")
				}
				if name == "nil_lease" {
					return nil, current, nil
				}
				lease, err := gate.Acquire(c)
				if err != nil {
					return nil, WorkItemMembershipResult{}, err
				}
				if err := owner.Retain(lease); err != nil {
					return nil, WorkItemMembershipResult{}, err
				}
				if name == "port_error_with_lease" {
					return lease, current, errors.New("S1 error")
				}
				if name == "late_cancel" {
					cancel()
				}
				return lease, current, nil
			})})
			if name == "nil_verifier" {
				engine.candidateVerifier = nil
			}
			if name == "nil_membership" {
				engine.workItemMembership = nil
			}
			if name == "no_owner" {
				ctx = context.Background()
			}
			var result InvestigationResult
			var hit, tuple bool
			var reuseErr error
			func() {
				defer func() {
					if panicValue := recover(); panicValue != nil {
						t.Fatalf("ordinary-miss invariant: reuse panicked instead of returning: %v", panicValue)
					}
				}()
				result, hit, tuple, reuseErr = engine.tryReuse(ctx, principal, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{Epoch: 12})
			}()
			if reuseErr != nil {
				t.Fatalf("unexpected serving error: %v", reuseErr)
			}
			if hit != expectedHit || tuple != expectedHit {
				t.Fatalf("hit=%t tuple=%t want=%t", hit, tuple, expectedHit)
			}
			wantAnchors, wantReads := 1, 1
			switch name {
			case "digest", "census_absent", "census_malformed", "census_unsupported", "reading_absent", "reading_malformed", "payload", "repository_changed", "nil_verifier":
				wantAnchors, wantReads = 0, 0
			case "project_excluded", "anchor_unverifiable", "anchor_absent", "anchor_cancel", "nil_membership", "no_owner":
				wantReads = 0
			}
			if anchors != wantAnchors || reads != wantReads {
				t.Fatalf("anchor=%d S1=%d want anchor=%d S1=%d", anchors, reads, wantAnchors, wantReads)
			}
			if hit {
				if !result.Reused || result.ResultID != stored.Result.ResultID {
					t.Fatal("not stored reused result")
				}
				if gate.Stats().InFlight != 1 {
					t.Fatal("hit lost response permit")
				}
				owner.Complete()
			}
			if blocked != nil {
				blocked.Release()
			}
			if got := gate.Stats().InFlight; got != 0 {
				t.Fatalf("declined lease blocks fresh work: occupied=%d", got)
			}
			fresh, err := gate.Acquire(context.Background())
			if err != nil {
				t.Fatalf("fresh fallback cannot acquire: %v", err)
			}
			fresh.Release()
		})
	}
}

func TestWorkItemTupleEngineKeepsPersistedPopulationWithoutBackfill(t *testing.T) {
	principal, request, stored, current := tupleReuseFixture(t)
	base := validInvestigationResult()
	payload := stored.Result
	base.ResultID = payload.ResultID
	base.RequestID = payload.RequestID
	base.SubjectResolution = payload.SubjectResolution
	base.Cohort = payload.Cohort
	base.Cohort.Complete = true
	base.EvidenceRefIDs = payload.EvidenceRefIDs
	base.EvidenceRefLabels = payload.EvidenceRefLabels
	base.ClaimedFacts = payload.ClaimedFacts
	base.RemainingWork = payload.RemainingWork
	base.AnswerPlan = payload.AnswerPlan
	base.AnswerPlan.FamilySource = QuestionFamilySourceModel
	base.AnswerPlan.FamilyVersion = QuestionFamilyTableVersion
	base.AnswerPlan.Requirements = []contractsv1.ContextFabricPlanRequirement{{Requirement: "count/member/work_item", Obligation: "count", Role: "member", Subject: SubjectWorkItem, Kind: "computed", Step: "membership_cardinality", StepExecution: "server_executed", InputClass: "resolved_member_set", Scope: "each_member", Quantifier: "exact"}}
	rows := []RequirementOutcomeRow{{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: "count/member/work_item", Obligation: "count", Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Declared: 1, Served: 1}}
	census := MembershipCardinality{Resolved: true, Kind: SubjectWorkItem, Declared: 7, Served: 1}
	rows = append(rows, membershipCardinalityOutcomeRow(census, "count/member/work_item", "count"))
	base.Completeness.Outcomes = rows
	base.Completeness = ComputeAnswerCompleteness(base)
	base.DeterministicAnswer = cardinalityAnswerSentence(census)
	stored.Result = base
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{ReuseGate: tupleReuseGate{stored}, CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
		return true, CandidateVerificationValid
	}, WorkItemMembership: tupleMembershipFunc(func(c context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		lease, err := gate.Acquire(c)
		return lease, current, err
	})})
	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reused || result.DeterministicAnswer != base.DeterministicAnswer {
		t.Fatalf("tuple census sentence changed: got=%q want=%q reused=%t", result.DeterministicAnswer, base.DeterministicAnswer, result.Reused)
	}
	if !reflect.DeepEqual(result.Completeness.Outcomes, base.Completeness.Outcomes) {
		t.Fatalf("stored census outcomes changed: got=%+v want=%+v", result.Completeness.Outcomes, base.Completeness.Outcomes)
	}
	if gate.Stats().InFlight != 0 {
		t.Fatal("direct successful Engine call leaked permit")
	}

	borrowedContext, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	if result, err := engine.Investigate(borrowedContext, principal, request); err != nil || !result.Reused {
		t.Fatalf("borrowed owner hit: reused=%t error=%v", result.Reused, err)
	}
	if gate.Stats().InFlight != 1 {
		t.Fatal("Engine completed its caller's owner on a hit")
	}
	owner.Complete()
	if gate.Stats().InFlight != 0 {
		t.Fatal("caller completion leaked hit permit")
	}
}

// TestWorkItemTupleReuseHitSettlesAdmissionAndStrips pins that a reuse HIT
// is this arm's admission decision settling too, exactly as the fresh
// path's own settlement point does (engine.go, beside
// workItemTupleEffectiveObligations's own doc comment): a hit is this
// decision's only settlement point on this path, since there is no later
// tighten call left to still reverse it, so it owes the same observable
// line -- computed here, PURELY, from the reading persisted beside the
// served row. CHAOS-5787: that persisted reading's frame is NEVER
// mutated -- it stays canonical for any later turn's composition boundary
// to revalidate.
func TestWorkItemTupleReuseHitSettlesAdmissionAndStrips(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	principal, request, stored, current := tupleReuseFixture(t)
	// AnchorTerms must match the payload fixture's own resolved candidate
	// term ("project", workItemTuplePayloadFixture's MatchedTerms) -- the
	// count-population-scope decision above the work-item branch binds the
	// anchor by that term, and this scenario must still reach the work-item
	// branch to exercise it.
	surveyFrame := frameWith([]InvestigationGoal{GoalRankOrSurvey}, SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"project"}, MemberKind: SubjectWorkItem},
	}, TemporalIntentCurrent, nil)
	stored.SemanticState.Frame = &surveyFrame
	if !surveyFrame.HasObligation(ObligationRanking) {
		t.Fatalf("fixture frame %+v does not carry ranking: cannot pin a strip with nothing to strip", surveyFrame)
	}
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{Telemetry: telemetry, ReuseGate: tupleReuseGate{stored}, CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
		return true, CandidateVerificationValid
	}, WorkItemMembership: tupleMembershipFunc(func(c context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		lease, err := gate.Acquire(c)
		return lease, current, err
	})})
	ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	_, hit, tuple, _, reuseErr := engine.tryReuseWithReading(ctx, principal, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{})
	if reuseErr != nil {
		t.Fatalf("unexpected serving error: %v", reuseErr)
	}
	if !hit || !tuple {
		t.Fatalf("hit=%t tuple=%t, want both true", hit, tuple)
	}
	if !surveyFrame.HasObligation(ObligationRanking) {
		t.Fatalf("BUG: the reuse hit mutated the persisted reading's frame -- ranking is gone, obligations=%v", surveyFrame.Obligations)
	}
	if len(telemetry.workItemTupleAdmissions) != 1 {
		t.Fatalf("settled admission lines = %d, want exactly 1", len(telemetry.workItemTupleAdmissions))
	}
	settled := telemetry.workItemTupleAdmissions[0]
	if !settled.Admitted {
		t.Fatalf("settled admission = %+v, want Admitted=true", settled)
	}
	if !reflect.DeepEqual(settled.StrippedObligations, []AnswerObligation{ObligationRanking}) {
		t.Fatalf("settled admission = %+v, want StrippedObligations=[ranking]", settled)
	}
}

func TestWorkItemTupleReuseDecisionUsesConfiguredLogger(t *testing.T) {
	principal, request, stored, current := tupleReuseFixture(t)
	request.RequestedScope.TeamIDs = []string{"team-new"}
	var output bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&output, nil)))
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{Telemetry: telemetry, ReuseGate: tupleReuseGate{stored}, CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
		return true, CandidateVerificationValid
	}, WorkItemMembership: tupleMembershipFunc(func(c context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		lease, err := gate.Acquire(c)
		return lease, current, err
	})})
	ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	_, hit, _, _ := engine.tryReuse(ctx, principal, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{})
	if !hit {
		t.Fatal("expected measured hit")
	}
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry["msg"] != "context fabric work item reuse" {
			continue
		}
		found = true
		if entry["decision"] != "hit" || entry["semantic_read"] != "available" || entry["census_read"] != "available" || !reflect.DeepEqual(entry["requested_team_ids"], []any{"team-new"}) {
			t.Fatalf("decision basis=%v", entry)
		}
	}
	if !found {
		t.Fatal("configured logger has no tuple decision")
	}
}
