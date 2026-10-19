package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func storedRouteGapLookalikes(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, contractsv1.ContextFabricWorkItemAuthorizationGapLimitation(false, 10+i, 50+i, false))
	}
	return out
}

// A work-item answer stored before draft lookalikes were stripped can hold a
// limitation list filled to the cap with recogniser-accepted model text, so the
// measured authorization gap was never appended. Read by id, such a row is not
// served as it was stored.
func TestStoredTupleFloodedLimitationsByIDRoute(t *testing.T) {
	f := newFreshTupleProducerFixture(t, "")
	f.client.rowsByPhase = map[string][][]any{"s1": {{f.client.memberID, "repo-1", "work-1", hostedTestRepository, uint8(1), uint64(2001), uint64(2001), uint64(0), uint64(0), uint64(0)}}}
	body := investigationRequestBody()
	body.Options.MaxCohortMembers = 1
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	fresh := serveFreshTupleRequest(t, f, body)
	stored, err := f.store.Get(context.Background(), f.principal, fresh.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SemanticState == nil || stored.SemanticState.WorkItemCensus == nil {
		t.Fatal("fresh tuple answer stored no census")
	}

	for _, tc := range []struct {
		name        string
		resultID    string
		limitations []string
		wantStatus  int
		wantBasis   string
	}{
		{name: "lookalikes fill the cap", resultID: "result_tuple_flooded_100", limitations: storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount), wantStatus: http.StatusNotFound, wantBasis: "limitations_flooded"},
		{name: "full list with a model caveat", resultID: "result_tuple_caveat_100", limitations: append(storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount-1), "The provider returned a stale status."), wantStatus: http.StatusOK, wantBasis: "digest_matched"},
		{name: "lookalikes under the cap", resultID: "result_tuple_lookalike_99", limitations: storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount - 1), wantStatus: http.StatusOK, wantBasis: "digest_matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := stored.Result
			row.ResultID = tc.resultID
			row.Limitations = tc.limitations
			if err := f.store.Save(context.Background(), f.principal, row, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateOf(stored.SemanticState)); err != nil {
				t.Fatalf("save pre-fix row: %v", err)
			}

			var logs bytes.Buffer
			f.app.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			request := httptest.NewRequest(http.MethodGet, ContextFabricInvestigationsPath+"/"+tc.resultID, nil)
			request.Header.Set("Authorization", "Bearer "+f.token)
			request.Header.Set("X-Request-ID", "req_00000000000000000000000000000002")
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			recorder := httptest.NewRecorder()
			f.app.InstrumentedHandler(f.app.Handler()).ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("HTTP %d, want %d: %.400s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				var served contextfabric.InvestigationResult
				if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
					t.Fatal(err)
				}
				if len(served.Limitations) != len(tc.limitations) {
					t.Fatalf("served %d limitations, want %d", len(served.Limitations), len(tc.limitations))
				}
			}

			sum := sha256.Sum256([]byte(tc.resultID))
			wantHash := hex.EncodeToString(sum[:6])
			line := storedServingLogLine(t, logs.Bytes())
			if line["basis"] != tc.wantBasis || line["surface"] != "result_by_id" || line["result_id_hash"] != wantHash {
				t.Fatalf("stored serving line = %v, want basis %q and result_id_hash %q", line, tc.wantBasis, wantHash)
			}
			t.Logf("HTTP=%d basis=%v result_id_hash=%v limitations=%d", recorder.Code, line["basis"], line["result_id_hash"], len(tc.limitations))
		})
	}
}

func storedServingLogLine(t *testing.T, logs []byte) map[string]any {
	t.Helper()
	for _, raw := range strings.Split(strings.TrimSpace(string(logs)), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			continue
		}
		if line["msg"] == contextfabric.WorkItemStoredServingLogMessage {
			return line
		}
	}
	t.Fatalf("no %q line in logs:\n%s", contextfabric.WorkItemStoredServingLogMessage, logs)
	return nil
}
