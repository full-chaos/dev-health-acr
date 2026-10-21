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
			_, err = query.Execute(ctx, call)
			proxycapture.MustFailWith(err, "query service call failed: transport")
			graphql, err := directread.NewHTTPGraphQLClientWithHTTP(proxycapture.Target, time.Second, supplied)
			if err != nil {
				panic(err)
			}
			_, err = graphql.Execute(ctx, call)
			proxycapture.MustFailWith(err, "query service call failed: transport")
		}
	}).AssertNothingReached(t)
}
