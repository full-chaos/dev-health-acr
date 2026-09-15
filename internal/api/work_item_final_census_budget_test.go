package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// All input rows pass through the real census/content readers, synthesis,
// response validation and codec store. No rendered result is authored here.
func newFinalCensusBudgetFixture(t *testing.T, floor, reserve bool) (*freshTupleProducerFixture, *bytes.Buffer, *[][]string) {
	t.Helper()
	f := newFreshTupleProducerFixture(t, "")
	f.model.statusOnly = true
	f.client.rowsByPhase = map[string][][]any{}
	count := uint64(2000)
	if floor {
		count++
	}
	for i := 0; i < 12; i++ {
		workID := fmt.Sprintf("work-%02d", i)
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", workID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.client.rowsByPhase["s1"] = append(f.client.rowsByPhase["s1"], []any{id, "repo-1", workID, hostedTestRepository, uint8(1), count, count, uint64(0), uint64(0), uint64(0)})
		f.client.rowsByPhase["status"] = append(f.client.rowsByPhase["status"], []any{workID, "open", "repo-1"})
		f.client.rowsByPhase["work"] = append(f.client.rowsByPhase["work"], []any{workID, strings.Repeat("Long descriptive title ", 21) + workID, "repo-1"})
	}
	info := &bytes.Buffer{}
	f.dependencies.Telemetry = contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(info, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if !reserve {
		f.engineOptions.SynthesisDeadlineReserve = 0
	}
	engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = engine
	attempts := [][]string{}
	f.model.observe = func(input contextfabric.SynthesisInput) {
		if input.Graph.Cohort == nil {
			t.Fatal("synthesis lost its retained cohort")
		}
		ids := []string{}
		for _, m := range input.Graph.Cohort.Members {
			ids = append(ids, m.Subject.CanonicalID)
		}
		sort.Strings(ids)
		attempts = append(attempts, ids)
		// This marker uses the same collected logger bytes, at actual model entry.
		info.WriteString(fmt.Sprintf("{\"msg\":\"test synthesis entered\",\"attempt\":%d}\n", len(attempts)))
	}
	return f, info, &attempts
}

func finalCensusBudgetRequest() contextfabric.InvestigationRequest {
	body := investigationRequestBody()
	body.Options.MaxCohortMembers = 12
	body.Question = "What is the state and count of Project Alpha work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	return body
}

func TestWorkItemFinalCensusParticipatesInBudgetRetry(t *testing.T) {
	body := finalCensusBudgetRequest()
	initial, _, _ := newFinalCensusBudgetFixture(t, true, true)
	baseline := serveFreshTupleRequest(t, initial, body)
	measured, err := contractsv1.MeasureContextFabricResponse(baseline)
	if err != nil {
		t.Fatal(err)
	}
	budget := int(measured.Bytes) - 200
	for _, tc := range []struct {
		name                           string
		floor, reserve                 bool
		members, budget, calls, status int
	}{
		{"floor_retry", true, true, 12, budget, 2, http.StatusOK},
		{"floor_retry_authority_on", true, true, 12, budget, 2, http.StatusOK},
		{"exact_control", false, true, 12, budget, 1, http.StatusOK},
		{"exact_control_authority_on", false, true, 12, budget, 1, http.StatusOK},
		{"six_member_control", true, true, 6, budget, 1, http.StatusOK},
		{"no_reserve", true, false, 12, budget, 1, http.StatusRequestEntityTooLarge},
		{"impossible_fit", true, true, 12, 8192, 2, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, info, attempts := newFinalCensusBudgetFixture(t, tc.floor, tc.reserve)
			if strings.HasSuffix(tc.name, "authority_on") {
				f.engineOptions.ServerCompletenessAuthorityEnabled = true
				engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
				if err != nil {
					t.Fatal(err)
				}
				f.engine = engine
			}
			request := body
			request.Options.MaxCohortMembers = tc.members
			request.Options.MaxSerializedBytes = tc.budget
			response := roundTripFreshTupleRequest(t, f, request)
			t.Logf("initial_bytes=%d budget=%d status=%d synthesis_calls=%d body_bytes=%d", measured.Bytes, tc.budget, response.Code, len(*attempts), response.Body.Len())
			if response.Code != tc.status || len(*attempts) != tc.calls {
				t.Fatalf("budget retry mechanism: HTTP%d synthesis%d want HTTP%d synthesis%d: %s", response.Code, len(*attempts), tc.status, tc.calls, response.Body.String())
			}
			if !reflect.DeepEqual(f.client.phases, []string{"s1", "status", "work"}) || f.graph.resolve != 1 || f.graph.discover != 0 {
				t.Fatalf("unexpected reads: phases=%v resolve=%d discover=%d", f.client.phases, f.graph.resolve, f.graph.discover)
			}
			assertResponseOwnerGateFree(t, f.gate)
			if tc.calls == 2 && !reflect.DeepEqual((*attempts)[1], (*attempts)[0][:tc.members/2]) {
				t.Fatalf("retry did not retain the canonical lexical half: %v", *attempts)
			}
			var finalBytes int64
			if tc.status == http.StatusOK {
				var result contextfabric.InvestigationResult
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				measurement, err := contractsv1.MeasureContextFabricResponse(result)
				if err != nil {
					t.Fatal(err)
				}
				finalBytes = measurement.Bytes
				if err := result.Validate(); err != nil {
					t.Fatalf("final result invalid: %v", err)
				}
				if len(result.Cohort.Members) != tc.members/tc.calls {
					t.Fatalf("served members=%d", len(result.Cohort.Members))
				}
				if err := contextfabric.ValidateWorkItemTuplePayload(result, f.principal); err != nil {
					t.Fatal(err)
				}
				stored, err := f.store.Get(context.Background(), f.principal, result.ResultID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(stored.Result, result) {
					t.Fatal("served result differs from codec-persisted result")
				}
				census := stored.SemanticState.WorkItemCensus
				if census == nil || census.Value != 2000 || census.Retained != len(result.Cohort.Members) {
					t.Fatalf("stored census=%+v", census)
				}
				details := 0
				for _, d := range result.Coverage.Details {
					if d.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
						details++
						if d.Declared == nil || *d.Declared != 2000 || d.Served == nil || *d.Served != len(result.Cohort.Members) {
							t.Fatalf("D47 census=%+v", d)
						}
					}
				}
				if tc.floor && (details != 1 || !strings.Contains(result.DeterministicAnswer, "at least 2000")) {
					t.Fatalf("floor census lost: details%d answer=%s", details, result.DeterministicAnswer)
				}
				if !tc.floor && details != 0 {
					t.Fatal("exact census gained a floor disclosure")
				}
			} else {
				var refusal contextfabric.AnswerBudgetRefusal
				if !errors.As(f.engineErr, &refusal) || refusal.Family != contextfabric.QuestionFamilyScopedCohortStatus || refusal.RetryAttempted != (tc.calls == 2) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes || refusal.MeasuredBytes <= int64(tc.budget) {
					t.Fatalf("refusal context=%+v error=%v", refusal, f.engineErr)
				}
			}
			assertFinalCensusBudgetInfo(t, info.Bytes(), tc.calls == 2, tc.status == http.StatusOK, tc.budget, finalBytes)
		})
	}
}

func assertFinalCensusBudgetInfo(t *testing.T, raw []byte, retried, fit bool, budget int, finalBytes int64) {
	t.Helper()
	selected, second, terminal := -1, -1, -1
	var first, final map[string]any
	for i, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		switch e["msg"] {
		case "test synthesis entered":
			if e["attempt"] == float64(2) {
				second = i
			}
		case "context fabric synthesis retry selected":
			if selected >= 0 {
				t.Fatal("duplicate retry selection")
			}
			selected = i
			first = e
		case "context fabric plan narrowing":
			if e["stage"] == "assembled_result" {
				terminal = i
				final = e
			}
		}
	}
	if terminal < 0 {
		t.Fatalf("missing final budget measurement: %s", raw)
	}
	if retried {
		if selected < 0 || second <= selected || terminal <= second {
			t.Fatalf("measurement/selection/synthesis/terminal order: %d %d %d", selected, second, terminal)
		}
		if first["overrun"] != "bytes" || first["measured_bytes"].(float64) <= float64(budget) || first["max_serialized_bytes"] != float64(budget) || first["retry_attempted"] != false || first["retry_fit"] != false || first["retry_failed"] != false {
			t.Fatalf("initial measurement not the real trigger: %v", first)
		}
	} else if selected >= 0 || second >= 0 {
		t.Fatalf("unselected retry emitted: %d %d", selected, second)
	}
	if final["family"] != "scoped_cohort_status" || final["retry_attempted"] != retried || final["max_serialized_bytes"] != float64(budget) {
		t.Fatalf("final measurement lost context: %v", final)
	}
	if fit && final["measured_bytes"] != float64(finalBytes) {
		t.Fatalf("retry budget measured a different document: measured=%v final=%d", final["measured_bytes"], finalBytes)
	}
	if fit && final["overrun"] != "fits" {
		t.Fatalf("served attempt did not fit: %v", final)
	}
	if !fit && final["overrun"] != "bytes" {
		t.Fatalf("refusal did not measure byte overrun: %v", final)
	}
}

// This test-only late writer is a fault injection at the final guard. It is
// not a claim that the real provider emits an oversized title. It verifies
// that a residual final assertion retains the actual execution context.
type finalCensusLateWriter struct {
	contextfabric.EngineTelemetry
	cohort   *contextfabric.Cohort
	injected bool
}

func (p *finalCensusLateWriter) RecordPlanNarrowing(ctx context.Context, principal storage.Principal, event contextfabric.PlanNarrowingEvent) {
	p.EngineTelemetry.RecordPlanNarrowing(ctx, principal, event)
	if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult && event.Overrun == contractsv1.ContextFabricBudgetFits && p.cohort != nil {
		p.cohort.Members[0].Subject.Label = strings.Repeat("late label ", 4000)
		p.injected = true
	}
}

func TestWorkItemFinalBudgetRefusalPreservesExecutionContext(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_%t", retry), func(t *testing.T) {
			f, _, attempts := newFinalCensusBudgetFixture(t, true, true)
			probe := &finalCensusLateWriter{EngineTelemetry: f.dependencies.Telemetry}
			observer := f.model.observe
			f.model.observe = func(input contextfabric.SynthesisInput) { observer(input); probe.cohort = input.Graph.Cohort }
			f.dependencies.Telemetry = probe
			engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
			if err != nil {
				t.Fatal(err)
			}
			f.engine = engine
			body := finalCensusBudgetRequest()
			body.Options.MaxSerializedBytes = 26628
			if !retry {
				body.Options.MaxCohortMembers = 6
			}
			response := roundTripFreshTupleRequest(t, f, body)
			wantCalls := 1
			if retry {
				wantCalls = 2
			}
			var refusal contextfabric.AnswerBudgetRefusal
			if !probe.injected || response.Code != http.StatusRequestEntityTooLarge || len(*attempts) != wantCalls || !errors.As(f.engineErr, &refusal) {
				t.Fatalf("late assertion not reached: injected=%v HTTP%d calls%d error=%v", probe.injected, response.Code, len(*attempts), f.engineErr)
			}
			if refusal.Family != contextfabric.QuestionFamilyScopedCohortStatus || refusal.RetryAttempted != retry || refusal.NarrowerContinuationAxis == "" || refusal.MeasuredBytes <= int64(body.Options.MaxSerializedBytes) {
				t.Fatalf("final refusal lost execution context: %+v", refusal)
			}
			assertResponseOwnerGateFree(t, f.gate)
		})
	}
}
