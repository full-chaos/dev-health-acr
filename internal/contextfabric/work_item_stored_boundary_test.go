package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemStoredUnavailableAuthorizationProof(t *testing.T) {
	for _, p := range []storage.Principal{{}, {OrgID: "org-1"}, {OrgID: "org-1", RepositoryScopes: []string{"org/repo"}}, {OrgID: "org-1", RepositoryScopes: []string{"org/*"}}, {RepositoryScopes: []string{"*"}}, {OrgID: "org-1", RepositoryScopes: []string{"*"}}, {OrgID: "org-1", RepositoryScopes: []string{"org/repo", "*"}}} {
		for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationNoMatch, InvestigationClarificationRequired} {
			candidate := workItemTuplePayloadFixture(t)
			candidate.Status = status
			candidate.Coverage.Sources = []SourceObservation{}
			got := ServeStoredWorkItemTuple(candidate, workItemTupleSemanticStateFixture(), SemanticStateReadAvailable, p)
			want := p.OrgID != "" && (len(p.RepositoryScopes) > 0 && p.RepositoryScopes[len(p.RepositoryScopes)-1] == "*")
			if (got.Disposition == WorkItemTupleByIDStored) != want || got.Err != nil {
				t.Errorf("principal=%+v status=%s decision=%+v want proof=%t", p, status, got, want)
			}
		}
	}
}

func boundaryCoverage(n int) Coverage {
	c := Coverage{Sources: []SourceObservation{}, Partial: true}
	for i := 0; i < n; i++ {
		d := storedWorkItemCensusDetail(SubjectRepository, 3000+i, 1)
		d.DetailID = fmt.Sprintf("cov-%03d", i)
		c.Details = append(c.Details, d)
		c.DegradedReasons = append(c.DegradedReasons, d.Raw)
	}
	return c
}

func TestWorkItemStoredCoverageValidationPreservesTruth(t *testing.T) {
	p := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"org/repo"}}
	for _, mode := range []string{"99", "100", "existing", "exact", "unmeasured", "unauthorized", "unavailable_invalid_coverage"} {
		t.Run(mode, func(t *testing.T) {
			result := workItemTuplePayloadFixture(t)
			result.Coverage = boundaryCoverage(100)
			if mode == "99" {
				result.Coverage = boundaryCoverage(99)
			}
			state := workItemTupleSemanticStateFixture()
			state.WorkItemCensus = validStoredWorkItemCensus(t, p, WorkItemMembershipCensusFloor, 2000, 1, p.RepositoryScopes)
			if mode == "existing" {
				result.Coverage.Details[0] = storedWorkItemCensusDetail(SubjectWorkItem, 2000, 0)
				result.Coverage.DegradedReasons[0] = result.Coverage.Details[0].Raw
			}
			if mode == "exact" {
				state.WorkItemCensus.State = WorkItemMembershipCensusExact
				state.WorkItemCensus.Value = 1
			}
			if mode == "unmeasured" {
				state.WorkItemCensus.State = WorkItemMembershipCensusUnmeasured
				state.WorkItemCensus.Value = 0
				state.WorkItemCensus.Retained = 0
			}
			before, _ := json.Marshal(result)
			current := p
			if mode == "unavailable_invalid_coverage" {
				// Direct historical-carrier guard: this malformed coverage is not
				// claimed to pass the current persistence write validator.
				result.Coverage = boundaryCoverage(101)
				before, _ = json.Marshal(result)
				state.WorkItemCensus = nil
				current.RepositoryScopes = []string{"*"}
			}
			if mode == "unauthorized" {
				current.RepositoryScopes = []string{"other/repo"}
			}
			got := ServeStoredWorkItemTuple(result, state, SemanticStateReadAvailable, current)
			if mode == "unauthorized" {
				if got.Disposition != WorkItemTupleByIDNotFound || got.Err != nil || got.Event.Basis != "digest_changed" {
					t.Fatalf("authorization priority=%+v", got)
				}
			} else if mode == "100" || mode == "unavailable_invalid_coverage" {
				if !errors.Is(got.Err, ErrInvalidResult) || got.Event.Basis != "coverage_invalid" || got.Event.DetailsAfter != 101 || got.Event.ReasonsAfter != 101 {
					t.Fatalf("capacity failure=%+v", got)
				}
			} else {
				if got.Err != nil || got.Result.Coverage.Validate() != nil {
					t.Fatalf("valid boundary rejected: %+v", got)
				}
				second := ServeStoredWorkItemTuple(got.Result, state, SemanticStateReadAvailable, current)
				firstJSON, _ := json.Marshal(got.Result)
				secondJSON, _ := json.Marshal(second.Result)
				if second.Err != nil || !bytes.Equal(firstJSON, secondJSON) {
					t.Fatal("repeated serving changed result")
				}
			}
			after, _ := json.Marshal(result)
			if !bytes.Equal(before, after) {
				t.Fatal("serving changed stored coverage/payload")
			}
		})
	}
}

func TestWorkItemStoredServingConfiguredInfo(t *testing.T) {
	var logs bytes.Buffer
	logger := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	for _, basis := range []string{"digest_matched", "digest_changed", "authorization_unverifiable", "universal_grant", "coverage_invalid"} {
		logs.Reset()
		event := WorkItemStoredServingEvent{Surface: StoredAnswerabilitySurfaceReuse, Basis: basis, SemanticRead: SemanticStateReadAvailable, CensusRead: WorkItemTupleCensusReadAvailable, DetailsBefore: 100, ReasonsBefore: 100, DetailsAfter: 101, ReasonsAfter: 101}
		logger.RecordWorkItemStoredServing(context.Background(), storage.Principal{OrgID: "org-1"}, event)
		var got map[string]any
		if err := json.Unmarshal(logs.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["level"] != "INFO" || got["msg"] != WorkItemStoredServingLogMessage || got["basis"] != basis || got["surface"] != "reuse" || got["coverage_details_after"] != float64(101) || got["coverage_bound"] != float64(contractsv1.ContextFabricCoverageEntriesMaxCount) {
			t.Fatalf("event=%v", got)
		}
	}
}

func TestWorkItemStoredReuseCoverageErrorDoesNotRestart(t *testing.T) {
	principal, request, stored, current := tupleReuseFixture(t)
	stored.Result.Coverage = boundaryCoverage(100)
	stored.SemanticState.WorkItemCensus.State = WorkItemMembershipCensusFloor
	stored.SemanticState.WorkItemCensus.Value = WorkItemMembershipCensusLimit
	current.Census.State = WorkItemMembershipCensusFloor
	current.Census.AuthorizedPopulation = WorkItemMembershipCensusLimit + 1
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	interpreter := &countingInterpreter{}
	store := &resultStoreStub{}
	reads := 0
	anchors := 0
	var logs bytes.Buffer
	engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: interpreter, Results: store, ReuseGate: tupleReuseGate{stored}, Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&logs, nil))), CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
		anchors++
		return true, CandidateVerificationValid
	}, WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		reads++
		lease, err := gate.Acquire(ctx)
		return lease, current, err
	})})
	before, _ := json.Marshal(stored)
	result, err := engine.Investigate(context.Background(), principal, request)
	if !errors.Is(err, ErrInvalidResult) || result.ResultID != "" {
		t.Fatalf("accepted reuse error=%v result=%+v", err, result)
	}
	if interpreter.calls != 0 || store.saved.ResultID != "" || reads != 1 || anchors != 1 {
		t.Fatalf("fresh work after accepted error: interpret=%d saved=%s S1=%d anchors=%d", interpreter.calls, store.saved.ResultID, reads, anchors)
	}
	if gate.Stats().InFlight != 0 {
		t.Fatal("method fallback leaked accepted-error lease")
	}
	if bytes.Contains(logs.Bytes(), []byte("context fabric answer reuse outcome")) {
		t.Fatal("accepted serving error was reported as generic reuse outcome")
	}
	decisions := 0
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		var event map[string]any
		if json.Unmarshal(line, &event) == nil && event["msg"] == WorkItemStoredServingLogMessage {
			decisions++
			if event["level"] != "INFO" || event["basis"] != "coverage_invalid" || event["surface"] != "reuse" || event["coverage_details_before"] != float64(100) || event["coverage_details_after"] != float64(101) || event["coverage_bound"] != float64(100) {
				t.Errorf("accepted failure decision=%v", event)
			}
		}
	}
	if decisions != 1 {
		t.Fatalf("stored serving Info decisions=%d want1", decisions)
	}
	after, _ := json.Marshal(stored)
	if !bytes.Equal(before, after) {
		t.Fatal("accepted error rewrote stored carrier")
	}
}
