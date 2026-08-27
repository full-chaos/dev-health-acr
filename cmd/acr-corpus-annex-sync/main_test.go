package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func testAnnex() map[string]any {
	positiveA := "project.v2:gitlab:abc"
	return map[string]any{
		"provenance": map[string]any{
			"corpus_sha8": "deadbeef",
			"signoff":     map[string]any{"status": "APPROVED"},
		},
		"cases": map[string]any{
			"0": map[string]any{"oracles": map[string]any{
				"kind":   map[string]any{"positive": "project"},
				"anchor": map[string]any{"positive_key": &positiveA},
			}},
			"1": map[string]any{"oracles": map[string]any{
				"kind":   map[string]any{"positive": "repository"},
				"anchor": map[string]any{"positive_key": nil},
			}},
		},
	}
}

func testCorpus() []map[string]any {
	return []map[string]any{
		// index 0: stale id, disagrees.
		{"question": "Is chaos-ops keeping up?", "expect_kind": "project", "expect_id": "project:stale-old-id", "subject_terms": []string{"chaos-ops"}},
		// index 1: wrong kind AND a leftover id where the annex has none.
		{"question": "How is dev-health-ops doing?", "expect_kind": "project", "expect_id": "project:70d529e0-77145099", "subject_terms": []string{"dev-health-ops"}},
	}
}

func TestSync_CorrectsDisagreementsAndUpdatesHash(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	corpusPath := filepath.Join(dir, "corpus.json")
	writeJSONFile(t, annexPath, testAnnex())
	writeJSONFile(t, corpusPath, testCorpus())

	origCorpus, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath)
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sync failed: %v\noutput:\n%s", err, out)
	}

	var corpus []map[string]any
	newCorpus, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(newCorpus) == string(origCorpus) {
		t.Fatal("corpus file was not modified")
	}
	if err := json.Unmarshal(newCorpus, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus[0]["expect_id"] != "project.v2:gitlab:abc" {
		t.Errorf("corpus[0].expect_id = %v, want project.v2:gitlab:abc", corpus[0]["expect_id"])
	}
	if corpus[1]["expect_kind"] != "repository" || corpus[1]["expect_id"] != "" {
		t.Errorf("corpus[1] = %+v, want expect_kind=repository expect_id=\"\"", corpus[1])
	}
	// question/subject_terms must be byte-identical to what was authored,
	// never touched by this tool.
	if corpus[0]["question"] != "Is chaos-ops keeping up?" {
		t.Errorf("corpus[0].question was modified: %v", corpus[0]["question"])
	}

	// The annex's corpus_sha8 pin must now match the CHANGED corpus's own
	// hash, or the real harness would refuse to load this exact pair.
	var annexDoc struct {
		Provenance struct {
			CorpusSHA8 string `json:"corpus_sha8"`
		} `json:"provenance"`
	}
	annexBytes, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(annexBytes, &annexDoc); err != nil {
		t.Fatal(err)
	}
	if annexDoc.Provenance.CorpusSHA8 == "deadbeef" {
		t.Error("annex provenance.corpus_sha8 was not updated after the corpus content changed")
	}
}

func TestSync_ThenCheckIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	corpusPath := filepath.Join(dir, "corpus.json")
	writeJSONFile(t, annexPath, testAnnex())
	writeJSONFile(t, corpusPath, testCorpus())

	if out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput(); err != nil {
		t.Fatalf("sync failed: %v\noutput:\n%s", err, out)
	}

	cmd := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath, "-check")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("-check after a clean sync should exit 0 (nothing left to do), got: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "nothing to do") {
		t.Errorf("expected a clean -check after sync, got:\n%s", out)
	}
}

func TestSync_AuditTrailAppendsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	corpusPath := filepath.Join(dir, "corpus.json")
	writeJSONFile(t, annexPath, testAnnex())
	writeJSONFile(t, corpusPath, testCorpus())

	if out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput(); err != nil {
		t.Fatalf("first sync failed: %v\noutput:\n%s", err, out)
	}

	auditPath := corpusPath + ".sync-audit.json"
	var history1 []syncAuditRecord
	raw1, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw1, &history1); err != nil {
		t.Fatal(err)
	}
	if len(history1) != 1 {
		t.Fatalf("audit history after first run = %d entries, want 1", len(history1))
	}

	// A second, LATER run against a now-disagreeing corpus (simulating a
	// future edit) must APPEND, not replace, the first run's record.
	var corpus []map[string]any
	raw, _ := os.ReadFile(corpusPath)
	json.Unmarshal(raw, &corpus)
	corpus[0]["expect_id"] = "project:regressed-again"
	writeJSONFile(t, corpusPath, corpus)

	if out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput(); err != nil {
		t.Fatalf("second sync failed: %v\noutput:\n%s", err, out)
	}
	var history2 []syncAuditRecord
	raw2, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw2, &history2); err != nil {
		t.Fatal(err)
	}
	if len(history2) != 2 {
		t.Fatalf("audit history after second run = %d entries, want 2 (first run's record must survive)", len(history2))
	}
}

func TestSync_NoOpWhenAlreadyAgreeing(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	corpusPath := filepath.Join(dir, "corpus.json")
	writeJSONFile(t, annexPath, testAnnex())
	agreeing := []map[string]any{
		{"question": "q", "expect_kind": "project", "expect_id": "project.v2:gitlab:abc"},
		{"question": "q2", "expect_kind": "repository", "expect_id": ""},
	}
	writeJSONFile(t, corpusPath, agreeing)
	before, _ := os.ReadFile(corpusPath)

	out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput()
	if err != nil {
		t.Fatalf("sync failed on an already-agreeing pair: %v\noutput:\n%s", err, out)
	}
	after, _ := os.ReadFile(corpusPath)
	if string(before) != string(after) {
		t.Error("corpus was rewritten despite already agreeing with the annex")
	}
	if _, err := os.Stat(corpusPath + ".sync-audit.json"); err == nil {
		t.Error("an audit file was written despite nothing changing")
	}
}
