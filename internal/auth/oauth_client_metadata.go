package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

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

var (
	ErrClientMetadataUnavailable = errors.New("client metadata document unavailable")
	errClientMetadataAddress     = errors.New("client metadata host resolves to a non-public address")
)

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
	dialer := &net.Dialer{Timeout: clientMetadataTimeout, Control: refuseNonPublicAddress}
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
	copied := *client
	copied.Timeout = clientMetadataTimeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPClientMetadataFetcher{client: &copied, now: time.Now, cache: make(map[string]cachedClientMetadata)}
}

// Fetch returns the document a metadata-document client ID names.
func (f *HTTPClientMetadataFetcher) Fetch(ctx context.Context, clientID string) (OAuthClientMetadata, error) {
	if !storage.ValidOAuthClientMetadataURL(clientID) {
		return OAuthClientMetadata{}, ErrClientMetadataUnavailable
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
		return OAuthClientMetadata{}, ErrClientMetadataUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := f.client.Do(request)
	if err != nil {
		return OAuthClientMetadata{}, fmt.Errorf("%w: request failed", ErrClientMetadataUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthClientMetadata{}, fmt.Errorf("%w: status %d", ErrClientMetadataUnavailable, response.StatusCode)
	}
	if mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return OAuthClientMetadata{}, fmt.Errorf("%w: not json", ErrClientMetadataUnavailable)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, clientMetadataMaxBytes+1))
	if err != nil || len(body) > clientMetadataMaxBytes {
		return OAuthClientMetadata{}, fmt.Errorf("%w: body unreadable or too large", ErrClientMetadataUnavailable)
	}
	var metadata OAuthClientMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return OAuthClientMetadata{}, fmt.Errorf("%w: invalid json", ErrClientMetadataUnavailable)
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

func cloneClientMetadata(metadata OAuthClientMetadata) OAuthClientMetadata {
	metadata.RedirectURIs = append([]string(nil), metadata.RedirectURIs...)
	return metadata
}

// refuseNonPublicAddress runs after DNS resolution, on the address actually
// dialled, so a name that resolves to a private address is refused too.
func refuseNonPublicAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errClientMetadataAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !PublicAddress(ip) {
		return errClientMetadataAddress
	}
	return nil
}

// PublicAddress reports whether an address is publicly routable: not
// loopback, private, link-local, multicast, unspecified, shared (100.64/10),
// or an IPv4-mapped form of any of those.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}
