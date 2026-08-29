package hosted

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// clickHouseMaxResultRowsHeadroomFactor (CHAOS-4418, codex R3) is the
// multiplier over clickHouseMaxResultRowsWorstCase this client's own
// MaxResultRows is configured with -- 2x, not a tight fit, so a caller
// approaching but not yet at the documented worst case never trips the
// driver's own row-count safety net before this file's own worst-case
// arithmetic would predict it.
const clickHouseMaxResultRowsHeadroomFactor = 2

// clickHouseMaxResultRowsWorstCase (CHAOS-4418, codex R3 finding,
// confirmed BLOCKER) is a DERIVED capacity, never a second,
// independently-chosen literal: contractsv1.ContextFabricMaxCohortMembersLimit
// (the validated ceiling on how many repository subjects one cohort-shaped
// FactQuery can carry -- ContextFabricInvestigationOptions.Validate())
// times devhealthfacts.MetricsSeriesPerRepositoryRowCap (the per-repository
// `LIMIT n BY repo_id` readRepositoryMetrics' own SQL enforces, since that
// query deliberately carries no query-wide LIMIT of its own anymore -- a
// per-repository cap is what stops one wide-window repository starving
// another out of a shared budget, metrics.go's own doc comment). "A
// relation between two literals is not checkable; a relation between two
// constants is" (context_fabric_types.go's own ContextFabricSerializedBytesMin/Max
// comment) -- TestClickHouseClientOptionsSetMaxResultRowsAboveTheDocumentedWorstCase
// reads the SHIPPED Options back and recomputes this product from the two
// upstream constants itself, so a future change to either one, or to the
// shipped value, fails loudly here instead of silently reopening the
// query-error gap this finding closed.
const clickHouseMaxResultRowsWorstCase = contractsv1.ContextFabricMaxCohortMembersLimit * devhealthfacts.MetricsSeriesPerRepositoryRowCap

// clickHouseClientOptions builds the production ClickHouse client's own
// Options. Extracted from openClickHouse (which cannot be called in a unit
// test -- it dials a server) so that
// TestClickHouseClientOptionsSetMaxResultRowsAboveTheDocumentedWorstCase can
// observe the MaxResultRows this binary actually ships with. Deleting or
// lowering that field is the regression this whole finding is about, and a
// test that only re-computes the constant expression cannot catch it.
func clickHouseClientOptions(cfg config.Config, tlsConfig *tls.Config) runtimeclickhouse.Options {
	return runtimeclickhouse.Options{
		DSN: cfg.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: cfg.ClickHouseMaxBytesToRead,
		// MaxResultRows: left unset, dev-health-go's own Options default
		// is 1,000 (clickhouse/options.go, "max_result_rows"), and
		// ClickHouse's default result_overflow_mode is "throw" --
		// exceeding it FAILS the whole query, not just truncates it. The
		// unset 1,000-row driver default would make devhealthfacts'
		// repository metrics query FAIL outright (worse than the
		// cross-repository starvation bug that query's own per-repository
		// cap exists to fix) for as few as ~12 repositories with a full
		// 90-day series each. See clickHouseMaxResultRowsWorstCase's own
		// doc comment for the derivation; every OTHER provider in that
		// package still bounds its own query with withRowLimit's
		// maxFactRowsPerQuery (200), so this does not relax any other
		// reader's own safety margin -- see this file's own blast-radius
		// note in the PR this shipped under for the full accounting of
		// every unbounded query in this client's reach.
		MaxResultRows: clickHouseMaxResultRowsHeadroomFactor * clickHouseMaxResultRowsWorstCase,
	}
}

func openClickHouse(_ context.Context, request clickHouseOpenRequest) (clickHouseComponents, error) {
	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(request.config.ClickHouseCACertPath)
	if err != nil {
		return clickHouseComponents{}, err
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(clickHouseClientOptions(request.config, tlsConfig))
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
