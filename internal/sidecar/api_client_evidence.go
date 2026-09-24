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
	return c.EvidenceInResult(ctx, evidenceRefID, "")
}

// EvidenceInResult is Evidence scoped to the stored investigation result
// named by resultID (CHAOS-6563): a Context Fabric ref is keyed by its subject
// and many results cite it, so the result_id of the answer being verified
// selects which result's citation is expanded. An empty resultID is Evidence.
func (c *Client) EvidenceInResult(ctx context.Context, evidenceRefID, resultID string) (contractsv1.ExpandedEvidence, error) {
	trimmed := strings.TrimSpace(evidenceRefID)
	if trimmed == "" {
		return contractsv1.ExpandedEvidence{}, errEmptyEvidenceReferenceID
	}
	subPath := evidencePathPrefix + url.PathEscape(trimmed)
	if scoped := strings.TrimSpace(resultID); scoped != "" {
		subPath += "?result_id=" + url.QueryEscape(scoped)
	}

	var expanded contractsv1.ExpandedEvidence
	if err := c.call(ctx, http.MethodGet, subPath, nil, &expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	if err := validateExpandedEvidence(expanded); err != nil {
		return contractsv1.ExpandedEvidence{}, err
	}
	return expanded, nil
}
