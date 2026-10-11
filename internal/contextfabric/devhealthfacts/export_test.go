package devhealthfacts

import (
	"time"

	"github.com/full-chaos/dev-health-go/readers"
)

func SetClockForTest(now func() time.Time) (restore func()) {
	previous := clock
	clock = now
	return func() { clock = previous }
}

func StatusSinceStatementForTest(scope readers.AuthorizationScope, settings readers.Settings) (string, []readers.Binding) {
	return statusSinceStatement(scope, settings)
}
