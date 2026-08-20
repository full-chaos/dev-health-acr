package hosted

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Environment variables read by buildWorkloadTokenExchange. Both
// envWorkloadTokenExchangeAudience and envWorkloadTrustDomain must be set
// for CHAOS-4013 to be enabled; everything else has an in-cluster default.
const (
	envWorkloadTokenExchangeAudience = "ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE"
	envWorkloadTrustDomain           = "ACR_WORKLOAD_TRUST_DOMAIN"
	envKubernetesAPIServerURL        = "ACR_KUBERNETES_API_SERVER_URL"
	envKubernetesCACertPath          = "ACR_KUBERNETES_CA_CERT_PATH"
	envKubernetesReviewerTokenPath   = "ACR_KUBERNETES_REVIEWER_TOKEN_PATH"

	// defaultKubernetesAPIServerURL/CACertPath/ReviewerTokenPath are the
	// standard in-cluster Kubernetes API access conventions: every pod
	// carries a projected service-account token and the cluster CA at
	// these fixed paths, and the in-cluster DNS name always resolves the
	// API server. Overridable for non-standard clusters or local testing.
	defaultKubernetesAPIServerURL      = "https://kubernetes.default.svc"
	defaultKubernetesCACertPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultKubernetesReviewerTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

	kubernetesAPIRequestTimeout = 10 * time.Second
)

// workloadTokenExchangeConfigured reports whether CHAOS-4013 workload
// token exchange is enabled for this deployment. Unset (the default)
// leaves api.RuntimeDependencies.WorkloadTokenExchange nil and the RFC
// 8693 grant degrades to a clean 503 (ADR 0007's "an unset deployment
// never fails closed" convention) -- the pre-existing device-code grant on
// the same endpoint is unaffected either way.
func workloadTokenExchangeConfigured(lookup func(string) (string, bool)) bool {
	audience, _ := lookup(envWorkloadTokenExchangeAudience)
	trustDomain, _ := lookup(envWorkloadTrustDomain)
	return strings.TrimSpace(audience) != "" && strings.TrimSpace(trustDomain) != ""
}

// buildWorkloadTokenExchange composes the CHAOS-4013 seams
// (SubjectTokenValidator over Kubernetes TokenReview, GrantResolver over
// postgres.workloadBindings, AccessTokenIssuer over the SAME credential
// lifecycle every other issuance path already uses) into the one service
// api.RuntimeDependencies.WorkloadTokenExchange needs. Returns (nil, nil)
// when unconfigured.
func buildWorkloadTokenExchange(postgres postgresComponents, now func() time.Time, lookup func(string) (string, bool)) (*auth.WorkloadTokenExchangeService, error) {
	if !workloadTokenExchangeConfigured(lookup) {
		return nil, nil
	}
	if postgres.workloadBindings == nil {
		return nil, errors.New("workload token exchange requires a workload binding store")
	}
	audience, _ := lookup(envWorkloadTokenExchangeAudience)
	trustDomain, _ := lookup(envWorkloadTrustDomain)
	httpClient, err := kubernetesAPIHTTPClient(stringEnvOrDefault(lookup, envKubernetesCACertPath, defaultKubernetesCACertPath))
	if err != nil {
		return nil, fmt.Errorf("build kubernetes api http client: %w", err)
	}
	validator, err := auth.NewKubernetesTokenReviewValidator(auth.KubernetesTokenReviewOptions{
		HTTPClient:   httpClient,
		APIServerURL: stringEnvOrDefault(lookup, envKubernetesAPIServerURL, defaultKubernetesAPIServerURL),
		// ReviewerToken is acr-api's OWN in-cluster service-account token
		// (design brief RBAC: create on tokenreviews only) -- re-read from
		// disk on every call, matching the in-cluster convention that
		// kubelet rotates a projected token in place (the same contract
		// internal/sidecar's CredentialSource already follows for the
		// client side of this same feature).
		ReviewerToken: rereadFileTokenSource(stringEnvOrDefault(lookup, envKubernetesReviewerTokenPath, defaultKubernetesReviewerTokenPath)),
		Audience:      audience, TrustDomain: trustDomain, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("build kubernetes token review validator: %w", err)
	}
	resolver, err := auth.NewGrantResolver(postgres.workloadBindings)
	if err != nil {
		return nil, fmt.Errorf("build workload grant resolver: %w", err)
	}
	credentialService, err := auth.NewService(postgres.credentials, auth.ServiceOptions{Now: now})
	if err != nil {
		return nil, fmt.Errorf("build credential service: %w", err)
	}
	issuer, err := auth.NewWorkloadAccessTokenIssuer(credentialService, now)
	if err != nil {
		return nil, fmt.Errorf("build workload access token issuer: %w", err)
	}
	return auth.NewWorkloadTokenExchangeService(validator, resolver, issuer)
}

// kubernetesAPIHTTPClient layers the in-cluster CA bundle (when present --
// see defaultKubernetesCACertPath) on top of the system trust store, the
// same "extend, don't replace" convention internal/sidecar's
// loadCACertPool already uses for its own optional CA bundle. A missing or
// unreadable CA file is not fatal: the system pool alone still lets a
// non-default APIServerURL (e.g. local testing against a non-in-cluster
// endpoint) work.
func kubernetesAPIHTTPClient(caCertPath string) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if raw, readErr := os.ReadFile(caCertPath); readErr == nil {
		pool.AppendCertsFromPEM(raw)
	}
	return &http.Client{
		Timeout: kubernetesAPIRequestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}

// rereadFileTokenSource returns a func that reads path fresh on every
// call -- see buildWorkloadTokenExchange's ReviewerToken doc comment.
func rereadFileTokenSource(path string) func() (string, error) {
	return func() (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read reviewer token file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
}

func stringEnvOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
