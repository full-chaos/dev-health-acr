package hosted_test

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// TestWireProductionEnv_ConfirmedKindVectorCensusMaxComparisons_TrialPrefixedInputReachesCensusConfig
// is CHAOS-4155 Phase 2's harness-enablement red-first proof.
//
// Background: #267 (CHAOS-4155 Phase 1) shipped the shadow kind-scoped
// vector completeness census gated behind a single production env var,
// falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons
// (ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS), but
// never added a wireProductionEnv set() line for it -- unlike every other
// production ACR_CONTEXT_FABRIC_* knob this shared trial harness function
// wires (see the ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED and
// falkorgraph.EnvCommitLoneFloor-and-friends precedents just above/below
// this one). Two independent facts combine to make the census
// unreachable from any trial run before this fix:
//
//  1. clearAmbientACREnv unconditionally strips every ACR_-prefixed
//     ambient env var that is neither ACR_TEST_TRIAL_*-prefixed nor on
//     acrEnvIsolationAllowlist -- and this var is neither, so a bare
//     operator export of ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS
//     before launching a trial script is wiped before the resolution
//     engine ever starts.
//  2. wireProductionEnv had no set() call re-deriving it from an
//     ACR_TEST_TRIAL_-prefixed source the way every other trial-input
//     knob is threaded, so there was no surviving path left either.
//
// This test proves both halves directly: an ambient (bare-prefixed) value
// is gone after wireProductionEnv runs, and the ACR_TEST_TRIAL_-prefixed
// value is what the census's own config key reads back as -- RED before
// the fix (the key reads back empty, not the trial-prefixed value), GREEN
// after (the one set() line added below the falkorgraph.EnvCommitLoneFloor
// block in wireProductionEnv).
func TestWireProductionEnv_ConfirmedKindVectorCensusMaxComparisons_TrialPrefixedInputReachesCensusConfig(t *testing.T) {
	// stubOtherRequiredTrialInputs sets every OTHER wireProductionEnv-required
	// ACR_TEST_TRIAL_ input -- none of these are exercised (wireProductionEnv
	// is pure env-var wiring, no I/O), they only need to be non-empty so
	// requireEnv doesn't Fatalf. modelOverridden=true skips the
	// MODEL/_API_KEY requirement entirely (chaos3884_replay_harness_test.go's
	// own precedent for a non-model-path caller).
	stubOtherRequiredTrialInputs := func(t *testing.T) {
		t.Helper()
		for key, value := range map[string]string{
			"ACR_TEST_TRIAL_POSTGRES_DSN":    "postgres://stub/stub",
			"ACR_TEST_TRIAL_CLICKHOUSE_DSN":  "clickhouse://stub/stub",
			"ACR_TEST_TRIAL_FALKOR_ADDR":     "stub:6379",
			"ACR_TEST_TRIAL_EMBED_MODEL":     "text-embedding-3-large",
			"ACR_TEST_TRIAL_EMBED_DIMENSION": "3072",
			"ACR_TEST_TRIAL_EMBED_API_KEY":   "stub-key",
		} {
			t.Setenv(key, value)
		}
	}

	t.Run("trial-prefixed input reaches the census config key", func(t *testing.T) {
		stubOtherRequiredTrialInputs(t)

		// Ambient (bare-prefixed) leak: simulates an operator's own leftover
		// export in the launching shell, or a stray direnv-loaded value --
		// exactly the class clearAmbientACREnv exists to neutralize.
		t.Setenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, "999999")
		// The explicit, trial-prefixed source: what a CHAOS-4155 Phase 2
		// measurement run actually sets to turn the shadow arm on.
		const wantMaxComparisons = "12345"
		t.Setenv("ACR_TEST_TRIAL_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS", wantMaxComparisons)

		wireProductionEnv(t, true)

		got := os.Getenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons)
		if got != wantMaxComparisons {
			t.Fatalf("after wireProductionEnv, %s = %q, want %q (the trial-prefixed input) -- the ambient value (999999) must never survive, and the trial-prefixed value must reach the census's own config key",
				falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, got, wantMaxComparisons)
		}
	})

	// codex R1 (Low, confirmed): the case above proves the trial-prefixed
	// input WINS over an ambient leak, but never independently proves an
	// ambient-only leak is actually stripped when the trial-prefixed input
	// is unset (a run that never opts in must NOT silently inherit
	// whatever the calling shell happened to have exported). This
	// subtest closes that gap.
	t.Run("ambient-only leak is stripped when the trial-prefixed input is unset", func(t *testing.T) {
		stubOtherRequiredTrialInputs(t)

		t.Setenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, "999999")
		// Deliberately NOT setting ACR_TEST_TRIAL_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS.

		wireProductionEnv(t, true)

		if got := os.Getenv(falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons); got != "" {
			t.Fatalf("after wireProductionEnv with no trial-prefixed input, %s = %q, want empty -- an ambient-only leak (999999) must never survive clearAmbientACREnv on its own",
				falkorgraph.EnvConfirmedKindVectorCensusMaxComparisons, got)
		}
	})
}

// TestWireProductionEnv_LogLevel_TrialPrefixedInputReachesACRLogLevel is the
// second half of CHAOS-4155 Phase 2's harness enablement, same shape as the
// census budget test above: wireProductionEnv had no set() call for
// ACR_LOG_LEVEL, so ACR_TEST_TRIAL_LOG_LEVEL had nowhere to go. This proves
// the passthrough alone; TestChaos3742TwoTurnLogger_HonorsConfiguredLogLevel
// below proves the OTHER half of the bug (the hardcoded slog.LevelWarn that
// discarded cfg.LogLevel regardless of what env reached it).
func TestWireProductionEnv_LogLevel_TrialPrefixedInputReachesACRLogLevel(t *testing.T) {
	for key, value := range map[string]string{
		"ACR_TEST_TRIAL_POSTGRES_DSN":    "postgres://stub/stub",
		"ACR_TEST_TRIAL_CLICKHOUSE_DSN":  "clickhouse://stub/stub",
		"ACR_TEST_TRIAL_FALKOR_ADDR":     "stub:6379",
		"ACR_TEST_TRIAL_EMBED_MODEL":     "text-embedding-3-large",
		"ACR_TEST_TRIAL_EMBED_DIMENSION": "3072",
		"ACR_TEST_TRIAL_EMBED_API_KEY":   "stub-key",
	} {
		t.Setenv(key, value)
	}

	// Ambient leak: ACR_LOG_LEVEL is bare-ACR_-prefixed, not
	// ACR_TEST_TRIAL_-prefixed, so clearAmbientACREnv strips it
	// unconditionally -- an operator's own "warn" export must never
	// survive on its own.
	t.Setenv("ACR_LOG_LEVEL", "warn")
	t.Setenv("ACR_TEST_TRIAL_LOG_LEVEL", "debug")

	wireProductionEnv(t, true)

	if got := os.Getenv("ACR_LOG_LEVEL"); got != "debug" {
		t.Fatalf("after wireProductionEnv, ACR_LOG_LEVEL = %q, want %q (the trial-prefixed input) -- the ambient value (warn) must never survive, and the trial-prefixed value must reach the level config.Load() reads",
			got, "debug")
	}
}

// TestChaos3742TwoTurnLogger_HonorsConfiguredLogLevel is CHAOS-4155 Phase
// 2's red-first proof for the actual bug: chaos3742_two_turn_confirmation_
// test.go used to hardcode `slog.HandlerOptions{Level: slog.LevelWarn}`
// immediately after loading (and discarding) cfg.LogLevel via
// config.Load() -- so no env var, however it reached ACR_LOG_LEVEL, could
// ever raise the harness's own log level above WARN. graphrank's
// confirmed_kind_scope stage (CHAOS-4155's own vector census telemetry
// rides on it) logs at DebugContext, so it could never reach a trial run's
// logs before this fix, regardless of ACR_LOG_LEVEL/ACR_TEST_TRIAL_LOG_LEVEL.
//
// This test reproduces the exact two-line pattern from that file (load
// config, build the handler from its LogLevel field) rather than invoking
// the ~8000-line integration test itself (which needs live Postgres/
// ClickHouse/FalkorDB and is not a unit-test target) -- it isolates
// precisely the two lines the fix touches and proves the SAME logger
// construction shape used in production code.
func TestChaos3742TwoTurnLogger_HonorsConfiguredLogLevel(t *testing.T) {
	newLoggerFromConfiguredLevel := func(t *testing.T, buf *bytes.Buffer) *slog.Logger {
		t.Helper()
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: cfg.LogLevel}))
	}

	t.Run("RED shape: effective default info level never emits a debug event", func(t *testing.T) {
		// codex R1 (Medium, confirmed): pin explicitly rather than relying
		// on ACR_LOG_LEVEL being ambiently absent -- an externally
		// exported "debug" in the calling shell would otherwise make this
		// subtest fail (or worse, pass for the wrong reason).
		t.Setenv("ACR_LOG_LEVEL", "info")
		var buf bytes.Buffer
		logger := newLoggerFromConfiguredLevel(t, &buf)
		logger.Debug("context fabric resolution trace: confirmed kind scope", "state", "complete")
		if buf.Len() != 0 {
			t.Fatalf("expected no output at the default log level, got: %s", buf.String())
		}
	})

	t.Run("GREEN: ACR_LOG_LEVEL=debug (as wired from ACR_TEST_TRIAL_LOG_LEVEL) emits the debug event", func(t *testing.T) {
		t.Setenv("ACR_LOG_LEVEL", "debug")
		var buf bytes.Buffer
		logger := newLoggerFromConfiguredLevel(t, &buf)
		logger.Debug("context fabric resolution trace: confirmed kind scope", "state", "complete")
		if !strings.Contains(buf.String(), "confirmed kind scope") || !strings.Contains(buf.String(), "state=complete") {
			t.Fatalf("expected the debug event to reach the log at ACR_LOG_LEVEL=debug, got: %q", buf.String())
		}
	})
}

// TestTwoTurnTraceCapture_SlogTee_ForwardsConfirmedKindScopeToSlog is
// CHAOS-4155 Phase 2's red-first proof for codex R1's High finding:
// installing twoTurnTraceCapture as hosted.Options.ResolutionTracer
// REPLACES graphrank.NewSlogResolutionTracer entirely (open.go's own doc
// comment) rather than composing with it, so confirmed_kind_scope's
// DebugContext emission -- the event CHAOS-4155's own vector census
// telemetry rides on -- never reached slog for the two-turn trial harness
// at all, independent of the log-level fix in this same PR. Every existing
// construction of twoTurnTraceCapture in this file (all in-process unit
// tests, never the live trial path) leaves the new slogTee field at its
// nil zero value, exactly matching that pre-fix universal behavior.
func TestTwoTurnTraceCapture_SlogTee_ForwardsConfirmedKindScopeToSlog(t *testing.T) {
	event := graphrank.ResolutionTraceEvent{
		Stage:                            "confirmed_kind_scope",
		ConfirmedKindScopeState:          "complete",
		ConfirmedKindScopeCandidateCount: 3,
	}

	t.Run("RED shape: nil slogTee (every pre-fix construction) never reaches slog", func(t *testing.T) {
		trace := &twoTurnTraceCapture{}
		trace.Trace(event)

		if len(trace.events) != 1 {
			t.Fatalf("in-process capture must be unaffected: got %d events, want 1", len(trace.events))
		}
		// No slog assertion possible here by construction (slogTee is
		// nil) -- this subtest documents that the in-process capture
		// alone, with nothing wired to forward it, is exactly what every
		// OTHER twoTurnTraceCapture{} in this file already does.
	})

	t.Run("GREEN: wired slogTee forwards the SAME event to slog at debug", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		trace := &twoTurnTraceCapture{slogTee: graphrank.NewSlogResolutionTracer(logger)}

		trace.Trace(event)

		if len(trace.events) != 1 {
			t.Fatalf("the tee must not change the in-process capture: got %d events, want 1", len(trace.events))
		}
		if !strings.Contains(buf.String(), "confirmed kind scope") || !strings.Contains(buf.String(), "state=complete") {
			t.Fatalf("expected the confirmed_kind_scope event to reach slog via the tee, got: %q", buf.String())
		}
	})
}
