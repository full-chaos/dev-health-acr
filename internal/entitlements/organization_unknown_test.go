package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const unknownOrg = "aaaaaaaa-0000-4000-8000-000000000001"

const unknownBody = `{"detail":"Not found","code":"acr_organization_unknown"}`

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func serveClient(t *testing.T, handler http.Handler, overrides entitlements.Config) *entitlements.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, overrides))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestOrganizationUnknownIsTrueOnlyForTheCodedNotFound(t *testing.T) {
	cases := map[string]struct {
		handler     http.HandlerFunc
		wantUnknown bool
		wantErr     bool
	}{
		"404 with the code":                   {respond(404, unknownBody), true, false},
		"404 with the code and extra fields":  {respond(404, `{"detail":"x","code":"acr_organization_unknown","more":1}`), true, false},
		"200 valid body":                      {respond(200, `{}`), false, false},
		"200 malformed body":                  {respond(200, `not json`), false, false},
		"200 empty body":                      {respond(200, ``), false, false},
		"404 without the code":                {respond(404, `{"detail":"Not found"}`), false, true},
		"404 empty body":                      {respond(404, ``), false, true},
		"404 html from a proxy":               {respond(404, `<html>not found</html>`), false, true},
		"404 other code":                      {respond(404, `{"code":"something_else"}`), false, true},
		"404 code not a string":               {respond(404, `{"code":1}`), false, true},
		"404 with the code past the body cap": {respond(404, `{"detail":"`+strings.Repeat("a", 4096)+`","code":"acr_organization_unknown"}`), false, true},
		"500 carrying the code":               {respond(500, unknownBody), false, true},
		"503 carrying the code":               {respond(503, unknownBody), false, true},
		"401 carrying the code":               {respond(401, unknownBody), false, true},
		"403 carrying the code":               {respond(403, unknownBody), false, true},
		"410 carrying the code":               {respond(410, unknownBody), false, true},
		"204":                                 {respond(204, ``), false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := serveClient(t, tc.handler, entitlements.Config{})
			unknown, err := client.OrganizationUnknown(context.Background(), unknownOrg)
			if unknown != tc.wantUnknown || (err != nil) != tc.wantErr {
				t.Fatalf("OrganizationUnknown() = (%v, %v), want unknown=%v err=%v", unknown, err, tc.wantUnknown, tc.wantErr)
			}
			if unknown && err != nil {
				t.Fatal("unknown must come with a nil error")
			}
		})
	}
}

func TestOrganizationUnknownIsUnavailableForATransportFailure(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		server := httptest.NewTLSServer(http.NotFoundHandler())
		client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		server.Close()
		unknown, err := client.OrganizationUnknown(context.Background(), unknownOrg)
		if unknown || err == nil {
			t.Fatalf("OrganizationUnknown() = (%v, %v), want (false, error)", unknown, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		done := make(chan struct{})
		t.Cleanup(func() { close(done) })
		client := serveClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			respond(404, unknownBody)(w, nil)
		}), entitlements.Config{Timeout: time.Second})
		unknown, err := client.OrganizationUnknown(context.Background(), unknownOrg)
		if unknown || err == nil {
			t.Fatalf("OrganizationUnknown() = (%v, %v), want (false, error)", unknown, err)
		}
	})
}

func TestOrganizationUnknownRejectsANonCanonicalIDBeforeAnyRequest(t *testing.T) {
	var calls atomic.Int32
	client := serveClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respond(404, unknownBody)(w, r)
	}), entitlements.Config{})
	for _, org := range []string{"", " ", "acme", "not-a-uuid", strings.ToUpper(unknownOrg), "{" + unknownOrg + "}", unknownOrg + " ", "urn:uuid:" + unknownOrg, "../" + unknownOrg} {
		unknown, err := client.OrganizationUnknown(context.Background(), org)
		if unknown || err == nil {
			t.Fatalf("OrganizationUnknown(%q) = (%v, %v), want (false, error)", org, unknown, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("server saw %d requests for ids that are not canonical UUIDs", calls.Load())
	}
}

func TestHasEntitlementReportsAnUnknownOrganizationAsNotEntitledAndUncached(t *testing.T) {
	var calls atomic.Int32
	client := serveClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respond(404, unknownBody)(w, r)
	}), entitlements.Config{NegativeCacheTTL: time.Minute})
	for i := 0; i < 2; i++ {
		entitled, err := client.HasEntitlement(context.Background(), unknownOrg, "agent_context_runtime")
		if entitled || !errors.Is(err, entitlements.ErrOrganizationUnknown) {
			t.Fatalf("HasEntitlement() = (%v, %v), want (false, ErrOrganizationUnknown)", entitled, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2: an unknown answer must not be cached", calls.Load())
	}
}

func TestHasEntitlementTreatsAnUncodedNotFoundAsUnavailable(t *testing.T) {
	client := serveClient(t, respond(404, `{"detail":"Not found"}`), entitlements.Config{})
	entitled, err := client.HasEntitlement(context.Background(), unknownOrg, "agent_context_runtime")
	if entitled || err == nil || errors.Is(err, entitlements.ErrOrganizationUnknown) {
		t.Fatalf("HasEntitlement() = (%v, %v), want (false, unavailable that is not ErrOrganizationUnknown)", entitled, err)
	}
}
