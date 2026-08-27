package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestFindStaleProjectIDs_ScopedToCasesOnly is the regression test for
// codex adversarial review round 2 (MEDIUM, confirmed): an earlier version
// scanned the ENTIRE raw JSON document for `"project:<raw>"` string
// literals, including this tool's own injected
// provenance.chaos4348_id_regenerations[].id_mappings block -- which
// records each OLD stale id as a map KEY by design. That made a subsequent
// -check permanently re-discover already-fixed ids as still stale,
// breaking the documented write-then-check workflow. Proven here
// directly: a fixture whose "cases" object has NO stale ids, but whose
// "provenance" object (simulating a prior run's own injected record) DOES
// contain the "project:<raw>" substring, must report zero stale ids.
func TestFindStaleProjectIDs_ScopedToCasesOnly(t *testing.T) {
	annex := `{
		"provenance": {
			"chaos4348_id_regenerations": [
				{
					"id_mappings": {
						"project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891": "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
					}
				}
			]
		},
		"cases": {
			"57": {
				"oracles": {
					"anchor": {
						"positive_key": "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
					}
				}
			}
		}
	}`
	found, err := findStaleProjectIDs([]byte(annex))
	if err != nil {
		t.Fatalf("findStaleProjectIDs() error = %v", err)
	}
	if len(found) != 0 {
		t.Errorf("findStaleProjectIDs() = %v, want empty -- a stale id inside provenance.chaos4348_id_regenerations must never be reported (it is historical record, not live annex content)", found)
	}
}

// TestFindStaleProjectIDs_StillCatchesRealStaleCaseIDs guards against the
// opposite regression: scoping to "cases" must not become scoping to
// nothing. A genuinely stale id inside "cases" must still be found.
func TestFindStaleProjectIDs_StillCatchesRealStaleCaseIDs(t *testing.T) {
	annex := `{
		"provenance": {},
		"cases": {
			"33": {
				"oracles": {
					"anchor": {
						"negatives": ["project:c67b1602-31db-4422-8dec-a4a02bbcc513"]
					}
				}
			}
		}
	}`
	found, err := findStaleProjectIDs([]byte(annex))
	if err != nil {
		t.Fatalf("findStaleProjectIDs() error = %v", err)
	}
	want := "project:c67b1602-31db-4422-8dec-a4a02bbcc513"
	if found[want] != 1 {
		t.Errorf("findStaleProjectIDs() = %v, want {%q: 1}", found, want)
	}
}

// TestRegenerateThenCheckIsIdempotent is the end-to-end regression test
// for the SAME codex round-2 finding: write, then -check-equivalent scan,
// on the tool's OWN output, must report zero stale ids for anything this
// run actually fixed -- not resurrect them via the provenance block this
// same run just wrote.
func TestRegenerateThenCheckIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	original := `{
		"provenance": {"signoff": {"status": "APPROVED", "by": "chris"}},
		"cases": {
			"57": {"oracles": {"anchor": {"positive_key": "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"}}}
		}
	}`
	if err := os.WriteFile(annexPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	staleID := "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"
	newID := "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
	replacements := map[string]string{staleID: newID}
	occurrences := map[string]int{staleID: 1}

	raw, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.ReplaceAll(string(raw), `"`+staleID+`"`, `"`+newID+`"`)
	updated, err = injectRegenerationProvenance(updated, []string{staleID}, replacements, occurrences, nil, "unit test", "probe.json")
	if err != nil {
		t.Fatalf("injectRegenerationProvenance() error = %v", err)
	}
	if err := os.WriteFile(annexPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	rewritten, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(rewritten) {
		t.Fatal("regenerated annex is not valid JSON")
	}
	found, err := findStaleProjectIDs(rewritten)
	if err != nil {
		t.Fatalf("findStaleProjectIDs() on regenerated annex: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("findStaleProjectIDs() after regeneration = %v, want empty -- re-running -check against this tool's own output must not resurrect an already-fixed id via the provenance block it just wrote", found)
	}
}

// TestLoadCorroboratedProjectIDs is the regression test for codex
// adversarial review (HIGH, both rounds): a -provider mapping's own
// well-formedness (matching the expected "project.v2:" scheme, round-trip
// decoding to the exact (provider, rawID) pair given) proves nothing about
// whether the CHOSEN provider is factually correct -- a wrong-but-
// well-formed mapping passes every other guard silently. This is the
// actual machine check: the derived id must appear as a real project-kind
// "corroboration" trace event in a real probe artifact.
func TestLoadCorroboratedProjectIDs(t *testing.T) {
	t.Run("extracts project-kind corroboration ids, ignores everything else", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "probe.json")
		artifact := `{
			"results": [
				{"turn1_trace_events": [
					{"Stage": "identity_gate", "Subject": {"kind": "project", "canonical_id": "project.v2:linear:aaa"}},
					{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "project.v2:linear:aaa"}},
					{"Stage": "corroboration", "Subject": {"kind": "repository", "canonical_id": "repository:bbb"}},
					{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "project.v2:gitlab:ccc"}}
				]}
			]
		}`
		if err := os.WriteFile(path, []byte(artifact), 0o644); err != nil {
			t.Fatal(err)
		}
		ids, err := loadCorroboratedProjectIDs([]string{path})
		if err != nil {
			t.Fatalf("loadCorroboratedProjectIDs() error = %v", err)
		}
		if !ids["project.v2:linear:aaa"] || !ids["project.v2:gitlab:ccc"] {
			t.Errorf("loadCorroboratedProjectIDs() = %v, want both project-kind corroboration ids present", ids)
		}
		if ids["repository:bbb"] {
			t.Error("loadCorroboratedProjectIDs() included a repository-kind id -- must only collect project-kind corroboration events")
		}
		if len(ids) != 2 {
			t.Errorf("loadCorroboratedProjectIDs() = %v, want exactly 2 entries", ids)
		}
	})

	t.Run("a wrong-provider id that was never corroborated is absent from the set", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "probe.json")
		artifact := `{"results": [{"turn1_trace_events": [
			{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "project.v2:linear:6241316a-85be-42ce-b243-8e41f2b18c8d"}}
		]}]}`
		if err := os.WriteFile(path, []byte(artifact), 0o644); err != nil {
			t.Fatal(err)
		}
		ids, err := loadCorroboratedProjectIDs([]string{path})
		if err != nil {
			t.Fatalf("loadCorroboratedProjectIDs() error = %v", err)
		}
		// A typo'd mapping -- "gitlab" instead of the real "linear" -- would
		// derive this well-formed but WRONG id. It must not be present.
		wrongID := "project.v2:gitlab:6241316a-85be-42ce-b243-8e41f2b18c8d"
		if ids[wrongID] {
			t.Errorf("loadCorroboratedProjectIDs() incorrectly contains the wrong-provider id %q", wrongID)
		}
	})

	t.Run("an artifact with zero project corroboration events is rejected as unusable evidence", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "probe.json")
		artifact := `{"results": [{"turn1_trace_events": [
			{"Stage": "corroboration", "Subject": {"kind": "repository", "canonical_id": "repository:bbb"}}
		]}]}`
		if err := os.WriteFile(path, []byte(artifact), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCorroboratedProjectIDs([]string{path}); err == nil {
			t.Error("loadCorroboratedProjectIDs() = nil error, want an error for an artifact with no project-kind corroboration events")
		}
	})
}

// TestMain_RefusesToWriteAnUnverifiedProviderMapping is an integration-
// level regression test: given a -provider mapping whose derived id is
// NOT present in -probe-evidence, the write path must refuse, never
// silently accept a well-formed-but-wrong id. Exercises the same
// derive+round-trip+corroboration-check sequence main() runs, without
// re-implementing main()'s flag parsing.
func TestMain_RefusesToWriteAnUnverifiedProviderMapping(t *testing.T) {
	dir := t.TempDir()
	probePath := filepath.Join(dir, "probe.json")
	// Evidence only corroborates the LINEAR id -- a "gitlab" mapping for
	// the same raw id must be rejected as unverified.
	artifact := `{"results": [{"turn1_trace_events": [
		{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "project.v2:linear:6241316a-85be-42ce-b243-8e41f2b18c8d"}}
	]}]}`
	if err := os.WriteFile(probePath, []byte(artifact), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := loadCorroboratedProjectIDs([]string{probePath})
	if err != nil {
		t.Fatal(err)
	}

	rawID := "6241316a-85be-42ce-b243-8e41f2b18c8d"
	wrongProvider := "gitlab" // the real provider (per probe evidence) is linear
	newID, _, err := identity.Derive(identity.KindProject, []string{wrongProvider, rawID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids[newID] {
		t.Fatalf("test setup invalid: the wrong-provider id %q was unexpectedly corroborated", newID)
	}
}

// TestInjectRegenerationProvenance_AppendsHistoryAcrossMultipleRuns is the
// regression test for codex adversarial review round 3 (MEDIUM,
// confirmed): an earlier version stored a SINGLE object under
// provenance.chaos4348_id_regeneration and unconditionally overwrote it on
// every call -- a second real run (e.g. a future follow-up that finally
// resolves the one known residual left by -allow-unmapped) would silently
// ERASE the first run's recorded mappings and probe evidence, even though
// the annex remains marked chris-approved throughout. Proven directly:
// two sequential calls, simulating two separate tool invocations, must
// leave BOTH records readable afterward, in order.
func TestInjectRegenerationProvenance_AppendsHistoryAcrossMultipleRuns(t *testing.T) {
	annex := `{"provenance": {"signoff": {"status": "APPROVED"}}, "cases": {}}`

	firstID := "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"
	firstNewID := "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
	afterFirstRun, err := injectRegenerationProvenance(
		annex,
		[]string{firstID},
		map[string]string{firstID: firstNewID},
		map[string]int{firstID: 6},
		[]string{"project:272efdae-c682-45b6-ae30-e8877eff15f4"},
		"first run: idx 57 probe",
		"probe57.json",
	)
	if err != nil {
		t.Fatalf("first injectRegenerationProvenance() error = %v", err)
	}

	// Second, LATER run: a hypothetical follow-up finally resolves the
	// residual left by the first run.
	secondID := "project:272efdae-c682-45b6-ae30-e8877eff15f4"
	secondNewID := "project.v2:someprovider:272efdae-c682-45b6-ae30-e8877eff15f4"
	afterSecondRun, err := injectRegenerationProvenance(
		afterFirstRun,
		[]string{secondID},
		map[string]string{secondID: secondNewID},
		map[string]int{secondID: 2},
		nil,
		"second run (later follow-up): resolved the residual",
		"probe-followup.json",
	)
	if err != nil {
		t.Fatalf("second injectRegenerationProvenance() error = %v", err)
	}

	var doc struct {
		Provenance struct {
			ChaosRegenerations []struct {
				VerifiedBy string            `json:"verified_by"`
				IDMappings map[string]string `json:"id_mappings"`
			} `json:"chaos4348_id_regenerations"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal([]byte(afterSecondRun), &doc); err != nil {
		t.Fatalf("unmarshal final annex: %v", err)
	}
	history := doc.Provenance.ChaosRegenerations
	if len(history) != 2 {
		t.Fatalf("chaos4348_id_regenerations has %d entries, want 2 (both runs) -- got %+v", len(history), history)
	}
	if history[0].IDMappings[firstID] != firstNewID {
		t.Errorf("first run's record was lost or corrupted: %+v", history[0])
	}
	if history[1].IDMappings[secondID] != secondNewID {
		t.Errorf("second run's record is missing or wrong: %+v", history[1])
	}
}

// TestWriteFileAtomically_NeverLeavesATruncatedFile is the regression
// test for codex adversarial review round 3 (MEDIUM, confirmed):
// os.WriteFile truncates the target before writing, so a partial write or
// interruption could leave the ONLY copy of a chris-signed annex empty or
// malformed with no way back. writeFileAtomically must leave either the
// COMPLETE old content or the COMPLETE new content, never a partial file,
// verified here by checking the original survives if a failing write path
// is simulated (a read-only target directory) and the real content lands
// correctly on a normal successful write.
func TestWriteFileAtomically_NeverLeavesATruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "annex.json")
	original := []byte(`{"original": true}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	newContent := []byte(`{"regenerated": true}`)
	if err := writeFileAtomically(path, newContent); err != nil {
		t.Fatalf("writeFileAtomically() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newContent) {
		t.Errorf("file content = %q, want %q", got, newContent)
	}

	// No leftover temp file in the directory -- successful Rename must
	// leave nothing behind for the deferred cleanup to find.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "annex.json" {
		t.Errorf("directory contents after a successful write = %v, want exactly [annex.json]", entries)
	}
}

// TestMain_ZeroReplacementRunIsANoOpThatDoesNotWrite is the regression
// test for codex adversarial review round 3's own first recommendation
// (MEDIUM): a run where every stale id is a known residual
// (-allow-unmapped, no -provider mapping for it) makes no content change
// and must exit 0 WITHOUT touching the annex at all -- not even to append
// an empty history entry. Exercises the real compiled binary via
// `go run .` (not a helper function) so the guard's actual wiring in
// main() -- the early return before the write path -- is what is under
// test, not a hand-simulated stand-in for it.
func TestMain_ZeroReplacementRunIsANoOpThatDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	original := `{"provenance": {"signoff": {"status": "APPROVED"}}, "cases": {"46": {"oracles": {"anchor": {"negatives": ["project:272efdae-c682-45b6-ae30-e8877eff15f4"]}}}}}`
	if err := os.WriteFile(annexPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "-annex", annexPath, "-allow-unmapped")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run . -allow-unmapped (no -provider mapping for the one residual) failed: %v\noutput:\n%s", err, out)
	}

	got, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("annex was modified by a zero-replacement run:\nbefore: %s\nafter:  %s", original, got)
	}
}

// TestMain_AllUnverifiedMappingsExitsNonzeroAndDoesNotWrite is the
// regression test for codex adversarial review round 4 (HIGH, confirmed):
// an earlier ordering checked "replacements is empty" (the no-op path)
// BEFORE checking "unverified is non-empty" -- when EVERY -provider
// mapping fails -probe-evidence corroboration, replacements ends up empty
// exactly like a genuine no-op does, so that ordering exited 0 with a
// misleading "nothing to regenerate" message instead of refusing.
// Exercised as a real subprocess (matching codex's own recommendation) so
// the actual exit code and actual annex-file mutation (none) are what is
// under test, not a hand-simulated stand-in.
func TestMain_AllUnverifiedMappingsExitsNonzeroAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	original := `{"provenance": {"signoff": {"status": "APPROVED"}}, "cases": {"57": {"oracles": {"anchor": {"positive_key": "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"}}}}}`
	if err := os.WriteFile(annexPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Evidence corroborates a COMPLETELY DIFFERENT project -- the one
	// mapping this run supplies can never be verified against it.
	probePath := filepath.Join(dir, "probe.json")
	probe := `{"results": [{"turn1_trace_events": [
		{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "project.v2:linear:unrelated-id"}}
	]}]}`
	if err := os.WriteFile(probePath, []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".",
		"-annex", annexPath,
		"-provider", "70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891=gitlab",
		"-verified-by", "deliberately wrong for this test",
		"-probe-evidence", probePath,
	)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go run . with an all-unverified mapping exited 0, want nonzero -- an unverified mapping must never read as success\noutput:\n%s", out)
	}
	if !strings.Contains(string(out), "NOT corroborated") {
		t.Errorf("expected the unverified-mapping refusal message in output, got:\n%s", out)
	}

	got, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("annex was modified despite the run being refused:\nbefore: %s\nafter:  %s", original, got)
	}
}

// TestMain_SecondRunResolvingAPriorResidualPreservesFirstRunsHistory is
// the regression test for codex adversarial review round 4 (MEDIUM,
// confirmed): an earlier version's id-substitution pass ran over the
// WHOLE serialized annex, not just "cases" -- so when a SECOND real
// invocation resolved an id the FIRST invocation had left unresolved
// (-allow-unmapped), the blind text replace also rewrote that id
// wherever it appeared inside the FIRST run's own already-written
// provenance.chaos4348_id_regenerations[0].unresolved_stale_ids,
// corrupting the append-only history to misreport what run 1 actually
// left unresolved. Exercised as TWO real, separate subprocess
// invocations against the SAME annex file -- not a single call to
// injectRegenerationProvenance -- so the full write path (including
// replaceIDsInCasesOnly) is what is under test.
func TestMain_SecondRunResolvingAPriorResidualPreservesFirstRunsHistory(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	staleA := "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891"
	staleB := "project:272efdae-c682-45b6-ae30-e8877eff15f4"
	original := `{"provenance": {"signoff": {"status": "APPROVED"}}, "cases": {
		"57": {"oracles": {"anchor": {"positive_key": "` + staleA + `"}}},
		"46": {"oracles": {"anchor": {"negatives": ["` + staleB + `"]}}}
	}}`
	if err := os.WriteFile(annexPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	probeAPath := filepath.Join(dir, "probeA.json")
	newA := "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
	if err := os.WriteFile(probeAPath, []byte(`{"results": [{"turn1_trace_events": [
		{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "`+newA+`"}}
	]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run 1: resolve A, leave B as a known residual (-allow-unmapped).
	run1 := exec.Command("go", "run", ".",
		"-annex", annexPath,
		"-provider", "70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891=gitlab",
		"-verified-by", "run 1: resolves A only",
		"-probe-evidence", probeAPath,
		"-allow-unmapped",
	)
	run1.Dir = "."
	if out, err := run1.CombinedOutput(); err != nil {
		t.Fatalf("run 1 failed: %v\noutput:\n%s", err, out)
	}

	// Run 2 (LATER, separate invocation): a hypothetical follow-up
	// finally resolves B.
	probeBPath := filepath.Join(dir, "probeB.json")
	newB := "project.v2:someprovider:272efdae-c682-45b6-ae30-e8877eff15f4"
	if err := os.WriteFile(probeBPath, []byte(`{"results": [{"turn1_trace_events": [
		{"Stage": "corroboration", "Subject": {"kind": "project", "canonical_id": "`+newB+`"}}
	]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run2 := exec.Command("go", "run", ".",
		"-annex", annexPath,
		"-provider", "272efdae-c682-45b6-ae30-e8877eff15f4=someprovider",
		"-verified-by", "run 2: resolves the residual B left by run 1",
		"-probe-evidence", probeBPath,
	)
	run2.Dir = "."
	if out, err := run2.CombinedOutput(); err != nil {
		t.Fatalf("run 2 failed: %v\noutput:\n%s", err, out)
	}

	final, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases      map[string]json.RawMessage `json:"cases"`
		Provenance struct {
			ChaosRegenerations []struct {
				VerifiedBy         string            `json:"verified_by"`
				IDMappings         map[string]string `json:"id_mappings"`
				UnresolvedStaleIDs []string          `json:"unresolved_stale_ids"`
			} `json:"chaos4348_id_regenerations"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(final, &doc); err != nil {
		t.Fatalf("unmarshal final annex: %v", err)
	}

	if !strings.Contains(string(final), newA) || !strings.Contains(string(final), newB) {
		t.Fatalf("final annex cases do not contain both regenerated ids -- got:\n%s", final)
	}
	if strings.Contains(string(doc.Cases["57"]), staleA) || strings.Contains(string(doc.Cases["46"]), staleB) {
		t.Errorf("final annex cases still contain a stale id after both runs")
	}

	history := doc.Provenance.ChaosRegenerations
	if len(history) != 2 {
		t.Fatalf("chaos4348_id_regenerations has %d entries, want 2 -- got %+v", len(history), history)
	}
	// The core assertion: run 1's OWN record must still say it left B
	// unresolved -- NOT that it silently resolved it (which the whole-
	// document text replace in run 2 would have caused by rewriting
	// run 1's history in place).
	if len(history[0].UnresolvedStaleIDs) != 1 || history[0].UnresolvedStaleIDs[0] != staleB {
		t.Errorf("run 1's history entry no longer records %q as unresolved -- got UnresolvedStaleIDs=%v (this is the exact corruption codex round 4 found: run 2's id substitution rewrote run 1's own history)", staleB, history[0].UnresolvedStaleIDs)
	}
	if history[0].IDMappings[staleA] != newA {
		t.Errorf("run 1's history entry lost its own id_mappings for %q: got %+v", staleA, history[0].IDMappings)
	}
	if history[1].IDMappings[staleB] != newB {
		t.Errorf("run 2's history entry is missing or wrong for %q: got %+v", staleB, history[1].IDMappings)
	}
}
