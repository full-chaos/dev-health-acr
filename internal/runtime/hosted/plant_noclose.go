package hosted

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type noCloseClient struct{ inner contextpacket.ClickHouseQueryClient }

type noCloseRows struct{ contextpacket.ClickHouseRowScanner }

func (noCloseRows) Close() error { return nil }

func (c noCloseClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	rows, err := c.inner.Query(ctx, statement, bindings)
	if err != nil {
		return nil, err
	}
	return noCloseRows{rows}, nil
}
