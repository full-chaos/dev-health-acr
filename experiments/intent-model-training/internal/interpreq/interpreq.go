// Package interpreq is the experiment's one RequestJSON decoder, shared by
// gocapture now and by gohelper after the review session. It reproduces
// gohelper/request.go exactly: the same strict decode, the same
// request_sha256 (sha256 of json.Marshal of the typed struct) and the same
// offline completion fields, so input_sha256 equals the helper render sha.
package interpreq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type requestJSON struct {
	Question             string                              `json:"question"`
	Conversation         []contextfabric.ConversationTurn    `json:"conversation,omitempty"`
	RequestedScope       contextfabric.RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          contextfabric.TimeContext           `json:"time_context"`
	PriorSubjectReceipts []contextfabric.BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
}

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

// Decoded is a strictly decoded RequestJSON.
type Decoded struct {
	Request       contextfabric.InvestigationRequest
	RequestSHA256 string
	ValidateErr   error
}

// Decode strictly decodes RequestJSON: unknown keys, duplicate keys and
// trailing data are errors.
func Decode(raw []byte) (Decoded, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Decoded{}, errors.New(`input requires "request" (object)`)
	}
	tree, err := parseOrdered(trimmed)
	if err != nil {
		return Decoded{}, fmt.Errorf("request is not valid JSON: %w", err)
	}
	if dups := tree.duplicatePaths(); len(dups) > 0 {
		return Decoded{}, fmt.Errorf("request has duplicate keys: %v", dups)
	}
	var parsed requestJSON
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return Decoded{}, fmt.Errorf("request does not match RequestJSON: %w", err)
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return Decoded{}, err
	}
	digest := SHA256Hex(canonical)
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
	return Decoded{Request: request, RequestSHA256: digest, ValidateErr: request.Validate()}, nil
}

// Rendered is the production user payload for a request.
type Rendered struct {
	Payload       string
	InputSHA256   string
	RequestSHA256 string
	Decoded       Decoded
}

// Render decodes raw and renders the production interpretation payload.
func Render(raw []byte) (Rendered, error) {
	decoded, err := Decode(raw)
	if err != nil {
		return Rendered{}, err
	}
	payload, err := genkitruntime.BuildInterpretationPrompt(decoded.Request, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		return Rendered{}, fmt.Errorf("render payload: %w", err)
	}
	return Rendered{Payload: payload, InputSHA256: SHA256Hex([]byte(payload)), RequestSHA256: decoded.RequestSHA256, Decoded: decoded}, nil
}

// SHA256Hex is the lowercase hex sha256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
