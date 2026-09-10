package modelruntimeresolver

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// CHAOS-5380: a per-organization BYO runtime must emit its decision line
// through the SAME sink the deployment-default runtime uses, exactly as
// Telemetry already does one field above it in orgModelProviderConfig.
// Without this a BYO organization's retries are invisible in the collected
// logs while the deployment default's are not -- the hardest kind of gap to
// notice, because the instrument looks installed.
func TestOrgModelProviderConfigInheritsTheLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	defaults := modelprovider.Config{Logger: logger}
	resolved := contextfabric.ResolvedOrgModelConfig{Provider: "byo", Model: "byo-model"}

	if got := orgModelProviderConfig(defaults, resolved); got.Logger != logger {
		t.Fatalf("orgModelProviderConfig().Logger = %#v, want the deployment default's logger", got.Logger)
	}
}
