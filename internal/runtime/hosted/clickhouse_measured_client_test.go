package hosted

import (
	"context"
	"errors"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type budgetFailingClient struct{}

func (budgetFailingClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return nil, &clickhousedriver.Exception{Code: 307}
}

// Every statement of the binary goes through the measured boundary, and a
// budget exception names the configured cap.
func TestMeasuredClickHouseQueryClientNamesTheConfiguredCap(t *testing.T) {
	t.Parallel()
	client := measuredClickHouseQueryClient(budgetFailingClient{}, config.Config{ClickHouseMaxBytesToRead: 256 << 20})
	_, err := client.Query(context.Background(), "SELECT 1", nil)
	var budget *devhealthfacts.BudgetExceededError
	if !errors.As(err, &budget) {
		t.Fatalf("error = %v, want a BudgetExceededError", err)
	}
	if budget.CapBytes != 256<<20 || budget.Code != 307 {
		t.Fatalf("budget error = %+v, want code 307 and the configured cap %d", budget, 256<<20)
	}
}
