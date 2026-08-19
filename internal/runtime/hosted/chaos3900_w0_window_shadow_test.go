package hosted_test

// CHAOS-3900 W0 window-inference shadow measurement (design brief
// ../.remember/chaos3900-design-brief.md (relative to the dev-health/acr repo root) v5.2, §7 "the D2(b) rerun is the
// acceptance instrument"). SHADOW/MEASUREMENT ONLY, exactly like
// chaos3899_d2b_cardinality_test.go, which this file extends rather than
// modifies (that harness ships as part of CHAOS-3899 and stays untouched):
// this file makes NO commit decision and calls no production decision
// path with its own window classification -- graphrank.ClassifyWindow,
// graphrank.DefaultRelativeID, and graphrank.ProposeWindowFromSpans are
// exercised ONLY by this measurement harness, never by
// internal/contextfabric/engine.go or graphrank.ResolveSubjects. It
// recomputes the SAME independent baseline/wired diff tally D2(b) does,
// for the SAME reason: "scorecard byte-identical" (W0's own acceptance
// criterion) must be verifiable from THIS run's own artifact, never merely
// asserted.
//
// Three things this harness adds beyond D2(b):
//  1. Per-case window classification: the model's own (sanitized) pick,
//     the engine post-pass outcome (class + source: model|fallback|none),
//     and BOTH DW0 candidate defaults (90d/365d policies) -- §9 decision
//     matrix row DW0, measured rather than chosen.
//  2. The binder pipeline (§1.2(d)/v5.2 descope), run over the verbatim
//     question in process memory: PROPOSAL-only, counted by its own closed
//     reason vocabulary, never reaching this artifact as anything but an
//     enum + a closed RelativeID.
//  3. Per-kind cardinality at BOTH candidate window widths together
//     (count_alltime/count_90d/count_365d), for every stalled case's
//     pooled census kinds -- §7's own per-kind row shape.
//  4. N=3 repeated interpretation per case with the per-question class
//     agreement recorded -- the divergence-rate metric §7/§11.1 name as a
//     pre-registered, pre-W1 tripwire (>10% forces the deterministic-
//     classifier fallback decision).
//
// Corpus/privacy discipline: identical to chaos3899_d2b_cardinality_test.go
// -- interpretations (which carry question-derived SubjectTerms) stay in
// process memory; the artifact carries counts/enums/ids/bools only. See
// TestW0CaseMeasurementCarriesNoQuestionOrTermText.

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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// w0KindMeasurement is ONE (case, kind) cardinality read at BOTH DW0
// candidate widths, alongside the existing all-time count (§7's per-kind
// row: "{kind, count_alltime, count_90d, count_365d, window_bound}").
type w0KindMeasurement struct {
	Kind         string `json:"kind"`
	CountAllTime int    `json:"count_alltime"`
	Count90D     int    `json:"count_90d"`
	Count365D    int    `json:"count_365d"`
	// WindowBound mirrors d2bKindMeasurement's own field: true iff the
	// INTERPRETED question carried a real Start/End/AsOf. CountAllTime
	// here is DELIBERATELY unconditional (built from nil/nil/nil, never
	// from the interpreted TimeContext) -- W0 measures the two DW0
	// candidate trailing widths against a true all-time baseline
	// regardless of what the interpreter extracted, which is the same
	// number as D2(b)'s own `count` ONLY when WindowBound is false. This
	// field makes that comparability explicit per case rather than
	// assumed from the corpus's own measured 0/50 window_bound rate.
	WindowBound bool `json:"window_bound"`
}

// w0CaseMeasurement is ONE case's full W0 shadow record.
type w0CaseMeasurement struct {
	Index     int    `json:"index"`
	IsControl bool   `json:"is_control"`
	Axis      string `json:"axis"`
	// Stalled mirrors d2bCaseMeasurement's own independent ambiguous check.
	Stalled           bool                `json:"stalled"`
	PooledCensusKinds int                 `json:"pooled_census_kinds"`
	Measurements      []w0KindMeasurement `json:"measurements,omitempty"`
	DiffClass         replayDiffClass     `json:"diff_class"`

	// Classification (§2/§7): the model's OWN sanitized pick (never a raw,
	// unsanitized string -- ModelExecutionReceipt already sanitizes this
	// at capture time, chaos3900_window_vocab.go), the engine post-pass
	// outcome, and both DW0 candidate defaults.
	ModelWindowClass             string `json:"model_window_class,omitempty"`
	ModelWindowClassUnrecognized bool   `json:"model_window_class_unrecognized,omitempty"`
	ModelWindowConfidence        string `json:"model_window_confidence,omitempty"`
	FinalWindowClass             string `json:"window_class,omitempty"`
	ClassSource                  string `json:"class_source,omitempty"` // model | fallback | none
	FinalWindowConfidence        string `json:"window_confidence,omitempty"`
	ClassDowngraded              bool   `json:"class_downgraded,omitempty"`
	RelativeID90D                string `json:"relative_id_90d,omitempty"`
	RelativeID365D               string `json:"relative_id_365d,omitempty"`

	// Binder (§1.2(d), SHADOW/PROPOSAL ONLY -- see this file's own doc
	// comment): the verbatim question is consulted in process memory only;
	// BoundWindowSpan/WindowBindOutcome carry no span text by construction,
	// so nothing here can leak corpus content.
	BinderReason     string `json:"binder_reason,omitempty"`
	BinderSpansBound int    `json:"binder_spans_bound,omitempty"`
	BinderRelativeID string `json:"binder_relative_id,omitempty"`

	// N=3 divergence (§2/§7/§11.1): the same question interpreted 3 times
	// (this case's own primary run plus two more), each run's OWN sanitized
	// model window_class recorded in order -- enum values only.
	RepeatWindowClasses []string `json:"repeat_window_classes,omitempty"`
	ClassDivergent      bool     `json:"class_divergent,omitempty"`
}

// w0Report is the whole run's artifact.
type w0Report struct {
	Provenance                  trialProvenance         `json:"provenance"`
	BaseSHA                     string                  `json:"base_sha"`
	CasesRun                    int                     `json:"cases_run"`
	StalledCases                int                     `json:"stalled_cases"`
	DiffTally                   map[replayDiffClass]int `json:"diff_tally"`
	ControlsCommittedInWiredArm int                     `json:"controls_committed_in_wired_arm"`
	DivergentCases              int                     `json:"divergent_cases"`
	// ClassifiedCases is how many cases reached classification at all
	// (interpretErr == nil for the primary run) -- the denominator for
	// DivergenceRate below, and for ClassDistribution/ClassSourceDistribution,
	// which do NOT sum to CasesRun (an interpret-error case is counted in
	// CasesRun but never classified).
	ClassifiedCases int `json:"classified_cases"`
	// DivergenceRate is DivergentCases / ClassifiedCases (never CasesRun,
	// which would silently dilute the rate with uninterpretable cases that
	// could never have diverged) -- the §7/§11.1 pre-registered tripwire
	// reads directly off this field (>0.10 forces the deterministic-
	// classifier fallback decision before W1).
	DivergenceRate           float64             `json:"divergence_rate"`
	ClassDistribution        map[string]int      `json:"class_distribution"`         // FinalWindowClass tally, "" counted as "none"
	ClassSourceDistribution  map[string]int      `json:"class_source_distribution"`  // model | fallback | none
	BinderReasonDistribution map[string]int      `json:"binder_reason_distribution"` // closed WindowBindReason tally
	Measurements             []w0CaseMeasurement `json:"measurements"`
}

// w0DefaultRelativeString applies policy and renders "" (never a bare
// unset RelativeWindowID) when the class carries no default -- keeps the
// JSON field genuinely omitempty-shaped rather than a present empty enum.
func w0DefaultRelativeString(outcome graphrank.WindowClassOutcome, policy graphrank.WindowDefaultPolicy) string {
	if id, ok := graphrank.DefaultRelativeID(outcome, policy); ok {
		return string(id)
	}
	return ""
}

// TestChaos3900W0WindowShadow is the live orchestration -- see this file's
// own doc comment. Skipped (like TestChaos3899D2BCardinality) when the
// corpus is not supplied.
//
//	ACR_TEST_TRIAL_CORPUS=<path> ACR_TEST_TRIAL_ORG=<org> \
//	ACR_TEST_W0_OUT=<path> ACR_TEST_TRIAL_EXCHANGE_DIR=<dir> \
//	ACR_TEST_TRIAL_ARM=w0 go test ./internal/runtime/hosted \
//	  -run TestChaos3900W0WindowShadow -v -timeout 4h
func TestChaos3900W0WindowShadow(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_W0_OUT")
	runStartedAt := time.Now().UTC().Format(time.RFC3339)

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
	if expected := os.Getenv("ACR_TEST_TRIAL_CORPUS_SHA256"); expected != "" && expected != corpusHash {
		t.Fatalf("corpus SHA-256 mismatch: got %s, want %s (ACR_TEST_TRIAL_CORPUS_SHA256) -- refusing to run against unexpected corpus content", corpusHash, expected)
	}
	source := requireGitSourceIdentity(t)
	wireProductionEnv(t, true)

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

	// Same two-arm baseline/wired composition D2(b) uses, for the SAME
	// scorecard-confirmation reason -- see this file's own doc comment.
	baselineGraph, err := buildReplayGraphReader(logger, client, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("build baseline graph reader: %v", err)
	}
	wiredGraph, err := buildReplayGraphReader(logger, client, true, nil, nil, nil)
	if err != nil {
		t.Fatalf("build wired graph reader: %v", err)
	}

	exchangeRuntime, err := newFileExchangeRuntime(exchangeDir, arm, exchangeTimeout)
	if err != nil {
		t.Fatalf("create file-exchange runtime: %v", err)
	}
	// exchangeRuntime.InterpretQuestion is called DIRECTLY (never through
	// the contextfabric.RuntimeQuestionInterpreter/QuestionInterpreter
	// wrapper) so this harness can also read the ModelExecutionReceipt
	// each call produces -- the ONLY place the sanitized window_class/
	// window_confidence capture (CHAOS-3900 W0) is observable, since the
	// QuestionInterpreter port intentionally returns only
	// (InterpretedQuestion, error). This harness replicates
	// RuntimeQuestionInterpreter.Interpret's own post-call Validate()
	// check by hand, for the same reason chaos3899_d2b_cardinality_test.go's
	// own doc comment gives for every OTHER piece of logic it recomputes
	// independently rather than borrowing.
	caseTimeout := 2*exchangeTimeout + 30*time.Second

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

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

	report := w0Report{
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: "file_exchange", RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			ExchangeModelName: arm, ExchangeSessionID: exchangeRuntime.nonce,
			ControlsContinue: os.Getenv("ACR_TEST_TRIAL_CONTROLS_CONTINUE") == "true",
		},
		BaseSHA:                  requireEnv(t, "ACR_TEST_TRIAL_BASE_SHA"),
		DiffTally:                map[replayDiffClass]int{},
		ClassDistribution:        map[string]int{},
		ClassSourceDistribution:  map[string]int{},
		BinderReasonDistribution: map[string]int{},
	}

	for _, i := range indices {
		tc := corpus[i]
		buildRequest := func(suffix string) contractsv1.ContextFabricInvestigationRequest {
			return contractsv1.ContextFabricInvestigationRequest{
				SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
				RequestID:     fmt.Sprintf("request_w0%06d_%s", i, suffix),
				Question:      tc.Question,
				TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
				Options: contractsv1.ContextFabricInvestigationOptions{
					MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
					MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
				},
				Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3900-w0", Version: "0.1.0", Surface: "trial"},
			}
		}

		callCtx, cancelCase := context.WithTimeout(ctx, 3*caseTimeout)
		func() {
			defer cancelCase()
			// Incremental write (I7): a partial artifact survives a
			// timeout/kill mid-run instead of losing the whole batch --
			// best-effort (log-only on failure; the final write after the
			// loop still fails loudly via t.Fatalf).
			defer func() {
				if report.ClassifiedCases > 0 {
					report.DivergenceRate = float64(report.DivergentCases) / float64(report.ClassifiedCases)
				}
				if werr := writeW0Report(report, outPath); werr != nil {
					t.Logf("case %d: incremental artifact write failed: %v", i, werr)
				}
			}()
			var m w0CaseMeasurement
			m.Index = i
			m.IsControl = tc.ExpectID == ""

			primary, primaryReceipt, interpretErr := exchangeRuntime.InterpretQuestion(callCtx, principal, buildRequest("r0"))
			if interpretErr == nil {
				interpretErr = primary.Validate()
			}
			if interpretErr != nil {
				m.DiffClass = replayUnchanged
				report.Measurements = append(report.Measurements, m)
				report.DiffTally[m.DiffClass]++
				report.CasesRun++
				return
			}
			m.Axis = string(primary.TimeContext.Axis)
			windowBound := primary.TimeContext.Start != nil || primary.TimeContext.End != nil || primary.TimeContext.AsOf != nil
			m.ModelWindowClass = string(primaryReceipt.WindowClass)
			m.ModelWindowClassUnrecognized = primaryReceipt.WindowClassUnrecognized
			m.ModelWindowConfidence = string(primaryReceipt.WindowConfidence)
			report.ClassifiedCases++

			classOutcome := graphrank.ClassifyWindow(primary, primaryReceipt.WindowClass, primaryReceipt.WindowConfidence)
			m.FinalWindowClass = string(classOutcome.Class)
			m.ClassSource = string(classOutcome.Source)
			m.FinalWindowConfidence = string(classOutcome.Confidence)
			m.ClassDowngraded = classOutcome.Downgraded
			m.RelativeID90D = w0DefaultRelativeString(classOutcome, graphrank.WindowDefaultPolicy90D)
			m.RelativeID365D = w0DefaultRelativeString(classOutcome, graphrank.WindowDefaultPolicy365D)
			report.ClassDistribution[classDistKey(m.FinalWindowClass)]++
			report.ClassSourceDistribution[m.ClassSource]++

			bindOutcome := graphrank.ProposeWindowFromSpans(tc.Question)
			m.BinderReason = string(bindOutcome.Reason)
			m.BinderSpansBound = bindOutcome.SpansBound
			m.BinderRelativeID = string(bindOutcome.RelativeID)
			report.BinderReasonDistribution[m.BinderReason]++

			// N=3 divergence (§2/§7/§11.1): two MORE independent
			// interpretations of the same question, each its own live
			// model call -- run 0 (primary, above) plus runs 1 and 2. A
			// run that itself errors contributes an empty class ("")
			// rather than being dropped, so a divergence between "picked
			// a class" and "failed to produce one" is not silently
			// discarded.
			m.RepeatWindowClasses = []string{m.ModelWindowClass}
			for run := 1; run <= 2; run++ {
				repeated, repeatedReceipt, repeatErr := exchangeRuntime.InterpretQuestion(callCtx, principal, buildRequest(fmt.Sprintf("r%d", run)))
				if repeatErr != nil || repeated.Validate() != nil {
					m.RepeatWindowClasses = append(m.RepeatWindowClasses, "")
					continue
				}
				m.RepeatWindowClasses = append(m.RepeatWindowClasses, string(repeatedReceipt.WindowClass))
			}
			for _, class := range m.RepeatWindowClasses[1:] {
				if class != m.RepeatWindowClasses[0] {
					m.ClassDivergent = true
					break
				}
			}
			if m.ClassDivergent {
				report.DivergentCases++
			}

			baselineRes, baselineErr := baselineGraph.ResolveSubjects(callCtx, principal, buildRequest("r0"), primary, contextfabric.ResolvedGraphBinding{})
			wiredRes, wiredErr := wiredGraph.ResolveSubjects(callCtx, principal, buildRequest("r0"), primary, contextfabric.ResolvedGraphBinding{})
			if baselineErr != nil || wiredErr != nil {
				m.DiffClass = replayChangedOther
				report.Measurements = append(report.Measurements, m)
				report.DiffTally[m.DiffClass]++
				report.CasesRun++
				return
			}
			m.DiffClass = classifyReplayDiff(tc, baselineRes.Committed, wiredRes.Committed)
			report.DiffTally[m.DiffClass]++
			report.CasesRun++
			if m.IsControl && len(wiredRes.Committed) > 0 {
				report.ControlsCommittedInWiredArm++
			}

			m.Stalled = replayStatus(wiredRes, nil) == "ambiguous"
			if m.Stalled {
				report.StalledCases++
				kinds := pooledCensusKinds(wiredRes.Candidates)
				m.PooledCensusKinds = len(kinds)
				now := time.Now().UTC()
				start90 := now.AddDate(0, 0, -90)
				start365 := now.AddDate(0, 0, -365)
				for _, kind := range kinds {
					allTime, aerr := devhealthsource.BuildCardinalityWindow(kind, nil, nil, nil)
					w90, w90err := devhealthsource.BuildCardinalityWindow(kind, &start90, &now, nil)
					w365, w365err := devhealthsource.BuildCardinalityWindow(kind, &start365, &now, nil)
					if aerr != nil || w90err != nil || w365err != nil {
						t.Logf("case %d kind %s: BuildCardinalityWindow error: alltime=%v 90d=%v 365d=%v", i, kind, aerr, w90err, w365err)
						continue
					}
					allTimeResult, aErr := devhealthsource.RunCardinalityCensus(callCtx, client, orgID, kind, allTime)
					r90, r90Err := devhealthsource.RunCardinalityCensus(callCtx, client, orgID, kind, w90)
					r365, r365Err := devhealthsource.RunCardinalityCensus(callCtx, client, orgID, kind, w365)
					if aErr != nil || r90Err != nil || r365Err != nil {
						t.Logf("case %d kind %s: RunCardinalityCensus error: alltime=%v 90d=%v 365d=%v", i, kind, aErr, r90Err, r365Err)
						continue
					}
					m.Measurements = append(m.Measurements, w0KindMeasurement{
						Kind: string(kind), CountAllTime: allTimeResult.Count, Count90D: r90.Count, Count365D: r365.Count,
						WindowBound: windowBound,
					})
				}
			}
			report.Measurements = append(report.Measurements, m)
			t.Logf("case %d: stalled=%v pooled_census_kinds=%d diff=%s axis=%s class=%s class_source=%s binder_reason=%s divergent=%v",
				i, m.Stalled, m.PooledCensusKinds, m.DiffClass, m.Axis, m.FinalWindowClass, m.ClassSource, m.BinderReason, m.ClassDivergent)
		}()
	}

	if report.ClassifiedCases > 0 {
		report.DivergenceRate = float64(report.DivergentCases) / float64(report.ClassifiedCases)
	}

	if err := writeW0Report(report, outPath); err != nil {
		t.Fatalf("write w0 report: %v", err)
	}
	t.Logf("w0 report written to %s: %d cases, %d stalled, diff_tally=%v, controls_committed_in_wired_arm=%d, classified=%d, divergent=%d (%.1f%%), class_distribution=%v, class_source_distribution=%v, binder_reason_distribution=%v",
		outPath, report.CasesRun, report.StalledCases, report.DiffTally, report.ControlsCommittedInWiredArm,
		report.ClassifiedCases, report.DivergentCases, report.DivergenceRate*100, report.ClassDistribution, report.ClassSourceDistribution, report.BinderReasonDistribution)
}

// writeW0Report marshals and writes report to outPath. Called both as the
// I7 incremental per-case snapshot (best-effort, log-only on failure) and
// as the final write (fail-loud via t.Fatalf at the call site) -- ONE
// marshal/write implementation for both, so a schema change can never
// leave the two writers producing different shapes.
func writeW0Report(report w0Report, outPath string) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal w0 report: %w", err)
	}
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		return fmt.Errorf("write w0 report: %w", err)
	}
	return nil
}

// classDistKey renders "" as "none" so the class_distribution map's keys
// are all non-empty, self-describing JSON object keys.
func classDistKey(class string) string {
	if class == "" {
		return "none"
	}
	return class
}

// --- pure logic: unit-testable without any live infrastructure ---

func TestW0DefaultRelativeString(t *testing.T) {
	t.Parallel()
	trend := graphrank.WindowClassOutcome{Class: contextfabric.WindowClassTrendAssessment, Source: graphrank.WindowClassSourceModel}
	if got := w0DefaultRelativeString(trend, graphrank.WindowDefaultPolicy90D); got != "trailing_90d" {
		t.Fatalf("w0DefaultRelativeString(trend, 90d) = %q, want trailing_90d", got)
	}
	none := graphrank.WindowClassOutcome{Source: graphrank.WindowClassSourceNone}
	if got := w0DefaultRelativeString(none, graphrank.WindowDefaultPolicy90D); got != "" {
		t.Fatalf("w0DefaultRelativeString(none) = %q, want empty", got)
	}
}

func TestClassDistKey(t *testing.T) {
	t.Parallel()
	if got := classDistKey(""); got != "none" {
		t.Fatalf("classDistKey(\"\") = %q, want \"none\"", got)
	}
	if got := classDistKey("trend_assessment"); got != "trend_assessment" {
		t.Fatalf("classDistKey(trend_assessment) = %q, want unchanged", got)
	}
}

// TestW0CaseMeasurementCarriesNoQuestionOrTermText mirrors
// TestD2BCaseMeasurementCarriesNoQuestionOrTermText's own reflection
// canary (chaos3899_d2b_cardinality_test.go) for this file's NEW artifact
// types.
func TestW0CaseMeasurementCarriesNoQuestionOrTermText(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"question": true, "term": true, "terms": true, "matchedterms": true,
		"matchreasons": true, "label": true, "text": true, "prompt": true,
		"span": true, "spantext": true,
	}
	var walk func(t *testing.T, typ reflect.Type)
	walk = func(t *testing.T, typ reflect.Type) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			lower := lowerASCII(field.Name)
			if forbidden[lower] {
				t.Errorf("%s.%s: field name suggests free text reaching the outcome-only w0 artifact", typ.Name(), field.Name)
			}
			fieldType := field.Type
			for fieldType.Kind() == reflect.Ptr || fieldType.Kind() == reflect.Slice || fieldType.Kind() == reflect.Map {
				fieldType = fieldType.Elem()
			}
			if fieldType.Kind() == reflect.Struct && fieldType.PkgPath() != "" {
				walk(t, fieldType)
			}
		}
	}
	walk(t, reflect.TypeOf(w0Report{}))
}
