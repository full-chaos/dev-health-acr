package genkitruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	errSuppliedSynthesisSchema = errors.New("supplied synthesis does not match the model output schema")
	errSuppliedSynthesisSize   = errors.New("supplied synthesis exceeds the output size bound")
)

// SuppliedSynthesizerConfig bounds the output a caller may send. A zero
// MaxOutputBytes takes the contract's own bound.
type SuppliedSynthesizerConfig struct {
	MaxOutputBytes int
}

// SuppliedSynthesizer turns the synthesis output a caller wrote on its own
// model into a draft, through the schema, the strict decode and the domain
// conversion a model output goes through. It has no generator: it cannot make
// a model call.
type SuppliedSynthesizer struct {
	schema   *gojsonschema.Schema
	maxBytes int
}

func NewSuppliedSynthesizer(config SuppliedSynthesizerConfig) (*SuppliedSynthesizer, error) {
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = contractsv1.ContextFabricSuppliedSynthesisMaxBytes
	}
	document, err := SynthesisOutputSchema()
	if err != nil {
		return nil, fmt.Errorf("synthesis output schema: %w", err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(document))
	if err != nil {
		return nil, fmt.Errorf("synthesis output schema: %w", err)
	}
	return &SuppliedSynthesizer{schema: schema, maxBytes: config.MaxOutputBytes}, nil
}

// Parse decodes raw into a draft. A failure is a *contextfabric.SynthesisRejection
// with a closed reason; its text is fixed and never carries a value or a key
// the caller wrote.
func (s *SuppliedSynthesizer) Parse(raw []byte) (contextfabric.SynthesisDraft, error) {
	if len(raw) > s.maxBytes {
		return contextfabric.SynthesisDraft{}, rejectSuppliedSynthesis(contextfabric.RejectionReasonOutputSchemaMismatch, errSuppliedSynthesisSize)
	}
	output, err := s.decode(raw)
	if err != nil {
		return contextfabric.SynthesisDraft{}, rejectSuppliedSynthesis(contextfabric.RejectionReasonOutputSchemaMismatch, err)
	}
	draft, err := output.toDomain()
	if err != nil {
		return contextfabric.SynthesisDraft{}, rejectSuppliedSynthesis(contextfabric.SynthesisRejectionReasonOf(err), err)
	}
	return draft, nil
}

func rejectSuppliedSynthesis(reason contextfabric.SynthesisRejectionReason, cause error) error {
	return contextfabric.NewSynthesisRejection(reason, fmt.Errorf("%w: %w: %w", contextfabric.ErrSynthesisRejected, contextfabric.ErrModelOutput, cause))
}

func (s *SuppliedSynthesizer) decode(raw []byte) (synthesisOutput, error) {
	result, err := s.schema.Validate(gojsonschema.NewBytesLoader(raw))
	if err != nil || !result.Valid() {
		return synthesisOutput{}, errSuppliedSynthesisSchema
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output synthesisOutput
	if err := decoder.Decode(&output); err != nil {
		return synthesisOutput{}, errSuppliedSynthesisSchema
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return synthesisOutput{}, errSuppliedSynthesisSchema
	}
	if !strictCoverageDisclosures(output.CoverageDisclosures) {
		return synthesisOutput{}, errSuppliedSynthesisSchema
	}
	return output, nil
}

// strictCoverageDisclosures holds a caller's draft to the closed shape the
// model path decodes leniently: absent or null, or an array of exactly
// {detail_id, text} string objects.
func strictCoverageDisclosures(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return true
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return false
	}
	for _, entry := range entries {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(entry, &members); err != nil || members == nil || len(members) != 2 {
			return false
		}
		for _, key := range []string{"detail_id", "text"} {
			value, ok := members[key]
			var text string
			if !ok || json.Unmarshal(value, &text) != nil || string(bytes.TrimSpace(value)) == "null" {
				return false
			}
		}
	}
	return true
}
