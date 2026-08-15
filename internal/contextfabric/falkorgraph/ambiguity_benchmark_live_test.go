package falkorgraph

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AC-3778-1 / AC-3778-2 MEASUREMENT HARNESS.
//
// This file is the harness, NOT the corpus. AC-3778-1 requires the ambiguity
// corpus to be "authored after implementation and withheld from the
// implementing lane", so it is supplied at run time:
//
//	ACR_TEST_AMBIGUITY_CORPUS=/path/to/corpus.json \
//	ACR_TEST_AMBIGUITY_ORG=<org-id> \
//	ACR_TEST_FALKOR_ADDR=host:port \
//	ACR_TEST_EMBED_BASE_URL=... ACR_TEST_EMBED_MODEL=... ACR_TEST_EMBED_DIMENSION=... \
//	[ACR_TEST_EMBED_API_KEY=...] \
//	  go test ./internal/contextfabric/falkorgraph -run AmbiguityBenchmark -v
//
// ACR_TEST_EMBED_API_KEY is OPTIONAL: a keyless local embedder is still
// supported (benchmarkLookup reports "not set" and the credential-free
// client path in embedprovider.newClientOptions applies). Set it to reach a
// real remote embedder that requires a credential -- without it,
// newClientOptions actively strips the Authorization header rather than
// falling back to an ambient OPENAI_API_KEY, so a remote embedder 401s.
//
// EVERY input is a dedicated ACR_TEST_* name, never the production
// ACR_CONTEXT_FABRIC_* names (codex round-1 F6). The earlier version documented
// the test names but READ the production ones, so as documented it recorded the
// lexical baseline and then silently skipped the hybrid half -- reporting no
// failure while measuring nothing. Dedicated names also mean a benchmark run
// can never reach a production embedder or graph through ambient environment.
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
	// SubjectTerms is OPTIONAL: the subject term(s) production's
	// QuestionInterpreter would have extracted from Question (CHAOS-3831 /
	// embed-text spec §5 L1). When absent, effectiveSubjectTerms falls back
	// to embedding the WHOLE question as a single term -- the pre-CHAOS-3831
	// behavior, kept for a corpus that has not been annotated yet. That
	// fallback is NOT parity: production never embeds a raw, un-interpreted
	// question, only the terms an interpretation step extracted from it, so
	// a corpus without this field understates (or misstates) production in a
	// way that will not match a real deployment's hybrid recall. See
	// loadAmbiguityCorpus, which logs how many cases fell back.
	SubjectTerms []string `json:"subject_terms,omitempty"`
}

// effectiveSubjectTerms is the harness-parity seam: it returns exactly what
// graphrank.SubjectTerms would receive as interpreted.SubjectTerms in
// production, given this case's authored terms (or the pre-parity
// whole-question fallback -- see the SubjectTerms field doc).
func (c ambiguityCase) effectiveSubjectTerms() []string {
	if len(c.SubjectTerms) > 0 {
		return c.SubjectTerms
	}
	return []string{c.Question}
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
	// CHAOS-3831 / embed-text spec §5 L1: a case with no subject_terms
	// measures the harness's pre-parity fallback (the whole question embedded
	// as one term), not production. That is a legitimate corpus to run, but
	// the gap must be visible rather than silently narrowing the measured
	// lift, so it is counted and logged once here rather than per-case.
	fallback := 0
	for _, c := range corpus {
		if len(c.SubjectTerms) == 0 {
			fallback++
		}
	}
	if fallback > 0 {
		t.Logf("AC-3831 harness-parity NOTICE: %d/%d corpus cases have no subject_terms and will use the whole-question fallback, which is NOT production parity (see ambiguityCase.SubjectTerms doc)", fallback, len(corpus))
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
		// CHAOS-3831 / L1: production never searches the raw question text --
		// QuestionInterpreter extracts subject terms first, and ResolveSubjects
		// searches those (graphrank.SubjectTerms), one deps.Search call per
		// term. effectiveSubjectTerms is this harness's parity seam for that
		// step; see the ambiguityCase.SubjectTerms field doc for what it does
		// when a corpus has not supplied extracted terms.
		interpreted := contextfabric.InterpretedQuestion{SubjectTerms: testCase.effectiveSubjectTerms()}
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
	address := os.Getenv("ACR_TEST_FALKOR_ADDR")
	if address == "" {
		t.Skip("ACR_TEST_FALKOR_ADDR is not set; this benchmark measures against live data")
	}
	orgID := os.Getenv("ACR_TEST_AMBIGUITY_ORG")
	if orgID == "" {
		t.Skip("ACR_TEST_AMBIGUITY_ORG is not set")
	}
	principal := storage.Principal{OrgID: orgID}
	ctx := context.Background()

	graphConfig, err := ConfigFromEnv(benchmarkLookup)
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

	embedderOptions, err := EmbedderFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("embedder configuration: %v", err)
	}
	if embedderOptions.Embedder == nil {
		// A hard failure, not a skip: reaching this point means the baseline
		// was already measured, so silently stopping here would report a
		// successful run that gated on nothing (codex round-1 F6).
		t.Fatal("ACR_TEST_EMBED_BASE_URL is not set; the hybrid half cannot run and the AC-3778-2 gate would measure nothing")
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

// benchmarkLookup translates this harness's dedicated ACR_TEST_* inputs into
// the production variable names the adapter constructors expect, WITHOUT ever
// reading the production names from the ambient environment.
//
// Codex round-1 F6: passing os.LookupEnv here is what made the documented
// invocation silently skip the hybrid gate, and it also meant a benchmark run
// on a machine with production configuration set would have pointed at a
// production embedder and graph. Every value below comes from an ACR_TEST_*
// name or is a fixed test-only default; a production name is never consulted.
func benchmarkLookup(key string) (string, bool) {
	value := func(name string) (string, bool) {
		v := os.Getenv(name)
		return v, v != ""
	}
	switch key {
	case EnvAddr:
		return value("ACR_TEST_FALKOR_ADDR")
	case EnvPassword:
		return value("ACR_TEST_FALKOR_PASSWORD")
	case EnvTLS:
		return "false", true
	case EnvAllowInsecure:
		return "true", true
	case EnvGraphPrefix:
		if v, ok := value("ACR_TEST_FALKOR_GRAPH_PREFIX"); ok {
			return v, true
		}
		return "acr-cf", true
	case embedprovider.EnvBaseURL:
		return value("ACR_TEST_EMBED_BASE_URL")
	case embedprovider.EnvModel:
		return value("ACR_TEST_EMBED_MODEL")
	case embedprovider.EnvDimension:
		return value("ACR_TEST_EMBED_DIMENSION")
	case embedprovider.EnvProvider:
		if v, ok := value("ACR_TEST_EMBED_PROVIDER"); ok {
			return v, true
		}
		return "ambiguity-benchmark", true
	case embedprovider.EnvAPIKey:
		// OPTIONAL: keyless local embedders remain supported (value("")
		// reports ok=false when unset, which EmbedderFromEnv/ConfigFromEnv
		// already treat as "no credential configured", not an error). A real
		// remote embedder needs this set -- embedprovider.newClientOptions
		// actively DELETES the Authorization header when the configured key
		// is empty, deliberately never falling back to an ambient
		// OPENAI_API_KEY (see its doc comment), so without this case a
		// benchmark run against a real remote embedder 401s.
		return value("ACR_TEST_EMBED_API_KEY")
	case embedprovider.EnvSimilarityFloor:
		return value("ACR_TEST_EMBED_SIMILARITY_FLOOR")
	case embedprovider.EnvAllowInsecureBaseURL:
		return "true", true
	default:
		return "", false
	}
}

// TestBenchmarkLookupEmbedAPIKey is the CHAOS-3849 regression: without a
// case for embedprovider.EnvAPIKey, benchmarkLookup falls through to its
// default branch and reports no credential at all, no matter what
// ACR_TEST_EMBED_API_KEY holds. embedprovider.newClientOptions then treats
// that as "credential-free by design" and actively DELETES the
// Authorization header rather than falling back to an ambient
// OPENAI_API_KEY (see its doc comment), so the benchmark/oracle harness
// would 401 against any real remote embedder that requires a key.
func TestBenchmarkLookupEmbedAPIKey(t *testing.T) {
	t.Run("set returns the configured value", func(t *testing.T) {
		t.Setenv("ACR_TEST_EMBED_API_KEY", "test-only-key-value")
		got, ok := benchmarkLookup(embedprovider.EnvAPIKey)
		if !ok || got != "test-only-key-value" {
			t.Fatalf("benchmarkLookup(EnvAPIKey) = (%q, %v), want (\"test-only-key-value\", true)", got, ok)
		}
	})
	t.Run("unset reports not-configured, not an error", func(t *testing.T) {
		t.Setenv("ACR_TEST_EMBED_API_KEY", "")
		got, ok := benchmarkLookup(embedprovider.EnvAPIKey)
		if ok || got != "" {
			t.Fatalf("benchmarkLookup(EnvAPIKey) = (%q, %v), want (\"\", false) when ACR_TEST_EMBED_API_KEY is unset -- keyless local embedders must remain supported", got, ok)
		}
	})
}
