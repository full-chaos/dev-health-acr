package hosted

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type noQueryClient struct{}

func (noQueryClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return nil, errors.New("not queried in this test")
}

// CHAOS-6180: Open composes the source-row resolver on the runtime's own
// ClickHouse client, and leaves it nil (never a typed nil) without one.
func TestOpenComposesSourceRowsOnTheClickHouseClient(t *testing.T) {
	events := []string{}
	request := testBuildRequest(t, &events, "")
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.Dependencies.Runtime.SourceRows != nil {
		t.Fatalf("SourceRows = %T without a ClickHouse client", runtime.Dependencies.Runtime.SourceRows)
	}

	request = testBuildRequest(t, &events, "")
	openClickHouse := request.factories.openClickHouse
	request.factories.openClickHouse = func(ctx context.Context, open clickHouseOpenRequest) (clickHouseComponents, error) {
		components, err := openClickHouse(ctx, open)
		components.queryClient = noQueryClient{}
		return components, err
	}
	runtime, err = open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if _, ok := runtime.Dependencies.Runtime.SourceRows.(*sourcerow.Resolver); !ok {
		t.Fatalf("SourceRows = %T, want *sourcerow.Resolver", runtime.Dependencies.Runtime.SourceRows)
	}
}
