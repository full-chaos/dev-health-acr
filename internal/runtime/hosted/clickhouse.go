package hosted

import (
	"context"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

func openClickHouse(_ context.Context, request clickHouseOpenRequest) (clickHouseComponents, error) {
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(request.config.ClickHouseCACertPath)
	if err != nil {
		return clickHouseComponents{}, err
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: request.config.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: request.config.ClickHouseMaxBytesToRead,
		// MaxResultRows (Codex R3, CHAOS-4418, confirmed BLOCKER): left
		// unset, dev-health-go's own Options default is 1,000
		// (clickhouse/options.go, "max_result_rows"), and ClickHouse's
		// default result_overflow_mode is "throw" -- exceeding it FAILS
		// the whole query, not just truncates it. devhealthfacts'
		// repository metrics reader (metrics.go's readRepositoryMetrics)
		// deliberately has no query-wide SQL LIMIT of its own anymore
		// (a per-repository `LIMIT n BY repo_id` instead, so no single
		// repository can starve another one out of a shared budget --
		// see that file's own metricsSeriesPerRepositoryRowCap doc
		// comment), so its own worst case is
		// (ContextFabricInvestigationOptions' own MaxCohortMembers upper
		// bound, 250 -- validate_context_fabric_request.go) times
		// metricsSeriesPerRepositoryRowCap (200) = 50,000 raw rows for
		// ONE query. The unset 1,000-row driver default would make that
		// query FAIL outright (worse than the starvation bug this reader
		// exists to fix) for as few as ~12 repositories with a full
		// 90-day series each. 100,000 leaves 2x headroom above the
		// documented worst case for this one reader; every OTHER
		// provider in this package still bounds its own query with
		// withRowLimit's maxFactRowsPerQuery (200), so this does not
		// relax any other reader's own safety margin.
		MaxResultRows: 100_000,
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
