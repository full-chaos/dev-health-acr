//go:build integration

package authworkload

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/workloadtoken"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

// TestExchangeAgainstAuthService proves acr's client against a live auth-service (CHAOS-11195 acceptance). It builds
// the client through the real acr loader from the real ACR_AUTH_WORKLOAD_* variables (the workload's registered
// subject: ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE on Compose, ACR_AUTH_WORKLOAD_SUBJECT_FILE on Kubernetes) and runs the
// exchange for both target audiences. A client with a wrong issuer must be refused.
//
//	ACR_AUTH_WORKLOAD_EXCHANGE_URL=http://auth-service:8095 ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP=true \
//	ACR_AUTH_WORKLOAD_ISSUER=<AUTH_TOKEN_ISSUER_URL> ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE=<file> \
//	go test -tags=integration -count=1 -run TestExchangeAgainstAuthService ./internal/authworkload
func TestExchangeAgainstAuthService(t *testing.T) {
	if _, set := os.LookupEnv("ACR_AUTH_WORKLOAD_EXCHANGE_URL"); !set {
		t.Skip("ACR_AUTH_WORKLOAD_EXCHANGE_URL is not set: no auth-service to exchange with")
	}
	cfg, err := config.LoadAuthWorkload(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := Build(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, audience := range []string{"dev-health-api", "auth-service"} {
		token, err := client.Token(ctx, audience)
		if err != nil {
			t.Fatalf("exchange for audience %s: %v", audience, err)
		}
		if strings.Count(token, ".") != 2 {
			t.Fatalf("exchange for audience %s returned no compact JWT", audience)
		}
	}

	wrong := cfg
	wrong.Issuer = "https://wrong-issuer.example.test"
	refusing, err := Build(wrong, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refusing.Token(ctx, "dev-health-api"); !errors.Is(err, workloadtoken.ErrNoUsableToken) {
		t.Fatalf("a token from an issuer other than the configured one must be refused: %v", err)
	}
}
