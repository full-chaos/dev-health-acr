package hosted_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
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
	runStartedAt := time.Now().UTC().Format(time.RFC3339)

	corpus, corpusHash := loadTrialCorpus(t, corpusPath)
	source := requireGitSourceIdentity(t)
	indices, targetedIndices := resolveTrialIndices(t, len(corpus))

	// ACR_TEST_TRIAL_EXCHANGE_DIR (arm 4, diagnostic): swaps ONLY the
	// generative transport for a file-exchange ModelRuntime an
	// out-of-process responder answers -- see file_exchange_runtime_test.go.
	// Everything else (graph, facts, receipts, engine, validation) is the
	// identical pipeline the other arms use.
	exchangeDir := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_DIR")
	wireProductionEnv(t, exchangeDir != "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// rawSignals (CHAOS-3858, measurement-only) captures the raw pre-remap
	// retrieval signal for every case's resolution -- see
	// trialRawSignalCollector's own doc comment. Wired unconditionally: it
	// costs nothing when nothing reads its snapshot, and every existing
	// report field stays additive/optional regardless.
	rawSignals := &trialRawSignalCollector{}
	options := hosted.Options{ServiceVersion: "chaos-3742-generative-trial", Logger: logger, Now: time.Now, RawSignalObserver: rawSignals}
	// caseTimeout bounds ONE Investigate() call, which can make up to two
	// sequential generative calls (interpret, then synthesize). 240s is
	// generous for a real API arm (historical p99 ~95s per LEG). For the
	// file-exchange arm this MUST exceed 2x the per-call exchange timeout
	// (fable-review S4/T5 finding: the prior fixed 240s could truncate an
	// exchange call before its own advertised timeout ever fired, making a
	// slow-but-legitimate responder answer look like a technical failure)
	// -- set generously below rather than trusting the two bounds to agree
	// by coincidence.
	caseTimeout := 240 * time.Second
	var exchangeRuntime *fileExchangeRuntime
	if exchangeDir != "" {
		timeout := 10 * time.Minute
		if raw := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT"); raw != "" {
			parsed, perr := time.ParseDuration(raw)
			if perr != nil {
				t.Fatalf("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT: %v", perr)
			}
			timeout = parsed
		}
		var ferr error
		exchangeRuntime, ferr = newFileExchangeRuntime(exchangeDir, arm, timeout)
		if ferr != nil {
			t.Fatalf("create file-exchange runtime: %v", ferr)
		}
		options.ModelRuntimeOverride = exchangeRuntime
		caseTimeout = 2*timeout + 30*time.Second
		t.Logf("arm %q uses the FILE-EXCHANGE diagnostic transport at %s (timeout %s/call, case budget %s, session %s) -- latency and token cost are NOT comparable to a real provider", arm, exchangeDir, timeout, caseTimeout, exchangeRuntime.nonce)
	}
	rt, err := hosted.Open(ctx, cfg, options)
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

	// targeted (ACR_TEST_TRIAL_INDICES set) means a small, explicit-index
	// reclassification rerun, not a normal sequential arm pass -- the
	// systematic-failure/sanity-anchor controls below are calibrated
	// against a full run starting at position 0 and would misfire on a
	// 3-6 case targeted subset, so they are skipped in that mode.
	targeted := targetedIndices

	// controlsContinue (CHAOS-3858 scorecard mode) opts a run OUT of the
	// control-violation abort below: it RECORDS the violation (same
	// ControlViolationAbort/outcome bookkeeping) and keeps going, instead of
	// stopping the arm. DEFAULT UNCHANGED -- every existing caller (every
	// ACR_TEST_TRIAL_* invocation that does not set this) still aborts on
	// the first control violation, exactly as today; this is an explicit
	// opt-in for the one case that needs every one of the 50 corpus cases
	// scored even if a control misfires (the epic's standing scorecard),
	// not a general relaxation of the sanity-anchor control.
	controlsContinue := os.Getenv("ACR_TEST_TRIAL_CONTROLS_CONTINUE") == "true"

	// Sanity-anchor checkpoint positions (fable-review "LIMIT<15 handling"
	// finding): the ORIGINAL fixed pos==9/pos==14 checks silently never
	// fired at all for a run shorter than 10/15 cases (ACR_TEST_TRIAL_LIMIT
	// under those values), so a short diagnostic run got NONE of the
	// systematic-failure/over-commit protection a full run gets. Both
	// checkpoints now scale to whatever the run actually has -- the LAST
	// position when the run is shorter than the nominal window, so a
	// short run is always checked once, at its own end, rather than never.
	earlyAbortCheckpoint := min(9, len(indices)-1)
	overCommitCheckpoint := min(14, len(indices)-1)

	provenance := trialProvenance{
		CorpusSHA256: corpusHash, Transport: "real_api", RunStartedAt: runStartedAt,
		SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
		Model: os.Getenv("ACR_TEST_TRIAL_MODEL"), ModelFallback: os.Getenv("ACR_TEST_TRIAL_MODEL_FALLBACK"),
		ControlsContinue: controlsContinue,
		CommitGate: trialCommitGateProvenance{
			LoneFloorEnv:                   os.Getenv(falkorgraph.EnvCommitLoneFloor),
			TopFloorEnv:                    os.Getenv(falkorgraph.EnvCommitTopFloor),
			TopGapEnv:                      os.Getenv(falkorgraph.EnvCommitTopGap),
			VectorMarginCommitThresholdEnv: os.Getenv(falkorgraph.EnvVectorMarginCommitThreshold),
		},
	}
	if exchangeRuntime != nil {
		provenance.Transport = "file_exchange"
		provenance.ExchangeModelName = arm
		provenance.ExchangeSessionID = exchangeRuntime.nonce
		provenance.Model, provenance.ModelFallback = "", ""
	}

	report := trialReport{Provenance: provenance, Arm: arm, CorpusTotal: len(corpus), CasesRun: 0}
	firstTen := make([]caseOutcome, 0, 10)
	for pos, i := range indices {
		testCase := corpus[i]
		outcome := runTrialCase(ctx, t, investigator, principal, i, testCase, caseTimeout, rawSignals)
		report.Cases = append(report.Cases, outcome)
		report.CasesRun++
		tallyOutcome(&report, outcome)

		if targeted {
			continue
		}

		// Sanity-anchor control (team-lead ruling): ANY control commitment
		// is a red flag reported immediately, not batched to the end --
		// stop this arm right here rather than spend the rest of the
		// budget on a possibly-miswired harness.
		if outcome.Outcome == "control_violation" {
			if controlsContinue {
				t.Logf("RECORDED (controls_continue): arm %q committed a subject for a no-match CONTROL case at index %d -- continuing per ACR_TEST_TRIAL_CONTROLS_CONTINUE", arm, i)
			} else {
				report.ControlViolationAbort = true
				t.Logf("STOP: arm %q committed a subject for a no-match CONTROL case at index %d -- suspected harness-wiring issue, aborting this arm for review rather than finishing it", arm, i)
				break
			}
		}

		if pos <= earlyAbortCheckpoint {
			firstTen = append(firstTen, outcome)
		}
		if pos == earlyAbortCheckpoint {
			if class, count, correct := earlyAbortSignature(firstTen); count >= 6 && correct <= 1 {
				report.EarlyAbort = true
				report.EarlyAbortSignature = fmt.Sprintf("%s x%d/%d (correct=%d)", class, count, len(firstTen), correct)
				t.Logf("EARLY ABORT for arm %q: dominant failure class %q in %d/%d first cases, only %d correct -- stopping this arm early per the systematic-failure control", arm, class, count, len(firstTen), correct)
				break
			}
		}
		// Sanity-anchor control (team-lead ruling): the benchmark's own
		// resolution behavior commits ~4/50 (~8%). A wild divergence after
		// enough cases to be meaningful (very high or persistently zero
		// commit rate) means STOP and report a suspected harness-wiring
		// issue before finishing the arm.
		if pos == overCommitCheckpoint {
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

// gitSourceIdentity reports the worktree's exact source state: HEAD SHA,
// whether the tree is dirty (git status --porcelain non-empty), and, when
// dirty, a digest of the actual diff content (so two independently-dirty
// runs are distinguishable, not just both flagged "dirty=true").
//
// FAILS CLOSED (sol review R1, from F2): a report whose provenance cannot
// name what code produced it is worse than no report -- refuse to run
// rather than silently write "" and let a reader assume "clean, known
// commit". A worktree/`git` genuinely unavailable is exactly the case
// provenance exists to catch, not paper over.
type gitSourceIdentity struct {
	commit     string
	dirty      bool
	diffDigest string // empty when !dirty
}

func requireGitSourceIdentity(t *testing.T) gitSourceIdentity {
	t.Helper()
	topOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("determine repo root (required for report provenance -- refusing to run rather than report with an unknown source identity): %v", err)
	}
	repoRoot := strings.TrimSpace(string(topOut))
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s (required for report provenance): %v", strings.Join(args, " "), err)
		}
		return out
	}
	commit := strings.TrimSpace(string(git("rev-parse", "HEAD")))
	statusOut := git("status", "--porcelain")
	identity := gitSourceIdentity{commit: commit}
	if strings.TrimSpace(string(statusOut)) != "" {
		identity.dirty = true
		diffOut := git("diff", "HEAD")
		digest := sha256.New()
		digest.Write(diffOut)
		// sol review R1 residual: `git diff HEAD` only covers TRACKED
		// changes -- a brand-new untracked file makes the tree dirty (the
		// status check above already sees it) but was invisible to the
		// digest. Fold every untracked path's content in too, sorted for
		// determinism, so the digest actually reflects everything that
		// makes this tree not equal to SourceCommit's. `git status
		// --porcelain` paths are REPO-ROOT-relative regardless of the
		// process's own cwd (verified live), so they are joined against
		// repoRoot here, not read as-is.
		for _, path := range untrackedPaths(t, statusOut) {
			content, err := os.ReadFile(filepath.Join(repoRoot, path))
			if err != nil {
				t.Fatalf("hash untracked file %s for provenance digest: %v", path, err)
			}
			digest.Write([]byte("\x00" + path + "\x00"))
			sum := sha256.Sum256(content)
			digest.Write(sum[:])
		}
		identity.diffDigest = hex.EncodeToString(digest.Sum(nil))
	}
	return identity
}

// untrackedPaths extracts the "??"-prefixed (untracked) file paths from
// `git status --porcelain` output, sorted for deterministic digest input.
func untrackedPaths(t *testing.T, porcelain []byte) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(string(porcelain), "\n") {
		if strings.HasPrefix(line, "?? ") {
			paths = append(paths, strings.TrimPrefix(line, "?? "))
		}
	}
	sort.Strings(paths)
	return paths
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

// loadTrialCorpus reads the corpus file ONCE (sol review R1, from F2:
// provenance must hash the exact bytes that were parsed, not a second,
// separately-read copy that could in principle differ) and returns both
// the parsed cases and the SHA-256 of those same raw bytes.
func loadTrialCorpus(t *testing.T, path string) ([]trialCase, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trial corpus: %v", err)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var corpus []trialCase
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse trial corpus: %v", err)
	}
	if len(corpus) < 50 {
		t.Fatalf("trial corpus has %d cases; CHAOS-3742 requires at least 50", len(corpus))
	}
	return corpus, hash
}

// resolveTrialIndices (CHAOS-3853: extracted out of TestGenerativeTrialCorpus
// so the frontier-baseline-arm harness can share the exact same
// ACR_TEST_TRIAL_INDICES/ACR_TEST_TRIAL_LIMIT semantics rather than
// reimplementing them) returns the ordered set of corpus positions a run
// processes, and whether this was a targeted (ACR_TEST_TRIAL_INDICES) run.
// ACR_TEST_TRIAL_INDICES (comma-separated, e.g. "27,28,29") selects an EXACT
// subset -- for targeted reclassification reruns, so a rerun does not have to
// pay for the other already-known cases. Otherwise ACR_TEST_TRIAL_LIMIT (a
// prefix count) or the full corpus applies.
func resolveTrialIndices(t *testing.T, corpusLen int) (indices []int, targeted bool) {
	t.Helper()
	if raw := os.Getenv("ACR_TEST_TRIAL_INDICES"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 0 || n >= corpusLen {
				t.Fatalf("ACR_TEST_TRIAL_INDICES: invalid index %q (corpus has %d cases)", part, corpusLen)
			}
			indices = append(indices, n)
		}
		return indices, true
	}
	limit := corpusLen
	if raw := os.Getenv("ACR_TEST_TRIAL_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("ACR_TEST_TRIAL_LIMIT must be a positive integer, got %q", raw)
		}
		if n < limit {
			limit = n
		}
	}
	for i := 0; i < limit; i++ {
		indices = append(indices, i)
	}
	return indices, false
}

// wireProductionEnv maps this harness's dedicated ACR_TEST_TRIAL_* inputs
// onto the real production env-var names hosted.Open's composition reads,
// via t.Setenv (auto-restored). Required-backing-stores auxiliary config
// (evidence key, device verification URL) is unused by this harness's
// investigation path but must satisfy config.Validate(); throwaway values
// are generated here rather than reused from any real deployment secret.
//
// modelOverridden is true for arm 4 (file-exchange): the env-driven model
// config (ACR_CONTEXT_FABRIC_MODEL/_FALLBACK/_API_KEY) is never read in
// that case -- hosted.Options.ModelRuntimeOverride takes priority in
// buildContextFabricInvestigator -- so those ACR_TEST_TRIAL_MODEL* inputs
// are not required.
// acrEnvIsolationAllowlist is every real ACR_* / FALKOR_* env var this
// composition path reads (per falkorgraph.ConfigFromEnv, embedprovider.
// ConfigFromEnv, modelprovider.ConfigFromEnv, modelconfigcrypto.
// ConfigFromEnv, and config.Load itself) that wireProductionEnv explicitly
// sets below. clearAmbientACREnv (fable-review finding) unsets every OTHER
// ACR_-prefixed var found in the ambient process environment BEFORE this
// function sets anything, so an ambient leak (a stray direnv-loaded value,
// a leftover export from a prior manual run in the same shell) can never
// silently reach this harness's composition -- explicit allowlist, not
// unset-by-default-and-hope. V3 (this session) found the runner shell
// already clean; this makes that guaranteed rather than merely observed.
var acrEnvIsolationAllowlist = map[string]bool{
	"ACR_ENVIRONMENT": true, "ACR_REQUEST_TIMEOUT": true, "ACR_REQUIRE_BACKING_STORES": true,
	"ACR_POSTGRES_DSN": true, "ACR_POSTGRES_CONNECTION_KIND": true, "ACR_CLICKHOUSE_DSN": true,
	"ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED": true, "ACR_CONTEXT_FABRIC_FALKOR_ADDR": true,
	"ACR_CONTEXT_FABRIC_FALKOR_TLS": true, "ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE": true,
	"ACR_CONTEXT_FABRIC_MODEL_PROVIDER": true, "ACR_CONTEXT_FABRIC_MODEL": true,
	// ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED is DELIBERATELY absent
	// from this allowlist (codex review finding, HIGH, on the FIRST
	// version of this fix-forward -- corrected here): wireProductionEnv's
	// own set() below only calls t.Setenv when
	// ACR_TEST_TRIAL_GRAPH_LIFECYCLE_ENABLED is non-empty, so allowlisting
	// the real name would have let an operator's UNRELATED ambient
	// ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED export survive
	// clearAmbientACREnv untouched whenever the new trial-prefixed var was
	// left unset -- silently enabling epoch-1 reads (or worse,
	// TestGenerativeTrialCorpus picking it up too, since that test shares
	// this same wireProductionEnv/allowlist and never records
	// ResolvedActiveEpoch/GraphLifecycleEnabled in its own provenance at
	// all) with no record of it happening. This is EXACTLY the ambient-env
	// leak class ACR_CONTEXT_FABRIC_MODEL_FALLBACK's own comment below
	// already documents for the identical reason -- the first version of
	// this comment incorrectly claimed an exemption from that reasoning;
	// it does not have one. Leaving it OUT of the allowlist means
	// clearAmbientACREnv unsets it unconditionally first, and set() is the
	// ONLY path that can ever re-enable it, from the explicit
	// ACR_TEST_TRIAL_ source alone.
	// ACR_CONTEXT_FABRIC_MODEL_FALLBACK is DELIBERATELY absent from this
	// allowlist (sol review F1): it is the one var this function sets
	// CONDITIONALLY (only when ACR_TEST_TRIAL_MODEL_FALLBACK is
	// non-empty -- that is how "nano alone"/"luna alone" express NO
	// fallback). Keeping it allowlisted meant clearAmbientACREnv skipped
	// it even on runs that never call set() for it, so an ambiently-set
	// value would leak straight through untouched -- the empty case must
	// mean unset, not "whatever inherited value happened to be present".
	"ACR_CONTEXT_FABRIC_MODEL_API_KEY":  true,
	"ACR_CONTEXT_FABRIC_EMBED_BASE_URL": true, "ACR_CONTEXT_FABRIC_EMBED_PROVIDER": true,
	"ACR_CONTEXT_FABRIC_EMBED_MODEL": true, "ACR_CONTEXT_FABRIC_EMBED_DIMENSION": true,
	"ACR_CONTEXT_FABRIC_EMBED_API_KEY": true, "ACR_CONTEXT_FABRIC_EMBED_TIMEOUT": true,
	"ACR_CONTEXT_FABRIC_EMBED_MAX_TRANSPORT_RETRIES": true,
	"ACR_EVIDENCE_ID_ACTIVE_KID":                     true, "ACR_EVIDENCE_ID_KEYS": true,
	"ACR_DEVICE_VERIFICATION_URL": true,
}

// clearAmbientACREnv unsets every ACR_-prefixed ambient env var NOT in
// acrEnvIsolationAllowlist, restoring each on test cleanup. In particular
// this guarantees ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS/
// _ACTIVE_KID (modelconfigcrypto's BYO-LLM keys -- see buildOrgModelConfigStore)
// are absent, so the per-organization model-config resolver
// (wrapWithOrgModelRuntimeResolver) can never wrap a
// ModelRuntimeOverride (arm 4) in an org's stored config: that resolver
// is constructed as nil whenever modelconfigcrypto.Configured is false,
// and returns the deployment-default runtime completely unchanged when
// nil -- see its own doc comment. Belt-and-suspenders alongside the
// V1-confirmed-empty org_model_config table for 70d529e0.
//
// Also spares this harness's OWN ACR_TEST_TRIAL_* input namespace (a
// distinct prefix from the production ACR_CONTEXT_FABRIC_*/ACR_* names it
// protects) -- those are read via os.Getenv/requireEnv elsewhere in this
// same function and must survive.
func clearAmbientACREnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "ACR_") || strings.HasPrefix(key, "ACR_TEST_TRIAL_") || acrEnvIsolationAllowlist[key] {
			continue
		}
		original, hadValue := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("clear ambient env %s: %v", key, err)
		}
		t.Cleanup(func() {
			if hadValue {
				_ = os.Setenv(key, original)
			}
		})
	}
}

func wireProductionEnv(t *testing.T, modelOverridden bool) {
	t.Helper()
	clearAmbientACREnv(t)
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
	// CHAOS-3896 Slice B (team-lead-authorized "NEVER-AGAIN RIDER"
	// fix-forward): clearAmbientACREnv wipes an operator's own
	// ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED export before this
	// function ever runs -- exactly the class of ambient-env bug this
	// whole isolation discipline exists to prevent, which is precisely
	// why "export the real var and hope" silently measured epoch 0 twice
	// before this fix. The trial-prefixed source var survives the clear
	// (ACR_TEST_TRIAL_ prefix exception, clearAmbientACREnv's own
	// condition) and is explicit, not ambient.
	set("ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED", os.Getenv("ACR_TEST_TRIAL_GRAPH_LIFECYCLE_ENABLED"))
	// ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED is DELIBERATELY NOT
	// wired here (codex adversarial review, 2026-08-21, medium finding on
	// the first version of this fix-forward): wireProductionEnv is SHARED
	// by every live trial test (TestGenerativeTrialCorpus's arms, W0, D2B,
	// chaos3884's replay harness, ...), but only
	// TestChaos3742TwoTurnConfirmationReplay's own report reads
	// cfg.AnchorMembershipOffersEnabled into its provenance today. Wiring
	// the env var HERE would let ACR_TEST_TRIAL_ANCHOR_MEMBERSHIP_ENABLED
	// silently enable the flag for every OTHER trial script sharing this
	// function too, while their artifacts have no field to report it in --
	// an artifact claiming false while the flag was actually on, exactly
	// the mismeasurement class this whole file exists to prevent. Wired
	// instead as a two-turn-specific t.Setenv AFTER this function returns
	// (chaos3742_two_turn_confirmation_test.go, right after its own
	// wireProductionEnv call) -- scoped to the one test that both sets it
	// and records it.

	if !modelOverridden {
		set("ACR_CONTEXT_FABRIC_MODEL_PROVIDER", "openai")
		set("ACR_CONTEXT_FABRIC_MODEL", requireEnv(t, "ACR_TEST_TRIAL_MODEL"))
		// Fallback is genuinely OPTIONAL (that is how "nano alone" / "luna
		// alone" are expressed): only mapped when the caller sets it.
		set("ACR_CONTEXT_FABRIC_MODEL_FALLBACK", os.Getenv("ACR_TEST_TRIAL_MODEL_FALLBACK"))
		set("ACR_CONTEXT_FABRIC_MODEL_API_KEY", requireEnv(t, "ACR_TEST_TRIAL_MODEL_API_KEY"))
	}

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

	// CHAOS-3857 gate-threshold sweep: clearAmbientACREnv above already
	// wiped any bare ACR_CONTEXT_FABRIC_COMMIT_*/VECTOR_MARGIN_COMMIT_*
	// left in the calling shell (they do not start with ACR_TEST_TRIAL_,
	// so they are not exempt) -- these four are OPTIONAL passthroughs, the
	// SAME ACR_TEST_TRIAL_* wrapping convention every other trial input
	// uses, each mapped 1:1 onto the real falkorgraph env var
	// (falkorgraph.EnvCommitLoneFloor and friends) only when the caller
	// actually set the corresponding ACR_TEST_TRIAL_ name. A sweep cell
	// that only wants to vary ONE knob sets only that one; the other
	// three staying unset is what lets CommitGatePolicy's own per-knob
	// defaulting (vector.go's EmbedderFromEnv) apply -- see that
	// function's CHAOS-3857 comment.
	set(falkorgraph.EnvCommitLoneFloor, os.Getenv("ACR_TEST_TRIAL_COMMIT_LONE_FLOOR"))
	set(falkorgraph.EnvCommitTopFloor, os.Getenv("ACR_TEST_TRIAL_COMMIT_TOP_FLOOR"))
	set(falkorgraph.EnvCommitTopGap, os.Getenv("ACR_TEST_TRIAL_COMMIT_TOP_GAP"))
	set(falkorgraph.EnvVectorMarginCommitThreshold, os.Getenv("ACR_TEST_TRIAL_VECTOR_MARGIN_COMMIT_THRESHOLD"))

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

	if modelOverridden {
		// Belt-and-suspenders for arm 4 (fable-review finding): assert the
		// BYO-LLM crypto keys are absent, so the org-config resolver is
		// provably nil and cannot wrap ModelRuntimeOverride in a stored
		// per-org config. clearAmbientACREnv already guarantees this; this
		// is a loud, explicit check rather than trusting that silently.
		for _, envVar := range []string{"ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS", "ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_ACTIVE_KID"} {
			if v := os.Getenv(envVar); v != "" {
				t.Fatalf("%s is set (%d bytes) with ModelRuntimeOverride active -- the org-config resolver would wrap the override in a stored per-org model config; clear it before running arm 4", envVar, len(v))
			}
		}
	}
}

// contextFabricRejectionClass mirrors internal/api/context_fabric_routes.go's
// writeContextFabricError classification chain (same order, same
// sentinels) so this harness's failure taxonomy matches production's,
// rather than inventing a parallel one.
func contextFabricRejectionClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
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
	// CHAOS-3742 fable-review finding: these two were omitted from the
	// original chain, so both fell into "unclassified" -- exactly the
	// PRE-CHAOS-3810 symptom internal/api/context_fabric_routes.go's own
	// comment describes ("every real-corpus investigation landed in the
	// fallthrough below"). Added in the SAME order routes.go checks them
	// (both ACR-side invariant breaches, checked last, right before its own
	// unclassified fallthrough).
	case errors.Is(err, contextfabric.ErrNoInvestigationSubjects):
		return "no_investigation_subjects"
	case errors.Is(err, contextfabric.ErrInvalidResult):
		return "invalid_result"
	default:
		return "unclassified"
	}
}

// trialCandidateMatchProvenance is the CHAOS-3880 candidate attribution
// record: which retrieval mechanism(s) proposed a candidate (exact / alias /
// provider_key / lexical / vector / traversal_parent -- see
// contractsv1.ContextFabricSubjectMatchMechanism) and the confidence that
// resulted, so a reader can tell a vector-only, lexical-only, or corroborated
// (2+ distinct mechanisms) candidate apart DIRECTLY instead of inferring the
// population from where its confidence happened to land inside a threshold
// band -- which is exactly what the CHAOS-3742 threshold-sweep verification
// could not do and this ticket exists to fix.
//
// IDs/kinds/mechanisms/confidences ONLY (privacy posture, same as the rest of
// this harness): Kind and CanonicalID are the graph's own canonical subject
// identity, already the corpus's own expect_id/observed outcome shape used
// elsewhere in this report -- never the human-readable Label
// (ContextFabricSubjectRef.Label), never MatchedTerms, never MatchReasons.
// Those three fields exist on the source contractsv1.ContextFabricSubjectCandidate
// but are DELIBERATELY not copied here; see
// TestCandidateMatchProvenanceNeverCarriesLabelsOrSearchText for the pinning
// canary.
type trialCandidateMatchProvenance struct {
	Kind        string   `json:"kind,omitempty"`
	CanonicalID string   `json:"canonical_id,omitempty"`
	Mechanisms  []string `json:"mechanisms,omitempty"`
	// Confidence deliberately has NO omitempty (luna review finding,
	// CHAOS-3880): 0.0 is a legitimate, in-range confidence value (the
	// contract's own Confidence field permits it -- see
	// ContextFabricSubjectCandidate.Confidence / validate_context_fabric_result.go),
	// not merely a zero-value stand-in for "absent". This record only ever
	// exists when there IS a candidate to describe (a nil
	// *trialCandidateMatchProvenance, or an absent slice entry, is how
	// "no data" is represented -- see committedMatchProvenance/
	// topNonCommittedMatchProvenance's own doc comments), so once present,
	// Confidence must always serialize, even at exactly 0.
	Confidence float64 `json:"confidence"`
	// RawVectorSimilarity/RawLexicalMatchedTerms/RawLexicalTermCount
	// (CHAOS-3858, additive/optional, measurement-only) are the raw
	// pre-remap signal graphrank.RawSignalObserver captured for this SAME
	// candidate, BEFORE falkorgraph's [0.50,0.75]/vector-band linear
	// remaps collapsed it into Confidence above -- see that observer's own
	// doc comment (graphrank/resolve.go) for the post-authorization-only
	// scope guarantee. omitempty/nil throughout: absent means "no raw
	// signal was captured for this candidate's mechanism" (e.g. an
	// exact-only match, or a run with no RawSignalObserver wired), never
	// "zero".
	RawVectorSimilarity    *float64 `json:"raw_vector_similarity,omitempty"`
	RawLexicalMatchedTerms *int     `json:"raw_lexical_matched_terms,omitempty"`
	RawLexicalTermCount    *int     `json:"raw_lexical_term_count,omitempty"`
}

// trialRawSignalCollector implements graphrank.RawSignalObserver
// (CHAOS-3858, measurement-only): it accumulates the raw pre-remap signal
// ResolveSubjects reports for every ACCEPTED candidate during ONE
// Investigate() call, keyed by graphrank.SubjectKey (the SAME identity
// committedMatchProvenance/topNonCommittedMatchProvenance already key on).
// A subject search may surface the same candidate more than once across
// multiple search terms in one resolution; this keeps the STRONGEST
// observed raw value per mechanism, the same "keep highest" idiom
// mergeSearchResults' own vectorArmSimilarity side-channel already uses.
//
// reset()/snapshotAndReset() give runTrialCase exactly one case's worth of
// data: reset() before Investigate(), snapshotAndReset() immediately after
// reading the result -- so case N's raw signal can never leak into case
// N+1's report entry.
type trialRawSignalCollector struct {
	mu        sync.Mutex
	bySubject map[string]graphrank.CandidateNode
}

func (c *trialRawSignalCollector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bySubject = nil
}

func (c *trialRawSignalCollector) snapshotAndReset() map[string]graphrank.CandidateNode {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := c.bySubject
	c.bySubject = nil
	return snapshot
}

func (c *trialRawSignalCollector) ObserveCandidate(ctx context.Context, subjectKey string, node graphrank.CandidateNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bySubject == nil {
		c.bySubject = map[string]graphrank.CandidateNode{}
	}
	existing, ok := c.bySubject[subjectKey]
	if !ok {
		c.bySubject[subjectKey] = node
		return
	}
	if node.VectorSimilarity != nil && (existing.VectorSimilarity == nil || *node.VectorSimilarity > *existing.VectorSimilarity) {
		existing.VectorSimilarity = node.VectorSimilarity
	}
	if node.LexicalMatchedTerms != nil && node.LexicalTermCount != nil && *node.LexicalTermCount > 0 {
		newRatio := float64(*node.LexicalMatchedTerms) / float64(*node.LexicalTermCount)
		haveExisting := existing.LexicalMatchedTerms != nil && existing.LexicalTermCount != nil && *existing.LexicalTermCount > 0
		existingRatio := 0.0
		if haveExisting {
			existingRatio = float64(*existing.LexicalMatchedTerms) / float64(*existing.LexicalTermCount)
		}
		if !haveExisting || newRatio > existingRatio {
			existing.LexicalMatchedTerms = node.LexicalMatchedTerms
			existing.LexicalTermCount = node.LexicalTermCount
		}
	}
	c.bySubject[subjectKey] = existing
}

// attachRawSignal enriches one already-built trialCandidateMatchProvenance
// (in place) with the raw signal snapshot recorded for the SAME subject
// (Kind+CanonicalID -> graphrank.SubjectKey, the identical identity the
// snapshot is keyed by). No-op on a nil prov (topNonCommittedMatchProvenance
// may return nil) or when the snapshot has no entry for this subject (e.g.
// an exact-hint-only commit, which never goes through mergeSearchResults at
// all).
func attachRawSignal(prov *trialCandidateMatchProvenance, snapshot map[string]graphrank.CandidateNode) {
	if prov == nil || snapshot == nil {
		return
	}
	key := graphrank.SubjectKey(contractsv1.ContextFabricSubjectRef{
		Kind: contractsv1.ContextFabricSubjectKind(prov.Kind), CanonicalID: prov.CanonicalID,
	})
	node, ok := snapshot[key]
	if !ok {
		return
	}
	prov.RawVectorSimilarity = node.VectorSimilarity
	prov.RawLexicalMatchedTerms = node.LexicalMatchedTerms
	prov.RawLexicalTermCount = node.LexicalTermCount
}

// candidateMatchProvenance projects the fields
// trialCandidateMatchProvenance allows out of one resolved candidate.
// Deliberately field-by-field (not a struct literal built from cand
// directly) so a future field added to contractsv1.ContextFabricSubjectCandidate
// (e.g. a label, a matched term) does NOT silently start flowing into the
// trial artifact -- see the type's own doc comment.
func candidateMatchProvenance(cand contractsv1.ContextFabricSubjectCandidate) trialCandidateMatchProvenance {
	mechanisms := make([]string, 0, len(cand.MatchMechanisms))
	for _, mechanism := range cand.MatchMechanisms {
		mechanisms = append(mechanisms, string(mechanism))
	}
	return trialCandidateMatchProvenance{
		Kind:        string(cand.Subject.Kind),
		CanonicalID: cand.Subject.CanonicalID,
		Mechanisms:  mechanisms,
		Confidence:  cand.Confidence,
	}
}

// candidatePoolMechanismComposition is the CHAOS-3858 population-attribution
// record: how many candidates in the FULL post-Phase-4 pool
// (result.SubjectResolution.Candidates, the same slice CandidateCount/
// TopCandidateConfidence already read) carry each distinct mechanism SET,
// keyed by the comma-joined mechanism list. It answers "is this an
// unanswerable-question pool (anchor-free) or a real-subject pool
// (anchored)" at the POPULATION level, which is what CHAOS-3858's
// mechanism-anchor design needs measured before it can be built --
// TopCandidateConfidence alone cannot answer that (see this trial's own
// prior finding: control and real no-commit pools share the same [0.50,
// 0.755] confidence band).
//
// Aggregated counts only, same privacy posture as candidateMatchProvenance:
// no canonical ID, no kind, no label, no per-candidate identity of any
// kind -- only "N candidates had mechanism set S" for whichever sets
// actually appeared. A candidate with NO recognized mechanism (mechanism.go:
// "an empty value records no mechanism... legal") is counted under the
// literal key "none" rather than an empty string, so it is distinguishable
// from an absent map entry.
//
// candidates' own MatchMechanisms is already canonically ordered (
// graphrank.MergeMechanisms is the only place that field is ever set), so
// joining it as-is is a stable set key without this test package importing
// graphrank to re-derive the order.
func candidatePoolMechanismComposition(candidates []contractsv1.ContextFabricSubjectCandidate) map[string]int {
	if len(candidates) == 0 {
		return nil
	}
	composition := make(map[string]int)
	for _, cand := range candidates {
		key := "none"
		if len(cand.MatchMechanisms) > 0 {
			mechanisms := make([]string, 0, len(cand.MatchMechanisms))
			for _, mechanism := range cand.MatchMechanisms {
				mechanisms = append(mechanisms, string(mechanism))
			}
			key = strings.Join(mechanisms, ",")
		}
		composition[key]++
	}
	return composition
}

// candidatePoolProvenance is CHAOS-3884's per-candidate, kind-carrying
// counterpart to candidatePoolMechanismComposition above: the SAME full
// post-Phase-4 candidate pool (result.SubjectResolution.Candidates), one
// trialCandidateMatchProvenance entry per candidate, IN THE POOL'S OWN
// ORDER -- never re-sorted or re-grouped here.
//
// candidatePoolMechanismComposition's aggregate deliberately never carries
// kind (see its own doc comment and
// TestCandidatePoolMechanismComposition_neverCarriesIdentity), which is
// correct for a population-level view but cannot answer CHAOS-3884's
// measurement question: for a given band of cases, are repository-kind
// candidates present in the pool at all, and where do they RANK relative to
// the observation-kind (ci_pipeline_run/pull_request/...) candidates above
// them? Rank is exactly what graphrank.ResolveFromMergedCandidatesWithGate's
// Phase 4 already decided (committed tier, then parent tier, then confidence
// descending) and result.SubjectResolution.Candidates already carries in
// that order -- this function's only job is to reuse the existing,
// privacy-canaried candidateMatchProvenance projection (kind/canonical_id/
// mechanisms/confidence only, never a label or search text -- see
// trialCandidateMatchProvenance's own doc comment and the structural
// canary TestTrialCandidateMatchProvenanceStructFields_onlyAllowedJSONTags)
// per entry, without touching the order at all.
//
// nil for an empty pool, mirroring every sibling provenance function's own
// "no data" convention (committedMatchProvenance, candidatePoolMechanismComposition).
func candidatePoolProvenance(candidates []contractsv1.ContextFabricSubjectCandidate) []trialCandidateMatchProvenance {
	if len(candidates) == 0 {
		return nil
	}
	pool := make([]trialCandidateMatchProvenance, 0, len(candidates))
	for _, cand := range candidates {
		pool = append(pool, candidateMatchProvenance(cand))
	}
	return pool
}

// subjectRefKey/subjectCandidateKey give committed/candidate matching a
// single comparable identity (kind+canonical_id), mirroring
// graphrank.SubjectKey's own (kind, id) identity without importing an
// internal package this test package does not otherwise depend on.
func subjectRefKey(ref contractsv1.ContextFabricSubjectRef) string {
	return string(ref.Kind) + "|" + ref.CanonicalID
}

func subjectCandidateKey(cand contractsv1.ContextFabricSubjectCandidate) string {
	return subjectRefKey(cand.Subject)
}

// committedMatchProvenance recovers, for each committed subject (usually
// exactly one -- committedMatchesTrial's own correctness rule requires
// exactly one for a "correct" outcome, but this reports whatever the engine
// actually committed, including the 0/2+ cases), the MatchMechanisms/
// Confidence its OWN candidate entry carried. Committed subjects are refs
// only (ContextFabricSubjectRef has no mechanism/confidence of its own) --
// resolution.Candidates is where that data lives, and Phase 4 truncation
// (graphrank.ResolveFromMergedCandidatesWithGate) keeps every committed
// subject's own candidate entry at tier 0 ahead of any truncation, in the
// COMMON single/typical-multi-commit case.
//
// ALWAYS returns exactly len(committed) entries, never fewer (luna review
// finding, CHAOS-3880): if a committed subject has no matching entry in
// candidates -- the one gap in the tier-0 guarantee above, reachable when
// the number of simultaneously committed exact-match subjects exceeds
// MaxSubjectCandidates, so Phase 4's own truncation keeps only the first
// `max` of them -- this emits an IDENTITY-ONLY record (kind/canonical_id,
// empty mechanisms, zero confidence) rather than silently dropping the
// entry. Silently dropping would make len(committed_matches) < CommittedCount
// with no signal why; a reader must be able to trust that count always
// matches, even when the per-candidate detail for one entry is unavailable.
// See TestCommittedMatchProvenance_identityOnlyWhenNoMatchingCandidateExists.
func committedMatchProvenance(committed []contractsv1.ContextFabricSubjectRef, candidates []contractsv1.ContextFabricSubjectCandidate) []trialCandidateMatchProvenance {
	if len(committed) == 0 {
		return nil
	}
	byKey := make(map[string]contractsv1.ContextFabricSubjectCandidate, len(candidates))
	for _, cand := range candidates {
		byKey[subjectCandidateKey(cand)] = cand
	}
	matches := make([]trialCandidateMatchProvenance, 0, len(committed))
	for _, ref := range committed {
		if cand, ok := byKey[subjectRefKey(ref)]; ok {
			matches = append(matches, candidateMatchProvenance(cand))
			continue
		}
		matches = append(matches, trialCandidateMatchProvenance{Kind: string(ref.Kind), CanonicalID: ref.CanonicalID})
	}
	return matches
}

// topNonCommittedMatchProvenance is the highest-confidence candidate that is
// NOT among the committed subjects -- the runner-up a threshold-sweep reader
// needs to see: how close did the next candidate come, and by which
// mechanism(s), to committing instead (or in addition).
//
// Tie-break is EXPLICIT and independent of input order (sol-review finding,
// CHAOS-3880): the ascending subjectCandidateKey (kind+canonical_id) wins a
// confidence tie, not "whichever the caller's candidates slice happened to
// list first". graphrank.ResolveFromMergedCandidatesWithGate's own output
// order is, as of this writing, already fully deterministic (it sorts by
// confidence desc/key asc before Phase 3, then only reorders by committed/
// parent/other TIER in Phase 4, stably) -- but this function does not lean
// on that as an invisible cross-package invariant. A future graphrank change
// to Phase 4's tiering, or a caller that assembles `candidates` some other
// way, must not silently change which runner-up this harness reports for an
// identical candidate SET, which is what "first encountered wins" would have
// done.
func topNonCommittedMatchProvenance(committed []contractsv1.ContextFabricSubjectRef, candidates []contractsv1.ContextFabricSubjectCandidate) *trialCandidateMatchProvenance {
	committedKeys := make(map[string]bool, len(committed))
	for _, ref := range committed {
		committedKeys[subjectRefKey(ref)] = true
	}
	var best *contractsv1.ContextFabricSubjectCandidate
	for i := range candidates {
		if committedKeys[subjectCandidateKey(candidates[i])] {
			continue
		}
		cand := &candidates[i]
		switch {
		case best == nil:
			best = cand
		case cand.Confidence > best.Confidence:
			best = cand
		case cand.Confidence == best.Confidence && subjectCandidateKey(*cand) < subjectCandidateKey(*best):
			best = cand
		}
	}
	if best == nil {
		return nil
	}
	provenance := candidateMatchProvenance(*best)
	return &provenance
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
	Stage      string `json:"stage"`
	Status     string `json:"status,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	// ErrorText is the raw classified Go error string (ACR's own sentinel/
	// wrapper text, never raw model output -- production sanitizes provider
	// error bodies before they ever reach this layer). Captured so an
	// "unclassified" case can be diagnosed after the fact without a rerun,
	// unlike arms 1-3's original runs (fable-review finding: the classifier
	// omitted two real sentinels, and there was no raw text to recover
	// which cases hit them).
	ErrorText              string  `json:"error_text,omitempty"`
	CommittedCount         int     `json:"committed_count"`
	CandidateCount         int     `json:"candidate_count,omitempty"`
	TopCandidateConfidence float64 `json:"top_candidate_confidence,omitempty"`
	CommittedKindMatch     bool    `json:"committed_kind_match,omitempty"`
	LatencyMS              int64   `json:"latency_ms"`
	AnswerLength           int     `json:"answer_length,omitempty"`
	ClaimedFacts           int     `json:"claimed_facts,omitempty"`
	Drivers                int     `json:"drivers,omitempty"`
	RetrievalDegraded      bool    `json:"retrieval_degraded,omitempty"`
	// StructureNeedsDisclosed (CHAOS-3927 P1 post-merge measurement,
	// additive) is true iff this case's own result carried a non-nil
	// StructureNeeds block -- a bool only, never the block's own content
	// (no offer text, no kind/anchor/handle values), matching this
	// harness's own counts/enums/ids/bools telemetry discipline. Only
	// meaningful when Status != "" (a real, validated result was reached --
	// see runOneCase); an "error:*" outcome leaves this at its zero value
	// and is excluded from structureNeedsCoverage's own stalled tally.
	StructureNeedsDisclosed bool `json:"structure_needs_disclosed,omitempty"`
	// CommittedMatches/TopNonCommittedMatch (CHAOS-3880, additive): the
	// committed candidate's own MatchMechanisms+Confidence, and the same for
	// the best candidate the engine did NOT commit. Both are ADDITIVE and
	// OPTIONAL -- omitempty, absent on every report generated before this
	// change, and absence must be read as "not recorded", never "no
	// mechanism matched" (mirrors ContextFabricSubjectCandidate.MatchMechanisms'
	// own additive-optional discipline). A pre-CHAOS-3880 report JSON is
	// still fully valid input to anything that decodes caseOutcome.
	CommittedMatches     []trialCandidateMatchProvenance `json:"committed_matches,omitempty"`
	TopNonCommittedMatch *trialCandidateMatchProvenance  `json:"top_non_committed_match,omitempty"`
	// CandidatePoolMechanisms (CHAOS-3858, additive/optional -- same
	// discipline as CommittedMatches/TopNonCommittedMatch above): counts of
	// the FULL post-Phase-4 candidate pool by mechanism SET, e.g.
	// {"lexical":6,"vector":2,"lexical,vector":2}. Absence must be read as
	// "not recorded", never "empty pool" -- CandidateCount already carries
	// that. See candidatePoolMechanismComposition's own doc comment.
	CandidatePoolMechanisms map[string]int `json:"candidate_pool_mechanisms,omitempty"`
	// ToolCallCount/CostUSDEstimate/WriteVerbAttempted (CHAOS-3853): added
	// for the frontier-baseline-arm transport only -- arms 1-4 never set
	// these (zero value / omitempty), so the existing gen-trial-*.json
	// shape and the scoring functions that read this struct are unchanged
	// for them. See trialProvenance.CostMethodology for what
	// CostUSDEstimate actually measures (subscription billing means this is
	// an ESTIMATE, not a metered figure).
	ToolCallCount   int     `json:"tool_call_count,omitempty"`
	CostUSDEstimate float64 `json:"cost_usd_estimate,omitempty"`
	// WriteVerbAttempted (CHAOS-3853 team-lead ruling #4): true if the
	// frontier agent's own transcript shows it attempted a mutating
	// command through gh/linear-cli/clickhouse-client, REGARDLESS of
	// whether the read-only wrapper blocked it. An attempt disqualifies
	// the case's outcome from "correct" even if the eventual commit-or-
	// abstain answer happened to be right -- the read-only contract itself
	// is part of what this arm is being scored on.
	WriteVerbAttempted bool `json:"write_verb_attempted,omitempty"`
	// CommittedKind/CommittedID (CHAOS-3853, team-lead-requested id-space
	// audit): the ACTUAL committed subject kind+canonical-id, frontier arm
	// only -- arms 1-4 never set these. These are coarse IDENTIFIERS (e.g.
	// "repository", "owner/repo" or "repository:<uuid>"), not corpus
	// question text -- the withholding discipline above is about the
	// question, not resolved-answer identifiers. Exists so a wrong_commit
	// can be audited (genuinely-different-subject vs an id-format/aliasing
	// mismatch against committedMatchesTrial's exact-string-equality rule)
	// without a rerun.
	CommittedKind string `json:"committed_kind,omitempty"`
	CommittedID   string `json:"committed_id,omitempty"`
	// ExpectID mirrors ExpectKind's existing precedent (a ground-truth
	// ANNOTATION, not corpus question text) -- frontier harness only, set
	// alongside CommittedID for the same audit. Arms 1-4 (runTrialCase)
	// deliberately left unchanged; this is scoped to the CHAOS-3853 ask.
	ExpectID string `json:"expect_id,omitempty"`
	// AbstainReason (CHAOS-3853, frontier arm only): the model's own
	// declared reason for a no-commit outcome -- "no_match" (nothing
	// plausible found) vs "ambiguous" (multiple plausible subjects, could
	// not separate). Both still tally as "no_commit" in tallyOutcome
	// (team-lead ruling); this field records the distinction the existing
	// outcome-class bucketing collapses.
	AbstainReason string `json:"abstain_reason,omitempty"`
	// ClickHouse*Tables (CHAOS-3853, frontier arm only, chris's report-shaping
	// ask): raw-event vs Dev-Health-COMPUTED-artifact ClickHouse tables the
	// agent actually queried for this case. Answers "did the generic agent
	// free-ride on Dev Health's own precomputed layer" -- an answer built
	// from computed_artifact tables is the computation layer winning
	// through a different door, not the baseline hypothesis being tested.
	ClickHouseRawEventTables         []string `json:"clickhouse_raw_event_tables,omitempty"`
	ClickHouseComputedArtifactTables []string `json:"clickhouse_computed_artifact_tables,omitempty"`
	ClickHouseUnknownTables          []string `json:"clickhouse_unknown_tables,omitempty"`
	// CandidatePool (CHAOS-3884, additive/optional -- same discipline as
	// every field above): the FULL post-Phase-4, rank-ordered candidate pool
	// (the same slice CandidateCount/TopCandidateConfidence/
	// CandidatePoolMechanisms already read), one trialCandidateMatchProvenance
	// entry per candidate in resolution order. Unlike CandidatePoolMechanisms
	// (aggregate mechanism-set counts, no kind, no identity -- see that
	// field's own privacy canary), this carries kind+canonical_id PER
	// CANDIDATE, which is what CHAOS-3884 needs to answer "are
	// repository-kind candidates present in the pool at all, and where do
	// they rank relative to the observation-kind candidates above them" --
	// a question the aggregate cannot answer. Bounded by the SAME
	// per-request MaxSubjectCandidates cap CandidateCount already reflects
	// (10 in this harness), so this can never grow unbounded. Absence must
	// be read as "not recorded", never "empty pool" -- CandidateCount
	// already carries that, same convention as CandidatePoolMechanisms.
	CandidatePool []trialCandidateMatchProvenance `json:"candidate_pool,omitempty"`
}

func runTrialCase(ctx context.Context, t *testing.T, investigator contextfabric.Investigator, principal storage.Principal, index int, tc trialCase, caseTimeout time.Duration, rawSignals *trialRawSignalCollector) caseOutcome {
	t.Helper()
	isControl := tc.ExpectID == ""
	if rawSignals != nil {
		rawSignals.reset()
	}
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

	callCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()

	started := time.Now()
	result, err := investigator.Investigate(callCtx, principal, request)
	latency := time.Since(started)

	outcome := caseOutcome{Index: index, IsControl: isControl, ExpectKind: tc.ExpectKind, LatencyMS: latency.Milliseconds()}

	if err != nil {
		outcome.ErrorClass = contextFabricRejectionClass(err)
		outcome.ErrorText = truncateErrorText(err.Error())
		outcome.Outcome = "error:" + outcome.ErrorClass
		outcome.Stage = errorStage(err)
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
	// CHAOS-3927 P1 post-merge measurement: recorded for every real,
	// validated result (Status is now set), not just stalled ones -- the
	// stalled-only filter lives in structureNeedsCoverage's own tally, not
	// here, so this field always reflects the true (result.StructureNeeds
	// != nil) fact for the case it is on.
	outcome.StructureNeedsDisclosed = result.StructureNeeds != nil
	outcome.CandidateCount = len(result.SubjectResolution.Candidates)
	for _, cand := range result.SubjectResolution.Candidates {
		if cand.Confidence > outcome.TopCandidateConfidence {
			outcome.TopCandidateConfidence = cand.Confidence
		}
	}
	outcome.RetrievalDegraded = result.SubjectResolution.RetrievalDegraded
	outcome.CommittedMatches = committedMatchProvenance(result.SubjectResolution.Committed, result.SubjectResolution.Candidates)
	outcome.TopNonCommittedMatch = topNonCommittedMatchProvenance(result.SubjectResolution.Committed, result.SubjectResolution.Candidates)
	outcome.CandidatePoolMechanisms = candidatePoolMechanismComposition(result.SubjectResolution.Candidates)
	outcome.CandidatePool = candidatePoolProvenance(result.SubjectResolution.Candidates)
	if rawSignals != nil {
		snapshot := rawSignals.snapshotAndReset()
		for i := range outcome.CommittedMatches {
			attachRawSignal(&outcome.CommittedMatches[i], snapshot)
		}
		attachRawSignal(outcome.TopNonCommittedMatch, snapshot)
		for i := range outcome.CandidatePool {
			attachRawSignal(&outcome.CandidatePool[i], snapshot)
		}
	}
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

// errorStage reads the engine's OWN stage tag (contextfabric.FailureStage,
// StageError) instead of guessing from the error-class string (sol review
// F4: the prior version bucketed everything that was not
// interpretation_rejected/synthesis_rejected into "model_call_failed",
// which is exactly wrong for e.g. a StageFactRead validation failure or a
// StageGraph outage -- neither is a model-call failure at all. This is the
// SAME mechanism internal/api/context_fabric_routes.go's own logging uses
// (CHAOS-3811), reused rather than paralleled with a second, driftable
// classification.
func errorStage(err error) string {
	stage, ok := contextfabric.FailureStage(err)
	if !ok {
		return "unknown"
	}
	return string(stage)
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
	case contractsv1.ContextFabricInvestigationDegraded:
		// fable-review finding: degraded previously fell into the generic
		// default branch, which reports committedCount==0 as
		// "clarification_required" -- wrong for degraded (the engine
		// reached a terminal degraded verdict, it did not ask a
		// clarifying question). Named explicitly: a real resolution/
		// synthesis outcome that is neither a clean answer nor a
		// clarification request.
		if committedCount > 0 {
			return "committed_no_synthesis"
		}
		return "degraded"
	default:
		if committedCount > 0 {
			return "committed_no_synthesis"
		}
		return "clarification_required"
	}
}

// truncateErrorText bounds a captured error string. ACR's own error text
// here is always internal sentinel/wrapper prose (provider error bodies are
// sanitized before they ever reach this layer -- modelprovider.
// SanitizeProviderErrorBody), never raw model or corpus content, but a
// bound is kept anyway as defense in depth.
func truncateErrorText(text string) string {
	const max = 500
	if len(text) <= max {
		return text
	}
	return text[:max] + "...(truncated)"
}

// committedMatchesTrial reuses the AC-3778-1 oracle's exact correctness
// rule (ambiguity_benchmark_live_test.go's committedMatches): EXACTLY one
// committed subject, matching kind and ID. Committing the right subject
// PLUS an extra one is not a correct commit for a corpus case that names
// exactly one (sol review F3 -- the original version accepted a match
// anywhere in a multi-commit list, which is a looser bar than the
// benchmark's own authority).
func committedMatchesTrial(committed []contractsv1.ContextFabricSubjectRef, tc trialCase) bool {
	if len(committed) != 1 {
		return false
	}
	return string(committed[0].Kind) == tc.ExpectKind && committed[0].CanonicalID == tc.ExpectID
}

// --- CHAOS-4058: harness-side model round-trip timing (observability
// only -- shared plumbing, consumed by chaos3742_two_turn_confirmation_test.go).
// Motivation (chris, 2026-08-21): trial cases cost ~3.5-4 min each and the
// quorum/measurement loop is permanent, so before optimizing (CHAOS-4033
// shard parallelism, CHAOS-4055 kiac shard stacks, the responder protocol
// itself) this harness needs to SEE where the time goes. Codex process
// startup is believed negligible (an auth-only base config); this
// instrumentation exists to confirm the cost is model round-trips, not
// prove or disprove it by construction -- it changes nothing about what
// is measured or how an outcome is classified.

// twoTurnModelCallSample is one InterpretQuestion/SynthesizeAnswer round
// trip's wall-clock duration, captured at the harness's own wait-for-
// response boundary (fileExchangeRuntime.exchange's request-write-then-
// poll loop, file_exchange_runtime_test.go). Observational only -- never
// read by any pass/fail check in this package.
type twoTurnModelCallSample struct {
	Operation string
	Duration  time.Duration
}

// twoTurnModelCallCapture is an in-process spy in front of a
// contextfabric.ModelRuntime, mirroring twoTurnTraceCapture's own
// reset-before/read-after single-caller discipline
// (chaos3742_two_turn_confirmation_test.go): the harness never issues
// concurrent model calls, so a caller resets this capture immediately
// before the Investigate() call it wants to attribute latency to, then
// reads stats() immediately after.
type twoTurnModelCallCapture struct {
	samples []twoTurnModelCallSample
}

func (c *twoTurnModelCallCapture) reset() {
	c.samples = nil
}

// stats reduces the captured samples to the (count, total, max) triple
// CHAOS-4058 asks the artifact to carry per (case, arm).
func (c *twoTurnModelCallCapture) stats() (count int, total, max time.Duration) {
	for _, s := range c.samples {
		count++
		total += s.Duration
		if s.Duration > max {
			max = s.Duration
		}
	}
	return count, total, max
}

// twoTurnTimedModelRuntime wraps a contextfabric.ModelRuntime (real or
// file-exchange) and times each InterpretQuestion/SynthesizeAnswer call at
// the harness's own wait-for-response site, recording it into capture --
// purely observational, it never alters the wrapped runtime's inputs,
// outputs, or errors. The file-exchange envelope (file_exchange_runtime_test.go's
// own ENVELOPE CONTRACT) carries no separate responder-process-start
// timestamp a responder could echo back, so this wall-clock duration is
// the finest-grained "time spent waiting on the model" split available
// without a responder protocol change -- out of this ticket's scope, the
// responder script itself is never modified.
type twoTurnTimedModelRuntime struct {
	underlying contextfabric.ModelRuntime
	capture    *twoTurnModelCallCapture
}

func (w *twoTurnTimedModelRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	started := time.Now()
	result, receipt, err := w.underlying.InterpretQuestion(ctx, principal, request)
	w.capture.samples = append(w.capture.samples, twoTurnModelCallSample{Operation: "interpret", Duration: time.Since(started)})
	return result, receipt, err
}

func (w *twoTurnTimedModelRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	started := time.Now()
	draft, receipt, err := w.underlying.SynthesizeAnswer(ctx, principal, input)
	w.capture.samples = append(w.capture.samples, twoTurnModelCallSample{Operation: "synthesize", Duration: time.Since(started)})
	return draft, receipt, err
}

// trialProvenance binds a report to exactly what produced it (sol review
// F2, the CalibrationArtifact discipline): a corpus content hash, the
// model configuration, which transport ran it, the source commit, and
// (for the file-exchange transport) the session identity -- so a report
// can be checked against these facts rather than trusted by filename
// alone, and a mislabeled arm is mechanically detectable.

// trialShardingProvenance (CHAOS-4100) records how a run was fanned out.
//
// CORPUS-SAFE: case INDICES, counts, milliseconds and a closed
// provisioning label. Never a question, never a subject, never a DSN --
// CaseIndices in particular is the annex's own integer positions, the same
// values twoTurnCaseResult.Index already carries.
type trialShardingProvenance struct {
	// CaseIndices is the exact set of corpus positions THIS shard ran, in
	// ascending order. It is what makes a merged artifact auditable: the
	// union across shards must equal the annex's own index set, and any
	// gap is a silently dropped case rather than a passing run.
	//
	// Recorded even when the launcher selected by modulo, because the
	// question a reader asks is "what did this shard actually run", not
	// "what rule was used to pick it".
	CaseIndices []int `json:"case_indices,omitempty"`
	// Granularity is the requested cases-per-shard (the ticket's C). 0
	// means the launcher did not say -- an older run, or one that set
	// shard count directly.
	Granularity int `json:"granularity,omitempty"`
	// ConcurrencyCap is how many shards the launcher permitted to run at
	// once. This is the number a wall-clock comparison actually turns on:
	// the same corpus at the same granularity takes wildly different wall
	// time at cap 8 versus cap 65, and without this recorded the two are
	// indistinguishable after the fact.
	ConcurrencyCap int `json:"concurrency_cap,omitempty"`
	// ProvisioningMode is a CLOSED label for what supplied this shard's
	// postgres: "template_clone" (CHAOS-4100: a CREATE DATABASE ...
	// TEMPLATE clone on the standing instance) or "container" (the
	// pre-CHAOS-4100 per-shard scratch container). Empty on any artifact
	// written before this field existed.
	//
	// It is recorded because the substrate is exactly what the
	// Docker-socket contention flake class turned on, so a run that
	// exhibits or does not exhibit it must say which substrate it had.
	ProvisioningMode string `json:"provisioning_mode,omitempty"`
	// DatabaseProvisionMillis is how long THIS shard's database took to
	// become usable -- the clone for template_clone, container start plus
	// migrate for container. The ticket's own premise is that cloning is
	// seconds where containers were not; this is the field that lets a
	// reader check that claim against a real run instead of taking it.
	DatabaseProvisionMillis int64 `json:"database_provision_millis,omitempty"`
}

type trialProvenance struct {
	CorpusSHA256      string `json:"corpus_sha256"`
	Model             string `json:"model,omitempty"`
	ModelFallback     string `json:"model_fallback,omitempty"`
	Transport         string `json:"transport"` // "real_api" | "file_exchange"
	ExchangeModelName string `json:"exchange_model_name,omitempty"`
	ExchangeSessionID string `json:"exchange_session_id,omitempty"`
	SourceCommit      string `json:"source_commit"`
	// SourceDirty/SourceDiffDigest (sol review R1): a report built from an
	// uncommitted change is still valid data, but must say so rather than
	// implying it came from SourceCommit's exact tree. DiffDigest lets two
	// independently-dirty runs be told apart, not just both "dirty=true".
	SourceDirty      bool   `json:"source_dirty"`
	SourceDiffDigest string `json:"source_diff_digest,omitempty"`
	RunStartedAt     string `json:"run_started_at"`
	// ExecutionShape/ShardIndex/ShardCount (CHAOS-4033) mark whether this
	// artifact came from the standard single-process run ("sequential",
	// the zero value -- every report before this field existed and every
	// run that does not shard) or from one shard of a corpus split across
	// N isolated per-shard environments ("parallel"). A merge-step-authored
	// artifact combining every shard is ALSO "parallel", never silently
	// relabeled "sequential" -- see the methodology guard in
	// scripts/trial/run-two-turn-parallel.sh: a parallel-shape artifact
	// must never substitute for a ratified sequential pair without its own
	// validation-run comparison on the same corpus/SHA.
	ExecutionShape string `json:"execution_shape,omitempty"`
	ShardIndex     *int   `json:"shard_index,omitempty"`
	ShardCount     *int   `json:"shard_count,omitempty"`
	// Sharding (CHAOS-4100) is HOW this run was fanned out, recorded on
	// the artifact rather than left in the launcher's scrollback.
	//
	// ShardIndex/ShardCount above say WHICH slice this is and how many
	// there were; they cannot say how the slices were chosen, how many ran
	// at once, or what provisioned their databases. Those three decide
	// what a wall-clock number means, so an artifact that omits them
	// cannot be compared with another one -- which is exactly the
	// comparison this ticket exists to make ("does per-case granularity
	// measure the same thing as coarse?"). Zero value for every run before
	// this field existed and every unsharded run.
	Sharding trialShardingProvenance `json:"sharding"`
	// ControlsContinue (CHAOS-3858 scorecard mode) records whether this run
	// was launched with ACR_TEST_TRIAL_CONTROLS_CONTINUE=true -- i.e.
	// whether a control-violation outcome recorded and continued instead of
	// aborting the arm. false (the default) for every run before this
	// field existed and every run that does not set the env var.
	ControlsContinue bool `json:"controls_continue"`
	// ResolvedActiveEpoch/GraphLifecycleEnabled (CHAOS-3896 Slice B,
	// team-lead-authorized "NEVER-AGAIN RIDER") are the run's own
	// structural proof of which graph epoch it actually read -- an epoch
	// claim without this is exactly the "measurement fails toward fine"
	// class chris's rider closes: two earlier checkpoint artifacts
	// believed they read epoch 1 with the lifecycle flag exported and
	// actually read epoch 0 (falkorgraph.Config.EpochResolver was never
	// wired). 0 with GraphLifecycleEnabled=false is every run before this
	// field existed and every run that does not enable the flag --
	// byte-identical, not a breaking change to this shared struct. Only
	// chaos3884_replay_harness_test.go populates these two fields today;
	// every other trial script's provenance leaves them at their zero
	// values.
	ResolvedActiveEpoch   int64 `json:"resolved_active_epoch"`
	GraphLifecycleEnabled bool  `json:"graph_lifecycle_enabled"`
	// AnchorMembershipOffersEnabled (CHAOS-3742 RUN 3 finding, lane-run3,
	// 2026-08-21): the SAME structural-proof discipline
	// ResolvedActiveEpoch/GraphLifecycleEnabled already apply, for a
	// second flag -- RUN 3 exported ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_
	// ENABLED=true at the shell, and clearAmbientACREnv silently wiped it
	// before config.Load() ever ran, so the artifact reported nothing and
	// an operator's shell export was the only (wrong) record of what
	// actually happened. Recorded from cfg.AnchorMembershipOffersEnabled
	// -- the SAME post-config.Load() cfg value the runtime itself acted
	// on, never the raw env var -- so a future run's artifact is
	// self-proving instead of depending on an operator's out-of-band
	// attestation. Only TestChaos3742TwoTurnConfirmationReplay populates
	// this today; every other trial script's provenance leaves it at its
	// zero value (false), matching the flag's off-by-default posture.
	AnchorMembershipOffersEnabled bool `json:"anchor_membership_offers_enabled"`
	// CommitGate is CHAOS-3857's sweep-cell record: the raw string this
	// run actually read for each of falkorgraph's four commit-gate env
	// vars (CommitGatePolicy's three thresholds + the M override), empty
	// when unset. Recorded UNPARSED and UNRESOLVED deliberately -- this
	// is a provenance record of what the SHELL COMMAND that launched this
	// run actually set, the same discipline CorpusSHA256/SourceCommit
	// already apply, not a restatement of falkorgraph's own default-
	// resolution logic (which stays the single authority for what value
	// actually took effect). An empty field means "the calibrated default
	// applied for this knob", exactly as it does at the env-var layer
	// itself.
	CommitGate trialCommitGateProvenance `json:"commit_gate"`
	// CostMethodology/SandboxMode (CHAOS-3853): empty for arms 1-4. The
	// frontier-baseline-arm runs through the codex CLI's subscription
	// billing (team-lead ruling: "harnesses not API keys" -- see
	// .remember/feedback_harnesses_not_api_keys.md), which is NOT metered
	// per-call the way the OpenAI arms' receipt rows are -- CostUSDEstimate
	// on each case is therefore a rough estimate from token-usage events in
	// the codex transcript times a published rate card, not an authoritative
	// bill. This field is the explicit asterisk chris accepted in place of
	// metered cost accounting -- a reader must not treat cost_usd_estimate
	// as comparable in kind to a real invoice line.
	CostMethodology string `json:"cost_methodology,omitempty"`
	SandboxMode     string `json:"sandbox_mode,omitempty"`
}

// trialCommitGateProvenance is the CHAOS-3857 sweep-cell config a report
// was run under. Field names/omitempty mirror the env vars themselves
// (falkorgraph.EnvCommitLoneFloor and friends) so a reader can go straight
// from a report to the exact `export` line that produced it.
type trialCommitGateProvenance struct {
	LoneFloorEnv                   string `json:"lone_floor_env,omitempty"`
	TopFloorEnv                    string `json:"top_floor_env,omitempty"`
	TopGapEnv                      string `json:"top_gap_env,omitempty"`
	VectorMarginCommitThresholdEnv string `json:"vector_margin_commit_threshold_env,omitempty"`
}

// structureNeedsCoverage (CHAOS-3927 P1 post-merge measurement) is the
// run-level rollup of caseOutcome.StructureNeedsDisclosed over every
// STALLED case (a real, validated result -- Status != "" -- that
// committed no subject: CommittedCount == 0, control cases included).
// DisclosedOnStalled/TotalStalled are counts only, never any question- or
// offer-derived content, matching this harness's own telemetry
// discipline. This is the standing P1/P3/P5 acceptance measurement for
// "StructureNeeds present on every non-decisive terminal" -- see
// composeStructureNeeds' own doc comment (internal/contextfabric/structure.go).
type structureNeedsCoverage struct {
	DisclosedOnStalled int `json:"disclosed_on_stalled"`
	TotalStalled       int `json:"total_stalled"`
}

type trialReport struct {
	Provenance        trialProvenance `json:"provenance"`
	Arm               string          `json:"arm"`
	CorpusTotal       int             `json:"corpus_total"`
	CasesRun          int             `json:"cases_run"`
	Correct           int             `json:"correct"`
	WrongCommit       int             `json:"wrong_commit"`
	ControlViolations int             `json:"control_violations"`
	NoCommit          int             `json:"no_commit"`
	Unusable          int             `json:"unusable"`
	CommittedTotal    int             `json:"committed_total"`
	FailureClasses    map[string]int  `json:"failure_classes,omitempty"`
	// StructureNeedsCoverage (CHAOS-3927 P1 post-merge measurement,
	// additive) is nil on any report generated before this change existed
	// (mirrors ClickHouseUsageSummary's own additive-optional discipline
	// below) -- absence must be read as "not measured", never "zero
	// coverage".
	StructureNeedsCoverage *structureNeedsCoverage `json:"structure_needs_coverage,omitempty"`
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
	// ClickHouseUsageSummary (CHAOS-3853, frontier arm only): run-level
	// rollup of caseOutcome.ClickHouse*Tables -- see that field's doc
	// comment for why this split matters. nil for arms 1-4.
	ClickHouseUsageSummary *clickHouseUsageSummary `json:"clickhouse_usage_summary,omitempty"`
}

// clickHouseUsageSummary aggregates the per-case raw-vs-computed
// ClickHouse table split across an entire run.
type clickHouseUsageSummary struct {
	CasesUsingAnyClickHouse        int      `json:"cases_using_any_clickhouse"`
	CasesUsingComputedArtifact     int      `json:"cases_using_computed_artifact"`
	CasesUsingOnlyRawEvent         int      `json:"cases_using_only_raw_event"`
	CasesUsingNoClickHouse         int      `json:"cases_using_no_clickhouse"`
	DistinctRawEventTables         []string `json:"distinct_raw_event_tables,omitempty"`
	DistinctComputedArtifactTables []string `json:"distinct_computed_artifact_tables,omitempty"`
	DistinctUnknownTables          []string `json:"distinct_unknown_tables,omitempty"`
}

// summarizeClickHouseUsage builds the run-level rollup from every case's
// already-computed per-case table lists -- pure aggregation, no re-parsing.
func summarizeClickHouseUsage(cases []caseOutcome) *clickHouseUsageSummary {
	s := &clickHouseUsageSummary{}
	rawSeen, computedSeen, unknownSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range cases {
		usedRaw := len(c.ClickHouseRawEventTables) > 0
		usedComputed := len(c.ClickHouseComputedArtifactTables) > 0
		usedUnknown := len(c.ClickHouseUnknownTables) > 0
		if usedRaw || usedComputed || usedUnknown {
			s.CasesUsingAnyClickHouse++
		} else {
			s.CasesUsingNoClickHouse++
		}
		if usedComputed {
			s.CasesUsingComputedArtifact++
		} else if usedRaw {
			s.CasesUsingOnlyRawEvent++
		}
		for _, t := range c.ClickHouseRawEventTables {
			rawSeen[t] = true
		}
		for _, t := range c.ClickHouseComputedArtifactTables {
			computedSeen[t] = true
		}
		for _, t := range c.ClickHouseUnknownTables {
			unknownSeen[t] = true
		}
	}
	s.DistinctRawEventTables = sortedKeys(rawSeen)
	s.DistinctComputedArtifactTables = sortedKeys(computedSeen)
	s.DistinctUnknownTables = sortedKeys(unknownSeen)
	return s
}

func tallyOutcome(report *trialReport, outcome caseOutcome) {
	if report.StageDistribution == nil {
		report.StageDistribution = map[string]int{}
	}
	report.StageDistribution[outcome.Stage]++
	report.CommittedTotal += outcome.CommittedCount

	// CHAOS-3927 P1 post-merge measurement: a case is STALLED iff it
	// reached a real, validated result (Status != "" -- excludes every
	// "error:*" outcome, which never got a result to check) AND committed
	// no subject (CommittedCount == 0, control cases included: a
	// correctly-abstaining control is stalled exactly like a genuine
	// no_commit case for this purpose -- composeStructureNeeds' own
	// subjectless-terminal path does not distinguish the two).
	if outcome.Status != "" && outcome.CommittedCount == 0 {
		if report.StructureNeedsCoverage == nil {
			report.StructureNeedsCoverage = &structureNeedsCoverage{}
		}
		report.StructureNeedsCoverage.TotalStalled++
		if outcome.StructureNeedsDisclosed {
			report.StructureNeedsCoverage.DisclosedOnStalled++
		}
	}

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
