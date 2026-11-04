package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestSynthesisDetailTableRowsNamesEachListedTableWithItsCount(t *testing.T) {
	t.Parallel()
	got := synthesisDetailTableRows([]CanonicalFact{detailFact(true)})
	if !slices.Equal(got.Tables, []string{"subcategory_breakdown=3"}) || got.Omitted != 0 {
		t.Fatalf("event = %+v; want the one detail table with its 3 rows (the pick table is not a detail table)", got)
	}
	if none := synthesisDetailTableRows([]CanonicalFact{detailFact(false)}); len(none.Tables) != 0 {
		t.Fatalf("event = %+v; an undeclared table is not listed", none)
	}
}

func TestSynthesisDetailTableRowsIsBounded(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	for i := 0; i < MaxLoggedDetailTables+3; i++ {
		fact.Fields["extra_"+string(rune('a'+i))] = fact.Fields["subcategory_breakdown"]
	}
	got := synthesisDetailTableRows([]CanonicalFact{fact})
	if len(got.Tables) != MaxLoggedDetailTables || got.Omitted != 4 {
		t.Fatalf("tables = %d, omitted = %d; want %d and 4", len(got.Tables), got.Omitted, MaxLoggedDetailTables)
	}
}

// The line is written from the input the synthesis was given, once, with names
// and counts only.
func TestComposeSynthesisResultLogsTheDetailTableRowsItWasGiven(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	input := SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{fact}}}
	draft := SynthesisDraft{Status: InvestigationPartial}
	var sink bytes.Buffer
	synthesizer := RuntimeAnswerSynthesizer{Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug})))}
	if _, err := synthesizer.composeSynthesisResult(context.Background(), storage.Principal{OrgID: "org_1"}, input, draft, validModelReceiptFixture(ModelOperationSynthesize), nil, nil); err != nil {
		t.Fatal(err)
	}
	logged := sink.String()
	if n := strings.Count(logged, "context fabric synthesis detail table rows"); n != 1 {
		t.Fatalf("%d detail table rows lines, want 1: %s", n, logged)
	}
	if !strings.Contains(logged, "subcategory_breakdown=3") {
		t.Fatalf("the line does not carry the table and its count: %s", logged)
	}
	for _, row := range []string{"quality.bugfix", "risk.security"} {
		if strings.Contains(logged, row) {
			t.Fatalf("the line carries a row value %q: %s", row, logged)
		}
	}
	sink.Reset()
	input.Facts.Facts = []CanonicalFact{detailFact(false)}
	if _, err := synthesizer.composeSynthesisResult(context.Background(), storage.Principal{OrgID: "org_1"}, input, draft, validModelReceiptFixture(ModelOperationSynthesize), nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.String(), "detail table rows") {
		t.Fatalf("a line was written for an input listing no detail table: %s", sink.String())
	}
}
