package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type retainedRankingSnapshotStore struct {
	legacyResultStore
	stored cf.StoredInvestigationResult
}

func (s retainedRankingSnapshotStore) Get(_ context.Context, _ storage.Principal, id string) (cf.StoredInvestigationResult, error) {
	if id != s.stored.Result.ResultID {
		return cf.StoredInvestigationResult{}, cf.ErrInvestigationResultNotFound
	}
	data, err := json.Marshal(s.stored)
	if err != nil {
		return cf.StoredInvestigationResult{}, err
	}
	var copied cf.StoredInvestigationResult
	err = json.Unmarshal(data, &copied)
	return copied, err
}
func TestRetainedRankingProductionSnapshotServing(t *testing.T) {
	dir := os.Getenv("CF_RETAINED_PROOF_DIR")
	if dir == "" {
		dir = filepath.Join("testdata", "retained-ranking")
	}
	for _, era := range []string{"historical", "current"} {
		for _, scenario := range []struct {
			name                          string
			haveFacts, capped, zeroSignal bool
		}{{"qualified", true, false, false}, {"insufficient", false, false, false}, {"capped", true, true, false}, {"zero_signal", false, false, true}} {
			haveFacts := scenario.haveFacts
			t.Run(era+"/"+scenario.name, func(t *testing.T) {
				path := filepath.Join(dir, era, fmt.Sprintf("ranking-facts-%v.json", haveFacts))
				if scenario.capped {
					path = filepath.Join(dir, era, "ranking-capped.json")
				}
				if scenario.zeroSignal {
					path = filepath.Join(dir, era, "ranking-not-applicable.json")
				}
				var stored cf.StoredInvestigationResult
				var data []byte
				var err error
				if era == "current" && os.Getenv("CF_RETAINED_PROOF_DIR") == "" {
					stored = captureRetainedRankingFromProduction(t, haveFacts, retainedRankingCaptureMode{capped: scenario.capped, zeroSignal: scenario.zeroSignal})
					data, err = json.Marshal(stored)
				} else {
					data, err = os.ReadFile(path)
					if err == nil {
						err = json.Unmarshal(data, &stored)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := stored.Result.ValidateStored(); err != nil {
					t.Fatal(err)
				}
				if stored.SemanticStateRead != cf.SemanticStateReadAvailable || stored.Result.Cohort == nil {
					t.Fatal("actual producer receipt or cohort missing")
				}
				store := retainedRankingReadOnlyStore{retainedRankingSnapshotStore: retainedRankingSnapshotStore{stored: stored}, t: t}
				assert := func(surface string, got cf.InvestigationResult) {
					t.Helper()
					if !reflect.DeepEqual(got.Cohort, stored.Result.Cohort) {
						t.Errorf("%s rewrote the stored ranking", surface)
					}
					count := 0
					for _, row := range got.Completeness.Outcomes {
						if row.Obligation == "ranking" && row.Stage == v1.ContextFabricOutcomeStageAssembledResult {
							count++
							if scenario.capped && (row.Impact != v1.ContextFabricAnswerImpactScope || row.Served != 0 || row.Declared != 0) {
								t.Fatalf("%s lost scope or invented population: %+v", surface, row)
							}
						}
					}
					t.Logf("surface=%s capture_sha256=%x assembled_rows=%d state=%s qualified=%s", surface, sha256.Sum256(data), count, got.Completeness.State, got.Cohort.Members[0].Outcome)
					for _, oldRow := range stored.Result.Completeness.Outcomes {
						found := false
						for _, row := range got.Completeness.Outcomes {
							if reflect.DeepEqual(row, oldRow) {
								found = true
								break
							}
						}
						if !found {
							t.Fatalf("%s rewrote stored outcome %+v", surface, oldRow)
						}
					}
					if got.Completeness.State != stored.Result.Completeness.State {
						t.Fatalf("%s changed independent health degradation", surface)
					}
					if count != 1 {
						t.Errorf("%s actual production ranking has %d assembled outcomes; want one", surface, count)
					}
				}
				app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)
				recorder := httptest.NewRecorder()
				app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.Result.ResultID))
				if recorder.Code != http.StatusOK {
					t.Fatalf("HTTP=%d body=%s", recorder.Code, recorder.Body.String())
				}
				var got cf.InvestigationResult
				if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				assert("HTTP by id", got)
				assertRetainedRankingLine(t, logs.Bytes(), era == "current", haveFacts, scenario.capped, scenario.zeroSignal)
				// Repeated reads use the same stored carrier, not the prior
				// response, and must neither accumulate rows nor rewrite it.
				again := httptest.NewRecorder()
				app.Handler().ServeHTTP(again, investigationResultRequest(t, token, stored.Result.ResultID))
				if again.Code != http.StatusOK {
					t.Fatalf("repeated HTTP=%d", again.Code)
				}
				var repeated cf.InvestigationResult
				if err := json.Unmarshal(again.Body.Bytes(), &repeated); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, repeated) {
					t.Fatal("repeated by-ID serving changed result")
				}
				savedAfter, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, stored.Result.ResultID)
				if err != nil || !reflect.DeepEqual(savedAfter, stored) {
					t.Fatalf("stored carrier changed: %v", err)
				}
				// Exercise the actual TLS route, projection and MCP forwarding.
				parityApp, parityToken := newParityHostedApp(t, nil, store)
				server := httptest.NewTLSServer(parityApp.Handler())
				defer server.Close()
				configureSidecarEnvironment(t, server, parityToken)
				boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
				if err != nil {
					t.Fatal(err)
				}
				assert("MCP by id", callRealMCPInvestigationResult(t, boot, stored.Result.ResultID))
				projection := getRealAPIProjection(t, server, parityToken, stored.Result.ResultID, 3, 1, 10)
				if len(projection.Completeness.Outcomes) < len(got.Completeness.Outcomes) || !reflect.DeepEqual(projection.Completeness.Outcomes[:len(got.Completeness.Outcomes)], got.Completeness.Outcomes) {
					t.Fatal("bounded projection changed ranking accounting")
				}

				reuseLogs := &bytes.Buffer{}
				reuseDeps := cf.EngineDependencies{
					Interpreter: surfaceInterpreter{}, Graph: retainedRankingReuseGraph{t: t}, Facts: surfaceFacts{t: t}, Synthesizer: surfaceSynthesizer{t: t}, Results: store,
					ReuseGate: surfaceReuseGate{candidate: stored.Result}, Telemetry: cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(reuseLogs, nil))),
				}
				reuseOptions := cf.EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }, NewResultID: func() string { return "result_retained_reuse" }}
				engine, err := cf.NewEngine(reuseDeps, reuseOptions)
				if err != nil {
					t.Fatal(err)
				}
				request := surfaceRequest("request_reuse_producer")
				request.Question = "Rank the team cohort"
				reused, err := engine.Investigate(observability.WithRequestID(context.Background(), "req_11111111111111111111111111111111"), storage.Principal{OrgID: callerOrgID}, request)
				if err != nil {
					t.Fatal(err)
				}
				if !reused.Reused {
					t.Fatal("actual Engine reuse was not reached")
				}
				assert("Engine reuse", reused)
				assertRetainedRankingLine(t, reuseLogs.Bytes(), era == "current", haveFacts, scenario.capped, scenario.zeroSignal)
				if era == "historical" {
					// A ceiling that fits the served copy without its added row
					// must refuse after that row is included. Use measured bytes,
					// not an assumed row size or an increased proof budget.
					without := got
					without.Completeness.Outcomes = nil
					for _, row := range got.Completeness.Outcomes {
						if row.Obligation != "ranking" || row.Stage != v1.ContextFabricOutcomeStageAssembledResult {
							without.Completeness.Outcomes = append(without.Completeness.Outcomes, row)
						}
					}
					_, oldBytes, err := marshalContextFabricResponse(without)
					if err != nil {
						t.Fatal(err)
					}
					_, servedBytes, err := marshalContextFabricResponse(got)
					if err != nil {
						t.Fatal(err)
					}
					if servedBytes <= oldBytes {
						t.Fatal("added accounting bytes were not measured")
					}
					app.config.MaxSerializedBytes = int(oldBytes)
					bounded := httptest.NewRecorder()
					app.Handler().ServeHTTP(bounded, investigationResultRequest(t, token, stored.Result.ResultID))
					if bounded.Code != http.StatusRequestEntityTooLarge {
						t.Fatalf("stored response accepted added bytes: code=%d body=%s", bounded.Code, bounded.Body.String())
					}
					var refusalBody struct {
						Error struct {
							Details struct {
								MeasuredBytes int64 `json:"measured_bytes"`
								MaxBytes      int64 `json:"max_serialized_bytes"`
							} `json:"details"`
						} `json:"error"`
					}
					if err := json.Unmarshal(bounded.Body.Bytes(), &refusalBody); err != nil {
						t.Fatal(err)
					}
					if refusalBody.Error.Details.MeasuredBytes != servedBytes || refusalBody.Error.Details.MaxBytes != oldBytes {
						t.Fatalf("by-ID refusal measurement=%s expected%d/%d", bounded.Body.String(), servedBytes, oldBytes)
					}
					reuseWithout := reused
					reuseWithout.Completeness.Outcomes = nil
					for _, row := range reused.Completeness.Outcomes {
						if row.Obligation != "ranking" || row.Stage != v1.ContextFabricOutcomeStageAssembledResult {
							reuseWithout.Completeness.Outcomes = append(reuseWithout.Completeness.Outcomes, row)
						}
					}
					measured, err := v1.MeasureContextFabricResponse(reuseWithout)
					if err != nil {
						t.Fatal(err)
					}
					reuseOptions.MaxSerializedBytes = measured.Bytes
					boundedEngine, buildErr := cf.NewEngine(reuseDeps, reuseOptions)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					_, err = boundedEngine.Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, request)
					var refusal cf.AnswerBudgetRefusal
					if !errors.As(err, &refusal) || refusal.Overrun != v1.ContextFabricBudgetOverrunBytes || refusal.MeasuredBytes <= measured.Bytes {
						t.Fatalf("reuse did not measure added accounting before refusal: %v %+v", err, refusal)
					}
				}
			})
		}
	}
}

func assertRetainedRankingLine(t *testing.T, raw []byte, existing, haveFacts, capped, zeroSignal bool) {
	t.Helper()
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	lines := log.LinesWithMsg(cf.RetainedRankingAccountingLogMessage)
	if len(lines) != 1 {
		t.Fatalf("retained accounting lines=%d; want1", len(lines))
	}
	qualified, insufficient, notApplicable := 0, 2, 0
	if zeroSignal {
		insufficient, notApplicable = 0, 2
	}
	outcome := "unavailable"
	if haveFacts {
		qualified, insufficient, outcome = 2, 0, "satisfied"
		if capped {
			outcome = "narrowed"
		}
	}
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.RetainedRankingAccounting, Want: map[string]any{
		"request_id": lines[0]["request_id"], "index": 1, "requirement": "ranking/member/team", "subject_kind": "team", "existing_row": existing, "qualification_recorded": true, "row_added": !existing,
		"assembled_outcome": outcome, "retained_members": 2, "qualified": qualified, "provisional": 0, "insufficient_evidence": insufficient, "not_applicable": notApplicable, "unrecorded_members": 0, "total": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// This is a malformed/legacy qualification negative control, not a claim that
// today's producer emits an uncomputed qualified member.
func TestRetainedRankingMissingQualificationIsExplicitOnStoredRead(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "retained-ranking", "historical", "ranking-facts-true.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored cf.StoredInvestigationResult
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Result.Cohort.Members[0].RankingComputed = false
	store := retainedRankingReadOnlyStore{retainedRankingSnapshotStore: retainedRankingSnapshotStore{stored: stored}, t: t}
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationResultRequest(t, token, stored.Result.ResultID))
	if response.Code != http.StatusOK {
		t.Fatalf("established stored read failed:%d %s", response.Code, response.Body.String())
	}
	var got cf.InvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, row := range got.Completeness.Outcomes {
		if row.Obligation == "ranking" && row.Stage == v1.ContextFabricOutcomeStageAssembledResult {
			t.Fatal("invented absent qualification")
		}
	}
	log, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	lines := log.LinesWithMsg(cf.RetainedRankingAccountingLogMessage)
	if len(lines) != 1 {
		t.Fatalf("missing skip event:%s", logs.Bytes())
	}
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.RetainedRankingAccounting, Want: map[string]any{
		"request_id": lines[0]["request_id"], "index": 1, "total": 1, "qualification_recorded": false, "existing_row": false, "row_added": false, "assembled_outcome": "none", "retained_members": 2, "qualified": 1, "provisional": 0, "insufficient_evidence": 0, "not_applicable": 0, "unrecorded_members": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
}
