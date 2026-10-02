package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// requestJSON is RequestJSON (TARGET-CONTRACT.md section 2): exactly the
// request fields genkitruntime.BuildInterpretationPrompt renders into the
// model-visible payload. The nested types are the production contract types.
type requestJSON struct {
	Question             string                              `json:"question"`
	Conversation         []contextfabric.ConversationTurn    `json:"conversation,omitempty"`
	RequestedScope       contextfabric.RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          contextfabric.TimeContext           `json:"time_context"`
	PriorSubjectReceipts []contextfabric.BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
}

// offlineConsumer and offlineOptions complete the request for
// InvestigationRequest.Validate(). Neither reaches the model payload.
var offlineConsumer = contractsv1.ContextFabricConsumerInfo{Name: "intent-training", Version: "0", Surface: "offline"}

var offlineOptions = contractsv1.ContextFabricInvestigationOptions{
	MaxSubjectCandidates: 10,
	MaxCohortMembers:     50,
	MaxRelationshipPaths: 25,
	MaxDrivers:           10,
	MaxEvidenceRefs:      50,
	MaxSerializedBytes:   65536,
	AllowClarification:   true,
}

type decodedRequest struct {
	Request      contextfabric.InvestigationRequest
	RequestSHA   string
	ValidateErr  error
	CanonicalRaw []byte
}

// decodeRequest strictly decodes RequestJSON: unknown keys, duplicate keys
// and trailing data are errors, because a request field the renderer drops
// would be training input the model never actually sees.
func decodeRequest(raw json.RawMessage) (decodedRequest, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return decodedRequest{}, errors.New(`input requires "request" (object)`)
	}
	tree, err := parseOrdered(trimmed)
	if err != nil {
		return decodedRequest{}, fmt.Errorf("request is not valid JSON: %w", err)
	}
	if dups := tree.duplicatePaths(); len(dups) > 0 {
		return decodedRequest{}, fmt.Errorf("request has duplicate keys: %v", dups)
	}
	var parsed requestJSON
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return decodedRequest{}, fmt.Errorf("request does not match RequestJSON: %w", err)
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return decodedRequest{}, err
	}
	digest := sha256Hex(canonical)
	request := contextfabric.InvestigationRequest{
		SchemaVersion:        contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:            "offline-" + digest[:24],
		Question:             parsed.Question,
		Conversation:         parsed.Conversation,
		PriorSubjectReceipts: parsed.PriorSubjectReceipts,
		RequestedScope:       parsed.RequestedScope,
		TimeContext:          parsed.TimeContext,
		Options:              offlineOptions,
		Consumer:             offlineConsumer,
	}
	return decodedRequest{Request: request, RequestSHA: digest, ValidateErr: request.Validate(), CanonicalRaw: canonical}, nil
}

type renderOutput struct {
	UserPayload   string `json:"user_payload"`
	SHA256        string `json:"sha256"`
	Bytes         int    `json:"bytes"`
	RequestSHA256 string `json:"request_sha256"`
	RequestOK     bool   `json:"request_ok"`
	RequestError  string `json:"request_error,omitempty"`
}

func render(raw json.RawMessage) (renderOutput, error) {
	decoded, err := decodeRequest(raw)
	if err != nil {
		return renderOutput{}, err
	}
	payload, err := genkitruntime.BuildInterpretationPrompt(decoded.Request, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		return renderOutput{}, fmt.Errorf("render payload: %w", err)
	}
	out := renderOutput{
		UserPayload:   payload,
		SHA256:        sha256Hex([]byte(payload)),
		Bytes:         len(payload),
		RequestSHA256: decoded.RequestSHA,
		RequestOK:     decoded.ValidateErr == nil,
	}
	if decoded.ValidateErr != nil {
		out.RequestError = decoded.ValidateErr.Error()
	}
	return out, nil
}
