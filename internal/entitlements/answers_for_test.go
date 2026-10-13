package entitlements_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestAnswersForIsTrueOnlyForHTTP200WhateverTheBody(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		want    bool
	}{
		"200 with a valid body":     {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }, true},
		"200 with a malformed body": {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) }, true},
		"200 with an empty body":    {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, true},
		"404":                       {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, false},
		"500":                       {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, false},
		"204 is not a 200":          {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(tc.handler)
			t.Cleanup(server.Close)
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if got := client.AnswersFor(context.Background(), "11111111-2222-3333-4444-555555555555"); got != tc.want {
				t.Fatalf("AnswersFor() = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		server := httptest.NewTLSServer(http.NotFoundHandler())
		client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		server.Close()
		if client.AnswersFor(context.Background(), "11111111-2222-3333-4444-555555555555") {
			t.Fatal("AnswersFor() = true for an unreachable service")
		}
	})
}
