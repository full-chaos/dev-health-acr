package falkorgraph

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

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
//	[ACR_TEST_EMBED_TIMEOUT=45s] [ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES=5] \
//	[ACR_TEST_EMBED_MAX_BATCH=...] [ACR_TEST_EMBED_MAX_TEXT_RUNES=...] \
//	[ACR_TEST_EMBED_PREFIX_FAMILY=...] [ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL=...] \
//	[ACR_TEST_EMBED_PROVIDER_LOCALITY=local|remote] [ACR_TEST_EMBED_INCLUDE_BODIES=true|false] \
//	  go test ./internal/contextfabric/falkorgraph -run AmbiguityBenchmark -v
//
// ACR_TEST_EMBED_API_KEY is OPTIONAL: a keyless local embedder is still
// supported (benchmarkLookup reports "not set" and the credential-free
// client path in embedprovider.newClientOptions applies). Set it to reach a
// real remote embedder that requires a credential -- without it,
// newClientOptions actively strips the Authorization header rather than
// falling back to an ambient OPENAI_API_KEY, so a remote embedder 401s.
//
// ACR_TEST_EMBED_TIMEOUT and ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES are also
// OPTIONAL (CHAOS-3849 round 2): unset, both fall through to
// embedprovider's own defaults (250ms / 0 retries), which are deliberately
// loopback-tuned for a local embedder and too tight for a real network call
// -- a benchmark run against a remote embedder needs both raised (production
// runs remote embedders at 45s / 5 retries) or every embed call fails with
// "context deadline exceeded".
//
// ACR_TEST_EMBED_MAX_BATCH, ACR_TEST_EMBED_MAX_TEXT_RUNES,
// ACR_TEST_EMBED_PREFIX_FAMILY, ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL,
// ACR_TEST_EMBED_PROVIDER_LOCALITY, and ACR_TEST_EMBED_INCLUDE_BODIES are
// also OPTIONAL (CHAOS-3849 round 3, review finding 3): each maps to its
// production embedprovider.Env* counterpart. Left unset, each falls through
// to embedprovider's own default for that field (see benchmarkLookup's
// per-case comments) -- correct for most runs. PREFIX_FAMILY and
// INCLUDE_BODIES are the two that matter most: both are SEMANTIC,
// identity-bearing (CHAOS-3833/3836) configuration that changes what text is
// actually embedded, so a run against a production deployment that sets
// either of these needs the SAME value here, or the harness measures a
// different embedding function than the one being evaluated.
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
	case embedprovider.EnvTimeout:
		// OPTIONAL (CHAOS-3849 round 2): unset falls through to
		// embedprovider.DefaultTimeout (250ms), which is deliberately
		// loopback-tuned -- bounding one COLD local-embedder call, not a
		// network round trip (see DefaultTimeout's doc comment). A remote
		// OpenAI-shaped embedder cannot complete in 250ms, so a benchmark run
		// against one needs this raised (production runs it at 45s) or every
		// embed call fails with "context deadline exceeded".
		return value("ACR_TEST_EMBED_TIMEOUT")
	case embedprovider.EnvMaxTransportRetries:
		// OPTIONAL (CHAOS-3849 round 2): unset falls through to
		// embedprovider.DefaultMaxTransportRetries (0), sensible only for a
		// local embedder where a retry buys nothing but latency (see its doc
		// comment). Production runs remote embedders at 5.
		return value("ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES")
	case embedprovider.EnvSimilarityFloor:
		return value("ACR_TEST_EMBED_SIMILARITY_FLOOR")
	case embedprovider.EnvMaxBatch:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset falls
		// through to embedprovider.DefaultMaxBatch. Not identity-bearing --
		// a batch-size mismatch changes request shape, not what gets
		// embedded -- but still worth matching production so a benchmark run
		// measures the same request pattern, not an artificially
		// smaller/larger one.
		return value("ACR_TEST_EMBED_MAX_BATCH")
	case embedprovider.EnvMaxTextRunes:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset falls
		// through to embedprovider.DefaultMaxTextRunes. A mismatch here
		// changes which characters of a long text actually reach the
		// embedder (TruncateRunes), silently altering what gets embedded
		// without any error.
		return value("ACR_TEST_EMBED_MAX_TEXT_RUNES")
	case embedprovider.EnvPrefixFamily:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset means
		// PrefixFamilyNone (see the constant's own doc comment). This is
		// SEMANTIC, identity-bearing configuration (CHAOS-3836) -- the
		// applied task-prefix pair changes what text is actually embedded,
		// so a harness left on PrefixFamilyNone against a production
		// deployment that configures a real family measures a different
		// embedding function entirely, silently.
		return value("ACR_TEST_EMBED_PREFIX_FAMILY")
	case embedprovider.EnvExpectResponseModel:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset means the
		// server must report exactly EnvModel's id (see its own doc
		// comment). Only needs setting for a provider known to rename its
		// own id in the response; leaving it unset is the correct default
		// for most harness runs, not an omission.
		return value("ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL")
	case embedprovider.EnvProviderLocality:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset means
		// "remote" (see BodiesIncluded's doc comment) -- a SEMANTIC,
		// identity-bearing (CHAOS-3833) fail-closed default, not an
		// omission by itself. Mapped so a harness run against a genuinely
		// local test embedder can affirmatively declare that, matching
		// whatever production declares for the SAME endpoint rather than
		// silently taking the fail-closed default regardless of what
		// production is actually configured to do.
		return value("ACR_TEST_EMBED_PROVIDER_LOCALITY")
	case embedprovider.EnvIncludeBodies:
		// OPTIONAL (CHAOS-3849 round 3, review finding 3): unset means the
		// locality-derived default applies (see BodiesIncluded). SEMANTIC,
		// identity-bearing (CHAOS-3833): the effective value joins the
		// composition tag, so a harness silently defaulting this away from
		// production's value measures a different composed search text,
		// not merely a differently-configured one.
		return value("ACR_TEST_EMBED_INCLUDE_BODIES")
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

// TestBenchmarkLookupOptionalEmbedProviderEnvVars is the CHAOS-3849 round-2
// AND round-3 regression, folded into one table: without a benchmarkLookup
// case, each of these embedprovider Env* keys falls through to the default
// branch and reports "not configured" no matter what its dedicated
// ACR_TEST_* variable holds, silently pinning every benchmark/oracle run to
// embedprovider's own default for that field instead of a value the harness
// operator actually asked for.
//
//   - EnvTimeout / EnvMaxTransportRetries (round 2): embedprovider.DefaultTimeout
//     (250ms) is deliberately loopback-tuned for a COLD LOCAL embedder call,
//     not a network round trip; DefaultMaxTransportRetries is 0. A real
//     remote embedder cannot complete within 250ms, so a run against one
//     fails every embed call with "context deadline exceeded" -- observed
//     live against a real 35,986-vector org once the round-1 credential fix
//     got past the 401.
//   - EnvMaxBatch / EnvMaxTextRunes / EnvPrefixFamily / EnvExpectResponseModel /
//     EnvProviderLocality / EnvIncludeBodies (round-3 review finding 3): any
//     of these silently defaulting can shift the TEST embedder's semantics
//     -- and PrefixFamily/IncludeBodies specifically shift its STAMPED
//     IDENTITY (CHAOS-3833/3836 composition tag) -- away from what
//     production actually runs, invalidating a measurement without an
//     error, not merely mistiming it.
//
// The retries case uses "5" (a valid integer), not a duration string:
// MaxTransportRetries is parsed with envInt (config.go), so a value like
// "45s" this lookup-translation layer would happily echo back is exactly
// the kind of thing that only breaks one layer downstream, in ConfigFromEnv,
// where this test could not see it fail.
func TestBenchmarkLookupOptionalEmbedProviderEnvVars(t *testing.T) {
	tests := []struct {
		name      string
		envKey    string
		key       string
		wantValue string
	}{
		{"timeout", "ACR_TEST_EMBED_TIMEOUT", embedprovider.EnvTimeout, "45s"},
		{"max transport retries", "ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES", embedprovider.EnvMaxTransportRetries, "5"},
		{"max batch", "ACR_TEST_EMBED_MAX_BATCH", embedprovider.EnvMaxBatch, "32"},
		{"max text runes", "ACR_TEST_EMBED_MAX_TEXT_RUNES", embedprovider.EnvMaxTextRunes, "4000"},
		{"prefix family", "ACR_TEST_EMBED_PREFIX_FAMILY", embedprovider.EnvPrefixFamily, "e5"},
		{"expect response model", "ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL", embedprovider.EnvExpectResponseModel, "text-embedding-3-small-v2"},
		{"provider locality", "ACR_TEST_EMBED_PROVIDER_LOCALITY", embedprovider.EnvProviderLocality, "local"},
		{"include bodies", "ACR_TEST_EMBED_INCLUDE_BODIES", embedprovider.EnvIncludeBodies, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name+" set returns the configured value", func(t *testing.T) {
			t.Setenv(tt.envKey, tt.wantValue)
			got, ok := benchmarkLookup(tt.key)
			if !ok || got != tt.wantValue {
				t.Fatalf("benchmarkLookup(%s) = (%q, %v), want (%q, true)", tt.key, got, ok, tt.wantValue)
			}
		})
		t.Run(tt.name+" unset reports not-configured, not an error", func(t *testing.T) {
			t.Setenv(tt.envKey, "")
			got, ok := benchmarkLookup(tt.key)
			if ok || got != "" {
				t.Fatalf("benchmarkLookup(%s) = (%q, %v), want (\"\", false) when %s is unset -- embedprovider's own default for this field must remain reachable", tt.key, got, ok, tt.envKey)
			}
		})
	}
}

// embedproviderEnvVars is a small, human-readable CROSS-CHECK list of every
// embedprovider.Env* configuration key expected to exist today -- NOT the
// closure guarantee itself (see TestBenchmarkLookupCoversEveryEmbedproviderEnvVar
// below, which is round-4-hardened to need no such list: it binds directly
// to discoverEmbedproviderEnvVars' parsed output, so a NEW constant is
// caught with zero human list-maintenance). This slice exists only so
// TestEmbedproviderEnvVarsListMatchesPackageSource can flag drift between
// what a reader of this file expects and what the source package actually
// declares, in either direction -- it plays no role in whether the closure
// test itself catches an omission.
var embedproviderEnvVars = []string{
	embedprovider.EnvProvider,
	embedprovider.EnvBaseURL,
	embedprovider.EnvModel,
	embedprovider.EnvDimension,
	embedprovider.EnvAPIKey,
	embedprovider.EnvSimilarityFloor,
	embedprovider.EnvTimeout,
	embedprovider.EnvMaxBatch,
	embedprovider.EnvMaxTextRunes,
	embedprovider.EnvPrefixFamily,
	embedprovider.EnvExpectResponseModel,
	embedprovider.EnvMaxTransportRetries,
	embedprovider.EnvAllowInsecureBaseURL,
	embedprovider.EnvProviderLocality,
	embedprovider.EnvIncludeBodies,
}

// benchmarkLookupEnvVarFor is the exact ACR_TEST_* variable name
// benchmarkLookup's switch reads for each production key, EXCEPT
// EnvAllowInsecureBaseURL (see TestBenchmarkLookupCoversEveryEmbedproviderEnvVar)
// which has no dedicated variable at all -- benchmarkLookup answers it with
// a fixed policy value regardless of environment. Kept as its own map
// (rather than merged into benchmarkLookup itself) so the closure test's
// expectation is written independently of the switch it is checking -- a
// copy-paste of the switch into the test would prove nothing. This map is
// necessarily still hand-maintained: the ACR_TEST_* name for a given
// production key is an arbitrary choice benchmarkLookup's author makes, not
// something derivable from parsing embedprovider's source -- but a NEW key
// missing from this map still fails TestBenchmarkLookupCoversEveryEmbedproviderEnvVar
// loudly (see that test), it just cannot ALSO be round-tripped until a human
// adds the entry here to match whatever benchmarkLookup's own case ends up
// using.
var benchmarkLookupEnvVarFor = map[string]string{
	embedprovider.EnvProvider:            "ACR_TEST_EMBED_PROVIDER",
	embedprovider.EnvBaseURL:             "ACR_TEST_EMBED_BASE_URL",
	embedprovider.EnvModel:               "ACR_TEST_EMBED_MODEL",
	embedprovider.EnvDimension:           "ACR_TEST_EMBED_DIMENSION",
	embedprovider.EnvAPIKey:              "ACR_TEST_EMBED_API_KEY",
	embedprovider.EnvSimilarityFloor:     "ACR_TEST_EMBED_SIMILARITY_FLOOR",
	embedprovider.EnvTimeout:             "ACR_TEST_EMBED_TIMEOUT",
	embedprovider.EnvMaxBatch:            "ACR_TEST_EMBED_MAX_BATCH",
	embedprovider.EnvMaxTextRunes:        "ACR_TEST_EMBED_MAX_TEXT_RUNES",
	embedprovider.EnvPrefixFamily:        "ACR_TEST_EMBED_PREFIX_FAMILY",
	embedprovider.EnvExpectResponseModel: "ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL",
	embedprovider.EnvMaxTransportRetries: "ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES",
	embedprovider.EnvProviderLocality:    "ACR_TEST_EMBED_PROVIDER_LOCALITY",
	embedprovider.EnvIncludeBodies:       "ACR_TEST_EMBED_INCLUDE_BODIES",
}

// embedproviderImportPath is the package packages.Load resolves for
// discoverEmbedproviderEnvVars, below -- an import path, not a filesystem
// path, so resolution goes through the SAME module-aware machinery
// `go build`/`go test` use, unlike round 4's manual filesystem-relative walk
// from runtime.Caller(0).
const embedproviderImportPath = "github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"

// discoverEmbedproviderEnvVars is the CHAOS-3849 round-5 fix (round 3 on
// this closure test's class: hardcoded list -> static go/parser AST ->
// compiler semantics via go/types -- no more syntactic patches). It loads
// the embedprovider package with golang.org/x/tools/go/packages
// (NeedTypes|NeedTypesInfo) and reads every exported package-scope object
// whose name starts with "Env" and whose TYPE-CHECKED value is a string
// constant, via go/constant.StringVal(obj.Val()). This is what
// TestBenchmarkLookupCoversEveryEmbedproviderEnvVar iterates over, NOT any
// hand-maintained list: a new Env* constant is picked up here automatically,
// with no test file for a human to remember to touch.
//
// This is STRUCTURAL closure, not another syntactic approximation -- for two
// reasons, both required by the round-5 review:
//
//	(a) The value comes from the type-checker's own constant evaluator
//	    (types.Const.Val()), not from pattern-matching source syntax against
//	    one hardcoded shape. Round 4's go/parser walk only recognized a BARE
//	    *ast.BasicLit string -- `const EnvNew = envPrefix + "NEW"`
//	    (concatenation), a type-aliased constant, a parenthesized literal, an
//	    iota-based continuation, or a value assembled from a constant
//	    declared in a DIFFERENT file of the same package would all have been
//	    silently excluded, each one a distinct syntactic form someone would
//	    eventually have to notice and patch this discovery function for.
//	    Every one of those is a legal Go constant expression that
//	    type-checks to a single computed string value, and go/types hands
//	    back exactly that computed value regardless of which spelling
//	    produced it -- there is no second form to special-case, because the
//	    type checker has already reduced all of them to the same
//	    representation this code reads.
//	(b) packages.Load runs through the SAME `go list`-driven build machinery
//	    `go build`/`go test` themselves use, so it applies the ACTIVE build
//	    configuration (GOOS/GOARCH, build tags) when deciding which files
//	    belong to the package -- an Env* constant in a file excluded by a
//	    build constraint on this platform is excluded from
//	    pkg.Types.Scope() exactly as it would be from the real build. Round
//	    4's parser.ParseDir walked every .go file in the directory
//	    unconditionally, with no notion of "inactive for this build" at all,
//	    so an inactive-platform constant would have over-collected and could
//	    have spuriously failed CI on a different GOOS/GOARCH.
//
// A load error is a hard test failure (t.Fatalf), never a skip: this
// closure test must run in CI, and a load failure silently skipping it
// would be the same "quietly measures nothing" failure mode the CHAOS-3831
// harness had before its own credential/timeout fixes (round 1/2 of this
// same ticket) -- no special GOFLAGS/env handling was needed here in
// practice (packages.Load works against this repo's ordinary module
// context), but a failure surfaces loudly rather than degrading to an
// empty, silently-passing set.
func discoverEmbedproviderEnvVars(t *testing.T) []string {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, embedproviderImportPath)
	if err != nil {
		t.Fatalf("packages.Load(%s): %v", embedproviderImportPath, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load(%s) reported %d package error(s) (printed above) -- embedprovider did not type-check cleanly", embedproviderImportPath, n)
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("packages.Load(%s) returned %d package(s) with usable type information, want exactly 1", embedproviderImportPath, len(pkgs))
	}
	scope := pkgs[0].Types.Scope()
	var names []string
	for _, name := range scope.Names() {
		if !ast.IsExported(name) || !strings.HasPrefix(name, "Env") {
			continue
		}
		constObj, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue // an Env*-prefixed non-const at package scope is not a lookup key.
		}
		basic, ok := constObj.Type().Underlying().(*types.Basic)
		if !ok || basic.Info()&types.IsString == 0 {
			continue // only a string-kind constant is an env-var lookup name.
		}
		// constant.StringVal reads the type checker's OWN evaluated value --
		// this is the line that makes (a) above true: it is identical
		// whether the source wrote a bare literal, a concatenation, an
		// alias, or an iota-derived expression.
		names = append(names, constant.StringVal(constObj.Val()))
	}
	if len(names) == 0 {
		t.Fatalf("loaded %s but found zero exported Env*-prefixed string constants -- the package load is broken, not that embedprovider suddenly has none", embedproviderImportPath)
	}
	sort.Strings(names)
	return names
}

// TestEmbedproviderEnvVarsListMatchesPackageSource is a CROSS-CHECK, not the
// closure guarantee itself (see TestBenchmarkLookupCoversEveryEmbedproviderEnvVar,
// which binds directly to discoverEmbedproviderEnvVars and needs no human
// list-maintenance to catch a new constant): it keeps the small,
// human-readable embedproviderEnvVars slice honest against the dynamically
// parsed set, failing in EITHER direction -- an entry in the slice that no
// longer exists in the package, or a constant in the package missing from
// the slice -- so the slice never quietly goes stale as documentation even
// though it is not what makes the closure test itself bite.
func TestEmbedproviderEnvVarsListMatchesPackageSource(t *testing.T) {
	discovered := discoverEmbedproviderEnvVars(t)
	discoveredSet := make(map[string]bool, len(discovered))
	for _, name := range discovered {
		discoveredSet[name] = true
	}
	listedSet := make(map[string]bool, len(embedproviderEnvVars))
	for _, name := range embedproviderEnvVars {
		listedSet[name] = true
	}
	for _, name := range discovered {
		if !listedSet[name] {
			t.Errorf("embedprovider defines %s but embedproviderEnvVars does not list it -- add it there (and to benchmarkLookupEnvVarFor / benchmarkLookup's switch if it needs a case)", name)
		}
	}
	for _, name := range embedproviderEnvVars {
		if !discoveredSet[name] {
			t.Errorf("embedproviderEnvVars lists %s but embedprovider no longer defines a matching exported Env*-prefixed string constant -- remove it", name)
		}
	}
}

// TestBenchmarkLookupCoversEveryEmbedproviderEnvVar is the round-3 class-
// closure test (review finding 3), made round-4-DYNAMIC (review finding 1):
// it iterates discoverEmbedproviderEnvVars' PARSED output, not a hardcoded
// list, so a future embedprovider Env* addition is discovered automatically
// and fails this test with zero human list-maintenance if benchmarkLookup
// (or this file's benchmarkLookupEnvVarFor) has not been updated for it.
//
// For every discovered key, benchmarkLookup must be a REAL mapping, not the
// unmapped default branch -- which ALSO returns ("", false) for an unset
// optional field, so the two are indistinguishable from a bare "is it
// falsy" check. This proves mapping by ROUND-TRIPPING a unique sentinel
// through each key's dedicated ACR_TEST_* variable (benchmarkLookupEnvVarFor,
// written independently of benchmarkLookup's own switch) via t.Setenv and
// confirming benchmarkLookup echoes it back -- an unmapped key would fall to
// the default branch and return ("", false) instead of the sentinel,
// failing the test.
//
// EnvAllowInsecureBaseURL is the one exception: benchmarkLookup deliberately
// answers it with a fixed policy value ("true", true) regardless of any
// environment variable (see its case), so it is checked for that fixed value
// instead of a sentinel round-trip, which would fail against a correctly
// mapped but intentionally non-configurable key. A discovered key that is
// NEITHER in benchmarkLookupEnvVarFor NOR EnvAllowInsecureBaseURL fails
// immediately with a message naming both possibilities, since a brand new
// constant's intended shape (round-tripped vs. fixed-value) is not something
// this test can infer on its own -- a human must decide and update
// benchmarkLookupEnvVarFor (or this test's exception) to match whatever
// benchmarkLookup ends up doing.
func TestBenchmarkLookupCoversEveryEmbedproviderEnvVar(t *testing.T) {
	for _, key := range discoverEmbedproviderEnvVars(t) {
		key := key
		t.Run(key, func(t *testing.T) {
			if key == embedprovider.EnvAllowInsecureBaseURL {
				got, ok := benchmarkLookup(key)
				if !ok || got != "true" {
					t.Fatalf("benchmarkLookup(%s) = (%q, %v), want the fixed policy value (\"true\", true) -- benchmarkLookup has no case for this key, or its case regressed", key, got, ok)
				}
				return
			}
			envName, known := benchmarkLookupEnvVarFor[key]
			if !known {
				t.Fatalf("embedprovider defines %s but neither benchmarkLookupEnvVarFor nor this test's fixed-value exception knows about it -- benchmarkLookup needs a case for it, and this test needs updating to check for it (a sentinel round-trip via a new ACR_TEST_* name, or a fixed-value check like EnvAllowInsecureBaseURL's)", key)
			}
			sentinel := "sentinel-value-for-" + key
			t.Setenv(envName, sentinel)
			got, ok := benchmarkLookup(key)
			if !ok || got != sentinel {
				t.Fatalf("benchmarkLookup(%s) = (%q, %v) with %s=%q set, want (%q, true) -- benchmarkLookup has no case mapping this embedprovider Env* key to its ACR_TEST_* variable, so it silently defaults instead of erroring",
					key, got, ok, envName, sentinel, sentinel)
			}
		})
	}
}
