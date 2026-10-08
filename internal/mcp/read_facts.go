package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// read_facts bounds, mirrored from the hosted contract (the hosted route is
// authoritative and re-validates every request).
const (
	readFactsMaxKinds       = 8
	readFactsMaxSubjects    = 25
	readFactsMinMaxBytes    = 4096
	readFactsMaxMaxBytes    = 262144
	readFactsMaxRangeDays   = 60
	readFactsMaxFieldLength = 256
)

type readFactsSubjectInput struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
}

type readFactsWindowInput struct {
	Mode  string     `json:"mode"`
	AsOf  *time.Time `json:"as_of"`
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
	Days  int        `json:"days"`
}

type readFactsInput struct {
	Kinds    []string                `json:"kinds"`
	Subjects []readFactsSubjectInput `json:"subjects"`
	Window   *readFactsWindowInput   `json:"window"`
	Tables   string                  `json:"tables"`
	MaxBytes int                     `json:"max_bytes"`
}

func (in readFactsInput) validate() error {
	if len(in.Kinds) < 1 || len(in.Kinds) > readFactsMaxKinds {
		return fmt.Errorf("kinds must hold 1 to %d items", readFactsMaxKinds)
	}
	for _, kind := range in.Kinds {
		if kind == "" || len(kind) > readFactsMaxFieldLength {
			return fmt.Errorf("kinds must be non-empty strings")
		}
	}
	if len(in.Subjects) < 1 || len(in.Subjects) > readFactsMaxSubjects {
		return fmt.Errorf("subjects must hold 1 to %d items", readFactsMaxSubjects)
	}
	for _, subject := range in.Subjects {
		if subject.Kind == "" || subject.CanonicalID == "" || len(subject.Kind) > readFactsMaxFieldLength || len(subject.CanonicalID) > readFactsMaxFieldLength {
			return fmt.Errorf("each subject needs a kind and a canonical_id")
		}
	}
	switch in.Tables {
	case "", "include", "omit", "only":
	default:
		return fmt.Errorf("tables must be include, omit or only")
	}
	if in.MaxBytes != 0 && (in.MaxBytes < readFactsMinMaxBytes || in.MaxBytes > readFactsMaxMaxBytes) {
		return fmt.Errorf("max_bytes is out of bounds")
	}
	if w := in.Window; w != nil {
		switch w.Mode {
		case "current", "as_of", "range", "trailing":
		default:
			return fmt.Errorf("window.mode must be current, as_of, range or trailing")
		}
		if w.Days < 0 || w.Days > readFactsMaxRangeDays {
			return fmt.Errorf("window.days is out of bounds")
		}
	}
	return nil
}

// handleReadFacts implements the read_facts tool (CHAOS-7073): validate the
// request shape, forward it unchanged to the hosted direct data route with
// the caller's own credential, and return the hosted response document as
// the tool result. No model runs on this path and nothing is narrowed or
// re-summarised here: the hosted route owns subject authorization, and the
// response is already bounded by max_bytes.
func handleReadFacts(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolReadFacts), nil
	}

	raw := rawArgs(req)
	var input readFactsInput
	if err := decodeToolArguments(raw, &input, false); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: toolArgumentsMessage(toolReadFacts, err)}), nil
	}
	if err := input.validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "read_facts arguments failed schema validation: " + err.Error()}), nil
	}

	response, err := caller.client.ReadDirectFacts(ctx, json.RawMessage(raw))
	if err != nil {
		return toolErrorResult(err), nil
	}
	var envelope struct {
		Tool            string `json:"tool"`
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil || envelope.Tool != toolReadFacts || envelope.ContractVersion == "" {
		return toolErrorResult(&classifiedError{category: "internal", message: "the hosted read_facts response was not the expected document"}), nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, response); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the hosted read_facts response was not valid JSON"}), nil
	}
	return buildToolResult(json.RawMessage(compact.Bytes()), compact.String())
}
