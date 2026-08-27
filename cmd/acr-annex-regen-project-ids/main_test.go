package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestFindStaleProjectIDs_ScopedToCasesOnly is the regression test for
// codex adversarial review round 2 (MEDIUM, confirmed): an earlier version
// scanned the ENTIRE raw JSON document for `"project:<raw>"` string
// literals, including this tool's own injected
// provenance.chaos4348_id_regeneration.id_mappings block -- which records
// each OLD stale id as a map KEY by design. That made a subsequent -check
// permanently re-discover already-fixed ids as still stale, breaking the
// documented write-then-check workflow. Proven here directly: a fixture
// whose "cases" object has NO stale ids, but whose "provenance" object
// (simulating a prior run's own injected record) DOES contain the
// "project:<raw>" substring, must report zero stale ids.
func TestFindStaleProjectIDs_ScopedToCasesOnly(t *testing.T) {
	annex := `{
		"provenance": {
			"chaos4348_id_regeneration": {
				"id_mappings": {
					"project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891": "project.v2:gitlab:70d529e0-3c06-4597-8480-794fd02328b6%3Agitlab%3A71133891"
				}
			}
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
		t.Errorf("findStaleProjectIDs() = %v, want empty -- a stale id inside provenance.chaos4348_id_regeneration must never be reported (it is historical record, not live annex content)", found)
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
