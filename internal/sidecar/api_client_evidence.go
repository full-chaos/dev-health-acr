package sidecar

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Evidence expands one opaque evidence reference ID. The ID is treated as
// an opaque handle (matching the hosted API's own treatment; see
// internal/api/read_routes.go's handleEvidence) and is always
// url.PathEscape-d into its own single path segment before being appended
// to the fixed evidence path prefix, so it can never introduce extra path
// segments, a query string, or a URL fragment.
func (c *Client) Evidence(ctx context.Context, evidenceRefID string) (contractsv1.ExpandedEvidence, error) {
	trimmed := strings.TrimSpace(evidenceRefID)
	if trimmed == "" {
		return contractsv1.ExpandedEvidence{}, errEmptyEvidenceReferenceID
	}
	subPath := evidencePathPrefix + url.PathEscape(trimmed)

	var expanded contractsv1.ExpandedEvidence
	if err := c.call(ctx, http.MethodGet, subPath, nil, &expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if err := validateExpandedEvidence(expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	return expanded, nil
}
