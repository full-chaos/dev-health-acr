package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// CHAOS-12156: every user of a first-party browser client shares its public
// client_id, so a client-keyed /authorize limit puts every login of every
// org -- and anyone who knows the id -- in one bucket of deviceCreationLimit
// a minute. The browser client's /authorize is limited per source (the
// client address acr resolves) under a per-client ceiling,
// browserAuthorizeClientLimit. A dynamic client keeps its per-client bucket.

func newBrowserAuthorizeLimitApp(t *testing.T) *App {
	t.Helper()
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// authorizeFrom sends one /authorize for clientID from source (an IPv4
// address), as the login named by state. It returns the status and the
// Retry-After header.
func authorizeFrom(app *App, clientID, source, state string) (int, string) {
	query := oauthTestAuthorizeQuery(clientID)
	query.Set("state", state)
	request := httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+query.Encode(), nil)
	request.RemoteAddr = source + ":40000"
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder.Code, recorder.Header().Get("Retry-After")
}

// distinctSource is the i-th of many distinct client addresses.
func distinctSource(i int) string {
	return "198.51." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250+1)
}

func TestOAuthBrowserClientAuthorizeIsLimitedPerSourceNotPerSharedClientID(t *testing.T) {
	app := newBrowserAuthorizeLimitApp(t)
	client := auth.DevBrowserClientID

	// Many users log in within one minute, each from their own address: none
	// is throttled (on the client-keyed baseline, login deviceCreationLimit+1
	// was refused).
	logins := 4 * deviceCreationLimit
	for i := range logins {
		status, retry := authorizeFrom(app, client, distinctSource(i), "user-"+strconv.Itoa(i))
		if status != http.StatusFound {
			t.Fatalf("login %d (user-%d, %s): status %d Retry-After %q, want 302: the browser client's logins share one bucket", i+1, i, distinctSource(i), status, retry)
		}
	}

	// One source that floods /authorize is throttled after deviceCreationLimit,
	// with Retry-After; another source is not.
	const flooder = "203.0.113.66"
	flood := map[int]int{}
	var retryAfter string
	for i := range deviceCreationLimit + 5 {
		status, retry := authorizeFrom(app, client, flooder, "flood-"+strconv.Itoa(i))
		flood[status]++
		if status == http.StatusTooManyRequests {
			retryAfter = retry
		}
	}
	if flood[http.StatusFound] != deviceCreationLimit || flood[http.StatusTooManyRequests] != 5 || retryAfter == "" {
		t.Fatalf("%d authorize calls from one source: statuses %v Retry-After %q, want %dx302 and 5x429 with Retry-After", deviceCreationLimit+5, flood, retryAfter, deviceCreationLimit)
	}
	if status, retry := authorizeFrom(app, client, "203.0.113.67", "innocent"); status != http.StatusFound {
		t.Fatalf("another source after the flood: status %d Retry-After %q, want 302", status, retry)
	}
}

// The browser client's per-client ceiling holds across sources: after
// browserAuthorizeClientLimit allowed requests in a window, a fresh source is
// refused with Retry-After. A refused request does not count toward it.
func TestOAuthBrowserClientAuthorizeHasAPerClientCeiling(t *testing.T) {
	if browserAuthorizeClientLimit <= deviceCreationLimit {
		t.Fatalf("browserAuthorizeClientLimit = %d, want far above the per-source limit %d", browserAuthorizeClientLimit, deviceCreationLimit)
	}
	app := newBrowserAuthorizeLimitApp(t)
	client := auth.DevBrowserClientID

	// Refused requests of one flooding source do not use the ceiling.
	for i := range deviceCreationLimit + 20 {
		authorizeFrom(app, client, "203.0.113.66", "flood-"+strconv.Itoa(i))
	}
	allowed := deviceCreationLimit
	for i := 0; allowed < browserAuthorizeClientLimit; i++ {
		status, retry := authorizeFrom(app, client, distinctSource(i), "user-"+strconv.Itoa(i))
		if status != http.StatusFound {
			t.Fatalf("allowed request %d of %d (source %s): status %d Retry-After %q, want 302", allowed+1, browserAuthorizeClientLimit, distinctSource(i), status, retry)
		}
		allowed++
	}
	status, retry := authorizeFrom(app, client, "192.0.2.200", "over-ceiling")
	if status != http.StatusTooManyRequests || retry == "" {
		t.Fatalf("request %d of the browser client from a fresh source: status %d Retry-After %q, want 429 with Retry-After", browserAuthorizeClientLimit+1, status, retry)
	}
	// The ceiling is the browser client's own: a dynamic client is not refused.
	if status, retry := authorizeFrom(app, "dynamic-client", "192.0.2.201", "dynamic"); status == http.StatusTooManyRequests {
		t.Fatalf("a dynamic client after the browser client's ceiling: status 429 Retry-After %q", retry)
	}
}

// A dynamic client keeps its per-client bucket: its /authorize from many
// sources still shares one bucket of deviceCreationLimit.
func TestOAuthDynamicClientAuthorizeStaysPerClient(t *testing.T) {
	app := newBrowserAuthorizeLimitApp(t)
	statuses := map[int]int{}
	for i := range deviceCreationLimit + 5 {
		status, _ := authorizeFrom(app, "dynamic-client", distinctSource(i), "user-"+strconv.Itoa(i))
		statuses[status]++
	}
	if statuses[http.StatusTooManyRequests] != 5 {
		t.Fatalf("%d authorize calls for one dynamic client from distinct sources: statuses %v, want 5x429", deviceCreationLimit+5, statuses)
	}
}

// Behind the ingress the source is the client address the trusted-proxy walk
// resolves, never the ingress peer: distinct forwarded clients through one
// trusted ingress are distinct sources.
func TestOAuthBrowserClientAuthorizeKeysTheResolvedClientAddress(t *testing.T) {
	app := newBrowserAuthorizeLimitApp(t)
	resolver, err := auth.NewTrustedProxyClientIPResolver([]string{"10.42.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	app.clientIP = resolver
	for i := range 3 * deviceCreationLimit {
		query := oauthTestAuthorizeQuery(auth.DevBrowserClientID)
		query.Set("state", "user-"+strconv.Itoa(i))
		request := httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+url.Values(query).Encode(), nil)
		request.RemoteAddr = "10.42.0.6:40000"
		request.Header.Set("X-Forwarded-For", distinctSource(i))
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusFound {
			t.Fatalf("login %d forwarded for %s through the ingress: status %d Retry-After %q, want 302", i+1, distinctSource(i), recorder.Code, recorder.Header().Get("Retry-After"))
		}
	}
}
