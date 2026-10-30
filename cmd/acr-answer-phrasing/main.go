// Command acr-answer-phrasing is the CHAOS-11600 corpus check: it reads stored
// Context Fabric answers and prints, per answer, how many reader-facing
// sentences state a derived number (a judged score, a model-categorized theme
// share, or a value the synthesizer derived) and how many of them state it as
// an observation, with no appears, leans or suggests. The measure is
// contextfabric.MeasureDerivedNumberPhrasing, the same function the engine logs
// on every saved answer, so a corpus run and a live log agree by construction.
//
// Usage: acr-answer-phrasing FILE...
//
// Each FILE holds one JSON document: an investigation result, or a stream
// event that carries one under "result". A file that is not one of those fails
// the run: a measurement that did not happen is never reported as zero.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "acr-answer-phrasing:", err)
		os.Exit(1)
	}
}

type document struct {
	Result *contextfabric.InvestigationResult `json:"result"`
}

func readAnswer(path string) (contextfabric.InvestigationResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return contextfabric.InvestigationResult{}, err
	}
	var wrapped document
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("%s: not JSON: %w", path, err)
	}
	var result contextfabric.InvestigationResult
	if wrapped.Result != nil {
		result = *wrapped.Result
	} else if err := json.Unmarshal(raw, &result); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("%s: %w", path, err)
	}
	// A wrapped document is held to the same check as a bare one: an empty or
	// foreign object under "result" is not an answer, and is never counted as
	// an answer with nothing derived in it.
	if !strings.HasPrefix(result.SchemaVersion, "context_fabric_investigation_result.") {
		return contextfabric.InvestigationResult{}, fmt.Errorf("%s: not an investigation result (schema_version %q)", path, result.SchemaVersion)
	}
	// Every stored answer has a result id and a status; an object that only
	// names the schema is not one.
	if strings.TrimSpace(result.ResultID) == "" || strings.TrimSpace(string(result.Status)) == "" {
		return contextfabric.InvestigationResult{}, fmt.Errorf("%s: not an investigation result (no result_id or status)", path)
	}
	return result, nil
}

func run(paths []string, out io.Writer) error {
	if len(paths) == 0 {
		return errors.New("usage: acr-answer-phrasing FILE...")
	}
	fmt.Fprintln(out, "file\tresult_id\tstatus\tsynthesis_source\tderived_values\tderived_number_sentences\tasserted")
	var answers, withSentence, withAsserted, sentences, asserted int
	for _, path := range paths {
		result, err := readAnswer(path)
		if err != nil {
			return err
		}
		measure := contextfabric.MeasureDerivedNumberPhrasing(result)
		answers++
		sentences += measure.Sentences
		asserted += measure.Asserted
		if measure.Sentences > 0 {
			withSentence++
		}
		if measure.Asserted > 0 {
			withAsserted++
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n", path, result.ResultID, result.Status, result.Versions.SynthesisSource,
			measure.DerivedValues, measure.Sentences, measure.Asserted)
	}
	fmt.Fprintf(out, "TOTAL\tanswers=%d\tanswers_stating_a_derived_number=%d\tanswers_asserting_one=%d\tderived_number_sentences=%d\tasserted=%d\n",
		answers, withSentence, withAsserted, sentences, asserted)
	return nil
}
