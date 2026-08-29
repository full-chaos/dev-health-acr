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
	ratify := flag.Bool("ratify", false, "advance signoff.approved_corpus_sha8 to the annex's current provenance.corpus_sha8 (requires -ratified-by and -ratify-note); does not sync")
	ratifiedBy := flag.String("ratified-by", "", "who ratified, recorded verbatim under signoff (required with -ratify)")
	ratifyNote := flag.String("ratify-note", "", "why, recorded verbatim under signoff.reratifications (required with -ratify)")
	flag.Parse()

	if *annexPath == "" || *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "acr-corpus-annex-sync: -annex and -corpus are both required")
		os.Exit(2)
	}

	// -ratify is a SEPARATE, deliberate act from a sync and never rides
	// along with one (CHAOS-4525): the sync path's own
	// stampApprovedCorpusSHA8IfAbsent exists precisely so a mechanical
	// content correction can never make an old approval look like it
	// covered content it never saw. Advancing the approval is a HUMAN
	// decision, so it needs its own invocation, its own two mandatory
	// justification flags, and no ability to change corpus content in the
	// same run.
	if *ratify {
		if *checkOnly {
			fmt.Fprintln(os.Stderr, "acr-corpus-annex-sync: -ratify and -check are mutually exclusive")
			os.Exit(2)
		}
		if *ratifiedBy == "" || *ratifyNote == "" {
			fmt.Fprintln(os.Stderr, "acr-corpus-annex-sync: -ratify requires both -ratified-by and -ratify-note (an unattributed, unexplained approval is not an approval)")
			os.Exit(2)
		}
		if err := ratifyCurrentCorpusSHA8(*annexPath, *corpusPath, *ratifiedBy, *ratifyNote); err != nil {
			fmt.Fprintf(os.Stderr, "acr-corpus-annex-sync: ratify: %v\n", err)
			os.Exit(1)
		}
		return
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

// stampApprovedCorpusSHA8IfAbsent records, under provenance.signoff, the
// corpus_sha8 chris's signoff was ACTUALLY approved against -- once. If
// signoff.approved_corpus_sha8 is already present (a prior run already
// recorded the real approval baseline), this is a no-op: a later
// mechanical sync must never make it look like the ORIGINAL approval
// covered content it never saw.
func stampApprovedCorpusSHA8IfAbsent(provenance map[string]json.RawMessage, approvedSHA8 string) error {
	if raw, ok := provenance["signoff"]; ok {
		var existing struct {
			ApprovedCorpusSHA8 string `json:"approved_corpus_sha8"`
		}
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("parse existing signoff: %w", err)
		}
		if existing.ApprovedCorpusSHA8 != "" {
			return nil
		}
	}
	var signoff map[string]json.RawMessage
	if raw, ok := provenance["signoff"]; ok {
		if err := json.Unmarshal(raw, &signoff); err != nil {
			return fmt.Errorf("parse signoff: %w", err)
		}
	} else {
		signoff = map[string]json.RawMessage{}
	}
	approvedJSON, err := json.Marshal(approvedSHA8)
	if err != nil {
		return fmt.Errorf("marshal approved_corpus_sha8: %w", err)
	}
	signoff["approved_corpus_sha8"] = approvedJSON
	signoffJSON, err := json.Marshal(signoff)
	if err != nil {
		return fmt.Errorf("marshal updated signoff: %w", err)
	}
	provenance["signoff"] = signoffJSON
	return nil
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

	// Team-lead ruling (2026-08-27, HIGH #2): keep provenance.signoff
	// UNTOUCHED (treating the signed annex as immutable would Fatal the
	// harness via requireAnnexSignedOff until chris re-ratifies -- not
	// this tool's call to force). Instead record WHAT was actually
	// approved, once: signoff.approved_corpus_sha8 is set to the CURRENT
	// (pre-update) corpus_sha8 the FIRST time this tool detects a
	// divergence, and never overwritten again by a later run -- it must
	// keep naming chris's real last approval, not silently follow every
	// subsequent mechanical sync. The harness stamps a loud, non-fatal
	// annex_signoff_stale into every report's provenance whenever this
	// differs from the live corpus_sha8 (chaos3742_two_turn_confirmation_test.go).
	if err := stampApprovedCorpusSHA8IfAbsent(provenance, currentSHA8); err != nil {
		return "", fmt.Errorf("stamp signoff.approved_corpus_sha8: %w", err)
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

// ratifyCurrentCorpusSHA8 advances signoff.approved_corpus_sha8 to the
// annex's own provenance.corpus_sha8 -- the mechanical path for what would
// otherwise be a hand edit of a signed artifact (CHAOS-4525, chris ruling
// 2026-08-29 07:31: sha8 re-ratification is not a chris call for incremental
// seed additions).
//
// It is deliberately narrow and fail-closed:
//
//   - It REFUSES unless the annex's recorded provenance.corpus_sha8 actually
//     matches the corpus file on disk. Ratifying a sha8 that no live corpus
//     has is how an approval ends up naming content nobody ever saw, which is
//     the whole failure this field exists to make visible.
//   - It writes only under provenance.signoff, via a json.RawMessage
//     round-trip, leaving every other byte of the annex (cases, the
//     chaos4348/chaos4525 audit histories) untouched.
//   - It appends to signoff.reratifications rather than replacing it, so the
//     chain of what was approved when survives -- the array the annex already
//     carries from the CHAOS-4348 re-ratification.
//   - A no-op ratification (approved already equals current) exits 0 and says
//     so, rather than appending a second identical record.
func ratifyCurrentCorpusSHA8(annexPath, corpusPath, ratifiedBy, note string) error {
	corpusBytes, err := os.ReadFile(corpusPath)
	if err != nil {
		return fmt.Errorf("read corpus: %w", err)
	}
	corpusSum := sha256.Sum256(corpusBytes)
	liveSHA8 := hex.EncodeToString(corpusSum[:])[:8]

	annexBytes, err := os.ReadFile(annexPath)
	if err != nil {
		return fmt.Errorf("read annex: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(annexBytes, &doc); err != nil {
		return fmt.Errorf("parse annex: %w", err)
	}
	var provenance map[string]json.RawMessage
	if err := json.Unmarshal(doc["provenance"], &provenance); err != nil {
		return fmt.Errorf("parse annex provenance: %w", err)
	}
	var recordedSHA8 string
	if err := json.Unmarshal(provenance["corpus_sha8"], &recordedSHA8); err != nil {
		return fmt.Errorf("parse provenance.corpus_sha8: %w", err)
	}
	if recordedSHA8 != liveSHA8 {
		return fmt.Errorf("refusing to ratify: annex provenance.corpus_sha8=%s but the corpus on disk hashes to %s -- run a sync first so the pair agrees", recordedSHA8, liveSHA8)
	}

	var signoff map[string]json.RawMessage
	if raw, ok := provenance["signoff"]; ok {
		if err := json.Unmarshal(raw, &signoff); err != nil {
			return fmt.Errorf("parse signoff: %w", err)
		}
	} else {
		signoff = map[string]json.RawMessage{}
	}
	var previous string
	if raw, ok := signoff["approved_corpus_sha8"]; ok {
		json.Unmarshal(raw, &previous)
	}
	if previous == liveSHA8 {
		fmt.Printf("acr-corpus-annex-sync: signoff.approved_corpus_sha8 is already %s -- nothing to ratify\n", liveSHA8)
		return nil
	}

	record := map[string]string{
		"by":                ratifiedBy,
		"at_utc":            time.Now().UTC().Format(time.RFC3339),
		"from_corpus_sha8":  previous,
		"to_corpus_sha8":    liveSHA8,
		"reason":            note,
		"ratified_via_tool": "cmd/acr-corpus-annex-sync -ratify",
	}
	var chain []json.RawMessage
	if raw, ok := signoff["reratifications"]; ok {
		if err := json.Unmarshal(raw, &chain); err != nil {
			return fmt.Errorf("parse signoff.reratifications: %w", err)
		}
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal reratification record: %w", err)
	}
	chain = append(chain, recordJSON)
	chainJSON, err := json.Marshal(chain)
	if err != nil {
		return fmt.Errorf("marshal reratification chain: %w", err)
	}
	approvedJSON, err := json.Marshal(liveSHA8)
	if err != nil {
		return fmt.Errorf("marshal approved_corpus_sha8: %w", err)
	}
	signoff["approved_corpus_sha8"] = approvedJSON
	signoff["reratifications"] = chainJSON

	signoffJSON, err := json.Marshal(signoff)
	if err != nil {
		return fmt.Errorf("marshal signoff: %w", err)
	}
	provenance["signoff"] = signoffJSON
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return fmt.Errorf("marshal provenance: %w", err)
	}
	doc["provenance"] = provenanceJSON
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal annex: %w", err)
	}
	if err := writeFileAtomically(annexPath, append(out, byte(10))); err != nil {
		return fmt.Errorf("write annex: %w", err)
	}
	fmt.Printf("acr-corpus-annex-sync: ratified signoff.approved_corpus_sha8 %s -> %s (by %s)\n", previous, liveSHA8, ratifiedBy)
	return nil
}
