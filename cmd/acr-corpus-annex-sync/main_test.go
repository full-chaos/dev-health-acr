package main

import (
	"crypto/sha256"
	"encoding/hex"
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
			Signoff    struct {
				Status             string `json:"status"`
				ApprovedCorpusSHA8 string `json:"approved_corpus_sha8"`
			} `json:"signoff"`
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
	// codex adversarial review (HIGH #2, team-lead ruling 2026-08-27):
	// signoff itself must stay untouched (still APPROVED -- clearing it
	// would Fatal the harness), but the corpus_sha8 chris ACTUALLY
	// approved must be recorded so the staleness is detectable.
	if annexDoc.Provenance.Signoff.Status != "APPROVED" {
		t.Errorf("annex signoff.status = %q, want APPROVED (must stay untouched, never auto-invalidated)", annexDoc.Provenance.Signoff.Status)
	}
	if annexDoc.Provenance.Signoff.ApprovedCorpusSHA8 != "deadbeef" {
		t.Errorf("annex signoff.approved_corpus_sha8 = %q, want %q (the corpus_sha8 chris's signoff actually covered, before this sync)", annexDoc.Provenance.Signoff.ApprovedCorpusSHA8, "deadbeef")
	}
}

// TestSync_ApprovedCorpusSHA8IsStampedOnceNotOverwritten proves a SECOND
// sync run (a later, genuinely new content correction) does not move the
// approved_corpus_sha8 baseline forward -- it must keep naming chris's
// real last approval until a human re-ratifies, never silently follow
// every subsequent mechanical sync.
func TestSync_ApprovedCorpusSHA8IsStampedOnceNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	annexPath := filepath.Join(dir, "annex.json")
	corpusPath := filepath.Join(dir, "corpus.json")
	writeJSONFile(t, annexPath, testAnnex())
	writeJSONFile(t, corpusPath, testCorpus())

	if out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput(); err != nil {
		t.Fatalf("first sync failed: %v\noutput:\n%s", err, out)
	}

	var corpus []map[string]any
	raw, _ := os.ReadFile(corpusPath)
	json.Unmarshal(raw, &corpus)
	corpus[0]["expect_id"] = "project:regressed-again"
	writeJSONFile(t, corpusPath, corpus)

	if out, err := exec.Command("go", "run", ".", "-annex", annexPath, "-corpus", corpusPath).CombinedOutput(); err != nil {
		t.Fatalf("second sync failed: %v\noutput:\n%s", err, out)
	}

	var annexDoc struct {
		Provenance struct {
			Signoff struct {
				ApprovedCorpusSHA8 string `json:"approved_corpus_sha8"`
			} `json:"signoff"`
		} `json:"provenance"`
	}
	annexBytes, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(annexBytes, &annexDoc); err != nil {
		t.Fatal(err)
	}
	if annexDoc.Provenance.Signoff.ApprovedCorpusSHA8 != "deadbeef" {
		t.Errorf("approved_corpus_sha8 = %q after a SECOND sync, want it still %q (must not move forward without a human re-ratifying)", annexDoc.Provenance.Signoff.ApprovedCorpusSHA8, "deadbeef")
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

// writeRatifyFixture writes a corpus/annex pair whose annex provenance
// records `recordedSHA8` and whose signoff already approves `approvedSHA8`.
// The corpus content is real JSON so its live sha8 is a genuine digest, never
// a literal the test could accidentally agree with itself about.
func writeRatifyFixture(t *testing.T, recordedSHA8, approvedSHA8 string) (annexPath, corpusPath, liveSHA8 string) {
	t.Helper()
	dir := t.TempDir()
	corpusPath = filepath.Join(dir, "corpus.json")
	corpusBody := []byte(`[{"question":"fixture, never real corpus text","expect_kind":"team","expect_id":""}]`)
	if err := os.WriteFile(corpusPath, corpusBody, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(corpusBody)
	liveSHA8 = hex.EncodeToString(sum[:])[:8]

	annexPath = filepath.Join(dir, "annex.json")
	annex := map[string]any{
		"cases": map[string]any{"0": map[string]any{"question_class": "cohort_assessment"}},
		"provenance": map[string]any{
			"corpus_sha8": recordedSHA8,
			"signoff": map[string]any{
				"by": "chris", "status": "APPROVED", "approved_corpus_sha8": approvedSHA8,
			},
		},
	}
	body, err := json.MarshalIndent(annex, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(annexPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return annexPath, corpusPath, liveSHA8
}

func readRatifiedSignoff(t *testing.T, annexPath string) (approved string, chainLen int) {
	t.Helper()
	raw, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases      map[string]any `json:"cases"`
		Provenance struct {
			Signoff struct {
				ApprovedCorpusSHA8 string           `json:"approved_corpus_sha8"`
				Reratifications    []map[string]any `json:"reratifications"`
			} `json:"signoff"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) != 1 {
		t.Errorf("ratify touched the annex's cases (%d present, want 1) -- it must write only under provenance.signoff", len(doc.Cases))
	}
	return doc.Provenance.Signoff.ApprovedCorpusSHA8, len(doc.Provenance.Signoff.Reratifications)
}

// TestRatifyCurrentCorpusSHA8 pins the CHAOS-4525 -ratify path: the mechanical
// alternative to hand-editing a signed artifact's approval (chris ruling
// 2026-08-29 07:31 -- sha8 re-ratification is not a chris call for incremental
// seed additions).
func TestRatifyCurrentCorpusSHA8(t *testing.T) {
	t.Run("advances the approval and appends to the chain", func(t *testing.T) {
		annexPath, corpusPath, liveSHA8 := writeRatifyFixture(t, "", "oldappr")
		// The annex must already record the live corpus's sha8 -- rewrite
		// the fixture's recorded value to the digest just computed.
		reRecordCorpusSHA8(t, annexPath, liveSHA8)

		if err := ratifyCurrentCorpusSHA8(annexPath, corpusPath, "team-lead", "seeds added"); err != nil {
			t.Fatalf("ratify: %v", err)
		}
		approved, chain := readRatifiedSignoff(t, annexPath)
		if approved != liveSHA8 {
			t.Errorf("approved_corpus_sha8 = %q, want %q", approved, liveSHA8)
		}
		if chain != 1 {
			t.Errorf("reratifications length = %d, want 1", chain)
		}
	})

	t.Run("REFUSES when the annex names a corpus the file on disk is not", func(t *testing.T) {
		// The load-bearing guard: ratifying a sha8 no live corpus has is
		// exactly how an approval ends up naming content nobody saw.
		annexPath, corpusPath, _ := writeRatifyFixture(t, "deadbeef", "oldappr")
		err := ratifyCurrentCorpusSHA8(annexPath, corpusPath, "team-lead", "seeds added")
		if err == nil {
			t.Fatal("ratify succeeded against a mismatched annex/corpus pair, want a refusal")
		}
		if !strings.Contains(err.Error(), "refusing to ratify") {
			t.Errorf("error = %v, want a 'refusing to ratify' message", err)
		}
		if approved, chain := readRatifiedSignoff(t, annexPath); approved != "oldappr" || chain != 0 {
			t.Errorf("a refused ratify still wrote: approved=%q chain=%d", approved, chain)
		}
	})

	t.Run("is a no-op when the approval already names the live corpus", func(t *testing.T) {
		annexPath, corpusPath, liveSHA8 := writeRatifyFixture(t, "", "")
		reRecordCorpusSHA8(t, annexPath, liveSHA8)
		reRecordApprovedSHA8(t, annexPath, liveSHA8)

		if err := ratifyCurrentCorpusSHA8(annexPath, corpusPath, "team-lead", "again"); err != nil {
			t.Fatalf("ratify: %v", err)
		}
		if _, chain := readRatifiedSignoff(t, annexPath); chain != 0 {
			t.Errorf("reratifications length = %d, want 0 -- a no-op must not append a second identical record", chain)
		}
	})
}

func reRecordCorpusSHA8(t *testing.T, annexPath, sha8 string) {
	t.Helper()
	rewriteAnnexField(t, annexPath, "corpus_sha8", sha8)
}

func reRecordApprovedSHA8(t *testing.T, annexPath, sha8 string) {
	t.Helper()
	raw, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["provenance"].(map[string]any)["signoff"].(map[string]any)["approved_corpus_sha8"] = sha8
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(annexPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteAnnexField(t *testing.T, annexPath, key, value string) {
	t.Helper()
	raw, err := os.ReadFile(annexPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["provenance"].(map[string]any)[key] = value
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(annexPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
