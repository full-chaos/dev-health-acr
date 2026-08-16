package hosted_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3742 three-arm GENERATIVE model trial harness.
//
// This is a NEW harness, not a repurposed one: `go test ./internal/api -run
// LiveEndpoint` (docs/operations.md's documented recipe) drives one fixed
// synthetic question against a FAKE graph/fact reader, repeated per
// invocation -- it is not a corpus sweep (that is how the historical "nano
// 2/33" / "nano+luna 16/17" diagnostic numbers were produced: the same
// question run 33 and 17 times). This harness instead runs a real,
// distinct-question corpus through the REAL production composition root
// (hosted.Open -- the exact entrypoint cmd/acr-api/main.go calls), so the
// trial measures production wiring, not a lookalike: real FalkorDB
// subject resolution, real canonical facts from Postgres/ClickHouse via
// devhealthfacts, and a real model runtime.
//
// Corpus format is the ambiguityCase shape already established by
// ambiguity_benchmark_live_test.go (internal/contextfabric/falkorgraph),
// derived from that code, never from the withheld corpus file itself:
//
//	[{"question": "...", "expect_kind": "project", "expect_id": "project_x"}, ...]
//
// An empty expect_id is a no-match control: the correct outcome is
// committing nothing.
//
// EVERY input is a dedicated ACR_TEST_TRIAL_* name, mapped onto the real
// ACR_CONTEXT_FABRIC_*/ACR_POSTGRES_DSN/ACR_CLICKHOUSE_DSN names only right
// before calling the real composition root -- the same "never read ambient
// production env" discipline ambiguity_benchmark_live_test.go documents,
// so a run can never silently reach a deployment's own configuration.
//
// PII / withholding discipline: the corpus is read but its question text is
// NEVER logged, persisted, or included in the output capture -- only an
// index, the coarse expected subject KIND (a category, e.g. "project", not
// authored content), outcome booleans, and result metadata (status,
// counts, latency). Mirrors internal/api/context_fabric_live_endpoint_test.go's
// "answer_length only, never the answer text itself" rule, extended to the
// question too.
//
// Side effects on the shared dev stack (disclosed, not hidden): the real
// composition root does what production does on every real investigation --
// each case call persists its own investigation-result row and its own
// model-receipt row to Postgres, scoped under fresh, non-colliding result
// IDs (this is the trial's own output, and the receipt rows are the cost
// data source this harness reports on). Opening the runtime also runs one
// synchronous packet-snapshot purge cycle (deletes only already-EXPIRED
// evidence-packet rows) and, for the run's duration, a 5-minute purge
// ticker -- the identical idempotent cleanup loop the already-running
// acr-api container performs continuously today. No graph write path is
// touched (GraphReader is read-only; the projection writer is a separate,
// unused adapter), and no existing row is mutated or deleted.
//
//	ACR_TEST_TRIAL_CORPUS=/path/to/corpus.json \
//	ACR_TEST_TRIAL_ORG=<org-id> \
//	ACR_TEST_TRIAL_OUT=/path/to/output.json \
//	ACR_TEST_TRIAL_ARM=nano_alone \
//	ACR_TEST_TRIAL_FALKOR_ADDR=127.0.0.1:16379 \
//	ACR_TEST_TRIAL_POSTGRES_DSN=postgres://... \
//	ACR_TEST_TRIAL_CLICKHOUSE_DSN=clickhouse://... \
//	ACR_TEST_TRIAL_MODEL=gpt-5-nano \
//	[ACR_TEST_TRIAL_MODEL_FALLBACK=gpt-5.6-luna] \
//	ACR_TEST_TRIAL_MODEL_API_KEY=... \
//	ACR_TEST_TRIAL_EMBED_MODEL=text-embedding-3-large \
//	ACR_TEST_TRIAL_EMBED_DIMENSION=3072 \
//	ACR_TEST_TRIAL_EMBED_API_KEY=... \
//	[ACR_TEST_TRIAL_LIMIT=2] \
//	  go test ./internal/runtime/hosted -run TestGenerativeTrialCorpus -v -timeout 2h
func TestGenerativeTrialCorpus(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_TRIAL_OUT")
	arm := os.Getenv("ACR_TEST_TRIAL_ARM")

	corpus := loadTrialCorpus(t, corpusPath)
	limit := len(corpus)
	if raw := os.Getenv("ACR_TEST_TRIAL_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("ACR_TEST_TRIAL_LIMIT must be a positive integer, got %q", raw)
		}
		if n < limit {
			limit = n
		}
	}

	wireProductionEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rt, err := hosted.Open(ctx, cfg, hosted.Options{ServiceVersion: "chaos-3742-generative-trial", Logger: logger, Now: time.Now})
	if err != nil {
		t.Fatalf("open hosted runtime: %v", err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Logf("runtime close: %v", err)
		}
	}()
	investigator := rt.Dependencies.Runtime.Investigator
	if investigator == nil {
		t.Fatal("investigator is nil -- graph reads not enabled or FalkorDB not configured; check ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED and the FALKOR_* mapping")
	}

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	report := trialReport{Arm: arm, CorpusTotal: len(corpus), CasesRun: 0}
	firstTen := make([]caseOutcome, 0, 10)
	for i := 0; i < limit; i++ {
		testCase := corpus[i]
		outcome := runTrialCase(ctx, t, investigator, principal, i, testCase)
		report.Cases = append(report.Cases, outcome)
		report.CasesRun++
		tallyOutcome(&report, outcome)

		// Sanity-anchor control (team-lead ruling): ANY control commitment
		// is a red flag reported immediately, not batched to the end --
		// stop this arm right here rather than spend the rest of the
		// budget on a possibly-miswired harness.
		if outcome.Outcome == "control_violation" {
			report.ControlViolationAbort = true
			t.Logf("STOP: arm %q committed a subject for a no-match CONTROL case at index %d -- suspected harness-wiring issue, aborting this arm for review rather than finishing it", arm, i)
			break
		}

		if i < 10 {
			firstTen = append(firstTen, outcome)
		}
		if i == 9 {
			if class, count, correct := earlyAbortSignature(firstTen); count >= 6 && correct <= 1 {
				report.EarlyAbort = true
				report.EarlyAbortSignature = fmt.Sprintf("%s x%d/10 (correct=%d)", class, count, correct)
				t.Logf("EARLY ABORT for arm %q: dominant failure class %q in %d/10 first cases, only %d correct -- stopping this arm early per the systematic-failure control", arm, class, count, correct)
				break
			}
		}
		// Sanity-anchor control (team-lead ruling): the benchmark's own
		// resolution behavior commits ~4/50 (~8%). A wild divergence after
		// enough cases to be meaningful (very high or persistently zero
		// commit rate) means STOP and report a suspected harness-wiring
		// issue before finishing the arm.
		if i == 14 {
			rate := float64(report.CommittedTotal) / float64(report.CasesRun)
			if rate > 0.30 {
				report.SuspectedWiringIssue = fmt.Sprintf("commit rate %.0f%% after %d cases is far above the benchmark's ~8%% -- suspected over-commit", rate*100, report.CasesRun)
				t.Logf("STOP: arm %q -- %s", arm, report.SuspectedWiringIssue)
				break
			}
		}
	}

	blob, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(outPath, blob, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("arm=%s cases_run=%d/%d correct=%d wrong_commit=%d control_violations=%d no_commit=%d unusable=%d early_abort=%v control_violation_abort=%v suspected_wiring_issue=%q stages=%v -> %s",
		report.Arm, report.CasesRun, report.CorpusTotal, report.Correct, report.WrongCommit, report.ControlViolations, report.NoCommit, report.Unusable, report.EarlyAbort, report.ControlViolationAbort, report.SuspectedWiringIssue, report.StageDistribution, outPath)
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is required", key)
	}
	return v
}

// trialCase mirrors falkorgraph's ambiguityCase JSON shape exactly (same
// corpus, same field names) -- derived from that code, not from the
// withheld corpus file.
type trialCase struct {
	Question   string `json:"question"`
	ExpectKind string `json:"expect_kind"`
	ExpectID   string `json:"expect_id"`
}

func loadTrialCorpus(t *testing.T, path string) []trialCase {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trial corpus: %v", err)
	}
	var corpus []trialCase
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse trial corpus: %v", err)
	}
	if len(corpus) < 50 {
		t.Fatalf("trial corpus has %d cases; CHAOS-3742 requires at least 50", len(corpus))
	}
	return corpus
}

// wireProductionEnv maps this harness's dedicated ACR_TEST_TRIAL_* inputs
// onto the real production env-var names hosted.Open's composition reads,
// via t.Setenv (auto-restored). Required-backing-stores auxiliary config
// (evidence key, device verification URL) is unused by this harness's
// investigation path but must satisfy config.Validate(); throwaway values
// are generated here rather than reused from any real deployment secret.
func wireProductionEnv(t *testing.T) {
	t.Helper()
	set := func(key, value string) {
		if value != "" {
			t.Setenv(key, value)
		}
	}
	set("ACR_ENVIRONMENT", "test")
	set("ACR_REQUEST_TIMEOUT", "490s")
	set("ACR_REQUIRE_BACKING_STORES", "true")
	set("ACR_POSTGRES_DSN", requireEnv(t, "ACR_TEST_TRIAL_POSTGRES_DSN"))
	set("ACR_CLICKHOUSE_DSN", requireEnv(t, "ACR_TEST_TRIAL_CLICKHOUSE_DSN"))

	set("ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED", "true")
	set("ACR_CONTEXT_FABRIC_FALKOR_ADDR", requireEnv(t, "ACR_TEST_TRIAL_FALKOR_ADDR"))
	set("ACR_CONTEXT_FABRIC_FALKOR_TLS", "false")
	set("ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE", "true")

	set("ACR_CONTEXT_FABRIC_MODEL_PROVIDER", "openai")
	set("ACR_CONTEXT_FABRIC_MODEL", requireEnv(t, "ACR_TEST_TRIAL_MODEL"))
	// Fallback is genuinely OPTIONAL (that is how "nano alone" / "luna
	// alone" are expressed): only mapped when the caller sets it.
	set("ACR_CONTEXT_FABRIC_MODEL_FALLBACK", os.Getenv("ACR_TEST_TRIAL_MODEL_FALLBACK"))
	set("ACR_CONTEXT_FABRIC_MODEL_API_KEY", requireEnv(t, "ACR_TEST_TRIAL_MODEL_API_KEY"))

	// Embedder identity MUST match the deployed
	// openai/text-embedding-3-large#t3:r2000:b0:pnone graph (proven-recipe
	// requirement) -- mismatched identity fences the vector arm off.
	set("ACR_CONTEXT_FABRIC_EMBED_BASE_URL", "https://api.openai.com/v1")
	set("ACR_CONTEXT_FABRIC_EMBED_PROVIDER", "openai")
	set("ACR_CONTEXT_FABRIC_EMBED_MODEL", requireEnv(t, "ACR_TEST_TRIAL_EMBED_MODEL"))
	set("ACR_CONTEXT_FABRIC_EMBED_DIMENSION", requireEnv(t, "ACR_TEST_TRIAL_EMBED_DIMENSION"))
	set("ACR_CONTEXT_FABRIC_EMBED_API_KEY", requireEnv(t, "ACR_TEST_TRIAL_EMBED_API_KEY"))
	set("ACR_CONTEXT_FABRIC_EMBED_TIMEOUT", "45s")
	set("ACR_CONTEXT_FABRIC_EMBED_MAX_TRANSPORT_RETRIES", "5")

	// Throwaway evidence key: unused by the Investigate() path this
	// harness exercises, but openClickHouse's evidence-ID codec
	// construction requires a structurally valid one to satisfy
	// RequireBackingStores composition.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate throwaway evidence key: %v", err)
	}
	set("ACR_EVIDENCE_ID_ACTIVE_KID", "trial")
	set("ACR_EVIDENCE_ID_KEYS", "trial="+base64.StdEncoding.EncodeToString(key))
	set("ACR_DEVICE_VERIFICATION_URL", "http://127.0.0.1/unused-trial-device-endpoint")
}

// contextFabricRejectionClass mirrors internal/api/context_fabric_routes.go's
// writeContextFabricError classification chain (same order, same
// sentinels) so this harness's failure taxonomy matches production's,
// rather than inventing a parallel one.
func contextFabricRejectionClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, contextfabric.ErrInvalidTimeBound):
		return "invalid_time_bound"
	case errors.Is(err, contextfabric.ErrRateLimited), errors.Is(err, contextfabric.ErrModelRateLimited):
		return "rate_limited"
	case errors.Is(err, contextfabric.ErrUnavailable), errors.Is(err, contextfabric.ErrModelUnavailable):
		return "dependency_unavailable"
	case errors.Is(err, contextfabric.ErrInterpretationRejected):
		return "interpretation_rejected"
	case errors.Is(err, contextfabric.ErrSynthesisRejected):
		return "synthesis_rejected"
	case errors.Is(err, contextfabric.ErrModelOutput):
		return "model_output_invalid"
	default:
		return "unclassified"
	}
}

type caseOutcome struct {
	Index      int    `json:"index"`
	IsControl  bool   `json:"is_control"`
	ExpectKind string `json:"expect_kind,omitempty"`
	Outcome    string `json:"outcome"`
	// Stage buckets the outcome by how FAR the investigation got, for the
	// stage-resolved distribution team-lead asked for: interpret_rejected /
	// synthesis_rejected / model_call_failed (stage-ambiguous provider/
	// transport failure) / invalid_result_downstream / no_match /
	// clarification_required / committed_no_synthesis / usable_answer.
	Stage                  string  `json:"stage"`
	Status                 string  `json:"status,omitempty"`
	ErrorClass             string  `json:"error_class,omitempty"`
	CommittedCount         int     `json:"committed_count"`
	CandidateCount         int     `json:"candidate_count,omitempty"`
	TopCandidateConfidence float64 `json:"top_candidate_confidence,omitempty"`
	CommittedKindMatch     bool    `json:"committed_kind_match,omitempty"`
	LatencyMS              int64   `json:"latency_ms"`
	AnswerLength           int     `json:"answer_length,omitempty"`
	ClaimedFacts           int     `json:"claimed_facts,omitempty"`
	Drivers                int     `json:"drivers,omitempty"`
	RetrievalDegraded      bool    `json:"retrieval_degraded,omitempty"`
}

func runTrialCase(ctx context.Context, t *testing.T, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase) caseOutcome {
	t.Helper()
	isControl := tc.ExpectID == ""
	request := contractsv1.ContextFabricInvestigationRequest{
		SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:     fmt.Sprintf("request_trial%06d", index),
		Question:      tc.Question,
		TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3742-trial", Version: "0.1.0", Surface: "trial"},
	}

	callCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()

	started := time.Now()
	result, err := investigator.Investigate(callCtx, principal, request)
	latency := time.Since(started)

	outcome := caseOutcome{Index: index, IsControl: isControl, ExpectKind: tc.ExpectKind, LatencyMS: latency.Milliseconds()}

	if err != nil {
		outcome.ErrorClass = contextFabricRejectionClass(err)
		outcome.Outcome = "error:" + outcome.ErrorClass
		outcome.Stage = errorStage(outcome.ErrorClass)
		return outcome
	}
	if verr := result.Validate(); verr != nil {
		outcome.ErrorClass = "invalid_result_downstream"
		outcome.Outcome = "error:" + outcome.ErrorClass
		outcome.Stage = "invalid_result_downstream"
		return outcome
	}

	outcome.Status = string(result.Status)
	outcome.CommittedCount = len(result.SubjectResolution.Committed)
	outcome.CandidateCount = len(result.SubjectResolution.Candidates)
	for _, cand := range result.SubjectResolution.Candidates {
		if cand.Confidence > outcome.TopCandidateConfidence {
			outcome.TopCandidateConfidence = cand.Confidence
		}
	}
	outcome.RetrievalDegraded = result.SubjectResolution.RetrievalDegraded
	outcome.AnswerLength = len(result.DeterministicAnswer)
	outcome.ClaimedFacts = len(result.ClaimedFacts)
	outcome.Drivers = len(result.Drivers)

	switch {
	case isControl && outcome.CommittedCount > 0:
		outcome.Outcome = "control_violation"
	case isControl:
		outcome.Outcome = "correct"
	case outcome.CommittedCount == 0:
		outcome.Outcome = "no_commit"
	case committedMatchesTrial(result.SubjectResolution.Committed, tc):
		outcome.CommittedKindMatch = true
		switch result.Status {
		case contractsv1.ContextFabricInvestigationComplete, contractsv1.ContextFabricInvestigationPartial:
			outcome.Outcome = "correct"
		default:
			outcome.Outcome = "resolved_but_unusable:" + string(result.Status)
		}
	default:
		outcome.Outcome = "wrong_commit"
	}
	outcome.Stage = successStage(result.Status, outcome.CommittedCount)
	return outcome
}

// errorStage maps an error-path failure class to the stage bucket
// team-lead asked for. interpretation_rejected/synthesis_rejected are
// stage-exact by construction; the remaining classes (model_output_invalid,
// dependency_unavailable, rate_limited, deadline_exceeded, invalid_time_bound,
// unclassified) can occur at either the interpret or synthesize call and
// contextfabric's sentinels do not carry which -- bucketed together rather
// than guessed.
func errorStage(class string) string {
	switch class {
	case "interpretation_rejected":
		return "interpret_rejected"
	case "synthesis_rejected":
		return "synthesis_rejected"
	default:
		return "model_call_failed"
	}
}

func successStage(status contractsv1.ContextFabricInvestigationStatus, committedCount int) string {
	switch status {
	case contractsv1.ContextFabricInvestigationNoMatch:
		return "no_match"
	case contractsv1.ContextFabricInvestigationClarificationRequired:
		return "clarification_required"
	case contractsv1.ContextFabricInvestigationComplete, contractsv1.ContextFabricInvestigationPartial:
		if committedCount > 0 {
			return "usable_answer"
		}
		return "clarification_required"
	default:
		if committedCount > 0 {
			return "committed_no_synthesis"
		}
		return "clarification_required"
	}
}

func committedMatchesTrial(committed []contractsv1.ContextFabricSubjectRef, tc trialCase) bool {
	for _, ref := range committed {
		if string(ref.Kind) == tc.ExpectKind && ref.CanonicalID == tc.ExpectID {
			return true
		}
	}
	return false
}

type trialReport struct {
	Arm               string         `json:"arm"`
	CorpusTotal       int            `json:"corpus_total"`
	CasesRun          int            `json:"cases_run"`
	Correct           int            `json:"correct"`
	WrongCommit       int            `json:"wrong_commit"`
	ControlViolations int            `json:"control_violations"`
	NoCommit          int            `json:"no_commit"`
	Unusable          int            `json:"unusable"`
	CommittedTotal    int            `json:"committed_total"`
	FailureClasses    map[string]int `json:"failure_classes,omitempty"`
	// StageDistribution is the stage-resolved outcome distribution
	// team-lead asked for -- most cases are expected to end in
	// clarification_required for every arm (the benchmark commits only
	// 4/50), so this is where an honest read of synthesis-quality coverage
	// (or the lack of it) lives, not just the top-line correct/total.
	StageDistribution     map[string]int `json:"stage_distribution"`
	EarlyAbort            bool           `json:"early_abort"`
	EarlyAbortSignature   string         `json:"early_abort_signature,omitempty"`
	ControlViolationAbort bool           `json:"control_violation_abort"`
	SuspectedWiringIssue  string         `json:"suspected_wiring_issue,omitempty"`
	Cases                 []caseOutcome  `json:"cases"`
}

func tallyOutcome(report *trialReport, outcome caseOutcome) {
	if report.StageDistribution == nil {
		report.StageDistribution = map[string]int{}
	}
	report.StageDistribution[outcome.Stage]++
	report.CommittedTotal += outcome.CommittedCount

	switch {
	case outcome.Outcome == "correct":
		report.Correct++
	case outcome.Outcome == "wrong_commit":
		report.WrongCommit++
	case outcome.Outcome == "control_violation":
		report.ControlViolations++
		report.WrongCommit++
	case outcome.Outcome == "no_commit":
		report.NoCommit++
	default:
		report.Unusable++
		if report.FailureClasses == nil {
			report.FailureClasses = map[string]int{}
		}
		class := outcome.ErrorClass
		if class == "" {
			class = outcome.Outcome
		}
		report.FailureClasses[class]++
	}
}

// earlyAbortSignature reports the most frequent TECHNICAL-failure class
// (an "error:" outcome -- interpretation/synthesis rejected, provider/
// transport failure, invalid downstream result) among the first ten cases,
// and how many correct answers appeared, for the systematic-failure
// early-abort control.
//
// Deliberately EXCLUDES clean no_commit/wrong_commit resolution outcomes
// (clarification_required, no_match) from the dominant-class count: those
// are a legitimate, expected engine behavior at this corpus's own known
// resolution rate (~4/50 commits per the AC-3778-2 benchmark; team-lead's
// ruling anticipated most cases ending in clarification for every arm), not
// a "systematic failure signature" the way a repeated technical error class
// is. The first diagnostic run against this corpus proved the distinction
// matters: 10/10 clean clarification_required, MaxSubjectCandidates-capped
// candidate sets at 0.5-0.755 confidence (below the auto-commit floor) on
// BOTH nano and luna alike -- explainable, model-independent retrieval
// behavior on a plausibly generic/ambiguous corpus segment, not a harness
// bug or a model malfunction. An earlier version of this function treated
// that as a dominant failure class and aborted the arm after 10 questions,
// which would have thrown away the other 80% of the corpus's signal on a
// false premise.
func earlyAbortSignature(firstTen []caseOutcome) (class string, count int, correct int) {
	counts := map[string]int{}
	for _, o := range firstTen {
		if o.Outcome == "correct" {
			correct++
			continue
		}
		if !strings.HasPrefix(o.Outcome, "error:") {
			continue
		}
		key := o.ErrorClass
		if key == "" {
			key = o.Outcome
		}
		counts[key]++
	}
	for k, v := range counts {
		if v > count {
			class, count = k, v
		}
	}
	return class, count, correct
}
