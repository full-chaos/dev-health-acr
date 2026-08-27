// Command acr-corpus-annex-sync corrects the two-turn trial corpus's own
// per-case expect_kind/expect_id fields to match the oracle annex, wherever
// the two disagree (CHAOS-4348: Run G found the corpus and annex are two
// separate, redundant copies of the same information -- trialCase.ExpectID/
// ExpectKind, what the harness actually checks retrieval against, is
// sourced from the CORPUS, never the annex -- and nothing before this kept
// them in sync).
//
// The annex is authoritative wherever the two disagree (team-lead ruling,
// 2026-08-27: human-annotated, chris-signed). This tool is purely
// mechanical -- it never guesses or derives a new value, only copies the
// annex's own oracles.kind.positive / oracles.anchor.positive_key for a
// case into the corpus's expect_kind / expect_id for that same case index.
// It never touches question/subject_terms.
//
// Usage:
//
//	acr-corpus-annex-sync -annex <path> -corpus <path> [-check]
//
// -check reports every disagreement (index + which fields, no values) and
// exits 1 without writing. The default mode corrects the corpus in place
// (atomic write) and appends an audit-trail record to a sibling
// "<corpus>.sync-audit.json" file (an ARRAY, one entry per run, never
// overwritten -- the corpus itself has no provenance object to carry this
// the way the annex does, and this tool must never invent one: every other
// consumer in the codebase parses the corpus as a bare `[]trialCase` JSON
// array, and wrapping it in an object would break every one of them).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// signedAnnexCaseOracles is the minimal slice of the real, on-disk annex
// schema (chaos3742_two_turn_confirmation_test.go's own signedOracleCase)
// this tool needs -- kind.positive and anchor.positive_key, nothing else.
type signedAnnexCaseOracles struct {
	Kind struct {
		Positive string `json:"positive"`
	} `json:"kind"`
	Anchor struct {
		PositiveKey *string `json:"positive_key"`
	} `json:"anchor"`
}

type signedAnnexCase struct {
	Oracles signedAnnexCaseOracles `json:"oracles"`
}

type signedAnnexFile struct {
	Cases map[string]signedAnnexCase `json:"cases"`
}

// corpusDisagreement names one index's before/after kind/id -- carried
// in-process only; never printed with question text, and the audit file
// below carries ids/kinds only, matching every other CHAOS-4348 diagnostic
// in this codebase.
type corpusDisagreement struct {
	Index                  int
	OldKind, NewKind       string
	OldID, NewID           string
	KindChanged, IDChanged bool
}

func main() {
	annexPath := flag.String("annex", "", "path to the oracle annex JSON file (required)")
	corpusPath := flag.String("corpus", "", "path to the trial corpus JSON file (required)")
	checkOnly := flag.Bool("check", false, "report disagreements and exit 1 without writing")
	flag.Parse()

	if *annexPath == "" || *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "acr-corpus-annex-sync: -annex and -corpus are both required")
		os.Exit(2)
	}

	annexRaw, err := os.ReadFile(*annexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: read annex: %v\n", err)
		os.Exit(1)
	}
	var annex signedAnnexFile
	if err := json.Unmarshal(annexRaw, &annex); err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: parse annex: %v\n", err)
		os.Exit(1)
	}

	corpusRaw, err := os.ReadFile(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: read corpus: %v\n", err)
		os.Exit(1)
	}
	// []map[string]json.RawMessage, not []trialCase: preserves every
	// existing key (question, subject_terms, and any future field) byte-
	// for-byte for every entry this tool does not touch, and for the two
	// fields it DOES touch on a disagreeing entry, replaces only those.
	var corpus []map[string]json.RawMessage
	if err := json.Unmarshal(corpusRaw, &corpus); err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: parse corpus: %v\n", err)
		os.Exit(1)
	}

	var disagreements []corpusDisagreement
	for i, entry := range corpus {
		annexCase, ok := annex.Cases[strconv.Itoa(i)]
		if !ok {
			continue // no annex case at this index: nothing to sync against
		}
		wantKind := annexCase.Oracles.Kind.Positive
		wantID := ""
		if annexCase.Oracles.Anchor.PositiveKey != nil {
			wantID = *annexCase.Oracles.Anchor.PositiveKey
		}

		var haveKind, haveID string
		json.Unmarshal(entry["expect_kind"], &haveKind)
		json.Unmarshal(entry["expect_id"], &haveID)

		d := corpusDisagreement{Index: i, OldKind: haveKind, NewKind: wantKind, OldID: haveID, NewID: wantID}
		d.KindChanged = haveKind != wantKind
		d.IDChanged = haveID != wantID
		if d.KindChanged || d.IDChanged {
			disagreements = append(disagreements, d)
		}
	}
	sort.Slice(disagreements, func(i, j int) bool { return disagreements[i].Index < disagreements[j].Index })

	if len(disagreements) == 0 {
		fmt.Println("acr-corpus-annex-sync: corpus already agrees with the annex on every case -- nothing to do")
		return
	}

	if *checkOnly {
		fmt.Printf("acr-corpus-annex-sync: %d disagreement(s):\n", len(disagreements))
		for _, d := range disagreements {
			fmt.Printf("  index=%d kind_changed=%v id_changed=%v\n", d.Index, d.KindChanged, d.IDChanged)
		}
		os.Exit(1)
	}

	for _, d := range disagreements {
		entry := corpus[d.Index]
		kindJSON, err := json.Marshal(d.NewKind)
		if err != nil {
			fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: marshal kind for index %d: %v\n", d.Index, err)
			os.Exit(1)
		}
		idJSON, err := json.Marshal(d.NewID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: marshal id for index %d: %v\n", d.Index, err)
			os.Exit(1)
		}
		entry["expect_kind"] = kindJSON
		entry["expect_id"] = idJSON
	}

	updatedCorpus, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: marshal updated corpus: %v\n", err)
		os.Exit(1)
	}
	if err := writeFileAtomically(*corpusPath, updatedCorpus); err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: write corpus: %v\n", err)
		os.Exit(1)
	}

	if err := appendSyncAudit(*corpusPath, disagreements); err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: append audit record: %v\n", err)
		os.Exit(1)
	}

	// Correcting corpus content changes its own bytes, which changes its
	// sha256 -- and TestChaos3742TwoTurnConfirmationReplay Fatals at load
	// time if annex.provenance.corpus_sha8 (the signed artifact's own pin
	// on WHICH corpus it was ratified against) does not match the loaded
	// corpus's hash prefix. Left stale, this tool's own fix would make the
	// very consumer it exists to unblock refuse to run at all. Updated
	// here, atomically, as part of the SAME coordinated operation -- never
	// a separate manual step a caller could forget.
	newCorpusSHA8, err := updateAnnexCorpusSHA8(*annexPath, updatedCorpus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: update annex corpus_sha8: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("acr-corpus-annex-sync: corrected %d case(s) in %s:\n", len(disagreements), *corpusPath)
	for _, d := range disagreements {
		fmt.Printf("  index=%d kind_changed=%v id_changed=%v\n", d.Index, d.KindChanged, d.IDChanged)
	}
	if newCorpusSHA8 != "" {
		fmt.Printf("acr-corpus-annex-sync: updated %s provenance.corpus_sha8 -> %s (corpus content changed)\n", *annexPath, newCorpusSHA8)
	}
}

// updateAnnexCorpusSHA8 recomputes updatedCorpusBytes' own sha256 and, if
// it differs from the annex's currently-recorded provenance.corpus_sha8,
// rewrites that one field in place (atomic write, same discipline as the
// corpus write above) -- narrowly, via a json.RawMessage round-trip that
// touches ONLY provenance.corpus_sha8, leaving every other byte of the
// annex (including the chaos4348_id_regenerations audit history) untouched.
// Returns the new sha8 if it updated the file, "" if the annex already
// matched (a no-op, not an error).
func updateAnnexCorpusSHA8(annexPath string, updatedCorpusBytes []byte) (string, error) {
	sum := sha256.Sum256(updatedCorpusBytes)
	newSHA8 := hex.EncodeToString(sum[:])[:8]

	annexRaw, err := os.ReadFile(annexPath)
	if err != nil {
		return "", fmt.Errorf("read annex: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(annexRaw, &top); err != nil {
		return "", fmt.Errorf("parse annex: %w", err)
	}
	provenanceRaw, ok := top["provenance"]
	if !ok {
		return "", fmt.Errorf("annex has no top-level \"provenance\" object")
	}
	var provenance map[string]json.RawMessage
	if err := json.Unmarshal(provenanceRaw, &provenance); err != nil {
		return "", fmt.Errorf("parse annex provenance: %w", err)
	}
	var currentSHA8 string
	json.Unmarshal(provenance["corpus_sha8"], &currentSHA8)
	if currentSHA8 == newSHA8 {
		return "", nil
	}

	newSHA8JSON, err := json.Marshal(newSHA8)
	if err != nil {
		return "", fmt.Errorf("marshal new corpus_sha8: %w", err)
	}
	provenance["corpus_sha8"] = newSHA8JSON
	newProvenance, err := json.MarshalIndent(provenance, "  ", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal updated provenance: %w", err)
	}
	top["provenance"] = newProvenance

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal updated annex: %w", err)
	}
	if err := writeFileAtomically(annexPath, append(out, '\n')); err != nil {
		return "", fmt.Errorf("write annex: %w", err)
	}
	return newSHA8, nil
}

// syncAuditRecord is corpus-safe by construction: kinds and ids only, no
// question text, matching every other CHAOS-4348 diagnostic.
type syncAuditRecord struct {
	Tool         string               `json:"tool"`
	Ticket       string               `json:"ticket"`
	AppliedAtUTC string               `json:"applied_at_utc"`
	Corrections  []corpusDisagreement `json:"corrections"`
}

func appendSyncAudit(corpusPath string, disagreements []corpusDisagreement) error {
	auditPath := corpusPath + ".sync-audit.json"
	var history []syncAuditRecord
	if existing, err := os.ReadFile(auditPath); err == nil {
		if err := json.Unmarshal(existing, &history); err != nil {
			return fmt.Errorf("parse existing audit file %s: %w", auditPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing audit file %s: %w", auditPath, err)
	}
	history = append(history, syncAuditRecord{
		Tool:         "cmd/acr-corpus-annex-sync",
		Ticket:       "CHAOS-4348",
		AppliedAtUTC: time.Now().UTC().Format(time.RFC3339),
		Corrections:  disagreements,
	})
	out, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal audit history: %w", err)
	}
	return writeFileAtomically(auditPath, out)
}

// writeFileAtomically mirrors cmd/acr-annex-regen-project-ids' own helper
// (temp file in the same directory, fsync, atomic rename) -- never a
// truncating os.WriteFile against a source-of-truth artifact.
func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".acr-corpus-annex-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file over %s: %w", path, err)
	}
	return nil
}
