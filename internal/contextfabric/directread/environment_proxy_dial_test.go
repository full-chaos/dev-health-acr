package directread_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheQueryClientsNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		call := directread.QueryCall{OrgID: "org", Document: "query { a }"}
		for _, supplied := range []*http.Client{nil, {}} {
			if client, err := directread.NewHTTPQueryClientWithHTTP(proxycapture.Target, time.Second, supplied); err == nil {
				_, _ = client.Execute(ctx, call)
			}
			if client, err := directread.NewHTTPGraphQLClientWithHTTP(proxycapture.Target, time.Second, supplied); err == nil {
				_, _ = client.Execute(ctx, call)
			}
		}
	}).AssertNothingReached(t)
}
