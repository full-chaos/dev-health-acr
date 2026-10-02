package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// derive rebuilds responses.jsonl and report.json from the canonical
// artifacts and the ledger (spec §8). Derived files carry no authority of
// their own; the evaluator re-verifies the artifacts.
func (s *captureSession) derive() error {
	return deriveOutputs(s.ledger, s.input, s.cfg.RunID, s.runDir, s.rawDir, interferenceThreshold(s.tuning).Milliseconds())
}

// canonicalTerminal is the pair's one scored outcome: its first terminal,
// or, for an uncertain pair, its one recovery outcome.
func canonicalTerminal(st *pairState) (*ledgerRecord, bool, bool) {
	switch {
	case st.Terminal != nil:
		return st.Terminal, false, false
	case st.Uncertain:
		if t := st.recoveryTerminal(); t != nil {
			return t, true, false
		}
		return nil, false, true
	}
	return nil, false, false
}

type responseRow struct {
	Candidate          string             `json:"candidate"`
	RowID              string             `json:"row_id"`
	ExampleID          string             `json:"example_id"`
	Draw               int                `json:"draw"`
	Remote             bool               `json:"remote"`
	ModelID            string             `json:"model_id"`
	ModelRevision      string             `json:"model_revision"`
	AdapterID          *string            `json:"adapter_id"`
	AdapterSHA256      *string            `json:"adapter_sha256"`
	RequestSHA256      string             `json:"request_sha256"`
	PromptSHA256       string             `json:"prompt_sha256"`
	TemplateSHA256     *string            `json:"template_sha256"`
	ContractMatch      bool               `json:"contract_match"`
	ContractReason     *string            `json:"contract_reason"`
	ContractDetail     contractDetail     `json:"contract_detail"`
	Decoding           map[string]any     `json:"decoding"`
	RawText            *string            `json:"raw_text"`
	FinishReason       string             `json:"finish_reason"`
	TokenCounts        map[string]int     `json:"token_counts"`
	TimingsS           map[string]float64 `json:"timings_s"`
	Error              *string            `json:"error"`
	ProductionReturned bool               `json:"production_returned"`
	Capture            captureRef         `json:"capture"`
	// Variant is set on prompt-variant rows only; the candidate is then
	// "incumbent-variant[<name>]" and never "incumbent".
	Variant *variantRecord `json:"variant,omitempty"`
}

type contractDetail struct {
	Method         string   `json:"method"`
	DescriptorSHAs []string `json:"descriptor_shas"`
}

type captureRef struct {
	ArtifactPath      string `json:"artifact_path"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	Outcome           string `json:"outcome"`
	ContentAttemptSeq *int   `json:"content_attempt_seq"`
	ResponseSHA256    string `json:"response_sha256"`
	Recovered         bool   `json:"recovered"`
}

type captureReport struct {
	Schema          string                    `json:"schema"`
	RunID           string                    `json:"run_id"`
	SealDigest      string                    `json:"seal_digest"`
	SealedRows      int                       `json:"sealed_rows"`
	Canonical       int                       `json:"canonical_pairs"`
	Expected        int                       `json:"expected_pairs"`
	Complete        bool                      `json:"complete"`
	Uncertain       int                       `json:"uncertain_pairs"`
	Recovered       int                       `json:"recovered_pairs"`
	OutcomesByDraw  map[string]map[string]int `json:"outcomes_by_replicate"`
	RunnerOwned     int                       `json:"runner_owned"`
	HTTPReservedRun int                       `json:"http_reserved_run"`
	ApprovalSpent   int                       `json:"approval_spent"`
	ApprovalCap     int                       `json:"approval_cap"`
	// Harness time inside production deadlines (sol r2): the largest
	// per-invocation overhead and the threshold that makes it material.
	MaxHarnessOverheadMS    int64 `json:"max_harness_overhead_ms"`
	InterferenceThresholdMS int64 `json:"interference_threshold_ms"`
	// Prompt-variant runs only.
	Variant   string `json:"variant,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

func deriveOutputs(ledger *approvalLedger, input verifiedInput, runID string, runDir, rawDir *cdir, thresholdMS int64) error {
	seal := input.Seal
	report := captureReport{Schema: "gocapture.report.v1", RunID: runID, SealDigest: seal.SealDigest, SealedRows: len(input.Rows),
		Expected: 3 * len(input.Rows), OutcomesByDraw: map[string]map[string]int{}, HTTPReservedRun: ledger.runSpent[runID],
		ApprovalSpent: ledger.spent, ApprovalCap: ledger.cap, InterferenceThresholdMS: thresholdMS}
	// The series is the run's own, as its run_started record names it: the
	// incumbent (no variant) or one prompt variant. Only that series' pairs
	// are derived, and every artifact must belong to it.
	variant := ledger.runs[runID].Variant
	if variant != "" {
		report.Variant, report.Candidate = variant, variantCandidate(variant)
	}
	pairs := ledger.pairsOf(variant)
	var rows []responseRow
	for _, row := range input.Rows {
		for replicate := 0; replicate < 3; replicate++ {
			key := pairKey{SealDigest: seal.SealDigest, RowID: row.RowID, Replicate: replicate}
			st := pairs[key]
			if st == nil {
				continue
			}
			term, recovered, uncertain := canonicalTerminal(st)
			if uncertain {
				report.Uncertain++
				continue
			}
			if term == nil {
				continue
			}
			if recovered {
				report.Recovered++
			}
			a, data, err := readArtifact(rawDir, term.ArtifactName)
			if err != nil {
				return fmt.Errorf("derive: artifact %s: %w", term.ArtifactName, err)
			}
			if sha256Hex(data) != term.ArtifactSHA256 {
				return errors.New("derive: artifact differs from its ledger record")
			}
			if err := a.seriesError(variant); err != nil {
				return fmt.Errorf("derive: artifact %s: %w", term.ArtifactName, err)
			}
			if a.RunID != runID {
				return fmt.Errorf("derive: artifact %s belongs to another run", term.ArtifactName)
			}
			report.Canonical++
			byDraw := report.OutcomesByDraw[fmt.Sprint(replicate)]
			if byDraw == nil {
				byDraw = map[string]int{}
				report.OutcomesByDraw[fmt.Sprint(replicate)] = byDraw
			}
			byDraw[a.Outcome]++
			if a.HarnessOverheadMS > report.MaxHarnessOverheadMS {
				report.MaxHarnessOverheadMS = a.HarnessOverheadMS
			}
			if a.RunnerOwned {
				report.RunnerOwned++
			}
			rows = append(rows, responseRowFor(a, term, input, row.ExampleID))
		}
	}
	report.Complete = report.Canonical == report.Expected && report.RunnerOwned == 0 && report.Uncertain == 0 && ledger.stopReason(runID) == ""
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RowID != rows[j].RowID {
			return rows[i].RowID < rows[j].RowID
		}
		return rows[i].Draw < rows[j].Draw
	})
	// Derived files are plain JSON (they carry floats); only canonical
	// artifacts and digested records use CJ.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	for _, r := range rows {
		if err := encoder.Encode(r); err != nil {
			return err
		}
	}
	if err := runDir.replaceDerived("responses.jsonl", buf.Bytes()); err != nil {
		return err
	}
	reportBytes, err := cj(report)
	if err != nil {
		return err
	}
	return runDir.replaceDerived("report.json", append(reportBytes, '\n'))
}

func responseRowFor(a artifact, term *ledgerRecord, input verifiedInput, exampleID string) responseRow {
	var inputSHA string
	for _, m := range input.Seal.Membership {
		if m.RowID == a.RowID {
			inputSHA = m.InputSHA256
		}
	}
	shas := []string{}
	for _, at := range a.Attempts {
		shas = append(shas, at.ObservedDescriptorSHA256)
	}
	row := responseRow{
		Candidate: "incumbent", RowID: a.RowID, ExampleID: exampleID, Draw: a.Replicate, Remote: true,
		ModelID: productionAllowlist.Model, ModelRevision: a.Receipt.ModelVersion,
		RequestSHA256: inputSHA, PromptSHA256: input.Seal.ExpectedSystemMessageSHA256,
		ContractMatch:  !a.RunnerOwned,
		ContractDetail: contractDetail{Method: "wire", DescriptorSHAs: shas},
		Decoding:       map[string]any{"temperature": nil, "max_tokens": nil, "seed": nil},
		RawText:        a.RawText, FinishReason: a.FinishReason,
		TokenCounts:        map[string]int{"prompt": a.Receipt.InputTokens, "completion": a.Receipt.OutputTokens},
		TimingsS:           map[string]float64{"generate": float64(a.DurationMS) / 1000},
		ProductionReturned: a.ProductionReturned,
		Capture:            captureRef{ArtifactPath: "raw/" + term.ArtifactName, ArtifactSHA256: term.ArtifactSHA256, Outcome: a.Outcome, ContentAttemptSeq: a.ContentAttemptSeq, Recovered: a.Recovered},
	}
	if len(a.Attempts) > 0 {
		for _, at := range a.Attempts {
			if at.Sent {
				if obs, ok := seedOf(at.ObservedDescriptor); ok {
					row.Decoding["seed"] = obs
				}
				break
			}
		}
	}
	if a.Variant != nil {
		// A prompt-variant row: its own candidate label, and the system
		// message it actually sent (production message + appendix).
		row.Candidate, row.PromptSHA256, row.Variant = variantCandidate(a.Variant.Name), a.Variant.SystemMessageSHA256, a.Variant
	}
	if a.RunnerOwned {
		reason := a.Outcome
		row.ContractReason = &reason
	}
	if a.Outcome != "ok" {
		e := a.Outcome
		row.Error = &e
	}
	if a.ContentAttemptSeq != nil {
		for _, at := range a.Attempts {
			if at.AttemptSeq == *a.ContentAttemptSeq {
				row.Capture.ResponseSHA256 = at.ResponseSHA256
			}
		}
	}
	return row
}

func seedOf(descriptorJSON []byte) (string, bool) {
	var d descriptor
	if err := strictUnmarshal(descriptorJSON, &d); err != nil || d.Seed == nil {
		return "", false
	}
	return *d.Seed, true
}
