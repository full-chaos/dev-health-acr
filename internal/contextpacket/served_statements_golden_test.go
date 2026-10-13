package contextpacket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
)

var writeStatementGolden = flag.Bool("write-statement-golden", false, "rewrite the source statement golden")

type statementRecorder struct{ statements []string }

func (r *statementRecorder) Query(_ context.Context, statement string, _ []ClickHouseBinding) (ClickHouseRowScanner, error) {
	r.statements = append(r.statements, statement)
	return nil, errors.New("stop")
}

// The golden holds the digest of every evidence statement as built before the
// provenance values came from constants.
func TestSourceStatementsAreByteIdenticalToTheGolden(t *testing.T) {
	var out bytes.Buffer
	line := func(id, statement string) {
		fmt.Fprintf(&out, "%s %x\n", id, sha256.Sum256([]byte(statement)))
	}
	recorder := &statementRecorder{}
	executor := NewClickHouseSourceExecutor(recorder)
	for _, query := range SourceQueryCatalogV1 {
		line("catalog/"+query.ID, query.Statement)
		_, _ = executor.QueryEvidence(context.Background(), query, nil)
		line("executed/"+query.ID, recorder.statements[len(recorder.statements)-1])
	}
	for _, query := range SourceRowOnlyQueriesV2 {
		line("rowonly/"+query.ID, query.Statement)
	}
	for _, query := range OrganizationRowQueriesV1 {
		line("organization/"+query.ID, query.Statement)
	}
	line("read_adapter", clickHouseEvidenceQueryV1)
	if out.Len() < 2000 {
		t.Fatalf("only %d bytes of statement digests; the measurement did not happen", out.Len())
	}
	const golden = "testdata/source_statements.golden.txt"
	if *writeStatementGolden {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("a source statement changed:\n%s\nwant:\n%s", out.Bytes(), want)
	}
}
