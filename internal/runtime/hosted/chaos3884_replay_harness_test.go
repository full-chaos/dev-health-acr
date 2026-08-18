package hosted_test

// CHAOS-3884 frozen-interpretation replay harness (team-lead ruling,
// 2026-08-17, option (c)): SINGLE BINARY, FEATURE-TOGGLE, IN-PROCESS.
//
// The scorecard's own numbers (TestGenerativeTrialCorpus) come from the
// real production pipeline, which re-interprets independently every time it
// runs -- fine for scoring "does the shipped pipeline get this case right,"
// but useless for attributing a CHANGE to resolution specifically, because
// two separate Investigate() calls can legitimately get two DIFFERENT
// interpretations for the identical question (LLM non-determinism), and
// that alone would explain a committed-subject diff with no code change at
// all.
//
// This harness isolates the ONE variable CHAOS-3884 actually touches. Per
// corpus case: interpret ONCE (the SAME contextfabric.QuestionInterpreter
// production uses), then resolve TWICE against the SAME live graph --
// arm "baseline" with the identity-universe dependency left nil (recovers
// pre-CHAOS-3884 resolver behavior, since the whole commit-path change sits
// behind that one nil-checked falkorgraph.Config.IdentityUniverse field --
// see buildContextFabricGraphReader's own doc comment), arm "wired" with it
// set exactly as production wires it. Same InterpretedQuestion value passed
// to both ResolveSubjects calls, in memory, never persisted. Any observed
// difference between the two arms is therefore attributable to the
// resolver change alone, not to a second interpretation.
//
// Package boundary note: this lives in hosted_test (external), the SAME
// package generative_trial_live_test.go already uses for corpus loading
// and trial-case helpers (trialCase, loadTrialCorpus, committedMatchesTrial,
// contextFabricRejectionClass, trialCandidateMatchProvenance, requireEnv,
// requireGitSourceIdentity, wireProductionEnv) -- reused here verbatim, not
// duplicated. buildContextFabricGraphReader (open.go) is unexported hosted
// package internals and NOT reachable from this package; rather than move
// reviewed corpus-handling code across the internal/external test boundary,
// this file re-composes the same two graph-reader arms directly from
// falkorgraph/devhealthsource/embedcache/runtimeclickhouse's OWN exported
// APIs -- the identical composition buildContextFabricGraphReader performs,
// just assembled from this side of the boundary.
//
// Corpus/privacy discipline (team-lead ruling): interpretations
// (SubjectTerms carry question-derived text) stay in PROCESS MEMORY only,
// never written to any file. The replay artifact (replayReport, written to
// ACR_TEST_REPLAY_OUT) records OUTCOME data only -- case index, per-arm
// status, committed kind/id/confidence/mechanism (via the ALREADY-canaried
// trialCandidateMatchProvenance type), and the diff classification. No
// question text, no term text, ever -- see
// TestReplayCaseOutcomeCarriesNoQuestionOrTermText for the pinning canary.
//
// HONEST LIMITATION (required by the ruling): "arm baseline equals
// pre-CHAOS-3884 behavior" holds ONLY because this ticket's entire
// commit-path change sits behind the nil-checked IdentityUniverse/
// AliasLookup dependency chain -- if a future change to this ticket's
// slice altered UNGUARDED shared resolution behavior (something outside
// that dependency), this replay would show arm baseline and arm wired
// UNCHANGED even though production behavior had shifted, because both arms
// share every code path except that one dependency. That residual is
// covered by two OTHER things, not by this harness: the full local test
// suites (kept green throughout this ticket) and the scorecard's own
// "every non-repo case's full-pipeline outcome is unchanged" acceptance
// target. This harness proves attribution for the cases that DO change; it
// cannot prove nothing else changed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedcache"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// replayDiffClass names how arm "wired"'s committed-subject set compares to
// arm "baseline"'s for ONE case -- the whole point of running both.
type replayDiffClass string

const (
	// replayUnchanged: identical committed-subject sets (including both
	// empty). The identity mechanism made no difference to this case.
	replayUnchanged replayDiffClass = "unchanged"
	// replayNewCommitCorrect: baseline did not correctly commit, wired now
	// commits AND it matches the corpus's own expectation.
	replayNewCommitCorrect replayDiffClass = "new_commit_correct"
	// replayNewCommitWrong: baseline committed nothing, wired now commits
	// something that does NOT match the corpus's expectation -- including a
	// control case (which expects nothing, ever).
	replayNewCommitWrong replayDiffClass = "new_commit_wrong"
	// replayChangedOther: any other change -- e.g. a regression (baseline
	// correct, wired no longer commits), a wrong-to-different-wrong swap, or
	// an ambiguity-status change without a committed-set change.
	replayChangedOther replayDiffClass = "changed_other"
)

// replayArmOutcome is ONE arm's (baseline or wired) resolution-level
// outcome for ONE case -- outcome data only, per the file's own privacy
// discipline. Status is this harness's OWN vocabulary (mirrors
// runTrialCase's Outcome classification, but computed from
// ContextFabricSubjectResolution alone, since this harness never runs facts
// retrieval or answer synthesis -- every one of the scorecard's acceptance
// targets is a property of subject resolution alone).
type replayArmOutcome struct {
	Status         string                          `json:"status"`
	CommittedCount int                             `json:"committed_count"`
	Committed      []trialCandidateMatchProvenance `json:"committed,omitempty"`
	// CandidatePool (team-lead ruling, 2026-08-17, coverage question A/B):
	// the FULL candidate pool this arm's resolution produced, not just what
	// committed -- reused verbatim from candidatePoolProvenance (already
	// canaried, kind/id/mechanism/confidence only), so a reader can tell
	// whether a repository-kind candidate was even IN the pool for a case
	// that never committed (e.g. stuck ambiguous), and which mechanism(s)
	// produced it.
	CandidatePool []trialCandidateMatchProvenance `json:"candidate_pool,omitempty"`
	Ambiguous     bool                            `json:"ambiguous"`
	ErrorClass    string                          `json:"error_class,omitempty"`
}

// replayCaseOutcome is ONE case's full replay record.
type replayCaseOutcome struct {
	Index     int  `json:"index"`
	IsControl bool `json:"is_control"`
	// Axis (team-lead ruling, 2026-08-17): interpreted.TimeContext.Axis, a
	// CLOSED VOCABULARY enum ("current"/"historical"/...), never free text
	// -- resolve.go derives AliasLookup's temporal gate from exactly this
	// value (reader.go:36, HIGH-6: a historical-axis question never gets
	// the identity mechanism at all), so recording it is required to
	// answer coverage question A (axis distribution, especially for cases
	// with a repository candidate) without touching corpus content.
	Axis string `json:"axis"`
	// IdentityUniverseCalls/IdentityMatchedRows (team-lead ruling,
	// 2026-08-17): C1/C2, read from the durable graphrank.ResolutionTracer's
	// own "alias_lookup" stage events -- see replayTraceCapture's own doc
	// comment. Counts only, never row or term content.
	IdentityUniverseCalls int `json:"identity_universe_calls"`
	IdentityMatchedRows   int `json:"identity_matched_rows"`
	// WiredSearchTruncated (CHAOS-3858 measurement-lane, pass-3, additive):
	// the wired arm's own decision-stage graphrank.ResolutionTraceEvent.
	// SearchTruncated (CHAOS-3897) for THIS case -- the literal, durable
	// signal for whether the commit switch's `case searchTruncated` branch
	// (resolution.go) preempted the LoneFloor/TopFloor confidence gates
	// entirely, as opposed to the gates themselves running and declining to
	// commit. Read via replayTraceCapture.decisionSearchTruncated(), same
	// idiom as IdentityUniverseCalls/IdentityMatchedRows above. Baseline has
	// no tracer wired (buildReplayGraphReader(..., false, nil) in this
	// file's own TestChaos3884ReplayHarness), so this is wired-arm only, by
	// construction -- there is no BaselineSearchTruncated to report.
	WiredSearchTruncated bool             `json:"wired_search_truncated"`
	Baseline             replayArmOutcome `json:"baseline"`
	Wired                replayArmOutcome `json:"wired"`
	DiffClass            replayDiffClass  `json:"diff_class"`
}

// replayReport is the whole run's artifact, written to ACR_TEST_REPLAY_OUT.
type replayReport struct {
	Provenance trialProvenance `json:"provenance"`
	// BaseSHA is the origin/main commit this branch was rebased onto
	// immediately before this run (team-lead ruling 2026-08-17) --
	// SourceCommit above is this BRANCH's own tip, a separate fact.
	BaseSHA string `json:"base_sha"`
	// PartAMeasurementDeferred (team-lead ruling 2026-08-17, item 4):
	// Part A's clarification-retrievability half is NOT measured by this
	// run -- it needs a rebuilt graph (repo nodes present in SEARCH pools,
	// not just reachable via nodeByKindID's existence check), and no
	// Rebuild ran for this run. Always true for this harness today; kept
	// as an explicit field (not a doc comment alone) so a reader of the
	// artifact JSON sees the limitation without cross-referencing this
	// source file.
	PartAMeasurementDeferred bool `json:"part_a_measurement_deferred"`
	// ArmBaselineLabel/ArmWiredLabel name what each arm actually is, for a
	// reader who only has the JSON, not this file's doc comment.
	ArmBaselineLabel string                  `json:"arm_baseline_label"`
	ArmWiredLabel    string                  `json:"arm_wired_label"`
	CasesRun         int                     `json:"cases_run"`
	DiffTally        map[replayDiffClass]int `json:"diff_tally"`
	Outcomes         []replayCaseOutcome     `json:"outcomes"`
}

// replayStatus classifies ONE arm's resolution outcome using this harness's
// own vocabulary -- "committed" / "ambiguous" / "no_commit" / "error:<class>"
// -- mirroring runTrialCase's Outcome switch but scoped to what
// ContextFabricSubjectResolution alone can say (no facts/synthesis stage
// exists in this harness).
func replayStatus(res contractsv1.ContextFabricSubjectResolution, err error) string {
	if err != nil {
		return "error:" + contextFabricRejectionClass(err)
	}
	switch {
	case len(res.Committed) == 1:
		return "committed"
	case len(res.Committed) == 0 && res.ClarificationPrompt != "":
		return "ambiguous"
	case len(res.Committed) == 0:
		return "no_commit"
	default:
		return "multi_commit"
	}
}

// buildReplayArmOutcome projects ONE arm's raw resolution result into the
// outcome-only record the replay artifact carries.
func buildReplayArmOutcome(res contractsv1.ContextFabricSubjectResolution, err error) replayArmOutcome {
	outcome := replayArmOutcome{Status: replayStatus(res, err)}
	if err != nil {
		outcome.ErrorClass = contextFabricRejectionClass(err)
		return outcome
	}
	outcome.CommittedCount = len(res.Committed)
	outcome.Ambiguous = res.ClarificationPrompt != ""
	outcome.Committed = committedMatchProvenance(res.Committed, res.Candidates)
	outcome.CandidatePool = candidatePoolProvenance(res.Candidates)
	return outcome
}

// sameCommittedSet reports whether a and b name the same subjects
// (Kind+CanonicalID), order-independent, both empty counting as equal.
func sameCommittedSet(a, b []contractsv1.ContextFabricSubjectRef) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, ref := range a {
		counts[string(ref.Kind)+"|"+ref.CanonicalID]++
	}
	for _, ref := range b {
		key := string(ref.Kind) + "|" + ref.CanonicalID
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

// classifyReplayDiff is this harness's own core judgment: given the corpus
// case's expectation (tc), what did wiring the identity mechanism change
// about this case's committed subject(s), relative to leaving it nil.
func classifyReplayDiff(tc trialCase, baseline, wired []contractsv1.ContextFabricSubjectRef) replayDiffClass {
	if sameCommittedSet(baseline, wired) {
		return replayUnchanged
	}
	baselineCorrect := committedMatchesTrial(baseline, tc)
	wiredCorrect := committedMatchesTrial(wired, tc)
	switch {
	case !baselineCorrect && wiredCorrect:
		return replayNewCommitCorrect
	case len(baseline) == 0 && len(wired) > 0 && !wiredCorrect:
		return replayNewCommitWrong
	default:
		return replayChangedOther
	}
}

// buildReplayGraphReader re-composes ONE arm's falkorgraph.Adapter directly
// from exported building blocks (falkorgraph/devhealthsource/embedcache/
// runtimeclickhouse), mirroring buildContextFabricGraphReader (open.go)
// field for field -- see this file's own package-boundary doc comment for
// why the two compositions live in two places rather than one being called
// from the other. wireIdentityUniverse=false is arm "baseline"; true is arm
// "wired", byte-identical to production's own wiring.
// replayTraceCapture (team-lead ruling, 2026-08-17: "STOP the throwaway
// invocation-counter... READ reachability from" a durable
// graphrank.ResolutionTracer instead) implements graphrank.ResolutionTracer,
// accumulating every event in memory. Content-free by construction --
// ResolutionTraceEvent's own fields already are (see its doc comment) --
// so no filtering logic is needed here either. Reset between cases; a
// single falkorgraph.Adapter is built once and reused across every case in
// a run, so per-case attribution requires clearing between resolutions.
type replayTraceCapture struct {
	events []graphrank.ResolutionTraceEvent
}

func (c *replayTraceCapture) Trace(event graphrank.ResolutionTraceEvent) {
	c.events = append(c.events, event)
}

func (c *replayTraceCapture) reset() {
	c.events = nil
}

// aliasLookupReachability reads C1 (was AliasLookup invoked) and C2 (did it
// match anything) directly from the captured "alias_lookup" stage
// events -- the reachability answer as OBSERVED by the durable tracer, not
// inferred from a bespoke counter or a passing unit test.
func (c *replayTraceCapture) aliasLookupReachability() (calls, matchedClaimants int) {
	for _, e := range c.events {
		if e.Stage != "alias_lookup" {
			continue
		}
		calls++
		matchedClaimants += e.AliasLookupMatchedClaimants
	}
	return calls, matchedClaimants
}

// decisionSearchTruncated reads the wired arm's OWN "decision" stage event
// for this case (CHAOS-3858 measurement-lane, pass-3) -- exactly one such
// event per ResolveSubjects call (resolution.go's tracer.Trace switch always
// fires once, for committed/ambiguous/no_commit), so the first (only) match
// is authoritative. false (the zero value) if no decision event was
// captured at all -- should not happen for a successful resolve, but this
// mirrors aliasLookupReachability's own "absence reads as zero" convention
// rather than panicking on an unexpected shape.
func (c *replayTraceCapture) decisionSearchTruncated() bool {
	for _, e := range c.events {
		if e.Stage != "decision" {
			continue
		}
		return e.SearchTruncated
	}
	return false
}

func buildReplayGraphReader(logger *slog.Logger, client *runtimeclickhouse.Client, wireIdentityUniverse bool, tracer graphrank.ResolutionTracer) (contextfabric.GraphReader, error) {
	graphConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	graphConfig.Telemetry = falkorgraph.SlogTelemetry{Logger: logger}
	graphConfig.ResolutionTracer = tracer
	if wireIdentityUniverse {
		graphConfig.IdentityUniverse = func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
			return devhealthsource.IdentityUniverse(ctx, client, orgID)
		}
	}
	embedderOptions, err := falkorgraph.EmbedderFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	embedCacheConfig, err := embedcache.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	embedderOptions.Embedder = embedcache.Wrap(embedderOptions.Embedder, embedCacheConfig)
	return falkorgraph.NewWithEmbedder(graphConfig, embedderOptions)
}

// TestChaos3884ReplayHarness is the live orchestration: loads the SAME
// withheld corpus TestGenerativeTrialCorpus uses (ACR_TEST_TRIAL_CORPUS),
// builds one interpreter and two graph-reader arms, and runs the
// interpret-once/resolve-twice replay described in this file's own doc
// comment. Skipped (like TestGenerativeTrialCorpus) when the corpus is not
// supplied.
//
// The interpreter is ALWAYS fileExchangeRuntime (arm 5's own transport,
// file_exchange_runtime_test.go) -- ACR_TEST_TRIAL_EXCHANGE_DIR is
// required, with no metered-API fallback, so this harness cannot
// accidentally run against a billed key. A responder (run-responder-codex.sh
// on subscription auth) must already be watching that directory before this
// test starts publishing requests -- mirror run-arm5.sh's lifecycle (start
// the responder, run this test, signal DONE, wait for the responder to
// exit), not a bare `go test` invocation.
//
//	ACR_TEST_TRIAL_CORPUS=<path> ACR_TEST_TRIAL_ORG=<org> \
//	ACR_TEST_REPLAY_OUT=<path> ACR_TEST_TRIAL_EXCHANGE_DIR=<dir> \
//	ACR_TEST_TRIAL_ARM=replay go test ./internal/runtime/hosted \
//	  -run TestChaos3884ReplayHarness -v -timeout 2h
func TestChaos3884ReplayHarness(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_REPLAY_OUT")
	runStartedAt := time.Now().UTC().Format(time.RFC3339)

	// Subscription-only, no metered key, ever (standing rule for this
	// epic): the interpreter is ALWAYS the SAME file-exchange transport
	// TestGenerativeTrialCorpus's arm 5 uses (fileExchangeRuntime,
	// file_exchange_runtime_test.go), never modelprovider.New's direct API
	// path. ACR_TEST_TRIAL_EXCHANGE_DIR is required, not optional -- there
	// is no metered fallback to accidentally fall into.
	exchangeDir := requireEnv(t, "ACR_TEST_TRIAL_EXCHANGE_DIR")
	exchangeTimeout := 10 * time.Minute
	if raw := os.Getenv("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT"); raw != "" {
		parsed, perr := time.ParseDuration(raw)
		if perr != nil {
			t.Fatalf("ACR_TEST_TRIAL_EXCHANGE_TIMEOUT: %v", perr)
		}
		exchangeTimeout = parsed
	}
	arm := os.Getenv("ACR_TEST_TRIAL_ARM")

	corpus, corpusHash := loadTrialCorpus(t, corpusPath)
	// Expected-hash verification (optional): when the caller supplies the
	// corpus's known-good hash out of band (never derived by reading the
	// corpus itself here -- the operator's own separate record), a mismatch
	// aborts before this run ever touches a live credential or model call,
	// rather than silently scoring against unexpected content.
	if expected := os.Getenv("ACR_TEST_TRIAL_CORPUS_SHA256"); expected != "" && expected != corpusHash {
		t.Fatalf("corpus SHA-256 mismatch: got %s, want %s (ACR_TEST_TRIAL_CORPUS_SHA256) -- refusing to run against unexpected corpus content", corpusHash, expected)
	}
	source := requireGitSourceIdentity(t)
	wireProductionEnv(t, true) // modelOverridden=true: the exchange transport never reads ACR_TEST_TRIAL_MODEL*

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(cfg.ClickHouseCACertPath)
	if err != nil {
		t.Fatalf("clickhouse TLS config: %v", err)
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: cfg.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: cfg.ClickHouseMaxBytesToRead,
	})
	if err != nil {
		t.Fatalf("open clickhouse client: %v", err)
	}
	defer func() { _ = client.Close() }()

	baselineGraph, err := buildReplayGraphReader(logger, client, false, nil)
	if err != nil {
		t.Fatalf("build baseline graph reader: %v", err)
	}
	traceCapture := &replayTraceCapture{}
	wiredGraph, err := buildReplayGraphReader(logger, client, true, traceCapture)
	if err != nil {
		t.Fatalf("build wired graph reader: %v", err)
	}

	exchangeRuntime, err := newFileExchangeRuntime(exchangeDir, arm, exchangeTimeout)
	if err != nil {
		t.Fatalf("create file-exchange runtime: %v", err)
	}
	interpreter := contextfabric.RuntimeQuestionInterpreter{Runtime: exchangeRuntime}
	caseTimeout := 2*exchangeTimeout + 30*time.Second

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	// indices (team-lead ruling, 2026-08-17): mirrors
	// TestGenerativeTrialCorpus's own ACR_TEST_TRIAL_INDICES/ACR_TEST_TRIAL_LIMIT
	// pair -- a reusable capability, not narrowed for THIS run (a guessed
	// index subset validates nothing; this run always covers the full
	// corpus unless an operator explicitly opts into a subset later).
	var indices []int
	if raw := os.Getenv("ACR_TEST_TRIAL_INDICES"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			n, convErr := strconv.Atoi(strings.TrimSpace(part))
			if convErr != nil || n < 0 || n >= len(corpus) {
				t.Fatalf("ACR_TEST_TRIAL_INDICES: invalid index %q (corpus has %d cases)", part, len(corpus))
			}
			indices = append(indices, n)
		}
	} else {
		limit := len(corpus)
		if raw := os.Getenv("ACR_TEST_TRIAL_LIMIT"); raw != "" {
			n, convErr := strconv.Atoi(raw)
			if convErr != nil || n <= 0 {
				t.Fatalf("ACR_TEST_TRIAL_LIMIT must be a positive integer, got %q", raw)
			}
			if n < limit {
				limit = n
			}
		}
		for i := 0; i < limit; i++ {
			indices = append(indices, i)
		}
	}

	report := replayReport{
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: "file_exchange", RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			ExchangeModelName: arm, ExchangeSessionID: exchangeRuntime.nonce,
			ControlsContinue: os.Getenv("ACR_TEST_TRIAL_CONTROLS_CONTINUE") == "true",
		},
		BaseSHA:                  requireEnv(t, "ACR_TEST_TRIAL_BASE_SHA"),
		PartAMeasurementDeferred: true,
		ArmBaselineLabel:         "baseline (identity-universe dependency nil, pre-CHAOS-3884 resolver behavior)",
		ArmWiredLabel:            "wired (identity-universe dependency set, exactly as production composes it)",
		DiffTally:                map[replayDiffClass]int{},
	}

	for _, i := range indices {
		tc := corpus[i]
		request := contractsv1.ContextFabricInvestigationRequest{
			SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
			RequestID:     fmt.Sprintf("request_replay%06d", i),
			Question:      tc.Question,
			TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			Options: contractsv1.ContextFabricInvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3884-replay", Version: "0.1.0", Surface: "trial"},
		}

		callCtx, cancelCase := context.WithTimeout(ctx, caseTimeout)
		interpreted, interpretErr := interpreter.Interpret(callCtx, principal, request)
		var outcome replayCaseOutcome
		outcome.Index = i
		outcome.IsControl = tc.ExpectID == ""
		if interpretErr != nil {
			errClass := "error:" + contextFabricRejectionClass(interpretErr)
			outcome.Baseline = replayArmOutcome{Status: errClass, ErrorClass: contextFabricRejectionClass(interpretErr)}
			outcome.Wired = outcome.Baseline
			outcome.DiffClass = replayUnchanged
			cancelCase()
			report.Outcomes = append(report.Outcomes, outcome)
			report.DiffTally[outcome.DiffClass]++
			report.CasesRun++
			continue
		}

		outcome.Axis = string(interpreted.TimeContext.Axis)
		traceCapture.reset()
		baselineRes, baselineErr := baselineGraph.ResolveSubjects(callCtx, principal, request, interpreted)
		wiredRes, wiredErr := wiredGraph.ResolveSubjects(callCtx, principal, request, interpreted)
		cancelCase()

		// Reachability, read from the durable ResolutionTracer (team-lead
		// ruling, 2026-08-17), not a bespoke counter: C1 is whether the
		// wired arm's resolver emitted an "alias_lookup" stage event AT
		// ALL for this case -- proof the composition actually reaches
		// deps.AliasLookup, not inferred from a unit test. C2 is that
		// event's own AliasLookupMatchedClaimants -- did it find a match.
		outcome.IdentityUniverseCalls, outcome.IdentityMatchedRows = traceCapture.aliasLookupReachability()
		// CHAOS-3858 measurement-lane, pass-3: the literal SearchTruncated
		// bit off the wired arm's own decision-stage event (CHAOS-3897) --
		// see WiredSearchTruncated's own doc comment.
		outcome.WiredSearchTruncated = traceCapture.decisionSearchTruncated()

		outcome.Baseline = buildReplayArmOutcome(baselineRes, baselineErr)
		outcome.Wired = buildReplayArmOutcome(wiredRes, wiredErr)
		if baselineErr != nil || wiredErr != nil {
			outcome.DiffClass = replayChangedOther
		} else {
			outcome.DiffClass = classifyReplayDiff(tc, baselineRes.Committed, wiredRes.Committed)
		}
		report.Outcomes = append(report.Outcomes, outcome)
		report.DiffTally[outcome.DiffClass]++
		report.CasesRun++
		t.Logf("case %d: baseline=%s wired=%s diff=%s identityUniverseCalls=%d identityMatchedRows=%d",
			i, outcome.Baseline.Status, outcome.Wired.Status, outcome.DiffClass, outcome.IdentityUniverseCalls, outcome.IdentityMatchedRows)
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal replay report: %v", err)
	}
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		t.Fatalf("write replay report: %v", err)
	}
	t.Logf("replay report written to %s: %d cases, tally=%v", outPath, report.CasesRun, report.DiffTally)
}

// --- pure logic: unit-testable without any live infrastructure ---

func replaySubject(kind, id string) contractsv1.ContextFabricSubjectRef {
	return contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectKind(kind), CanonicalID: id}
}

func TestSameCommittedSet(t *testing.T) {
	t.Parallel()
	a := []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "r1")}
	b := []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "r1")}
	if !sameCommittedSet(a, b) {
		t.Error("sameCommittedSet(a, b) = false, want true for identical single-subject sets")
	}
	if !sameCommittedSet(nil, nil) {
		t.Error("sameCommittedSet(nil, nil) = false, want true -- both empty counts as equal")
	}
	c := []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "r2")}
	if sameCommittedSet(a, c) {
		t.Error("sameCommittedSet(a, c) = true, want false -- different canonical IDs")
	}
	d := []contractsv1.ContextFabricSubjectRef{replaySubject("team", "r1")}
	if sameCommittedSet(a, d) {
		t.Error("sameCommittedSet(a, d) = true, want false -- same ID, different kind")
	}
}

func TestClassifyReplayDiff(t *testing.T) {
	t.Parallel()
	repoCase := trialCase{Question: "q", ExpectKind: "repository", ExpectID: "repository:r1"}
	controlCase := trialCase{Question: "q"}
	r1 := []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "repository:r1")}
	wrong := []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "repository:other")}

	cases := []struct {
		name            string
		tc              trialCase
		baseline, wired []contractsv1.ContextFabricSubjectRef
		want            replayDiffClass
	}{
		{"both empty", repoCase, nil, nil, replayUnchanged},
		{"both same commit", repoCase, r1, r1, replayUnchanged},
		{"baseline empty, wired correct", repoCase, nil, r1, replayNewCommitCorrect},
		{"baseline empty, wired wrong", repoCase, nil, wrong, replayNewCommitWrong},
		{"baseline empty, wired commits on control", controlCase, nil, r1, replayNewCommitWrong},
		{"baseline correct, wired empty (regression)", repoCase, r1, nil, replayChangedOther},
		{"baseline wrong, wired different wrong", repoCase, wrong, []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "repository:third")}, replayChangedOther},
		{"baseline wrong, wired correct", repoCase, wrong, r1, replayNewCommitCorrect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyReplayDiff(tc.tc, tc.baseline, tc.wired)
			if got != tc.want {
				t.Errorf("classifyReplayDiff(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestReplayStatus(t *testing.T) {
	t.Parallel()
	if got := replayStatus(contractsv1.ContextFabricSubjectResolution{}, context.DeadlineExceeded); got != "error:deadline_exceeded" {
		t.Errorf("replayStatus(err) = %q, want error:deadline_exceeded", got)
	}
	committed := contractsv1.ContextFabricSubjectResolution{Committed: []contractsv1.ContextFabricSubjectRef{replaySubject("repository", "r1")}}
	if got := replayStatus(committed, nil); got != "committed" {
		t.Errorf("replayStatus(committed) = %q, want committed", got)
	}
	ambiguous := contractsv1.ContextFabricSubjectResolution{ClarificationPrompt: "which one?"}
	if got := replayStatus(ambiguous, nil); got != "ambiguous" {
		t.Errorf("replayStatus(ambiguous) = %q, want ambiguous", got)
	}
	if got := replayStatus(contractsv1.ContextFabricSubjectResolution{}, nil); got != "no_commit" {
		t.Errorf("replayStatus(empty) = %q, want no_commit", got)
	}
}

// TestReplayCaseOutcomeCarriesNoQuestionOrTermText is the structural privacy
// canary this file's own doc comment promises: reflection-enumerates every
// JSON-tagged field reachable from replayCaseOutcome (through replayArmOutcome
// and trialCandidateMatchProvenance) and fails if anything is added whose
// name suggests free text a question or a matched term could populate.
// Mirrors the SAME discipline TestCandidateMatchProvenanceNeverCarriesLabelsOrSearchText
// already enforces on trialCandidateMatchProvenance itself -- this canary
// covers the two NEW wrapper types this file adds on top of it.
func TestReplayCaseOutcomeCarriesNoQuestionOrTermText(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"question": true, "term": true, "terms": true, "matchedterms": true,
		"matchreasons": true, "label": true, "text": true, "prompt": true,
	}
	var walk func(t *testing.T, typ reflect.Type)
	walk = func(t *testing.T, typ reflect.Type) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			lower := lowerASCII(field.Name)
			if forbidden[lower] {
				t.Errorf("%s.%s: field name suggests free text (question/term/label) reaching the outcome-only replay artifact", typ.Name(), field.Name)
			}
			fieldType := field.Type
			for fieldType.Kind() == reflect.Ptr || fieldType.Kind() == reflect.Slice {
				fieldType = fieldType.Elem()
			}
			if fieldType.Kind() == reflect.Struct && fieldType.PkgPath() != "" {
				walk(t, fieldType)
			}
		}
	}
	walk(t, reflect.TypeOf(replayCaseOutcome{}))
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
