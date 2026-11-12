package storage

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-13562: a device authorization keeps up to contractsv1.MaxRepositoryScopes
// repository hints, the bound its request schema and the approval share.
func TestDeviceAuthorizationHintsBoundIsTheRepositoryScopeBound(t *testing.T) {
	hints := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("org/repo-%04d", i)
		}
		return out
	}
	for _, cell := range []struct {
		count int
		ok    bool
	}{{100, true}, {101, true}, {contractsv1.MaxRepositoryScopes, true}, {contractsv1.MaxRepositoryScopes + 1, false}} {
		err := ValidateDeviceAuthorizationHints(DeviceAuthorizationCreateInput{RepositoryHints: hints(cell.count)})
		if (err == nil) != cell.ok {
			t.Errorf("%d hints: err = %v, want accepted = %v", cell.count, err, cell.ok)
		}
	}
}
