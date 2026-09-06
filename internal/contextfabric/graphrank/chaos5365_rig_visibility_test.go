package graphrank

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSlogResolutionTracer_RigVisibilityAuditPromotedStagesReachInfo is
// the rig-visibility audit's acceptance test for every stage the audit's measurement
// table found bounded/safe (see the PR body for the full measurement:
// each was either gated behind its own precondition, a single emission
// site, or bounded by a small count -- never retrieval-pool-sized).
// Table-driven, one entry per promoted stage, exercised directly against
// the sink (matching TestChaos5218_ProductionSinkEmitsTheWithholdingAtTheProductionLogLevel's
// own unit-level convention) rather than through a full resolution for
// every case -- some of these stages (the evidence_* shadow-census ones)
// need a specific CensusFunc-wired fixture to reach at all, which
// TestSlogResolutionTracer_StageLinesVisibleAtProductionLogLevel and this
// file's own TestSlogResolutionTracer_EvidenceCensusStagesReachInfo cover
// separately, end to end.
//
// Red on the unfixed tree: every one of these stages was DebugContext
// before this ticket (verified by reading tracer.go's own pre-fix
// switch); at the production default (slog.LevelInfo) none of them would
// appear at all.
func TestSlogResolutionTracer_RigVisibilityAuditPromotedStagesReachInfo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		event ResolutionTraceEvent
	}{
		{"search", ResolutionTraceEvent{Stage: "search", TermHash: "h", SearchResultCount: 1}},
		{"search_question", ResolutionTraceEvent{Stage: "search_question", SearchResultCount: 1}},
		{"alias_lookup", ResolutionTraceEvent{Stage: "alias_lookup", AliasLookupComplete: true}},
		// kind_hint_search, exact_name_search, and evidence_source_native_probe
		// are DELIBERATELY not in this table -- an adversarial review round
		// reproduced all three as retrieval-pool-sized (90 Info lines each
		// on realistic multi-match fixtures), so they were reverted to
		// Debug rather than promoted; see
		// TestSlogResolutionTracer_RevertedStagesStayDebug below for their
		// own negative proof.
		{"kind_coverage_floor", ResolutionTraceEvent{Stage: "kind_coverage_floor", KindCoverageFloorFired: true}},
		{"confirmed_kind_rescue", ResolutionTraceEvent{Stage: "confirmed_kind_rescue", ConfirmedKindRescueFired: true}},
		{"confirmed_kind_scope", ResolutionTraceEvent{Stage: "confirmed_kind_scope", ConfirmedKindScopeState: "complete"}},
		{"anchor_offer", ResolutionTraceEvent{Stage: "anchor_offer", AnchorOfferLabelsNormalizedCount: 1}},
		{"identity_universe", ResolutionTraceEvent{Stage: "identity_universe", IdentityUniverseComplete: true}},
		{"evidence_round", ResolutionTraceEvent{Stage: "evidence_round", ShadowOutcome: "commit_sound"}},
		{"evidence_probe", ResolutionTraceEvent{Stage: "evidence_probe", CensusComplete: true}},
		{"evidence_census_commit", ResolutionTraceEvent{Stage: "evidence_census_commit", Outcome: "merged"}},
		{"evidence_source_native", ResolutionTraceEvent{Stage: "evidence_source_native", ShadowSourceNativeMatchCount: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			NewSlogResolutionTracer(logger).Trace(tc.event)
			if got := strings.TrimSpace(buf.String()); got == "" {
				t.Fatalf("stage %q emitted nothing at the production default level (Info) -- this stage was measured safe to promote but is still gated at DebugContext", tc.name)
			}
			if !strings.Contains(buf.String(), `"stage":"`+tc.name+`"`) {
				t.Fatalf("stage %q's Info line does not carry its own stage token: %s", tc.name, buf.String())
			}
		})
	}
}

// TestSlogResolutionTracer_RevertedStagesStayDebug is the negative proof
// for the three stages an adversarial review round found genuinely
// retrieval-pool-sized, contrary to this PR's own first-pass claim that
// they were bounded: kind_hint_search and exact_name_search (measured 90
// Info lines each on a 90-node fixture, reproduced independently by the
// round), and evidence_source_native_probe (90 lines from 45 grammar
// matches -- its sibling "evidence_source_native" event already carries
// the bounded aggregate an operator needs, so this one was reverted
// rather than folded). Red on the tree between this PR's first commit and
// this fix: all three appeared at Info; green here: none do.
func TestSlogResolutionTracer_RevertedStagesStayDebug(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		event ResolutionTraceEvent
	}{
		{"kind_hint_search", ResolutionTraceEvent{Stage: "kind_hint_search", TermHash: "h"}},
		{"exact_name_search", ResolutionTraceEvent{Stage: "exact_name_search", TermHash: "h"}},
		{"evidence_source_native_probe", ResolutionTraceEvent{Stage: "evidence_source_native_probe", ShadowSourceNativeResolved: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			NewSlogResolutionTracer(logger).Trace(tc.event)
			if got := strings.TrimSpace(buf.String()); got != "" {
				t.Fatalf("stage %q emitted %q at the production default level (Info) -- an adversarial review round found this stage genuinely retrieval-pool-sized, it must stay Debug", tc.name, got)
			}
		})
	}
}

// TestIdentityGateSummaryBuffer_FlushSurvivesAPanic is the red-first proof
// for the panic-safety fix an adversarial review round's own attack found:
// the buffer's flush was originally a bare statement placed right after
// the resolveSubjects(...) call in ResolveSubjectsWithCommitBasis, which a
// panic anywhere inside that call skips entirely -- the already-observed
// per-candidate identity_gate events still reach the real tracer (Trace
// forwards them immediately, unconditionally), but the aggregate summary
// silently never fires. Fixed by deferring the flush instead. This test
// exercises identityGateSummaryBuffer directly (not through a full
// resolution, which would need a genuinely panicking backend) --
// confirming the buffer itself correctly flushes from within a deferred
// call even when the goroutine is already unwinding a panic.
func TestIdentityGateSummaryBuffer_FlushSurvivesAPanic(t *testing.T) {
	tracer := &recordingTracer{}
	func() {
		defer func() {
			_ = recover()
		}()
		buf := &identityGateSummaryBuffer{real: tracer, requestID: "req-panic"}
		defer buf.flush()
		buf.Trace(ResolutionTraceEvent{Stage: "identity_gate", Subject: contextfabric.SubjectRef{CanonicalID: "r1"}, GateFired: true})
		panic("deliberate: simulating a genuinely reachable backend panic mid-resolution")
	}()
	summaries := 0
	for _, e := range tracer.events {
		if e.Stage == "identity_gate" && e.IdentityGateSummary {
			summaries++
			if e.IdentityGateCandidateCount != 1 || e.IdentityGateFiredCount != 1 {
				t.Fatalf("summary = %+v, want CandidateCount=1 FiredCount=1", e)
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("identity_gate summaries after a panic = %d, want exactly 1 -- the deferred flush must still run during panic unwinding", summaries)
	}
}

// TestSlogResolutionTracer_FoldedSummariesReachInfoPerCandidateStaysDebug
// is the red-first proof for the rig-visibility audit's three volume-gated folds
// (corroboration/identity_gate/slice_b_survivor_verdict): each measured
// retrieval-pool-sized (96/90/unbounded-by-budget events respectively),
// so unlike the table above they do NOT promote straight to Info -- the
// per-candidate line stays Debug and a single folded summary event
// (discriminated by its own *Summary bool, sharing the SAME stage token)
// is what reaches Info instead, mirroring RankedCutSummary's own shape
// (CHAOS-5222). Red on the unfixed tree: before this ticket these
// summary events did not exist at all (the fields are new), so a
// Debug-level capture shows the per-candidate lines with no summary, and
// an Info-level capture shows NOTHING for any of the three stages.
func TestSlogResolutionTracer_FoldedSummariesReachInfoPerCandidateStaysDebug(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		debug   ResolutionTraceEvent
		summary ResolutionTraceEvent
	}{
		{
			name:    "corroboration",
			debug:   ResolutionTraceEvent{Stage: "corroboration", BaseConfidence: 0.5, FinalConfidence: 0.5},
			summary: ResolutionTraceEvent{Stage: "corroboration", CorroborationSummary: true, CorroborationCandidateCount: 40, CorroborationTopIDs: []string{"c_a", "c_b"}, CorroborationMinConfidence: 0.3, CorroborationMaxConfidence: 0.9},
		},
		{
			name:    "identity_gate",
			debug:   ResolutionTraceEvent{Stage: "identity_gate", GateFired: false},
			summary: ResolutionTraceEvent{Stage: "identity_gate", IdentityGateSummary: true, IdentityGateCandidateCount: 40, IdentityGateFiredCount: 3, IdentityGateFiredIDs: []string{"r_a", "r_b"}},
		},
		{
			name:    "slice_b_survivor_verdict",
			debug:   ResolutionTraceEvent{Stage: "slice_b_survivor_verdict", SurvivorVerdict: "neutral"},
			summary: ResolutionTraceEvent{Stage: "slice_b_survivor_verdict", SurvivorVerdictSummary: true, SurvivorVerdictCandidateCount: 40, SurvivorVerdictNeutralCount: 38, SurvivorVerdictEliminatedCount: 2, SurvivorVerdictEliminatedIDs: []string{"e_a", "e_b"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var infoBuf bytes.Buffer
			infoLogger := slog.New(slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			tracer := NewSlogResolutionTracer(infoLogger)
			tracer.Trace(tc.debug)
			if got := strings.TrimSpace(infoBuf.String()); got != "" {
				t.Fatalf("stage %q's per-candidate line reached Info: %s -- it must stay Debug, only the folded summary is promoted", tc.name, got)
			}
			tracer.Trace(tc.summary)
			if got := strings.TrimSpace(infoBuf.String()); got == "" {
				t.Fatalf("stage %q's summary event emitted nothing at Info", tc.name)
			}
			if strings.Count(infoBuf.String(), `"stage":"`+tc.name+`"`) != 1 {
				t.Fatalf("stage %q: want exactly 1 Info line (the summary, not the per-candidate one), got: %s", tc.name, infoBuf.String())
			}
		})
	}
}

// TestSlogResolutionTracer_EvidenceCensusStagesReachInfo drives the
// evidence_round/evidence_probe/evidence_census_commit/evidence_source_native
// family end to end through the real production construction path (not
// hand-constructed events), proving these ARE live production paths --
// CensusFunc is genuinely wired at internal/runtime/hosted/open.go:466,
// contrary to an initial assumption during this ticket's own measurement
// pass that it might be shadow-only-in-tests. Reuses
// TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate's own fixture
// shape (chaos3896_slice_c_resolve_wiring_test.go).
func TestSlogResolutionTracer_EvidenceCensusStagesReachInfo(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
		},
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(logger)
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly one committed subject", resolution.Committed)
	}
	log := buf.String()
	for _, stage := range []string{"evidence_round", "evidence_probe", "evidence_census_commit", "evidence_source_native"} {
		if !strings.Contains(log, `"stage":"`+stage+`"`) {
			t.Errorf("no %q stage line at the production default log level (Info) -- CensusFunc IS wired in production (open.go), so this is a live rig-visibility gap, not a hypothetical one", stage)
		}
	}
}

// perCandidateCorroborationEvents, perCandidateIdentityGateEvents, and
// perCandidateSurvivorVerdictEvents are the rig-visibility audit's own filter helpers,
// the same shape as chaos4234_ranked_cut_test.go's perCandidateRankedCutEvents
// (CHAOS-5222): each fold added a SECOND event sharing its per-candidate
// stage's own token (CorroborationSummary/IdentityGateSummary/
// SurvivorVerdictSummary discriminate it), which pollutes any existing
// count-based assertion keyed on that token. Unlike perCandidateRankedCutEvents
// (which takes a *captureResolutionTracer specifically), these take a plain
// []ResolutionTraceEvent so they work against ANY tracer's own event slice
// -- captureResolutionTracer.eventsForStage's return, recordingTracer.events,
// or a bespoke test-local tracer's events field alike -- since every
// existing call site in this package uses a different capture type.

func perCandidateCorroborationEvents(events []ResolutionTraceEvent) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range events {
		if e.Stage == "corroboration" && !e.CorroborationSummary {
			out = append(out, e)
		}
	}
	return out
}

func perCandidateIdentityGateEvents(events []ResolutionTraceEvent) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range events {
		if e.Stage == "identity_gate" && !e.IdentityGateSummary {
			out = append(out, e)
		}
	}
	return out
}

func perCandidateSurvivorVerdictEvents(events []ResolutionTraceEvent) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range events {
		if e.Stage == "slice_b_survivor_verdict" && !e.SurvivorVerdictSummary {
			out = append(out, e)
		}
	}
	return out
}
