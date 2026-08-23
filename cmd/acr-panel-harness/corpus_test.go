package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/panelharness"
)

// TestRun_RequiresExactlyOneOfQuestionOrCorpus is CHAOS-4146(b)'s own
// regression test for the mode-selection guard: -question and -corpus are
// mutually exclusive, and exactly one is required.
func TestRun_RequiresExactlyOneOfQuestionOrCorpus(t *testing.T) {
	base := []string{"-api-base-url=https://acr.example.com", "-org-id=org-1", "-panelists=/tmp/panelists.json"}
	tests := []struct {
		name string
		args []string
	}{
		{"neither set", base},
		{"both set", append(append([]string{}, base...), "-question=q", "-corpus=/tmp/corpus.json", "-output=/tmp/out.json", "-output-dir=/tmp/out")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, os.Stdout, os.Stderr); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// TestRun_RequiresOutputDirInCorpusMode pins that -corpus mode needs
// -output-dir, not the ad-hoc mode's -output.
func TestRun_RequiresOutputDirInCorpusMode(t *testing.T) {
	args := []string{"-api-base-url=https://acr.example.com", "-org-id=org-1", "-panelists=/tmp/panelists.json", "-corpus=/tmp/corpus.json"}
	if err := run(args, os.Stdout, os.Stderr); err == nil {
		t.Error("expected an error: -corpus mode requires -output-dir")
	}
}

// TestRun_RejectsNegativeCaseStartAndCaseCount pins both flags fail closed
// on a negative value rather than silently underflowing the corpus slice
// computation.
func TestRun_RejectsNegativeCaseStartAndCaseCount(t *testing.T) {
	base := []string{"-api-base-url=https://acr.example.com", "-org-id=org-1", "-panelists=/tmp/panelists.json", "-corpus=/tmp/corpus.json", "-output-dir=/tmp/out"}
	tests := []struct {
		name string
		args []string
	}{
		{"negative case-start", append(append([]string{}, base...), "-case-start=-1")},
		{"negative case-count", append(append([]string{}, base...), "-case-count=-1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, os.Stdout, os.Stderr); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// minimalDecisiveResult returns a contractsv1.ContextFabricInvestigationResult
// that satisfies ValidateStored() and carries no StructureNeeds -- every
// case in this file's corpus batch tests is decisive on turn 1 (nothing for
// the panel's clarification loop to act on), which keeps these tests
// focused on the corpus-slicing/provenance-stamping logic itself, not on
// exercising the N-turn loop (already covered by internal/panelharness's
// own tests).
func minimalDecisiveResult(resultID, requestID string) contractsv1.ContextFabricInvestigationResult {
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      resultID, RequestID: requestID,
		GeneratedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Question:    "placeholder", // never asserted on; the wire question is this test's own corpus text
		Status:      contractsv1.ContextFabricInvestigationComplete,
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeSingleSubject, RequestedJudgment: "release_readiness",
			TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		},
		SubjectResolution:   contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{}},
		DirectJudgment:      "direct-judgment-placeholder",
		DeterministicAnswer: "deterministic-answer-placeholder",
		StrongestPressures:  []string{},
		Drivers:             []contractsv1.ContextFabricDriverJudgment{},
		RemainingWork:       []contractsv1.ContextFabricFinding{},
		ReadinessGaps:       []contractsv1.ContextFabricFinding{},
		Paths:               []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:           []contractsv1.ContextFabricFinding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contractsv1.ContextFabricClaimedFact{},
		Warnings:            []string{},
		Coverage:            contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
	}
}

// failSelector proves SelectReceipts is never invoked -- every case in this
// file's corpus batch tests is decisive on turn 1, so nothing should ever
// reach the Selector.
type failSelector struct{ t *testing.T }

func (s failSelector) SelectReceipts(context.Context, string, contractsv1.ContextFabricStructureNeeds) (map[string]string, error) {
	s.t.Helper()
	s.t.Fatal("SelectReceipts called, want it never invoked for a decisive turn-1 result")
	return nil, nil
}

// TestRunCorpusBatch_ProcessesTheRequestedSliceAndStampsProvenance is
// CHAOS-4146(b)/(c)'s own end-to-end regression test: a 3-case corpus,
// -case-start=1 -case-count=2 selects cases 1 and 2 only (never case 0),
// each written manifest carries the correct case_index/run_tag/corpus_path/
// corpus_sha256, and the corpus's own question text never appears on
// stdout (corpus-safety discipline: index only, never text).
func TestRunCorpusBatch_ProcessesTheRequestedSliceAndStampsProvenance(t *testing.T) {
	const secretQuestionMarker = "UNIQUE_CORPUS_QUESTION_TEXT_MARKER"
	var seenQuestions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		seenQuestions = append(seenQuestions, request.Question)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(minimalDecisiveResult("result_"+request.RequestID, request.RequestID))
	}))
	defer server.Close()

	client, err := panelharness.NewClient(server.URL, testValidBearerToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := panelharness.Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: failSelector{t: t}}

	corpusDir := t.TempDir()
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	corpusContent := []byte(`[
		{"question": "` + secretQuestionMarker + `_0"},
		{"question": "` + secretQuestionMarker + `_1"},
		{"question": "` + secretQuestionMarker + `_2"}
	]`)
	if err := os.WriteFile(corpusPath, corpusContent, 0o644); err != nil {
		t.Fatalf("write corpus fixture: %v", err)
	}

	outputDir := t.TempDir()
	var capturedStdout bytes.Buffer

	batchErr := runCorpusBatch(context.Background(), corpusBatchConfig{
		orgID: "org-test", panelists: []panelharness.Panelist{panelist},
		baseRequest: buildBaseRequest(nil, nil, nil),
		corpusPath:  corpusPath, caseStart: 1, caseCount: 2,
		outputDir: outputDir, runTag: "test-run-tag",
	}, &capturedStdout)
	if batchErr != nil {
		t.Fatalf("runCorpusBatch: %v", batchErr)
	}

	if strings.Contains(capturedStdout.String(), secretQuestionMarker) {
		t.Errorf("stdout leaked corpus question text: %q", capturedStdout.String())
	}

	if len(seenQuestions) != 2 {
		t.Fatalf("server observed %d requests, want exactly 2 (cases 1 and 2 only)", len(seenQuestions))
	}
	if seenQuestions[0] != secretQuestionMarker+"_1" || seenQuestions[1] != secretQuestionMarker+"_2" {
		t.Errorf("questions sent = %v, want case 1 then case 2 only (never case 0)", seenQuestions)
	}

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("output dir has %d file(s), want exactly 2", len(entries))
	}

	for _, wantIndex := range []int{1, 2} {
		path := filepath.Join(outputDir, "test-run-tag-case"+strconv.Itoa(wantIndex)+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read manifest for case %d: %v", wantIndex, err)
		}
		var manifest panelharness.PanelRunManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("decode manifest for case %d: %v", wantIndex, err)
		}
		if manifest.CaseIndex == nil || *manifest.CaseIndex != wantIndex {
			t.Errorf("case %d manifest CaseIndex = %v, want %d", wantIndex, manifest.CaseIndex, wantIndex)
		}
		if manifest.RunTag != "test-run-tag" {
			t.Errorf("case %d manifest RunTag = %q, want test-run-tag", wantIndex, manifest.RunTag)
		}
		if manifest.CorpusPath != corpusPath {
			t.Errorf("case %d manifest CorpusPath = %q, want %q", wantIndex, manifest.CorpusPath, corpusPath)
		}
		if manifest.CorpusSHA256 == "" {
			t.Errorf("case %d manifest CorpusSHA256 is empty, want a non-empty digest", wantIndex)
		}
		if strings.Contains(string(raw), secretQuestionMarker) {
			t.Errorf("case %d manifest leaked corpus question text", wantIndex)
		}
	}
}

// TestRunCorpusBatch_RejectsCaseStartOutOfRange and
// TestRunCorpusBatch_RejectsCaseCountOverflow pin the "no silent caps"
// discipline: a misconfigured shard range must fail loudly, never
// silently clamp.
func TestRunCorpusBatch_RejectsCaseStartOutOfRange(t *testing.T) {
	corpusDir := t.TempDir()
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if err := os.WriteFile(corpusPath, []byte(`[{"question":"a"}]`), 0o644); err != nil {
		t.Fatalf("write corpus fixture: %v", err)
	}
	err := runCorpusBatch(context.Background(), corpusBatchConfig{
		orgID: "org-test", corpusPath: corpusPath, caseStart: 5, outputDir: t.TempDir(),
	}, os.Stdout)
	if err == nil {
		t.Error("expected an error: case-start is out of range")
	}
}

func TestRunCorpusBatch_RejectsCaseCountOverflow(t *testing.T) {
	corpusDir := t.TempDir()
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if err := os.WriteFile(corpusPath, []byte(`[{"question":"a"},{"question":"b"}]`), 0o644); err != nil {
		t.Fatalf("write corpus fixture: %v", err)
	}
	err := runCorpusBatch(context.Background(), corpusBatchConfig{
		orgID: "org-test", corpusPath: corpusPath, caseStart: 1, caseCount: 5, outputDir: t.TempDir(),
	}, os.Stdout)
	if err == nil {
		t.Error("expected an error: case-start + case-count exceeds the corpus size")
	}
}

// TestRunCorpusBatch_RejectsCaseCountThatOverflowsInt is a regression test
// for codex xhigh review round 1, HIGH: the old range guard compared
// cfg.caseStart+count > len(cases), which OVERFLOWS int for a large enough
// -case-count (wrapping negative and silently passing the guard), then
// panicked on an out-of-range slice index inside the batch loop. The fixed
// guard (count > len(cases)-cfg.caseStart) must reject this without ever
// reaching that index.
func TestRunCorpusBatch_RejectsCaseCountThatOverflowsInt(t *testing.T) {
	corpusDir := t.TempDir()
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if err := os.WriteFile(corpusPath, []byte(`[{"question":"a"},{"question":"b"}]`), 0o644); err != nil {
		t.Fatalf("write corpus fixture: %v", err)
	}
	err := runCorpusBatch(context.Background(), corpusBatchConfig{
		orgID: "org-test", corpusPath: corpusPath, caseStart: 1, caseCount: math.MaxInt, outputDir: t.TempDir(),
	}, os.Stdout)
	if err == nil {
		t.Error("expected an error: an overflowing case-start+case-count must never panic or silently pass validation")
	}
}

// failingWriter always returns an error, never writing anything -- used to
// prove a progress-line write failure fails the whole batch (codex xhigh
// review round 1, MEDIUM).
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

var errWriteFailed = errors.New("simulated stdout write failure")

// TestRunCorpusBatch_StdoutWriteFailureFailsTheBatch is a regression test
// for codex xhigh review round 1, MEDIUM: a progress-log write failure
// used to be silently ignored, letting later cases keep running and the
// batch ultimately return nil -- violating this function's own fail-fast
// doc comment. A single case is enough: the very first progress line must
// now surface the write error.
func TestRunCorpusBatch_StdoutWriteFailureFailsTheBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contractsv1.ContextFabricInvestigationRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(minimalDecisiveResult("result_"+request.RequestID, request.RequestID))
	}))
	defer server.Close()

	client, err := panelharness.NewClient(server.URL, testValidBearerToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	panelist := panelharness.Panelist{CanonicalModelIdentity: "anthropic/sol-max", Client: client, Selector: failSelector{t: t}}

	corpusDir := t.TempDir()
	corpusPath := filepath.Join(corpusDir, "corpus.json")
	if err := os.WriteFile(corpusPath, []byte(`[{"question":"a"}]`), 0o644); err != nil {
		t.Fatalf("write corpus fixture: %v", err)
	}

	batchErr := runCorpusBatch(context.Background(), corpusBatchConfig{
		orgID: "org-test", panelists: []panelharness.Panelist{panelist}, baseRequest: buildBaseRequest(nil, nil, nil),
		corpusPath: corpusPath, outputDir: t.TempDir(), runTag: "test-run-tag",
	}, failingWriter{})
	if batchErr == nil {
		t.Fatal("expected an error: a stdout write failure must fail the batch")
	}
	if !errors.Is(batchErr, errWriteFailed) {
		t.Errorf("runCorpusBatch error = %v, want it to wrap the simulated write failure", batchErr)
	}
}
