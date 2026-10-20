package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// Evidence expansion applies the same serving decision to every admitted
// citing result, and says on the stored-serving line which tuple decided and
// why, so a flooded row withheld there is identified too.
func TestStoredTupleFloodedLimitationsEvidenceExpansion(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "example-org/widget-service:WI-1")
	for _, tc := range []struct {
		name        string
		limitations []string
		status      int
		basis       string
	}{
		{name: "lookalikes fill the cap", limitations: storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount), status: http.StatusNotFound, basis: "limitations_flooded"},
		{name: "lookalikes under the cap", limitations: storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount - 1), status: http.StatusOK, basis: "digest_matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := storedWorkItemTupleRouteFixture(t, principal)
			stored.Result.Limitations = tc.limitations
			store := &storedTupleEvidenceStore{storedWorkItemTupleRouteStore{stored: stored}}
			logs := &bytes.Buffer{}
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			expand := httptest.NewRecorder()
			app.Handler().ServeHTTP(expand, scopedEvidenceRequest(t, token, ref, stored.Result.ResultID))
			if expand.Code != tc.status {
				t.Fatalf("source evidence status %d, want %d: %.400s", expand.Code, tc.status, expand.Body.String())
			}
			sum := sha256.Sum256([]byte(stored.Result.ResultID))
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.WorkItemStoredServing, Want: map[string]any{
				"org_id": callerOrgID, "surface": "evidence_expansion", "basis": tc.basis, "result_id_hash": hex.EncodeToString(sum[:6]),
			}}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
			t.Logf("HTTP=%d surface=evidence_expansion basis=%s limitations=%d", expand.Code, tc.basis, len(tc.limitations))
		})
	}
}

type orderedTupleEvidenceStore struct {
	contextfabric.InvestigationResultStore
	rows []contextfabric.StoredInvestigationResult
}

func (s *orderedTupleEvidenceStore) ResultIDsCitingEvidence(_ context.Context, _ storage.Principal, _ string, offset, _ int) ([]string, error) {
	if offset > 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(s.rows))
	for _, row := range s.rows {
		ids = append(ids, row.Result.ResultID)
	}
	return ids, nil
}

func (s *orderedTupleEvidenceStore) Get(_ context.Context, reader storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	for _, row := range s.rows {
		if row.Result.ResultID == resultID {
			row.GrantDigest = contextfabric.StoredResultGrantDigest(reader)
			return row, nil
		}
	}
	return contextfabric.StoredInvestigationResult{}, fmt.Errorf("%w: %s", contextfabric.ErrInvestigationResultNotFound, resultID)
}

// With several citing tuples, the stored-serving line names the tuple that
// was served, else the first one withheld, else the first one that could not
// be served, whatever order the citing results are read in.
func TestStoredTupleFloodedEvidenceExpansionNamesTheDecisiveTuple(t *testing.T) {
	principal := storage.Principal{OrgID: callerOrgID, RepositoryScopes: []string{hostedTestRepository}}
	ref := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-flood-0001")
	row := func(id string, mutate func(*contextfabric.StoredInvestigationResult)) contextfabric.StoredInvestigationResult {
		stored := storedWorkItemTupleRouteFixture(t, principal)
		stored.Result.ResultID = id
		stored.Result.EvidenceRefIDs = append(stored.Result.EvidenceRefIDs, ref)
		labels := map[string]string{}
		for cited, label := range stored.Result.EvidenceRefLabels {
			labels[cited] = label
		}
		labels[ref], _ = contractsv1.ContextFabricEvidenceRefLabel(ref)
		stored.Result.EvidenceRefLabels = labels
		mutate(&stored)
		return stored
	}
	flooded := func(stored *contextfabric.StoredInvestigationResult) {
		stored.Result.Limitations = storedRouteGapLookalikes(contractsv1.ContextFabricLimitationsMaxCount)
	}
	overfull := func(stored *contextfabric.StoredInvestigationResult) {
		storedPGBoundaryCoverage(&stored.Result, contractsv1.ContextFabricCoverageEntriesMaxCount, false)
	}
	plain := func(*contextfabric.StoredInvestigationResult) {}
	for _, tc := range []struct {
		name     string
		rows     []contextfabric.StoredInvestigationResult
		status   int
		basis    string
		decisive string
	}{
		{name: "flooded then admitted but unservable", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_flooded_first", flooded), row("result_tuple_overfull_second", overfull)}, status: http.StatusServiceUnavailable, basis: "limitations_flooded", decisive: "result_tuple_flooded_first"},
		{name: "admitted but unservable then flooded", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_overfull_first", overfull), row("result_tuple_flooded_second", flooded)}, status: http.StatusServiceUnavailable, basis: "limitations_flooded", decisive: "result_tuple_flooded_second"},
		{name: "two flooded", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_flooded_first", flooded), row("result_tuple_flooded_second", flooded)}, status: http.StatusNotFound, basis: "limitations_flooded", decisive: "result_tuple_flooded_first"},
		{name: "admitted but unservable alone", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_overfull_only", overfull)}, status: http.StatusServiceUnavailable, basis: "coverage_invalid", decisive: "result_tuple_overfull_only"},
		{name: "admitted but unservable then served", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_overfull_first", overfull), row("result_tuple_served_second", plain)}, status: http.StatusOK, basis: "digest_matched", decisive: "result_tuple_served_second"},
		{name: "flooded then served", rows: []contextfabric.StoredInvestigationResult{row("result_tuple_flooded_first", flooded), row("result_tuple_served_second", plain)}, status: http.StatusOK, basis: "digest_matched", decisive: "result_tuple_served_second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &orderedTupleEvidenceStore{rows: tc.rows}
			logs := &bytes.Buffer{}
			app, token := newParityHostedAppWithLogs(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
			expand := httptest.NewRecorder()
			app.Handler().ServeHTTP(expand, evidenceRequest(t, token, ref))
			if expand.Code != tc.status {
				t.Fatalf("source evidence status %d, want %d: %.400s", expand.Code, tc.status, expand.Body.String())
			}
			sum := sha256.Sum256([]byte(tc.decisive))
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.WorkItemStoredServing, Want: map[string]any{
				"org_id": callerOrgID, "surface": "evidence_expansion", "basis": tc.basis, "result_id_hash": hex.EncodeToString(sum[:6]),
			}}); err != nil {
				t.Fatalf("certify: %v\n%s", err, logs.String())
			}
			t.Logf("HTTP=%d basis=%s decisive=%s", expand.Code, tc.basis, tc.decisive)
		})
	}
}
