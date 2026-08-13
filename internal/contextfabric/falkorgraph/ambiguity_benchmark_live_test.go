package falkorgraph

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AC-3778-1 / AC-3778-2 MEASUREMENT HARNESS.
//
// This file is the harness, NOT the corpus. AC-3778-1 requires the ambiguity
// corpus to be "authored after implementation and withheld from the
// implementing lane", so it is supplied at run time:
//
//	ACR_TEST_AMBIGUITY_CORPUS=/path/to/corpus.json \
//	ACR_TEST_EMBED_BASE_URL=... ACR_TEST_EMBED_MODEL=... ACR_TEST_EMBED_DIMENSION=... \
//	ACR_CONTEXT_FABRIC_FALKOR_ADDR=... \
//	  go test ./internal/contextfabric/falkorgraph -run AmbiguityBenchmark -v
//
// Corpus format -- a JSON array. Each case names the question and the subject
// that a correct resolution must commit. A case with an EMPTY expected subject
// is a NO-MATCH CONTROL: the correct outcome is committing nothing (AC-3778-4).
//
//	[
//	  {"question": "the auth work", "expect_kind": "project", "expect_id": "project_auth"},
//	  {"question": "the flurbish subsystem", "expect_kind": "", "expect_id": ""}
//	]
//
// What it measures, per AC-3778-1's ordering: the LEXICAL-ONLY baseline first,
// then the hybrid run, over the same corpus against the same live data.
//
// What it asserts:
//
//   - AC-3778-2: hybrid correct-commit rate must exceed the lexical-only
//     baseline by at least 25 percentage points.
//   - AC-3778-3: the wrong-subject commit rate must NOT rise above the
//     baseline.
//   - AC-3778-4: a no-match control must still commit nothing.
//
// The lane that implemented CHAOS-3778 authors no corpus file. Running this
// without one skips.
type ambiguityCase struct {
	Question   string `json:"question"`
	ExpectKind string `json:"expect_kind"`
	ExpectID   string `json:"expect_id"`
}

// benchmarkOutcome counts one run over the corpus.
type benchmarkOutcome struct {
	total          int
	correctCommits int
	wrongCommits   int
	noCommit       int
	// controlViolations counts no-match controls that committed ANYTHING.
	// This is the AC-3778-4 failure and is reported separately because it is
	// the highest-severity outcome in the issue, not merely a wrong commit.
	controlViolations int
}

func (o benchmarkOutcome) correctRate() float64 {
	if o.total == 0 {
		return 0
	}
	return float64(o.correctCommits) / float64(o.total)
}

func (o benchmarkOutcome) wrongRate() float64 {
	if o.total == 0 {
		return 0
	}
	return float64(o.wrongCommits) / float64(o.total)
}

func loadAmbiguityCorpus(t *testing.T) []ambiguityCase {
	t.Helper()
	path := os.Getenv("ACR_TEST_AMBIGUITY_CORPUS")
	if path == "" {
		t.Skip("ACR_TEST_AMBIGUITY_CORPUS is not set; the AC-3778-1 corpus is authored separately and withheld from the implementing lane")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ambiguity corpus: %v", err)
	}
	var corpus []ambiguityCase
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse ambiguity corpus: %v", err)
	}
	// AC-3778-1 requires at least 50 questions. A smaller corpus cannot
	// support a 25-point claim, so it is a hard error rather than a warning:
	// reporting a lift measured on 6 questions as if it met the bar would be
	// worse than not measuring at all.
	if len(corpus) < 50 {
		t.Fatalf("ambiguity corpus has %d cases; AC-3778-1 requires at least 50", len(corpus))
	}
	return corpus
}

// runCorpus resolves every case and tallies the outcome.
func runCorpus(ctx context.Context, t *testing.T, adapter *Adapter, principal storage.Principal, corpus []ambiguityCase) benchmarkOutcome {
	t.Helper()
	outcome := benchmarkOutcome{total: len(corpus)}
	for _, testCase := range corpus {
		request := contextfabric.InvestigationRequest{
			Question: testCase.Question,
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, AllowClarification: true,
			},
		}
		interpreted := contextfabric.InterpretedQuestion{SubjectTerms: []string{testCase.Question}}
		resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
		if err != nil {
			t.Fatalf("ResolveSubjects(%q): %v", testCase.Question, err)
		}
		isControl := testCase.ExpectID == ""
		switch {
		case len(resolution.Committed) == 0:
			outcome.noCommit++
		case isControl:
			// A control that committed anything at all is the AC-3778-4
			// failure.
			outcome.controlViolations++
			outcome.wrongCommits++
		case committedMatches(resolution.Committed, testCase):
			outcome.correctCommits++
		default:
			outcome.wrongCommits++
		}
	}
	return outcome
}

func committedMatches(committed []contextfabric.SubjectRef, testCase ambiguityCase) bool {
	if len(committed) != 1 {
		// Committing more than one subject is not a correct commit for a
		// corpus case that names exactly one.
		return false
	}
	return string(committed[0].Kind) == testCase.ExpectKind && committed[0].CanonicalID == testCase.ExpectID
}

// TestAmbiguityBenchmarkMeasuresTheHybridLift is the AC-3778-2 gate.
func TestAmbiguityBenchmarkMeasuresTheHybridLift(t *testing.T) {
	corpus := loadAmbiguityCorpus(t)
	address := os.Getenv("ACR_CONTEXT_FABRIC_FALKOR_ADDR")
	if address == "" {
		t.Skip("ACR_CONTEXT_FABRIC_FALKOR_ADDR is not set; this benchmark measures against live data")
	}
	orgID := os.Getenv("ACR_TEST_AMBIGUITY_ORG")
	if orgID == "" {
		t.Skip("ACR_TEST_AMBIGUITY_ORG is not set")
	}
	principal := storage.Principal{OrgID: orgID}
	ctx := context.Background()

	graphConfig, err := ConfigFromEnv(os.LookupEnv)
	if err != nil {
		t.Fatalf("graph configuration: %v", err)
	}

	// AC-3778-1 ORDERING: the lexical-only baseline is recorded FIRST, with
	// no embedder attached at all -- not merely with vector results ignored,
	// so the baseline is genuinely the pre-CHAOS-3778 code path.
	lexicalAdapter, err := New(graphConfig)
	if err != nil {
		t.Fatalf("lexical adapter: %v", err)
	}
	baseline := runCorpus(ctx, t, lexicalAdapter, principal, corpus)
	t.Logf("AC-3778-1 lexical-only baseline: correct=%d/%d (%.1f%%) wrong=%d no-commit=%d",
		baseline.correctCommits, baseline.total, baseline.correctRate()*100, baseline.wrongCommits, baseline.noCommit)

	embedderOptions, err := EmbedderFromEnv(os.LookupEnv)
	if err != nil {
		t.Fatalf("embedder configuration: %v", err)
	}
	if embedderOptions.Embedder == nil {
		t.Skip("no embedder configured; the hybrid half of the benchmark cannot run")
	}
	hybridAdapter, err := NewWithEmbedder(graphConfig, embedderOptions)
	if err != nil {
		t.Fatalf("hybrid adapter: %v", err)
	}
	hybrid := runCorpus(ctx, t, hybridAdapter, principal, corpus)
	t.Logf("hybrid: correct=%d/%d (%.1f%%) wrong=%d no-commit=%d",
		hybrid.correctCommits, hybrid.total, hybrid.correctRate()*100, hybrid.wrongCommits, hybrid.noCommit)

	lift := (hybrid.correctRate() - baseline.correctRate()) * 100
	t.Logf("AC-3778-2 lift: %+.1f percentage points (bar: +25.0)", lift)

	// AC-3778-4 first: it is the highest-severity failure in the issue, so it
	// is reported even when the lift bar also fails.
	if hybrid.controlViolations > 0 {
		t.Errorf("AC-3778-4 VIOLATED: %d no-match control question(s) committed a subject", hybrid.controlViolations)
	}
	// AC-3778-3: semantic retrieval must not buy the lift with wrong commits.
	if hybrid.wrongRate() > baseline.wrongRate() {
		t.Errorf("AC-3778-3 VIOLATED: wrong-commit rate rose from %.1f%% to %.1f%%",
			baseline.wrongRate()*100, hybrid.wrongRate()*100)
	}
	// AC-3778-2 last: a lift bought by breaking either bar above is not a
	// pass, which is why the two run first.
	if lift < 25.0 {
		t.Errorf("AC-3778-2 NOT MET: lift %+.1f percentage points, bar +25.0", lift)
	}
}
