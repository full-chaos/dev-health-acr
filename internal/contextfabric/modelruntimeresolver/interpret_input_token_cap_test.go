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
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A per-organization runtime built by the resolver keeps the deployment's
// interpret input token cap: an input over the cap ends with the input
// overflow before any dial. Without the cap the same call is placed and
// refused at the dial to the loopback base_url.
func TestAResolvedOrgRuntimeKeepsTheInterpretInputTokenCap(t *testing.T) {
	const org = "0123456789abcdef"
	request := validInterpretRequest()
	created := time.Date(2026, 8, 11, 19, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		request.Conversation = append(request.Conversation, contextfabric.ConversationTurn{
			TurnID: fmt.Sprintf("turn_%d", index), Role: contextfabric.ConversationUser,
			Content: strings.Repeat("x", 12000), CreatedAt: created,
		})
	}
	tokenCap := genkitruntime.MinInterpretInputTokens()

	call := func(tokenCap int) error {
		configs := newOrgConfigStub(nil)
		write := writeRequest("org-model")
		write.BaseURL = "https://127.0.0.1:9/v1"
		if _, err := configs.UpsertOrgModelConfig(context.Background(), storage.Principal{OrgID: org}, write); err != nil {
			t.Fatal(err)
		}
		defaults := modelprovider.Config{Timeout: 3 * time.Second, MaxAttempts: 1, MaxInterpretInputTokens: tokenCap}
		resolver := modelruntimeresolver.New(nil, configs, modelruntimeresolver.NewModelProviderBuild(defaults))
		_, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: org}, request)
		return err
	}

	var overflow *contextfabric.ModelInputOverflow
	if err := call(tokenCap); !errors.As(err, &overflow) {
		t.Fatalf("err = %v, want the input overflow", err)
	}
	if err := call(0); !errors.Is(err, contextfabric.ErrModelUnavailable) || errors.As(err, &overflow) {
		t.Fatalf("cap off: err = %v, want the call placed and refused at the dial", err)
	}
}
