package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type freshTupleSaveFailure struct {
	contextfabric.InvestigationResultStore
	calls int
}

func (s *freshTupleSaveFailure) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64, string, contextfabric.SemanticStateWrite) error {
	s.calls++
	return errors.New("controlled fresh tuple save failure")
}

type freshTupleFactFailure struct{}

func (freshTupleFactFailure) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{}, errors.New("controlled fact-stage failure after measured S1")
}

// Actual S1 and content producers acquire the installed response owner. The
// controlled writer observes occupancy after Engine returns and before the
// synchronous response finishes, including failed and aborted responses.
func TestWorkItemFreshProducerLeaseThroughHTTPCompletion(t *testing.T) {
	for _, name := range []string{"normal", "save_error", "marshal_error", "engine_error", "writer_error", "writer_partial", "cancel_during_write", "writer_panic"} {
		t.Run(name, func(t *testing.T) {
			fixture := newFreshTupleProducerFixture(t, "")
			saveFailure := &freshTupleSaveFailure{InvestigationResultStore: fixture.store}
			if name == "save_error" {
				fixture.dependencies.Results = saveFailure
			}
			if name == "engine_error" {
				fixture.dependencies.Facts = freshTupleFactFailure{}
			}
			engine, err := contextfabric.NewEngine(fixture.dependencies, fixture.engineOptions)
			if err != nil {
				t.Fatal(err)
			}
			completed := false
			app, token := newParityHostedApp(t, investigatorFunc(func(ctx context.Context, p storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				result, err := engine.Investigate(ctx, p, request)
				completed = true
				if name == "marshal_error" {
					// The actual produced result reaches the route's real JSON
					// encoder with a deliberately non-JSON number.
					result.SubjectResolution.Candidates[0].Confidence = math.NaN()
				}
				return result, err
			}), fixture.store)
			app.config.RequestTimeout = 5 * time.Second
			writer := newResponseOwnerBlockingWriter()
			if name == "writer_error" || name == "writer_partial" {
				writer.writeErr = errors.New("client disconnected")
			}
			if name == "writer_partial" {
				writer.partialN = 4
			}
			if name == "writer_panic" {
				writer.panicCount = 1
			}
			body := investigationRequestBody()
			body.Question = "What is the state and count of this project's work items?"
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(raw))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set("Content-Type", "application/json")
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			done := startResponseOwnerHandler(app.InstrumentedHandler(app.Handler()), writer, request.WithContext(ctx))
			defer func() {
				select {
				case <-writer.unblock:
				default:
					close(writer.unblock)
				}
			}()
			waitForResponseOwnerWrite(t, writer)
			assertResponseOwnerGateHeld(t, fixture.gate)
			wantPhases := []string{"s1", "status", "work"}
			if name == "engine_error" {
				wantPhases = []string{"s1"}
			}
			if !completed || !reflect.DeepEqual(fixture.client.phases, wantPhases) || fixture.graph.resolve != 1 || fixture.graph.discover != 0 {
				t.Fatalf("fresh producer path missing: completed=%v phases=%v resolve=%d discover=%d", completed, fixture.client.phases, fixture.graph.resolve, fixture.graph.discover)
			}
			if name == "cancel_during_write" {
				cancel()
				assertResponseOwnerGateHeld(t, fixture.gate)
			}
			close(writer.unblock)
			if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
				t.Fatalf("handler escaped panic: %v", recovered)
			}
			assertResponseOwnerGateFree(t, fixture.gate)
			if name == "save_error" && saveFailure.calls != 1 {
				t.Errorf("save failure was not reached: calls=%d", saveFailure.calls)
			}
			if name == "writer_partial" && writer.lastN != 4 {
				t.Errorf("partial write not exercised: wrote=%d", writer.lastN)
			}
			wantStatus := http.StatusOK
			if name == "save_error" || name == "marshal_error" || name == "engine_error" || name == "writer_panic" {
				wantStatus = http.StatusInternalServerError
			}
			if writer.status != wantStatus {
				t.Errorf("HTTP status=%d want=%d body=%s", writer.status, wantStatus, writer.body.String())
			}
		})
	}
}

type freshTupleBudgetTelemetry struct {
	contextfabric.EngineTelemetry
	plans  []contextfabric.PlanNarrowingEvent
	ranked int
}

func (t *freshTupleBudgetTelemetry) RecordPlanNarrowing(_ context.Context, _ storage.Principal, event contextfabric.PlanNarrowingEvent) {
	t.plans = append(t.plans, event)
}

func (t *freshTupleBudgetTelemetry) RecordCohortRanked(context.Context, storage.Principal, contextfabric.CohortRankedEvent) {
	t.ranked++
}

// Both over-budget sites must preserve the sole project anchor. The listed members are outside
// the item ceiling, so the overrun is the member claims: with 30 members the actual
// provider/synthesizer result carries 61 claims against 46, and the retry narrows what the
// model reads to 15 members while the answer still lists all 30 (and says the summary covers the
// first 15); with 25 members and no time for a retry the claims are cut by the trim lever and the
// answer is partial. Neither removes the anchor's authority.
func TestWorkItemFreshRetryKeepsAuthorizedAnchor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		members     int
		retry       bool
		wantStatus  string
		wantTrim    int
		wantSizes   []int
		wantCovered bool
	}{{"retry", 30, true, "complete", 0, []int{30, 15}, true}, {"no_reserve", 25, false, "partial", 1, []int{25}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 46, MaxTokens: 16000, MaxBytes: 1 << 20})
			telemetry := &freshTupleBudgetTelemetry{EngineTelemetry: contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(io.Discard, nil)))}
			f.dependencies.Telemetry = telemetry
			if !tc.retry {
				f.engineOptions.SynthesisDeadlineReserve = 0
			}
			engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
			if err != nil {
				t.Fatal(err)
			}
			f.engine = engine
			f.client.rowsByPhase = map[string][][]any{}
			for i := 0; i < tc.members; i++ {
				workID := fmt.Sprintf("work-%03d", i)
				id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", workID}, nil)
				if err != nil {
					t.Fatal(err)
				}
				f.client.rowsByPhase["s1"] = append(f.client.rowsByPhase["s1"], []any{id, "repo-1", workID, hostedTestRepository, uint8(1), uint64(tc.members), uint64(tc.members), uint64(0), uint64(0), uint64(0)})
				f.client.rowsByPhase["status"] = append(f.client.rowsByPhase["status"], []any{workID, "open", "repo-1", ""})
				f.client.rowsByPhase["work"] = append(f.client.rowsByPhase["work"], []any{workID, "Title " + workID, "repo-1"})
			}
			sizes := []int{}
			f.model.observe = func(input contextfabric.SynthesisInput) {
				sizes = append(sizes, len(input.Graph.Cohort.Members))
				resolution := input.Graph.Resolution
				if len(resolution.Candidates) != 1 || len(resolution.Committed) != 1 || resolution.Candidates[0].Subject.CanonicalID != f.graph.projectID || resolution.Committed[0].CanonicalID != f.graph.projectID {
					t.Errorf("synthesis lost authorized anchor: %+v", resolution)
				}
			}
			body := investigationRequestBody()
			body.Question = "What is the state and count of this project's work items?"
			body.Options.MaxCohortMembers = 250
			body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			recorder := roundTripFreshTupleRequest(t, f, body)
			var served struct {
				Status            string   `json:"status"`
				Limitations       []string `json:"limitations"`
				SubjectResolution struct {
					Candidates []struct {
						Subject struct {
							CanonicalID string `json:"canonical_id"`
						} `json:"subject"`
					} `json:"candidates"`
				} `json:"subject_resolution"`
				Cohort struct {
					Members []json.RawMessage `json:"members"`
				} `json:"cohort"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || f.engineErr != nil || served.Status != tc.wantStatus {
				t.Fatalf("over-budget outcome HTTP%d %s; engine=%v", recorder.Code, recorder.Body.String(), f.engineErr)
			}
			trimLines := 0
			for _, limitation := range served.Limitations {
				if contractsv1.IsContextFabricBudgetTrimLimitation(limitation) {
					trimLines++
				}
			}
			if trimLines != tc.wantTrim || len(served.Cohort.Members) != tc.members {
				t.Errorf("trim lines=%d members=%d, want %d lines and all %d members listed", trimLines, len(served.Cohort.Members), tc.wantTrim, tc.members)
			}
			covered := false
			for _, limitation := range served.Limitations {
				if contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation(limitation) {
					covered = true
				}
			}
			if covered != tc.wantCovered {
				t.Errorf("synthesis coverage sentence present=%v, want %v: %v", covered, tc.wantCovered, served.Limitations)
			}
			if len(served.SubjectResolution.Candidates) != 1 || served.SubjectResolution.Candidates[0].Subject.CanonicalID != f.graph.projectID {
				t.Errorf("served answer lost its authorized anchor: %+v", served.SubjectResolution)
			}
			if !reflect.DeepEqual(sizes, tc.wantSizes) || !reflect.DeepEqual(f.client.phases, []string{"s1", "status", "work"}) {
				t.Errorf("synthesis=%v phases=%v", sizes, f.client.phases)
			}
			for _, event := range telemetry.plans {
				if event.OutcomeReductionApplied || event.OutcomeReductionInnerFit || event.RefusalPlanned {
					t.Errorf("tuple reduced its anchor candidate or planned a refusal: %+v", event)
				}
			}
			if telemetry.ranked != 0 {
				t.Errorf("rank events=%d", telemetry.ranked)
			}
			assertResponseOwnerGateFree(t, f.gate)
		})
	}
}
