package main

import (
	"path/filepath"
	"testing"
)

// These cover CHAOS-3565's cohort-scoping of record_episode: the default oracle state
// (nil/false episode_write) must stay the unconditional "never observed" check,
// byte-identical to before CHAOS-3565 (review finding M4's "no landed oracle's behavior may
// change"), and episode_write=true must resolve to a permitted-optional state -- never a
// hard "must be observed" requirement, since nothing in this harness obliges an LLM agent to
// call record_episode even when it is reachable.

func TestRecordEpisodeExpectedState_defaultsToForbidden(t *testing.T) {
	if state := recordEpisodeExpectedState(Oracle{}); state != recordEpisodeForbidden {
		t.Fatalf("default state = %v, want recordEpisodeForbidden", state)
	}
}

func TestRecordEpisodeExpectedState_explicitFalseStaysForbidden(t *testing.T) {
	write := false
	if state := recordEpisodeExpectedState(Oracle{EpisodeWrite: &write}); state != recordEpisodeForbidden {
		t.Fatalf("explicit-false state = %v, want recordEpisodeForbidden", state)
	}
}

func TestRecordEpisodeExpectedState_explicitTrueIsPermittedOptionalNotRequired(t *testing.T) {
	write := true
	if state := recordEpisodeExpectedState(Oracle{EpisodeWrite: &write}); state != recordEpisodePermittedOptional {
		t.Fatalf("explicit-true state = %v, want recordEpisodePermittedOptional (never a hard-required state)", state)
	}
}

// TestRecordEpisodeCheck_forbiddenState_isByteIdenticalToPreCHAOS3565Behavior is the M4/M5
// regression test for the default (forbidden) path: same check id
// ("record_episode_never_observed"), same expected/actual rendering, same pass condition, for
// every oracle that predates CHAOS-3565 (nil/false episode_write). Confirmed RED against a
// mutant that reused the old hard-boolean-equality logic
// (observedRecordEpisode == expectRecordEpisode with expectRecordEpisode from a stale
// recordEpisodeExpectation helper) collapsed into this single-check-id function: see the
// commit message for the manual mutation used to prove this and
// TestRecordEpisodeCheck_permittedOptionalState_neverFailsWhenNotObserved below for the
// specific behavior that mutation would have broken.
func TestRecordEpisodeCheck_forbiddenState_isByteIdenticalToPreCHAOS3565Behavior(t *testing.T) {
	t.Run("not observed passes", func(t *testing.T) {
		l := newLayer("L3", "mcp")
		recordEpisodeCheck(l, recordEpisodeForbidden, false, nil, nil)
		assertSingleCheck(t, l, "record_episode_never_observed", true, false, "not observed", "false")
	})
	t.Run("observed fails", func(t *testing.T) {
		l := newLayer("L3", "mcp")
		recordEpisodeCheck(l, recordEpisodeForbidden, true, &ToolInvocation{Name: "record_episode"}, nil)
		assertSingleCheck(t, l, "record_episode_never_observed", false, false, "not observed", "true")
	})
}

// TestRecordEpisodeCheck_permittedOptionalState_neverFailsWhenNotObserved is the core M4 fix:
// a cohort-scoped task whose agent simply never chose to call record_episode must not fail
// the run -- it must SKIP (visible as neither a silent pass nor a failure; see report.go's
// skip doc comment), not require observation.
func TestRecordEpisodeCheck_permittedOptionalState_neverFailsWhenNotObserved(t *testing.T) {
	l := newLayer("L3", "mcp")
	recordEpisodeCheck(l, recordEpisodePermittedOptional, false, nil, nil)
	if len(l.Checks) != 1 || l.Checks[0].Name != "record_episode_permitted_optional" {
		t.Fatalf("checks = %#v, want one record_episode_permitted_optional check", l.Checks)
	}
	if !l.Checks[0].Skipped {
		t.Fatalf("check = %#v, want Skipped=true (never observed must not silently read as a pass, nor as a failure)", l.Checks[0])
	}
	if !l.OK {
		t.Fatal("a skipped optional check must not fail the layer")
	}
}

func TestRecordEpisodeCheck_permittedOptionalState_failsOnFailedInvocation(t *testing.T) {
	l := newLayer("L3", "mcp")
	recordEpisodeCheck(l, recordEpisodePermittedOptional, true, &ToolInvocation{Name: "record_episode", Status: "error"}, nil)
	assertSingleCheck(t, l, "record_episode_permitted_optional", false, false, "a successful record_episode result", "tool call failed")
}

func TestRecordEpisodeCheck_permittedOptionalState_validatesObservedResultAgainstContract(t *testing.T) {
	schemas := newSchemaLoader(filepath.Join(repoRoot(t), "contracts/jsonschema/v1"))
	validResult := `{
	  "schema_version": "mcp_record_episode_response.v1",
	  "status": "recorded",
	  "client_episode_id": "client_ep_01J0ACR001",
	  "idempotency_key": "idem_01J0ACR001",
	  "episode_id": "ep_01J0ACR001",
	  "created_at": "2026-07-10T14:10:01Z",
	  "redaction_state": "active",
	  "duplicate": false,
	  "scope": {"branch": "main", "commit_sha": "22e472d"},
	  "transcript_disposition": "not_submitted"
	}`

	t.Run("valid result passes", func(t *testing.T) {
		l := newLayer("L3", "mcp")
		recordEpisodeCheck(l, recordEpisodePermittedOptional, true, &ToolInvocation{Name: "record_episode", Status: "completed", ResultText: validResult}, schemas)
		assertSingleCheck(t, l, "record_episode_permitted_optional", true, false, "valid mcp_record_episode_response.v1", "valid")
	})
	t.Run("malformed result fails", func(t *testing.T) {
		l := newLayer("L3", "mcp")
		recordEpisodeCheck(l, recordEpisodePermittedOptional, true, &ToolInvocation{Name: "record_episode", Status: "completed", ResultText: `{"schema_version":"mcp_record_episode_response.v1"}`}, schemas)
		assertSingleCheck(t, l, "record_episode_permitted_optional", false, false, "valid mcp_record_episode_response.v1", "invalid")
	})
}

// assertSingleCheck fails the test unless l has exactly one check named name with the given
// ok/skipped state AND the given Expected/Actual rendering. Comparing Expected/Actual matters
// (review finding NEW-5): a mutation that swaps the literal label passed to l.add (e.g.
// "not observed" -> "observed" at layers.go's recordEpisodeCheck) leaves the pass/fail
// condition -- and so OK/Skipped -- untouched, so a check that only compared those would stay
// green against a report that now claims the wrong expected state.
func assertSingleCheck(t *testing.T, l *Layer, name string, ok, skipped bool, expected, actual string) {
	t.Helper()
	if len(l.Checks) != 1 {
		t.Fatalf("checks = %#v, want exactly one", l.Checks)
	}
	check := l.Checks[0]
	if check.Name != name || check.OK != ok || check.Skipped != skipped || check.Expected != expected || check.Actual != actual {
		t.Fatalf("check = %#v, want {Name:%q OK:%t Skipped:%t Expected:%q Actual:%q}", check, name, ok, skipped, expected, actual)
	}
}
