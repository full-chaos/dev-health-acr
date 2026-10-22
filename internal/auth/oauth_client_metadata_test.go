package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
)

// metadataServer serves a client ID metadata document over TLS and counts
// every request it receives.
type metadataServer struct {
	*httptest.Server
	hits atomic.Int64
	pool *x509.CertPool
}

func newMetadataServer(t *testing.T, handler http.HandlerFunc) *metadataServer {
	t.Helper()
	s := &metadataServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	s.pool = x509.NewCertPool()
	s.pool.AddCert(s.Certificate())
	return s
}

func (s *metadataServer) transport() *http.Transport {
	transport := newPublicTransport()
	transport.TLSClientConfig = &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12}
	return transport
}

// unguardedFetcher trusts the test server and dials it directly.
func (s *metadataServer) unguardedFetcher() *HTTPClientMetadataFetcher {
	transport := s.transport()
	transport.DialContext = (&net.Dialer{Timeout: time.Second}).DialContext
	return NewClientMetadataFetcher(&http.Client{Transport: transport})
}

func jsonDocument(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func documentFor(url string) string {
	return fmt.Sprintf(`{"client_id":%q,"client_name":"Test","redirect_uris":["http://127.0.0.1:33418/cb"],"token_endpoint_auth_method":"none","token_endpoint_auth_methods_supported":["none"]}`, url)
}

func TestClientMetadataFetchDialGuard(t *testing.T) {
	server := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonDocument(w, documentFor("https://"+r.Host+r.URL.Path))
	})
	documentURL := server.URL + "/client.json"

	// Control: the same transport with the dial-time hook replaced by a plain
	// dialer fetches the loopback server, so the refusal below is the hook's.
	got, err := server.unguardedFetcher().Fetch(context.Background(), documentURL)
	if err != nil || got.ClientID != documentURL || len(got.RedirectURIs) != 1 {
		t.Fatalf("control fetch = %+v %v", got, err)
	}
	if server.hits.Load() != 1 {
		t.Fatalf("control hits = %d, want 1", server.hits.Load())
	}

	// The production dial guard refuses the same loopback server by address...
	server.hits.Store(0)
	guarded := NewClientMetadataFetcher(&http.Client{Transport: server.transport()})
	if _, err := guarded.Fetch(context.Background(), documentURL); !errors.Is(err, ErrClientMetadataUnavailable) || fetchRefusal(err) != oauthvocab.ClientRefusalPrivateAddress {
		t.Fatalf("loopback fetch through the guarded transport: err = %v, want the private_address refusal", err)
	}
	// ...and by name: localhost resolves to loopback after DNS.
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if _, err := guarded.Fetch(context.Background(), fmt.Sprintf("https://localhost:%d/client.json", port)); !errors.Is(err, ErrClientMetadataUnavailable) || fetchRefusal(err) != oauthvocab.ClientRefusalPrivateAddress {
		t.Fatalf("localhost fetch through the guarded transport: err = %v", err)
	}
	if server.hits.Load() != 0 {
		t.Fatalf("the guarded transport reached the server %d times, want 0", server.hits.Load())
	}
}

func TestPublicTransportUsesNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	if transport := newPublicTransport(); transport.Proxy != nil {
		t.Fatal("the metadata-document transport must not honour proxy settings")
	}
}

// fetchRefusal is the client refusal class a Fetch error carries; "" for
// no error.
func fetchRefusal(err error) string {
	if err == nil {
		return ""
	}
	var failure *ClientMetadataError
	if errors.As(err, &failure) {
		return failure.Refusal
	}
	return "untyped:" + err.Error()
}

func TestClientMetadataFetchResponses(t *testing.T) {
	padded := func(size int) string {
		return `{"client_id":"https://placeholder","redirect_uris":["https://c.example.test/cb"]}` + strings.Repeat(" ", size)
	}
	for _, tc := range []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		want    string
	}{
		{"json", func(w http.ResponseWriter, r *http.Request) {
			jsonDocument(w, documentFor("https://"+r.Host+r.URL.Path))
		}, ""},
		{"json with charset", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(documentFor("https://" + r.Host + r.URL.Path)))
		}, ""},
		{"exactly 5 KiB", func(w http.ResponseWriter, _ *http.Request) {
			body := padded(0)
			jsonDocument(w, body+strings.Repeat(" ", clientMetadataMaxBytes-len(body)))
		}, ""},
		{"5 KiB plus one byte", func(w http.ResponseWriter, _ *http.Request) {
			body := padded(0)
			jsonDocument(w, body+strings.Repeat(" ", clientMetadataMaxBytes-len(body)+1))
		}, oauthvocab.ClientRefusalTooLarge},
		{"large body", func(w http.ResponseWriter, _ *http.Request) { jsonDocument(w, padded(1<<20)) }, oauthvocab.ClientRefusalTooLarge},
		{"html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(padded(0)))
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"text plain", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(padded(0)))
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"json subtype", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/jwk-set+json")
			_, _ = w.Write([]byte(padded(0)))
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"no content type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = w.Write([]byte(padded(0)))
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"404 with a valid document", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(documentFor("https://" + r.Host + r.URL.Path)))
		}, oauthvocab.ClientRefusalFetchFailed},
		{"201 with a valid document", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(documentFor("https://" + r.Host + r.URL.Path)))
		}, oauthvocab.ClientRefusalFetchFailed},
		{"500 with a valid document", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(documentFor("https://" + r.Host + r.URL.Path)))
		}, oauthvocab.ClientRefusalFetchFailed},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { jsonDocument(w, "<html>") }, oauthvocab.ClientRefusalInvalidDocument},
		{"duplicate member", func(w http.ResponseWriter, r *http.Request) {
			jsonDocument(w, strings.TrimSuffix(documentFor("https://"+r.Host+r.URL.Path), "}")+`,"redirect_uris":["https://evil.example.test/cb"]}`)
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"case-variant known member", func(w http.ResponseWriter, r *http.Request) {
			jsonDocument(w, `{"Client_Id":"https://`+r.Host+r.URL.Path+`","redirect_uris":["http://127.0.0.1:33418/cb"]}`)
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"case-variant unknown members", func(w http.ResponseWriter, r *http.Request) {
			jsonDocument(w, strings.TrimSuffix(documentFor("https://"+r.Host+r.URL.Path), "}")+`,"logo_uri":"a","Logo_URI":"b"}`)
		}, oauthvocab.ClientRefusalInvalidDocument},
		{"distinct extension members", func(w http.ResponseWriter, r *http.Request) {
			jsonDocument(w, strings.TrimSuffix(documentFor("https://"+r.Host+r.URL.Path), "}")+`,"logo_uri":"a","jwks_uri":"b","nested":{"x":1,"x":2}}`)
		}, ""},
		{"json array", func(w http.ResponseWriter, _ *http.Request) { jsonDocument(w, `[]`) }, oauthvocab.ClientRefusalInvalidDocument},
		{"json null", func(w http.ResponseWriter, _ *http.Request) { jsonDocument(w, `null`) }, oauthvocab.ClientRefusalInvalidDocument},
		{"not found", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, oauthvocab.ClientRefusalFetchFailed},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", http.StatusInternalServerError) }, oauthvocab.ClientRefusalFetchFailed},
		{"no content", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, oauthvocab.ClientRefusalFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newMetadataServer(t, tc.handler)
			_, err := server.unguardedFetcher().Fetch(context.Background(), server.URL+"/client.json")
			if got := fetchRefusal(err); got != tc.want || (err != nil && !errors.Is(err, ErrClientMetadataUnavailable)) {
				t.Fatalf("err = %v (refusal %q), want refusal %q", err, got, tc.want)
			}
		})
	}
}

func TestClientMetadataFetchFollowsNoRedirect(t *testing.T) {
	target := newMetadataServer(t, func(w http.ResponseWriter, _ *http.Request) { jsonDocument(w, documentFor("https://target")) })
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			target.hits.Store(0)
			origin := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/client.json", status)
			})
			fetcher := origin.unguardedFetcher()
			// The redirect target is trusted and reachable, so only the
			// refusal to follow keeps its hit count at zero.
			if _, err := fetcher.Fetch(context.Background(), origin.URL+"/client.json"); !errors.Is(err, ErrClientMetadataUnavailable) || fetchRefusal(err) != oauthvocab.ClientRefusalFetchFailed {
				t.Fatalf("redirected fetch: err = %v, want ErrClientMetadataUnavailable", err)
			}
			if target.hits.Load() != 0 {
				t.Fatalf("redirect target hit %d times, want 0", target.hits.Load())
			}
		})
	}
}

// TestClientMetadataFetchIsBoundedInBytesRead: a server that never stops
// writing is cut off after the limit, not read to the end.
func TestClientMetadataFetchIsBoundedInBytesRead(t *testing.T) {
	const total = 64 << 20
	var completed atomic.Bool
	server := newMetadataServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := []byte(strings.Repeat(" ", 32<<10))
		for written := 0; written < total; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		completed.Store(true)
	})
	if _, err := server.unguardedFetcher().Fetch(context.Background(), server.URL+"/client.json"); !errors.Is(err, ErrClientMetadataUnavailable) || fetchRefusal(err) != oauthvocab.ClientRefusalTooLarge {
		t.Fatalf("err = %v, want the too_large refusal", err)
	}
	// Give the handler time to observe the closed connection.
	time.Sleep(200 * time.Millisecond)
	if completed.Load() {
		t.Fatalf("the fetcher read all %d bytes the server offered", total)
	}
}

func TestClientMetadataFetchInvalidURLsAreNeverRequested(t *testing.T) {
	server := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) { jsonDocument(w, documentFor("x")) })
	fetcher := server.unguardedFetcher()
	for _, id := range []string{"", "http://" + server.Listener.Addr().String() + "/c.json", server.URL, server.URL + "/", server.URL + "/a/../c.json", server.URL + "/c.json#f"} {
		if _, err := fetcher.Fetch(context.Background(), id); !errors.Is(err, ErrClientMetadataUnavailable) {
			t.Errorf("Fetch(%q) err = %v, want ErrClientMetadataUnavailable", id, err)
		}
	}
	if server.hits.Load() != 0 {
		t.Fatalf("an invalid client id reached the network %d times", server.hits.Load())
	}
}

func TestClientMetadataFetchCache(t *testing.T) {
	server := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonDocument(w, documentFor("https://"+r.Host+r.URL.Path))
	})
	fetcher := server.unguardedFetcher()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fetcher.now = func() time.Time { return now }
	url := server.URL + "/client.json"
	fetch := func() OAuthClientMetadata {
		t.Helper()
		metadata, err := fetcher.Fetch(context.Background(), url)
		if err != nil {
			t.Fatal(err)
		}
		return metadata
	}
	first := fetch()
	first.RedirectURIs[0] = "https://mutated.example.test/cb" // a caller cannot poison the cache
	first.TokenEndpointAuthMethodsSupported[0] = "mutated"
	second := fetch()
	if server.hits.Load() != 1 || second.RedirectURIs[0] == "https://mutated.example.test/cb" || second.TokenEndpointAuthMethodsSupported[0] != "none" {
		t.Fatalf("second fetch: hits %d redirects %v methods %v, want one cached, unmodified fetch", server.hits.Load(), second.RedirectURIs, second.TokenEndpointAuthMethodsSupported)
	}
	second.RedirectURIs[0] = "https://mutated.example.test/cb" // nor can a caller served from the cache
	second.TokenEndpointAuthMethodsSupported[0] = "mutated"
	if third := fetch(); third.RedirectURIs[0] == "https://mutated.example.test/cb" || third.TokenEndpointAuthMethodsSupported[0] != "none" {
		t.Fatalf("a cached result aliases the cache: third fetch redirects %v methods %v", third.RedirectURIs, third.TokenEndpointAuthMethodsSupported)
	}
	now = now.Add(clientMetadataCacheTTL - time.Second)
	fetch()
	if server.hits.Load() != 1 {
		t.Fatalf("hits = %d just inside the 5-minute TTL, want 1", server.hits.Load())
	}
	now = now.Add(time.Second)
	fetch()
	if server.hits.Load() != 2 {
		t.Fatalf("hits = %d at the TTL, want a refetch (2)", server.hits.Load())
	}

	// A failed fetch is not cached: the next call asks again.
	failing := newMetadataServer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", http.StatusBadGateway) })
	failFetcher := failing.unguardedFetcher()
	for range 2 {
		if _, err := failFetcher.Fetch(context.Background(), failing.URL+"/c.json"); err == nil {
			t.Fatal("expected a failed fetch")
		}
	}
	if failing.hits.Load() != 2 {
		t.Fatalf("failed fetch hits = %d, want 2 (failures are not cached)", failing.hits.Load())
	}
}

func TestClientMetadataCacheIsBounded(t *testing.T) {
	server := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonDocument(w, documentFor("https://"+r.Host+r.URL.Path))
	})
	fetcher := server.unguardedFetcher()
	fetcher.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
	for i := range clientMetadataCacheMax + 50 {
		if _, err := fetcher.Fetch(context.Background(), fmt.Sprintf("%s/c%d.json", server.URL, i)); err != nil {
			t.Fatal(err)
		}
		if len(fetcher.cache) > clientMetadataCacheMax {
			t.Fatalf("cache holds %d entries after %d fetches, want at most %d", len(fetcher.cache), i+1, clientMetadataCacheMax)
		}
	}
}

func TestClientMetadataFetcherBoundsTimeAndRedirects(t *testing.T) {
	fetcher := NewClientMetadataFetcher(&http.Client{Timeout: time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }})
	if fetcher.client.Timeout != clientMetadataTimeout || clientMetadataTimeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", fetcher.client.Timeout)
	}
	if fetcher.client.CheckRedirect == nil || fetcher.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects must never be followed")
	}
	if clientMetadataMaxBytes != 5*1024 || clientMetadataCacheTTL != 5*time.Minute {
		t.Fatalf("limits drifted: %d bytes, %v", clientMetadataMaxBytes, clientMetadataCacheTTL)
	}
}

func TestClientMetadataFetchHonoursContext(t *testing.T) {
	release := make(chan struct{})
	server := newMetadataServer(t, func(w http.ResponseWriter, _ *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := server.unguardedFetcher().Fetch(ctx, server.URL+"/client.json"); !errors.Is(err, ErrClientMetadataUnavailable) || fetchRefusal(err) != oauthvocab.ClientRefusalFetchFailed {
		t.Fatalf("err = %v, want the fetch_failed refusal", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("a cancelled fetch took %v", time.Since(started))
	}
}

// TestClientMetadataMemberNamesAreTheDecodedFields pins the names the
// ambiguity scan protects to the fields OAuthClientMetadata decodes.
func TestClientMetadataMemberNamesAreTheDecodedFields(t *testing.T) {
	want := []string{"client_id", "client_name", "redirect_uris", "token_endpoint_auth_method", "token_endpoint_auth_methods_supported"}
	if !slices.Equal(clientMetadataMemberNames, want) {
		t.Fatalf("member names = %v, want %v", clientMetadataMemberNames, want)
	}
}

// TestClientMetadataCacheKeepsAnEmptyMethodList: an empty
// token_endpoint_auth_methods_supported list is not the same as no list, on
// the first read and on a cached one.
func TestClientMetadataCacheKeepsAnEmptyMethodList(t *testing.T) {
	server := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonDocument(w, `{"client_id":"https://`+r.Host+r.URL.Path+`","redirect_uris":["http://127.0.0.1:33418/cb"],"token_endpoint_auth_methods_supported":[]}`)
	})
	fetcher := server.unguardedFetcher()
	for read := range 2 {
		metadata, err := fetcher.Fetch(context.Background(), server.URL+"/client.json")
		if err != nil || metadata.TokenEndpointAuthMethodsSupported == nil || len(metadata.TokenEndpointAuthMethodsSupported) != 0 {
			t.Fatalf("read %d: methods %#v err %v, want an empty, present list", read, metadata.TokenEndpointAuthMethodsSupported, err)
		}
	}
	if server.hits.Load() != 1 {
		t.Fatalf("hits = %d, want the second read from the cache", server.hits.Load())
	}
	absent := newMetadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		jsonDocument(w, `{"client_id":"https://`+r.Host+r.URL.Path+`","redirect_uris":["http://127.0.0.1:33418/cb"]}`)
	})
	absentFetcher := absent.unguardedFetcher()
	for read := range 2 {
		metadata, err := absentFetcher.Fetch(context.Background(), absent.URL+"/client.json")
		if err != nil || metadata.TokenEndpointAuthMethodsSupported != nil {
			t.Fatalf("read %d of a document without the list: methods %#v err %v, want no list", read, metadata.TokenEndpointAuthMethodsSupported, err)
		}
	}
}
