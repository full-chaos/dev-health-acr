package modelruntimeresolver_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A per-organization runtime built by the resolver keeps the deployment's
// synthesis input token cap: an input over the cap ends with the input
// overflow and the byte bound the cap leaves, before any dial. Without the cap
// the same call is placed and refused at the dial to the loopback base_url.
func TestAResolvedOrgRuntimeKeepsTheSynthesisInputTokenCap(t *testing.T) {
	const org = "0123456789abcdef"
	input := contextfabric.SynthesisInput{Request: validInterpretRequest()}
	for index := 0; index < 400; index++ {
		id := fmt.Sprintf("work_item_%04d", index)
		input.Facts.Facts = append(input.Facts.Facts, contextfabric.CanonicalFact{
			Kind:        contextfabric.FactWork,
			Subject:     contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: id},
			Fields:      map[string]contextfabric.FactValue{"title": contextfabric.StringFactValue(id + " " + strings.Repeat("x", 80))},
			SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	tokenCap := genkitruntime.MinSynthesisInputTokens
	overhead := len(synthesisprompt.System()) + len(synthesisprompt.OutputSchema())

	call := func(tokenCap int) error {
		configs := newOrgConfigStub(nil)
		request := writeRequest("org-model")
		request.BaseURL = "https://127.0.0.1:9/v1"
		if _, err := configs.UpsertOrgModelConfig(context.Background(), storage.Principal{OrgID: org}, request); err != nil {
			t.Fatal(err)
		}
		defaults := modelprovider.Config{Timeout: 3 * time.Second, MaxAttempts: 1, MaxSynthesisInputTokens: tokenCap}
		resolver := modelruntimeresolver.New(nil, configs, modelruntimeresolver.NewModelProviderBuild(defaults))
		_, _, err := resolver.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: org}, input)
		return err
	}

	var overflow *contextfabric.ModelInputOverflow
	if err := call(tokenCap); !errors.As(err, &overflow) || overflow.MaxBytes != 3*tokenCap-overhead {
		t.Fatalf("err = %v, want the input overflow at a bound of %d bytes", err, 3*tokenCap-overhead)
	}
	if err := call(0); !errors.Is(err, contextfabric.ErrModelUnavailable) || errors.As(err, &overflow) {
		t.Fatalf("cap off: err = %v, want the call placed and refused at the dial", err)
	}
}
