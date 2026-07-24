package main

import (
	"encoding/json"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/tests/fullstack/sidecarmd"
)

// clientEvidenceCall is one source_evidence tool invocation as actually observed in the
// OpenCode event stream: the argument the client sent, and, if the call succeeded, the
// ExpandedEvidence document the MCP server actually returned to it. This is the primary
// source of truth for L4/L5's evidence-agreement checks (Codex finding 3) -- unlike the
// driver's direct-HTTP expanded-evidence/*.json captures, which exist regardless of whether
// the client ever called source_evidence at all.
type clientEvidenceCall struct {
	// ArgumentEvidenceRefID is the evidence_ref_id the client actually sent, per
	// contracts/jsonschema/v1/mcp_source_evidence_request.v1.schema.json. Empty if the
	// argument did not parse.
	ArgumentEvidenceRefID string
	ArgumentParsed        bool
	// ArgumentIsLivePacketEvidence is true iff ArgumentEvidenceRefID is a member of the live
	// packet's own evidence_ref_ids -- i.e. the client asked to expand something the packet
	// actually offered, not an invented identifier.
	ArgumentIsLivePacketEvidence bool
	// Failed mirrors the tool invocation's own reported status (ToolInvocation.Failed()).
	Failed bool
	// Doc/DocRaw are set iff the result was forwarded as JSON and parsed against
	// mcp_source_evidence_response.v1 / expanded_evidence.v1. OpenCode 1.18.4 does NOT do
	// this -- see Rendered below -- so on a real run these are nil and only clients that
	// forward MCP StructuredContent populate them.
	Doc    *contractsv1.ExpandedEvidence
	DocRaw []byte
	// Rendered/RenderedParsed carry the sidecar's markdown rendering of the same document,
	// which is what OpenCode actually records in part.state.output (the MCP text content).
	// This is the normal path: the client is graded on what it genuinely received.
	Rendered       sidecarmd.Evidence
	RenderedParsed bool
	DocError       error
}

// echoedRequestedReference reports whether the result the client received actually identifies
// the reference the client asked for. It is what makes the markdown path a real round-trip
// proof rather than "some text came back": the rendered "# Evidence <id>" heading is authored
// by the sidecar from the document it resolved, so a result naming the requested
// evidence_ref_id cannot be produced without the service having resolved it.
func (c clientEvidenceCall) echoedRequestedReference() bool {
	switch {
	case c.Doc != nil:
		return c.Doc.Evidence.EvidenceRefID == c.ArgumentEvidenceRefID
	case c.RenderedParsed:
		return c.Rendered.EvidenceRefID == c.ArgumentEvidenceRefID
	default:
		return false
	}
}

// observedEntity is the entity identity the client saw for this expansion, from whichever
// representation it was actually given.
func (c clientEvidenceCall) observedEntity() (entityType, entityID string, ok bool) {
	switch {
	case c.Doc != nil:
		return c.Doc.Evidence.Source.EntityType, c.Doc.Evidence.Source.EntityID, true
	case c.RenderedParsed:
		return c.Rendered.EntityType, c.Rendered.EntityID, true
	default:
		return "", "", false
	}
}

// observedAvailability is the availability the client saw, same rationale as observedEntity.
func (c clientEvidenceCall) observedAvailability() (string, bool) {
	switch {
	case c.Doc != nil:
		return string(c.Doc.Availability), true
	case c.RenderedParsed:
		return c.Rendered.Availability, true
	default:
		return "", false
	}
}

// succeeded reports whether this call is a genuine, verifiable expansion: the argument was a
// real evidence reference the live packet returned, the tool call itself did not fail, and the
// result the client received identifies that same reference. This is what
// min_expandable_evidence counts against (Codex finding 3's "at least N successful
// source_evidence invocations").
func (c clientEvidenceCall) succeeded() bool {
	return c.ArgumentParsed && c.ArgumentIsLivePacketEvidence && !c.Failed && c.echoedRequestedReference()
}

// mcpSourceEvidenceRequestArg decodes a source_evidence tool invocation's arguments per
// contracts/jsonschema/v1/mcp_source_evidence_request.v1.schema.json ({"evidence_ref_id":...}).
func mcpSourceEvidenceRequestArg(raw []byte) (string, error) {
	var args struct {
		EvidenceRefID string `json:"evidence_ref_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode source_evidence arguments: %w", err)
	}
	if args.EvidenceRefID == "" {
		return "", fmt.Errorf("source_evidence arguments carry no evidence_ref_id")
	}
	return args.EvidenceRefID, nil
}

// decodeSourceEvidenceResult reads a source_evidence result as the client received it.
//
// The sidecar answers every tool call twice -- MCP StructuredContent carrying the JSON
// contract, and a bounded markdown rendering as the text content (internal/mcp/result.go) --
// and OpenCode 1.18.4 forwards only the text content, both to the model and into its own event
// stream. Grading the client's round trips by JSON-decoding part.state.output therefore fails
// on every real run ("invalid character '#'"), which is a property of the client, not of the
// agent under test. Both representations are accepted: JSON when a client forwards structured
// content, the rendering otherwise.
func decodeSourceEvidenceResult(schemas *schemaLoader, resultText string) (doc *contractsv1.ExpandedEvidence, raw []byte, rendered sidecarmd.Evidence, renderedOK bool, err error) {
	if sidecarmd.Looks(resultText) {
		parsed := sidecarmd.Parse(resultText)
		if len(parsed.Evidence) != 1 {
			return nil, nil, sidecarmd.Evidence{}, false,
				fmt.Errorf("source_evidence rendering carries %d evidence sections, want exactly 1", len(parsed.Evidence))
		}
		return nil, nil, parsed.Evidence[0], true, nil
	}
	structured, structuredRaw, jsonErr := mcpSourceEvidenceResult(schemas, resultText)
	if jsonErr != nil {
		return nil, nil, sidecarmd.Evidence{}, false, jsonErr
	}
	return &structured, structuredRaw, sidecarmd.Evidence{}, false, nil
}

// mcpSourceEvidenceResult decodes a source_evidence tool invocation's result text per
// contracts/jsonschema/v1/mcp_source_evidence_response.v1.schema.json
// ({"schema_version":"mcp_source_evidence_response.v1","structured":<expanded_evidence.v1>,
// "rendered_markdown":{...}}). ResultText is deliberately just the raw JSON-encoded string
// OpenCode's event stream carries (see events.go); this is where it finally gets decoded.
//
// The whole response is validated against the contract, not just schema_version and a
// non-empty structured field (Codex round-2 finding 3): mcp_source_evidence_response.v1 also
// requires a shaped, additionalProperties:false rendered_markdown object, which two hand
// checks would not catch a client silently dropping or malforming. The schema embeds
// expanded_evidence.v1/evidence_ref.v1 in $defs (drift-checked byte-for-byte against the
// canonical files by internal/contractcheck), so this one call also fully validates the
// structured document -- L4's own expanded_evidence.v1 check against the same bytes is then a
// second, independent proof, not a redundant one.
func mcpSourceEvidenceResult(schemas *schemaLoader, resultText string) (contractsv1.ExpandedEvidence, []byte, error) {
	raw := []byte(resultText)
	if err := schemas.validateJSON("mcp_source_evidence_response.v1.schema.json", raw); err != nil {
		return contractsv1.ExpandedEvidence{}, nil, fmt.Errorf("source_evidence result does not validate against mcp_source_evidence_response.v1: %w", err)
	}
	var wrapper struct {
		Structured json.RawMessage `json:"structured"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return contractsv1.ExpandedEvidence{}, nil, fmt.Errorf("decode source_evidence result: %w", err)
	}
	var doc contractsv1.ExpandedEvidence
	if err := json.Unmarshal(wrapper.Structured, &doc); err != nil {
		return contractsv1.ExpandedEvidence{}, nil, fmt.Errorf("decode source_evidence structured evidence: %w", err)
	}
	return doc, wrapper.Structured, nil
}

// observedEvidenceRefID renders, for a check's actual-value column, whichever evidence
// reference the client was actually handed back.
func observedEvidenceRefID(c clientEvidenceCall) string {
	switch {
	case c.Doc != nil:
		return c.Doc.Evidence.EvidenceRefID
	case c.RenderedParsed:
		return c.Rendered.EvidenceRefID
	default:
		return ""
	}
}

// collectClientEvidenceCalls turns every source_evidence ToolInvocation into a
// clientEvidenceCall, resolving each argument against rc.packetKnownIDs (the live packet's
// own evidence_ref_ids) and each result against the MCP response contract. It does not
// itself decide pass/fail -- layerMCP records the checks -- it only builds the structured
// record both layerMCP and layerEvidence/layerAgentResult consume.
func collectClientEvidenceCalls(rc *runContext, invocations []ToolInvocation) []clientEvidenceCall {
	var calls []clientEvidenceCall
	for _, inv := range invocations {
		if inv.Name != "source_evidence" {
			continue
		}
		call := clientEvidenceCall{Failed: inv.Failed()}
		if id, err := mcpSourceEvidenceRequestArg(inv.Arguments); err != nil {
			call.ArgumentParsed = false
		} else {
			call.ArgumentParsed = true
			call.ArgumentEvidenceRefID = id
			call.ArgumentIsLivePacketEvidence = rc.packetKnownIDs.has(id)
		}
		if !call.Failed && inv.ResultText != "" {
			doc, raw, rendered, renderedOK, err := decodeSourceEvidenceResult(rc.packetSchemas, inv.ResultText)
			switch {
			case err != nil:
				call.DocError = err
			default:
				call.Doc, call.DocRaw = doc, raw
				call.Rendered, call.RenderedParsed = rendered, renderedOK
			}
		}
		calls = append(calls, call)
	}
	return calls
}
