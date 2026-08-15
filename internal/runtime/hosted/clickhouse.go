package hosted

import (
	"context"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
)

func openClickHouse(_ context.Context, request clickHouseOpenRequest) (clickHouseComponents, error) {
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(request.config.ClickHouseCACertPath)
	if err != nil {
		return clickHouseComponents{}, err
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: request.config.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: request.config.ClickHouseMaxBytesToRead,
	})
	if err != nil {
		return clickHouseComponents{}, err
	}
	fail := func(cause error) (clickHouseComponents, error) {
		return clickHouseComponents{}, errors.Join(cause, client.Close())
	}
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{
		ActiveKID: request.config.EvidenceIDActiveKID, Keys: request.config.EvidenceIDKeys,
	})
	if err != nil {
		return fail(fmt.Errorf("create evidence identifier codec: %w", err))
	}
	rows := contextpacket.NewObservedCatalogClickHouseRows(client, request.assemblyObserver)
	factory := contextpacket.NewObservedEvidenceStoreFactory(codec, request.expansionObserver, request.assemblyObserver)
	evidence, err := factory(rows)
	if err != nil {
		return fail(fmt.Errorf("create evidence store: %w", err))
	}
	return clickHouseComponents{
		evidence: evidence, factory: factory, queryClient: client,
		check: func(ctx context.Context) error { return checkClickHouseRuntime(ctx, client.Ping, client) }, close: client.Close,
	}, nil
}
