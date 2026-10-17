package devhealthfacts

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestReadFailureCarriesTheClickHouseCodeAndNotTheText(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cause := fmt.Errorf("read: %w", &clickhousedriver.Exception{Code: 241, Message: "memory limit exceeded: would use 267.49 MiB"})
	var failure *contextfabric.FactReadFailure
	if !errors.As(readFailure("query team owned repositories", cause), &failure) {
		t.Fatal("readFailure must return a FactReadFailure")
	}
	if failure.SourceCode != 241 {
		t.Fatalf("SourceCode = %d, want 241", failure.SourceCode)
	}
	line := logs.String()
	if !strings.Contains(line, `"clickhouse_code":241`) {
		t.Fatalf("read_failed line lacks clickhouse_code: %s", line)
	}
	if strings.Contains(line, "267.49") || strings.Contains(line, "memory limit") {
		t.Fatalf("read_failed line carries the exception text: %s", line)
	}

	logs.Reset()
	if !errors.As(readFailure("query team owned repositories", errors.New("dial failed")), &failure) || failure.SourceCode != 0 {
		t.Fatalf("a failure without a ClickHouse exception must carry SourceCode 0, got %+v", failure)
	}
	if strings.Contains(logs.String(), "clickhouse_code") {
		t.Fatalf("a failure without a ClickHouse exception must not log clickhouse_code: %s", logs.String())
	}
}
