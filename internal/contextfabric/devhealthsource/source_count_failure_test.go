package devhealthsource

import (
	"context"
	"errors"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type refusingCountClient struct {
	code    int32
	calls   int
	readers []string
}

func (c *refusingCountClient) Query(ctx context.Context, _ string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls++
	reader, _ := contextpacket.StatementReaderFromContext(ctx)
	c.readers = append(c.readers, reader)
	return nil, &clickhousedriver.Exception{Code: c.code, Name: "DB::Exception", Message: "synthetic refusal"}
}

// A refused count read names the ClickHouse code and limit class, stops at the
// first refused statement, and labels its statements for system.query_log.
func TestProjectionSourceCountsRefusedReadNamesCodeAndLimitClass(t *testing.T) {
	t.Parallel()
	for code, want := range map[int32]string{241: "memory", 159: "execution_time", 158: "rows_to_read", 307: "bytes_to_read", 396: "result_rows", 60: "other"} {
		client := &refusingCountClient{code: code}
		source, err := NewClickHouseProjectionSource(client)
		if err != nil {
			t.Fatal(err)
		}
		_, err = source.ProjectionSourceCounts(context.Background(), "org-a")
		var failure contextfabric.SourceReadFailure
		if !errors.As(err, &failure) || failure.ClickHouseCode() != code || failure.LimitClass() != want {
			t.Fatalf("code %d: error %v does not carry code and limit class %q", code, err, want)
		}
		if client.calls != 1 || client.readers[0] != "projector_count_check_source_count" {
			t.Fatalf("code %d: %d statements, readers %v; want 1 statement labelled projector_count_check_source_count", code, client.calls, client.readers)
		}
	}
}
