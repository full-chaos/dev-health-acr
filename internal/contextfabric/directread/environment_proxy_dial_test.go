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
			query, err := directread.NewHTTPQueryClientWithHTTP(proxycapture.Target, time.Second, supplied)
			if err != nil {
				panic(err)
			}
			_, _ = query.Execute(ctx, call)
			graphql, err := directread.NewHTTPGraphQLClientWithHTTP(proxycapture.Target, time.Second, supplied)
			if err != nil {
				panic(err)
			}
			_, _ = graphql.Execute(ctx, call)
		}
	}).AssertNothingReached(t)
}
