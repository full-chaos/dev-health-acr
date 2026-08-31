package hosted_test

// CHAOS-3899 D2(b) cardinality measurement (chris-approved follow-up to
// Slice A's shadow acceptance run, 2026-08-18): the shadow round's own
// acceptance run measured ZERO reach for D2(a) (grammar-bound handle /
// unique-claimant anchor) on the standing 50-question corpus -- every
// stalled case refused with no_discriminators. This is the OTHER half of
// the question the design brief's own D2 decision matrix poses: if D2(a)
// is this narrow, what would D2(b) (window+kind alone, no keyed class)
// actually see? Chris's framing: cardinality is something the design will
// need to manage regardless of which D2 option ships -- Slice A tried to
// make it zero by requiring a keyed class; that may be unrealistic given
// how little the interpretation layer currently extracts. Measure what is
// actually there, cheaply, before deciding whether D2(b) (or a
// managed-cardinality variant) is worth its risk, or whether the real
// lever is interpretation-side window/anchor extraction.
//
// SHADOW ONLY, exactly like the D2(a) round: this file makes NO commit
// decision and NEVER calls graphrank.RunShadowEvidenceRound or any
// production commit path with this measurement's own predicate. It reuses
// the SAME two-arm (baseline/wired) comparison chaos3884_replay_harness_test.go
// already runs -- computed independently in THIS test, not borrowed from a
// prior run -- so the "scorecard unchanged" claim is verifiable from this
// run's own artifact, not merely asserted. For each case where the WIRED
// arm reports ambiguous (this file's own, independent replayStatus check --
// the "stalled" case chris named), and for every census-registered kind
// present in the wired arm's own candidate pool, it runs ONE bare
// aggregate statement (devhealthsource.RunCardinalityCensus: count(),
// now64() only, no row fetch, no witness -- cheaper even than the D2(a)
// round's own protocol, since a distribution question never needs a
// natural key) with a window predicate built from the INTERPRETED
// question's own TimeContext (Start/End/AsOf) -- never the raw request's.
//
// Corpus/privacy discipline: identical to chaos3884_replay_harness_test.go
// (see its own doc comment) -- interpretations stay in process memory,
// the artifact carries counts/enums/bools only.

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
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// d2bKindMeasurement is ONE (case, kind) cardinality read.
type d2bKindMeasurement struct {
	Kind string `json:"kind"`
	// Count is the bare aggregate count for kind, org-scoped, with the
	// window predicate applied when WindowBound is true.
	Count int `json:"count"`
	// WindowBound is true iff the interpreted question carried a real
	// Start/End/AsOf -- false means this count is ORG-WIDE, ALL-TIME for
	// this kind (the interpretation-layer gap chris named: a missing
	// window falls back to unbounded, which this field makes directly
	// observable per case/kind rather than inferred).
	WindowBound bool `json:"window_bound"`
}

// d2bCaseMeasurement is ONE case's full D2(b) cardinality record.
type d2bCaseMeasurement struct {
	Index     int    `json:"index"`
	IsControl bool   `json:"is_control"`
	Axis      string `json:"axis"`
	// Stalled is this file's OWN independent ambiguous check on the wired
	// arm (replayStatus(wiredRes, wiredErr) == "ambiguous") -- Measurements
	// is only ever populated when this is true.
	Stalled bool `json:"stalled"`
	// PooledCensusKinds is how many census-registered kinds this case's
	// wired-arm candidate pool contained -- 0 is itself informative (a
	// stalled case whose pool never touched a censusable kind at all).
	PooledCensusKinds int                  `json:"pooled_census_kinds"`
	Measurements      []d2bKindMeasurement `json:"measurements,omitempty"`
	// DiffClass mirrors chaos3884_replay_harness_test.go's own scorecard
	// classification for this SAME case, computed independently here --
	// the artifact's own proof that this measurement pass changed
	// nothing about baseline-vs-wired committed-subject behavior.
	DiffClass replayDiffClass `json:"diff_class"`
}

// d2bReport is the whole run's artifact.
type d2bReport struct {
	Provenance trialProvenance `json:"provenance"`
	BaseSHA    string          `json:"base_sha"`
	CasesRun   int             `json:"cases_run"`
	// StalledCases/DiffTally/ControlsCommittedInWiredArm are the SAME
	// scorecard-confirmation numbers chaos3884_replay_harness_test.go's
	// own run reports -- recomputed independently in this run, not copied.
	StalledCases                int                     `json:"stalled_cases"`
	DiffTally                   map[replayDiffClass]int `json:"diff_tally"`
	ControlsCommittedInWiredArm int                     `json:"controls_committed_in_wired_arm"`
	Measurements                []d2bCaseMeasurement    `json:"measurements"`
}

// pooledCensusKinds returns the DEDUPLICATED census-registered kinds
// present in candidates -- the SAME "hypothesized kinds" notion
// graphrank.RunShadowEvidenceRound's own pooledKinds derivation uses
// (resolution.Candidates' Subject.Kind, narrowed to the closed registry),
// reproduced here independently since this file never calls that function
// (this is a D2(b) measurement, D2(a)'s own decisive machinery must stay
// untouched by it).
func pooledCensusKinds(candidates []contractsv1.ContextFabricSubjectCandidate) []graphrank.CensusKind {
	seen := map[graphrank.CensusKind]bool{}
	var kinds []graphrank.CensusKind
	for _, c := range candidates {
		kind := graphrank.CensusKind(c.Subject.Kind)
		if seen[kind] {
			continue
		}
		if !graphrank.IsCensusKindRegistered(kind) {
			continue
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	return kinds
}

// TestChaos3899D2BCardinality is the live orchestration -- see this file's
// own doc comment for the full design. Skipped (like TestChaos3884ReplayHarness)
// when the corpus is not supplied.
//
//	ACR_TEST_TRIAL_CORPUS=<path> ACR_TEST_TRIAL_ORG=<org> \
//	ACR_TEST_D2B_OUT=<path> ACR_TEST_TRIAL_EXCHANGE_DIR=<dir> \
//	ACR_TEST_TRIAL_ARM=d2b go test ./internal/runtime/hosted \
//	  -run TestChaos3899D2BCardinality -v -timeout 2h
func TestChaos3899D2BCardinality(t *testing.T) {
	corpusPath := os.Getenv("ACR_TEST_TRIAL_CORPUS")
	if corpusPath == "" {
		t.Skip("ACR_TEST_TRIAL_CORPUS is not set; the CHAOS-3742 trial corpus is withheld and supplied at run time")
	}
	orgID := requireEnv(t, "ACR_TEST_TRIAL_ORG")
	outPath := requireEnv(t, "ACR_TEST_D2B_OUT")
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

	// Two arms, exactly like chaos3884_replay_harness_test.go, for the
	// SAME scorecard-confirmation reason -- this run's own DiffClass tally
	// must independently reproduce 5/0/45 and 0 controls committed, not
	// rely on a prior run's number. No CensusFunc on either arm: THIS
	// measurement never rides graphrank.ResolveSubjects' own shadow-round
	// gate -- it runs its own bare aggregate reads, entirely separate from
	// D2(a)'s decisive registry.
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
	interpreter := contextfabric.RuntimeQuestionInterpreter{Runtime: exchangeRuntime}
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

	report := d2bReport{
		Provenance: trialProvenance{
			CorpusSHA256: corpusHash, Transport: "file_exchange", RunStartedAt: runStartedAt,
			SourceCommit: source.commit, SourceDirty: source.dirty, SourceDiffDigest: source.diffDigest,
			ExchangeModelName: arm, ExchangeSessionID: exchangeRuntime.nonce,
			ControlsContinue: os.Getenv("ACR_TEST_TRIAL_CONTROLS_CONTINUE") == "true",
		},
		BaseSHA:   source.commit,
		DiffTally: map[replayDiffClass]int{},
	}

	for _, i := range indices {
		tc := corpus[i]
		request := contractsv1.ContextFabricInvestigationRequest{
			SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
			RequestID:     fmt.Sprintf("request_d2b%06d", i),
			Question:      tc.Question,
			TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			Options: contractsv1.ContextFabricInvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos-3899-d2b", Version: "0.1.0", Surface: "trial"},
		}

		// callCtx's cancel is DEFERRED to the end of this case's own loop
		// iteration (a bug found live: an earlier version called
		// cancelCase() right after the two resolves, mirroring
		// chaos3884_replay_harness_test.go's OWN placement -- correct
		// THERE, since nothing after it needs a live context, but wrong
		// HERE, since this file's cardinality census calls below still
		// need callCtx to be alive. The symptom was a 100% RunCardinalityCensus
		// failure rate against a context already canceled before the first
		// query ever ran -- caught by this run itself, not a unit test,
		// because no fake-client test exercises context cancellation
		// timing).
		callCtx, cancelCase := context.WithTimeout(ctx, caseTimeout)
		func() {
			defer cancelCase()
			interpreted, _, interpretErr := interpreter.Interpret(callCtx, principal, request)
			var m d2bCaseMeasurement
			m.Index = i
			m.IsControl = tc.ExpectID == ""
			if interpretErr != nil {
				m.DiffClass = replayUnchanged
				report.Measurements = append(report.Measurements, m)
				report.DiffTally[m.DiffClass]++
				report.CasesRun++
				return
			}
			m.Axis = string(interpreted.TimeContext.Axis)

			baselineRes, _, _, _, baselineErr := baselineGraph.ResolveSubjects(callCtx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
			wiredRes, _, _, _, wiredErr := wiredGraph.ResolveSubjects(callCtx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)

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
				for _, kind := range kinds {
					window, werr := devhealthsource.BuildCardinalityWindow(kind, interpreted.TimeContext.Start, interpreted.TimeContext.End, interpreted.TimeContext.AsOf)
					if werr != nil {
						t.Logf("case %d kind %s: BuildCardinalityWindow error: %v", i, kind, werr)
						continue
					}
					result, cerr := devhealthsource.RunCardinalityCensus(callCtx, client, orgID, kind, window)
					if cerr != nil {
						t.Logf("case %d kind %s: RunCardinalityCensus error: %v", i, kind, cerr)
						continue
					}
					m.Measurements = append(m.Measurements, d2bKindMeasurement{
						Kind: string(kind), Count: result.Count, WindowBound: window.Bound,
					})
				}
			}
			report.Measurements = append(report.Measurements, m)
			t.Logf("case %d: stalled=%v pooled_census_kinds=%d diff=%s axis=%s", i, m.Stalled, m.PooledCensusKinds, m.DiffClass, m.Axis)
		}()
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal d2b report: %v", err)
	}
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		t.Fatalf("write d2b report: %v", err)
	}
	t.Logf("d2b report written to %s: %d cases, %d stalled, diff_tally=%v, controls_committed_in_wired_arm=%d",
		outPath, report.CasesRun, report.StalledCases, report.DiffTally, report.ControlsCommittedInWiredArm)
}

// --- pure logic: unit-testable without any live infrastructure ---

func TestPooledCensusKinds(t *testing.T) {
	t.Parallel()
	candidates := []contractsv1.ContextFabricSubjectCandidate{
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:1"}},
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:2"}}, // duplicate KIND, different id -- dedup by kind only
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:r1"}},   // NOT a census kind -- must be dropped
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:w1"}},
	}
	got := pooledCensusKinds(candidates)
	if len(got) != 2 {
		t.Fatalf("pooledCensusKinds = %#v, want exactly 2 (pull_request, work_item deduplicated, repository dropped)", got)
	}
	seen := map[graphrank.CensusKind]bool{}
	for _, k := range got {
		seen[k] = true
	}
	if !seen[contractsv1.ContextFabricSubjectPullRequest] || !seen[contractsv1.ContextFabricSubjectWorkItem] {
		t.Fatalf("pooledCensusKinds = %#v, want pull_request and work_item present", got)
	}
}

// TestD2BCaseMeasurementCarriesNoQuestionOrTermText mirrors
// TestReplayCaseOutcomeCarriesNoQuestionOrTermText's own discipline
// (chaos3884_replay_harness_test.go) for this file's NEW artifact types --
// reflection-enumerates every field reachable from d2bReport and fails if
// any field name suggests free text a question or a matched term could
// populate.
func TestD2BCaseMeasurementCarriesNoQuestionOrTermText(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"question": true, "term": true, "terms": true, "matchedterms": true,
		"matchreasons": true, "label": true, "text": true, "prompt": true,
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
				t.Errorf("%s.%s: field name suggests free text reaching the outcome-only d2b artifact", typ.Name(), field.Name)
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
	walk(t, reflect.TypeOf(d2bReport{}))
}

func TestPooledCensusKinds_Empty(t *testing.T) {
	t.Parallel()
	if got := pooledCensusKinds(nil); len(got) != 0 {
		t.Fatalf("pooledCensusKinds(nil) = %#v, want empty", got)
	}
	onlyNonCensus := []contractsv1.ContextFabricSubjectCandidate{
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:r1"}},
	}
	if got := pooledCensusKinds(onlyNonCensus); len(got) != 0 {
		t.Fatalf("pooledCensusKinds(only non-census kinds) = %#v, want empty", got)
	}
}
