package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Client ID metadata documents (draft-ietf-oauth-client-id-metadata-document,
// the MCP 2026-07-28 preferred registration): the client_id is an HTTPS URL
// and the document it serves names the client's redirect URIs. Fetching one
// is an outbound request to a caller-chosen host, so the fetcher refuses every
// address that is not publicly routable, follows no redirects, and bounds the
// time and bytes it spends.

const (
	clientMetadataMaxBytes = 5 * 1024
	clientMetadataTimeout  = 5 * time.Second
	clientMetadataCacheTTL = 5 * time.Minute
	clientMetadataCacheMax = 256
)

var ErrClientMetadataUnavailable = errors.New("client metadata document unavailable")

// ClientMetadataError is a fetch refusal and its class, one of the
// oauthvocab client refusals. It matches ErrClientMetadataUnavailable.
type ClientMetadataError struct {
	Refusal string
}

func (e *ClientMetadataError) Error() string {
	return ErrClientMetadataUnavailable.Error() + ": " + e.Refusal
}

func (e *ClientMetadataError) Unwrap() error { return ErrClientMetadataUnavailable }

func clientMetadataFailure(refusal string) error { return &ClientMetadataError{Refusal: refusal} }

// HTTPClientMetadataFetcher fetches and caches client ID metadata documents.
type HTTPClientMetadataFetcher struct {
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]cachedClientMetadata
}

type cachedClientMetadata struct {
	metadata  OAuthClientMetadata
	expiresAt time.Time
}

// NewPublicClientMetadataFetcher builds the production fetcher: HTTPS only,
// public addresses only, no redirects, no proxy.
func NewPublicClientMetadataFetcher() *HTTPClientMetadataFetcher {
	return NewClientMetadataFetcher(&http.Client{Transport: newPublicTransport()})
}

// newPublicTransport is the production transport: no proxy, and a dial-time
// Control hook that refuses every non-public address after DNS resolution.
func newPublicTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: clientMetadataTimeout, Control: outboundhttp.RefuseNonPublicAddress}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   clientMetadataTimeout,
		ResponseHeaderTimeout: clientMetadataTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}

// NewClientMetadataFetcher builds a fetcher over the given client. Redirects
// are always refused and the overall request time is always bounded.
func NewClientMetadataFetcher(client *http.Client) *HTTPClientMetadataFetcher {
	copied := *outboundhttp.WithTransport(client)
	copied.Timeout = clientMetadataTimeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPClientMetadataFetcher{client: &copied, now: time.Now, cache: make(map[string]cachedClientMetadata)}
}

// Fetch returns the document a metadata-document client ID names.
func (f *HTTPClientMetadataFetcher) Fetch(ctx context.Context, clientID string) (OAuthClientMetadata, error) {
	if !storage.ValidOAuthClientMetadataURL(clientID) {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalUnsupportedClientID)
	}
	now := f.now()
	f.mu.Lock()
	if cached, ok := f.cache[clientID]; ok && now.Before(cached.expiresAt) {
		f.mu.Unlock()
		return cloneClientMetadata(cached.metadata), nil
	}
	f.mu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalFetchFailed)
	}
	request.Header.Set("Accept", "application/json")
	response, err := f.client.Do(request)
	if errors.Is(err, outboundhttp.ErrNonPublicAddress) {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalPrivateAddress)
	}
	if err != nil {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalFetchFailed)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalFetchFailed)
	}
	if mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalInvalidDocument)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, clientMetadataMaxBytes+1))
	if err != nil {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalFetchFailed)
	}
	if len(body) > clientMetadataMaxBytes {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalTooLarge)
	}
	var metadata OAuthClientMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalInvalidDocument)
	}
	if !unambiguousClientMetadataObject(body) {
		return OAuthClientMetadata{}, clientMetadataFailure(oauthvocab.ClientRefusalInvalidDocument)
	}
	f.mu.Lock()
	if len(f.cache) >= clientMetadataCacheMax {
		for key, cached := range f.cache {
			if !now.Before(cached.expiresAt) {
				delete(f.cache, key)
			}
		}
		if len(f.cache) >= clientMetadataCacheMax {
			clear(f.cache)
		}
	}
	f.cache[clientID] = cachedClientMetadata{metadata: cloneClientMetadata(metadata), expiresAt: now.Add(clientMetadataCacheTTL)}
	f.mu.Unlock()
	return metadata, nil
}

// clientMetadataMemberNames are the member names OAuthClientMetadata decodes,
// read from its json tags.
var clientMetadataMemberNames = func() []string {
	kind := reflect.TypeFor[OAuthClientMetadata]()
	names := make([]string, 0, kind.NumField())
	for i := range kind.NumField() {
		names = append(names, kind.Field(i).Tag.Get("json"))
	}
	return names
}()

// unambiguousClientMetadataObject reports whether a body encoding/json has
// already decoded is a JSON object whose top-level members a case-sensitive
// reader and encoding/json (which matches member names case-insensitively,
// the last one winning) read the same way: no two members whose names are
// equal ignoring case, and no member whose name equals a decoded member's name
// only ignoring case. A JSON null, which decodes to the zero document, is not
// an object. The body is valid JSON (Unmarshal accepted the same bytes), so
// the decoder reports no error here; were one to occur, More reports false
// and the scan ends.
func unambiguousClientMetadataObject(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if open, _ := decoder.Token(); open != json.Delim('{') {
		return false
	}
	seen := make([]string, 0, 16)
	for decoder.More() {
		token, _ := decoder.Token()
		name, _ := token.(string)
		for _, other := range seen {
			if strings.EqualFold(other, name) {
				return false
			}
		}
		for _, decoded := range clientMetadataMemberNames {
			if name != decoded && strings.EqualFold(name, decoded) {
				return false
			}
		}
		seen = append(seen, name)
		var value json.RawMessage
		_ = decoder.Decode(&value)
	}
	return true
}

func cloneClientMetadata(metadata OAuthClientMetadata) OAuthClientMetadata {
	metadata.RedirectURIs = append([]string(nil), metadata.RedirectURIs...)
	if metadata.TokenEndpointAuthMethodsSupported != nil {
		metadata.TokenEndpointAuthMethodsSupported = append(make([]string, 0, len(metadata.TokenEndpointAuthMethodsSupported)), metadata.TokenEndpointAuthMethodsSupported...)
	}
	return metadata
}
