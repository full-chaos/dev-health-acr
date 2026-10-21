package outboundhttp

import (
	"context"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func sendCanary(ctx context.Context, client *http.Client) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, proxycapture.Target, nil)
	if err != nil {
		panic(err)
	}
	request.Header.Set("Authorization", "Bearer "+proxycapture.Canary)
	if response, err := client.Do(request); err == nil {
		_ = response.Body.Close()
	}
}

func TestTransportAndSharedTransportNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		sendCanary(ctx, &http.Client{Transport: Transport()})
		sendCanary(ctx, &http.Client{Transport: SharedTransport()})
	}).AssertNothingReached(t)
}

func TestSharedTransportIsOnePool(t *testing.T) {
	if SharedTransport() != SharedTransport() {
		t.Fatal("SharedTransport returned two transports: every caller would get a pool of its own")
	}
	if Transport() == Transport() {
		t.Fatal("Transport returned the same transport twice: a caller that changes a field would change every client")
	}
	if SharedTransport().Proxy != nil || Transport().Proxy != nil {
		t.Fatal("a transport of this package has a Proxy function")
	}
}

type replacedDefaultTransport struct{}

func (replacedDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

// A dependency may replace http.DefaultTransport with another RoundTripper; Transport must still build a no-proxy
// transport instead of panicking on the type assertion.
func TestTransportSurvivesAReplacedDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = replacedDefaultTransport{}
	transport := Transport()
	if transport == nil || transport.Proxy != nil || transport.DialContext == nil || transport.MaxIdleConns != 100 {
		t.Fatalf("Transport() over a replaced DefaultTransport = %+v, want net/http's default settings with no proxy", transport)
	}
}

func TestWithTransportGivesASuppliedClientWithoutATransportNoEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) { sendCanary(ctx, WithTransport(&http.Client{})) }).AssertNothingReached(t)
	supplied := &http.Client{}
	if WithTransport(supplied).Transport == nil || supplied.Transport != nil {
		t.Fatal("WithTransport must give its copy a transport and leave the supplied client unchanged")
	}
	own := &http.Client{Transport: &http.Transport{}}
	if WithTransport(own) != own || WithTransport(nil) != nil {
		t.Fatal("WithTransport must return a client that has a transport, and nil, unchanged")
	}
}
