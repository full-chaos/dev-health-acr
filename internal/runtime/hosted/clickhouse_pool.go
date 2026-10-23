package hosted

import (
	"time"

	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The native ClickHouse client keeps one warm TCP+TLS connection per statement
// slot. Its idle queue holds MaxIdleConns connections and closes any surplus
// on release, so an idle cap below the open cap makes every burst past it pay
// a new TCP+TLS+hello (about 3 round trips) on the next burst.
const (
	ClickHousePoolMaxOpenConns    = 8
	ClickHousePoolMaxIdleConns    = ClickHousePoolMaxOpenConns
	ClickHousePoolConnMaxLifetime = 30 * time.Minute
)

// ClickHousePoolOptions sets the shared warm-pool shape on a ClickHouse client's
// Options. The API and the projector both call it.
func ClickHousePoolOptions(options runtimeclickhouse.Options) runtimeclickhouse.Options {
	options.MaxOpenConns = ClickHousePoolMaxOpenConns
	options.MaxIdleConns = ClickHousePoolMaxIdleConns
	options.ConnMaxLifetime = ClickHousePoolConnMaxLifetime
	return options
}
