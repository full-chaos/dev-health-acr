package sidecar

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

// The token endpoint must be https (or loopback), so a proxied exchange shows as a CONNECT to the capture proxy.
func TestTheDefaultWorkloadExchangeClientNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(context.Context) {
		dir, err := os.MkdirTemp("", "workload-subject-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		tokenFile := filepath.Join(dir, "token")
		if err := os.WriteFile(tokenFile, []byte(proxycapture.Canary), 0o600); err != nil {
			panic(err)
		}
		endpoint, err := url.Parse("https://upstream.invalid/api/v1/oauth/token")
		if err != nil {
			panic(err)
		}
		source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{TokenEndpoint: endpoint, SubjectTokenFile: tokenFile, Timeout: 5 * time.Second})
		if err != nil {
			panic(err)
		}
		_, _ = source()
		supplied, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{TokenEndpoint: endpoint, SubjectTokenFile: tokenFile, Timeout: 5 * time.Second, HTTPClient: &http.Client{}})
		if err != nil {
			panic(err)
		}
		_, _ = supplied()
	}).AssertNothingReached(t)
}
