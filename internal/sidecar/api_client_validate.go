package sidecar

import (
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ErrInvalidResponse reports that a hosted API response decoded as valid
// JSON matching its contract shape (see decodeExact) but failed the
// canonical, JSON-Schema-parity-tested semantic validation each contract
// type implements as its own Validate() method in internal/contracts/v1
// (Capabilities.Validate, ContextPacket.Validate,
// ExpandedEvidence.Validate): a required field was empty/zero, an enum
// held an unrecognized value, a numeric bound was violated, or a required
// list field was nil (which marshals to JSON null, rejected by the
// schema's "type": "array"). decodeExact alone only rejects unknown
// fields and trailing JSON; it does not require any particular field to
// be present or non-zero, so a truncated or malformed-but-shape-conforming
// response would otherwise be accepted.
var ErrInvalidResponse = errors.New("acr: hosted API response failed semantic validation")

// validateCapabilities, validateContextPacket, and validateExpandedEvidence
// delegate entirely to the canonical Validate() methods on
// internal/contracts/v1 - the same methods the MCP response validators use
// and that internal/contracts/v1's own parity tests (validate_*_test.go)
// prove reject every JSON-Schema-invalid mutation the golden fixtures'
// schemas reject. The sidecar client must never re-derive its own copy of
// these bounds, enums, or required-field checks: a second implementation
// is exactly what let the earlier local validators drift weaker than the
// schema (accepting an unrecognized service constant, an unsupported
// enabled_tools entry, a missing query_version/ranking_version, malformed
// nested required_checks/freshness/coverage, or an EvidenceRef missing its
// provenance/citation). Every one of those checks now lives in exactly one
// place.
func validateCapabilities(c contractsv1.Capabilities) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%w: capabilities: %w", ErrInvalidResponse, err)
	}
	return nil
}

func validateContextPacket(p contractsv1.ContextPacket) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: context packet: %w", ErrInvalidResponse, err)
	}
	return nil
}

func validateExpandedEvidence(e contractsv1.ExpandedEvidence) error {
	if err := e.Validate(); err != nil {
		return fmt.Errorf("%w: evidence: %w", ErrInvalidResponse, err)
	}
	return nil
}
