//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"strconv"
)

type receiptSummary struct {
	Outcome      string `json:"outcome"`
	Attempts     int    `json:"attempts"`
	FallbackUsed bool   `json:"fallback_used"`
	ErrorClass   string `json:"error_class"`
	ModelVersion string `json:"model_version"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type artifact struct {
	Schema                string              `json:"schema"`
	ApprovalID            string              `json:"approval_id"`
	SealDigest            string              `json:"seal_digest"`
	RowID                 string              `json:"row_id"`
	Replicate             int                 `json:"replicate"`
	RunID                 string              `json:"run_id"`
	ExampleID             string              `json:"example_id"`
	Set                   string              `json:"set"`
	SourcePin             string              `json:"source_pin"`
	BinarySHA256          string              `json:"binary_sha256"`
	ProfileSHA256         string              `json:"profile_sha256"`
	RunConfigSHA256       string              `json:"run_config_sha256"`
	Attempts              []attemptRecord     `json:"attempts"`
	DrawAuthorizations    []drawAuthorization `json:"draw_authorizations"`
	LatchTripped          bool                `json:"latch_tripped"`
	LatchReason           string              `json:"latch_reason"`
	Outcome               string              `json:"outcome"`
	RunnerOwned           bool                `json:"runner_owned"`
	ProductionReturned    bool                `json:"production_returned"`
	ContentAttemptSeq     *int                `json:"content_attempt_seq"`
	DiagnosticAttemptSeqs []int               `json:"diagnostic_attempt_seqs"`
	RawText               *string             `json:"raw_text"`
	FinishReason          string              `json:"finish_reason"`
	Receipt               receiptSummary      `json:"receipt"`
	NormalizedSHA256      string              `json:"normalized_sha256"`
	DurationMS            int64               `json:"duration_ms"`
	HarnessOverheadMS     int64               `json:"harness_overhead_ms"`
	Recovered             bool                `json:"recovered"`
	RecoveryIndex         int                 `json:"recovery_index"`
	RecoveryOf            string              `json:"recovery_of"`
	CapturedAt            string              `json:"captured_at"`
	// Variant is set on prompt-variant artifacts only (variant.go), together
	// with the variant schema. An incumbent artifact has no such member.
	Variant    *variantRecord `json:"variant,omitempty"`
	SelfSHA256 string         `json:"self_sha256,omitempty"`
}

const artifactSchema = "gocapture.artifact.v1"

// seriesError refuses an artifact that belongs to another series than the
// one named: "" is the incumbent, anything else a prompt variant.
func (a *artifact) seriesError(variant string) error {
	switch {
	case variant == "" && (a.Schema != artifactSchema || a.Variant != nil):
		return errors.New("a prompt-variant artifact in an incumbent run")
	case variant != "" && (a.Schema != variantArtifactSchema || a.Variant == nil || a.Variant.Name != variant):
		return errors.New("an artifact of another series in a prompt-variant run")
	}
	return nil
}

// seal computes self_sha256 over CJ of the artifact without that field and
// returns the canonical bytes to publish.
func (a *artifact) seal() ([]byte, error) {
	a.SelfSHA256 = ""
	body, err := cj(a)
	if err != nil {
		return nil, err
	}
	a.SelfSHA256 = sha256Hex(body)
	return cj(a)
}

func verifyArtifactBytes(data []byte) (artifact, error) {
	var a artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return artifact{}, err
	}
	claimed := a.SelfSHA256
	check := a
	body, err := check.seal()
	if err != nil {
		return artifact{}, err
	}
	if claimed == "" || check.SelfSHA256 != claimed || string(body) != string(data) {
		return artifact{}, errors.New("artifact self_sha256 or canonical bytes do not verify")
	}
	return a, nil
}

func artifactName(rowID string, replicate int, recovery int) string {
	name := sha256Hex([]byte(rowID)) + "-r" + strconv.Itoa(replicate)
	if recovery > 0 {
		name += ".recovery-" + strconv.Itoa(recovery)
	}
	return name + ".json"
}

func readArtifact(dir *cdir, name string) (artifact, []byte, error) {
	data, err := dir.readFile(name)
	if err != nil {
		return artifact{}, nil, err
	}
	a, err := verifyArtifactBytes(data)
	return a, data, err
}
