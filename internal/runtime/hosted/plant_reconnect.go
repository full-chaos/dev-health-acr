package hosted

import (
	"context"

	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type perStatementClient struct{ options runtimeclickhouse.Options }

type perStatementRows struct {
	contextpacket.ClickHouseRowScanner
	client *runtimeclickhouse.Client
}

func (r perStatementRows) Close() error {
	err := r.ClickHouseRowScanner.Close()
	_ = r.client.Close()
	return err
}

func (c perStatementClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(c.options)
	if err != nil {
		return nil, err
	}
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return perStatementRows{ClickHouseRowScanner: rows, client: client}, nil
}
