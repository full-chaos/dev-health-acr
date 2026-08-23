// CHAOS-4146(b): the batch corpus driver -- runs the configured panel over a
// contiguous slice of a JSON array corpus file, one internal/panelharness.Run
// per case, writing one manifest per case. See this repository's
// docs/design/context-fabric-panel-run-manifest.md §5 for the full design,
// and §4 for the frozen-corpus exclusion this driver never overrides: it is
// validated against .remember/acr-3778-corpus-ext65.json (CHAOS-4146(d)'s
// own ext65 corpus), never the frozen holdout.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/panelharness"
)

// corpusCase is the minimal shape this driver reads from a corpus array
// element -- only Question is used; a corpus like
// .remember/acr-3778-corpus-ext65.json carries additional oracle-scoring
// fields (expect_id, expect_kind, subject_terms) this driver has no use for
// and deliberately does not decode, so it works against any corpus sharing
// this minimal shape without coupling to one corpus's exact field set.
type corpusCase struct {
	Question string `json:"question"`
}

// corpusBatchConfig collects runCorpusBatch's own inputs -- kept as a
// struct rather than a long parameter list because run() and any future
// caller (tests) construct it once from already-validated flag values.
type corpusBatchConfig struct {
	orgID       string
	panelists   []panelharness.Panelist
	baseRequest contractsv1.ContextFabricInvestigationRequest
	corpusPath  string
	caseStart   int
	caseCount   int
	outputDir   string
	runTag      string
}

// runCorpusBatch loads cfg.corpusPath, resolves the requested case slice,
// and drives one panelharness.Run per case in that slice, writing each
// case's manifest to cfg.outputDir. Fails fast on the first case that
// errors -- a partial batch with an unexplained gap in case coverage is
// worse than a batch that stops and names exactly which case failed, so an
// operator (or a re-run of just that shard) can retry precisely.
func runCorpusBatch(ctx context.Context, cfg corpusBatchConfig, stdout io.Writer) error {
	raw, err := os.ReadFile(cfg.corpusPath)
	if err != nil {
		return fmt.Errorf("read corpus %s: %w", cfg.corpusPath, err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return fmt.Errorf("parse corpus %s: %w", cfg.corpusPath, err)
	}
	if len(cases) == 0 {
		return fmt.Errorf("corpus %s named no cases", cfg.corpusPath)
	}
	if cfg.caseStart >= len(cases) {
		return fmt.Errorf("-case-start %d is out of range for corpus %s (%d case(s))", cfg.caseStart, cfg.corpusPath, len(cases))
	}
	count := cfg.caseCount
	if count == 0 {
		count = len(cases) - cfg.caseStart
	}
	// codex-anticipated: a mis-sized -case-count must fail LOUDLY, never
	// silently clamp to whatever remains -- a clamped shard boundary would
	// make a parallel batch's case coverage silently incomplete with no
	// signal an operator would ever see (this repo's "no silent caps"
	// discipline).
	if cfg.caseStart+count > len(cases) {
		return fmt.Errorf("-case-start %d + -case-count %d exceeds corpus %s's %d case(s)", cfg.caseStart, count, cfg.corpusPath, len(cases))
	}

	if err := os.MkdirAll(cfg.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", cfg.outputDir, err)
	}

	runTag := cfg.runTag
	if runTag == "" {
		runTag = fmt.Sprintf("panelbatch-%s-%d", time.Now().UTC().Format("20060102T150405Z"), os.Getpid())
	}
	corpusHash := sha256.Sum256(raw)
	corpusSHA256 := hex.EncodeToString(corpusHash[:])

	for offset := 0; offset < count; offset++ {
		index := cfg.caseStart + offset
		caseIndex := index // fresh variable per iteration -- its address is taken below
		question := cases[index].Question
		manifest, err := panelharness.Run(ctx, panelharness.RunConfig{
			OrgID: cfg.orgID, Question: question, Panelists: cfg.panelists, BaseRequest: cfg.baseRequest,
			CaseIndex: &caseIndex, RunTag: runTag, CorpusPath: cfg.corpusPath, CorpusSHA256: corpusSHA256,
		})
		if err != nil {
			// index only in the error text -- never the question this case
			// carried (corpus-safety discipline, docs §3/§2.4).
			return fmt.Errorf("panel run for corpus case %d: %w", index, err)
		}
		outputPath := filepath.Join(cfg.outputDir, runTag+"-case"+strconv.Itoa(index)+".json")
		if err := manifest.WriteFile(outputPath); err != nil {
			return fmt.Errorf("write manifest for corpus case %d: %w", index, err)
		}
		fmt.Fprintf(stdout, "wrote panel run manifest %s (case_index=%d, panel_run_id=%s, %d member(s))\n", outputPath, index, manifest.PanelRunID, len(manifest.Members))
	}
	fmt.Fprintf(stdout, "corpus batch complete: run_tag=%s cases=%d..%d (%d total)\n", runTag, cfg.caseStart, cfg.caseStart+count-1, count)
	return nil
}
