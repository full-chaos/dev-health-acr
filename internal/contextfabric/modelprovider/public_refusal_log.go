package modelprovider

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

const (
	refusalOrgPrefixRunes = 8
	refusalReason         = "non_public_address"
)

// refusalLogging names a refused public-only dial in one Warn line. The model client turns the dial error into its
// own sanitized transport error, which no longer carries outboundhttp.ErrNonPublicAddress, so this wrapper (below the
// client, over the shared public transport) is the last place a refusal can be told from a network outage. The line
// holds the reason and, when known, an eight-character organization prefix: never the host, address or url.
type refusalLogging struct {
	next   http.RoundTripper
	logger *slog.Logger
	orgID  string
}

func (t refusalLogging) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil && errors.Is(err, outboundhttp.ErrNonPublicAddress) {
		t.warn(request)
	}
	return response, err
}

func (t refusalLogging) warn(request *http.Request) {
	logger := t.logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{"reason", refusalReason}
	if prefix := orgPrefix(t.orgID); prefix != "" {
		attrs = append(attrs, "org_id", contextfabric.SanitizeLogAttr(prefix))
	}
	logger.WarnContext(request.Context(), "context fabric BYO model endpoint refused: non-public address", attrs...)
}

func orgPrefix(orgID string) string {
	runes := []rune(orgID)
	if len(runes) > refusalOrgPrefixRunes {
		runes = runes[:refusalOrgPrefixRunes]
	}
	return string(runes)
}
