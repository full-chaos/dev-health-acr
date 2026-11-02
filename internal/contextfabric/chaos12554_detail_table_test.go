package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos12554_detail_table_test.go pins the declared detail table
// (CHAOS-12554): it never takes part in the single-table pick, so the table
// every claim of the fact gets stays the same, and a claim carries it only
// when it is asked for by name.

func detailRows(column string, values ...string) []FactValueRow {
	rows := make([]FactValueRow, 0, len(values))
	for _, value := range values {
		value := value
		share := 0.5
		rows = append(rows, FactValueRow{Fields: map[string]FactValue{column: {String: &value}, "share": {Number: &share}}})
	}
	return rows
}

// detailFact is an investment fact with a theme table, a subcategory table
// and a scalar. detail marks the subcategory table a detail table.
func detailFact(detail bool) CanonicalFact {
	themes := TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"theme"}, Measures: []string{"share"},
		Rows: detailRows("theme", "quality", "risk")})
	subcategories := TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"subcategory"}, Measures: []string{"share"}, Detail: detail,
		Rows: detailRows("subcategory", "quality.bugfix", "quality.testing", "risk.security")})
	share := 0.5
	return CanonicalFact{
		ID: "fact_detail", Kind: FactInvestment,
		Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:one"},
		Fields:  map[string]FactValue{"theme_breakdown": themes, "subcategory_breakdown": subcategories, "theme_quality": {Number: &share}},
	}
}

func TestCHAOS12554ADetailTableNeverTakesPartInThePick(t *testing.T) {
	t.Parallel()
	field, ambiguous, ok := canonicalRowsField(detailFact(true))
	if !ok || ambiguous || field != "theme_breakdown" {
		t.Fatalf("pick = %q, ambiguous %v, ok %v; want theme_breakdown, unambiguous", field, ambiguous, ok)
	}
	// Without the declaration the same fact holds two tables and fails closed:
	// the declaration is what keeps the pick.
	if field, ambiguous, ok := canonicalRowsField(detailFact(false)); ok || !ambiguous {
		t.Fatalf("undeclared: pick = %q, ambiguous %v, ok %v; want the fail-closed ambiguity", field, ambiguous, ok)
	}
	if _, unique := uniqueNonTimeSeriesRowsField(detailFact(true)); !unique {
		t.Fatal("the detail table counted toward the non-time_series fields")
	}
}

// A detail table declared time_series is not the second, time_series table
// either.
func TestCHAOS12554ADetailTimeSeriesIsNoSecondTable(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	value := fact.Fields["subcategory_breakdown"]
	value.Table.Shape = FactTableTimeSeries
	fact.Fields["subcategory_breakdown"] = value
	if field, ok := canonicalTimeSeriesField(fact); ok {
		t.Fatalf("time_series pick = %q, want none: a detail table is out of every pick", field)
	}
}

func TestCHAOS12554AClaimCarriesADetailTableOnlyByName(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	share := 0.5
	scalar := ClaimedFact{ClaimID: "claim_scalar", Kind: FactInvestment, Subject: fact.Subject, Field: "theme_quality", Value: ScalarValue{Number: &share}}
	named := ClaimedFact{ClaimID: "claim_named", Kind: FactInvestment, Subject: fact.Subject, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}}
	attached, rows, _, truncated := attachCanonicalRows([]ClaimedFact{scalar, named}, []CanonicalFact{fact})
	if truncated {
		t.Fatal("nothing was cut")
	}
	if got := attached[0].Table; got == nil || got.Field != "theme_breakdown" || len(attached[0].Rows) != 2 {
		t.Fatalf("scalar claim: table %+v with %d rows, want theme_breakdown with 2", got, len(attached[0].Rows))
	}
	if got := attached[1].Table; got == nil || got.Field != "subcategory_breakdown" || len(attached[1].Rows) != 3 {
		t.Fatalf("named claim: table %+v with %d rows, want subcategory_breakdown with 3", got, len(attached[1].Rows))
	}
	if attached[1].TimeSeriesTable != nil || attached[1].TimeSeriesRows != nil {
		t.Fatal("a claim with a detail table carries a second table")
	}
	if rows != 5 {
		t.Fatalf("rows = %d, want 2 + 3", rows)
	}
	// An undeclared table of the same name is not a detail table.
	if _, _, ok := attachDetailTable(named, detailFact(false), "subcategory_breakdown"); ok {
		t.Fatal("attachDetailTable attached an undeclared table")
	}
	if _, _, ok := attachDetailTable(named, fact, "theme_breakdown"); ok {
		t.Fatal("attachDetailTable attached the fact's pick table by name")
	}
}

// A detail table replaces whatever table a claim carried, the time_series
// table included, and is cut like any table.
func TestCHAOS12554ADetailTableReplacesBothTablesAndIsCut(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	value := fact.Fields["subcategory_breakdown"]
	labels := make([]string, contractsv1.ContextFabricClaimedFactMaxRows+3)
	for index := range labels {
		labels[index] = "row_" + strings.Repeat("x", index%7) + string(rune('a'+index%26))
	}
	value.Rows = detailRows("subcategory", labels...)
	fact.Fields["subcategory_breakdown"] = value
	claim := ClaimedFact{
		ClaimID: "claim_named", Kind: FactInvestment, Subject: fact.Subject, Field: "subcategory_breakdown", Value: ScalarValue{Null: true},
		Table: &contractsv1.ContextFabricClaimedFactTable{Field: "theme_breakdown"}, TimeSeriesTable: &contractsv1.ContextFabricClaimedFactTable{Field: "daily"},
		TimeSeriesRows: []contractsv1.ContextFabricClaimedFactRow{{}},
	}
	attached, truncated, ok := attachDetailTable(claim, fact, "subcategory_breakdown")
	if !ok || !truncated || len(attached.Rows) != contractsv1.ContextFabricClaimedFactMaxRows {
		t.Fatalf("ok %v, truncated %v, %d rows; want a cut to %d", ok, truncated, len(attached.Rows), contractsv1.ContextFabricClaimedFactMaxRows)
	}
	if attached.Table.Field != "subcategory_breakdown" || attached.TimeSeriesTable != nil || attached.TimeSeriesRows != nil {
		t.Fatalf("tables = %+v / %+v, want the detail table alone", attached.Table, attached.TimeSeriesTable)
	}
	// The attach step reports the cut too.
	if _, _, _, cut := attachCanonicalRows([]ClaimedFact{claim}, []CanonicalFact{fact}); !cut {
		t.Fatal("attachCanonicalRows did not report the cut of a detail table")
	}
}

func TestCHAOS12554ADetailTableIsNotDirectOnly(t *testing.T) {
	t.Parallel()
	table := FactTable{Shape: FactTableBreakdown, Key: []string{"subcategory"}, Measures: []string{"share"}, Detail: true, DirectOnly: true,
		Rows: detailRows("subcategory", "quality.bugfix")}
	if err := table.Validate(); err == nil || !strings.Contains(err.Error(), "both a detail table and direct-only") {
		t.Fatalf("Validate() = %v, want the both-declared refusal", err)
	}
	table.DirectOnly = false
	if err := table.Validate(); err != nil {
		t.Fatalf("a detail table alone is valid: %v", err)
	}
}

// B (lead ruling on CHAOS-12554): a claim whose field names a declared detail
// table, with value null, grounds against that fact. Nothing else about
// grounding changes, and every near miss stays ungrounded.
func TestCHAOS12554ANullClaimOnADetailTableGroundsAndNothingElseDoes(t *testing.T) {
	t.Parallel()
	share, other, yes := 0.5, "x", true
	count := int64(3)
	fact := detailFact(true)
	fact.Fields["theme_null"] = FactValue{Null: true}
	empty := detailFact(true)
	value := empty.Fields["subcategory_breakdown"]
	value.Rows = nil
	empty.Fields["subcategory_breakdown"] = value
	for _, tc := range []struct {
		name  string
		fact  CanonicalFact
		field string
		value ScalarValue
		want  bool
	}{
		{"null on a detail table", fact, "subcategory_breakdown", ScalarValue{Null: true}, true},
		{"a number on a detail table", fact, "subcategory_breakdown", ScalarValue{Number: &share}, false},
		{"a string on a detail table", fact, "subcategory_breakdown", ScalarValue{String: &other}, false},
		{"an integer on a detail table", fact, "subcategory_breakdown", ScalarValue{Integer: &count}, false},
		{"a boolean on a detail table", fact, "subcategory_breakdown", ScalarValue{Boolean: &yes}, false},
		{"no value on a detail table", fact, "subcategory_breakdown", ScalarValue{}, false},
		{"null beside a number", fact, "subcategory_breakdown", ScalarValue{Null: true, Number: &share}, false},
		{"null beside a string", fact, "subcategory_breakdown", ScalarValue{Null: true, String: &other}, false},
		{"null beside an integer", fact, "subcategory_breakdown", ScalarValue{Null: true, Integer: &count}, false},
		{"null beside a boolean", fact, "subcategory_breakdown", ScalarValue{Null: true, Boolean: &yes}, false},
		{"null on an undeclared table", detailFact(false), "subcategory_breakdown", ScalarValue{Null: true}, false},
		{"null on the pick table", fact, "theme_breakdown", ScalarValue{Null: true}, false},
		{"null on a detail table with no rows", empty, "subcategory_breakdown", ScalarValue{Null: true}, false},
		{"null on a missing field", fact, "risk_breakdown", ScalarValue{Null: true}, false},
		{"null on a scalar", fact, "theme_quality", ScalarValue{Null: true}, false},
		{"null restating a null scalar", fact, "theme_null", ScalarValue{Null: true}, true},
		{"a scalar restated", fact, "theme_quality", ScalarValue{Number: &share}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := ClaimedFact{ClaimID: "claim_detail", Kind: FactInvestment, Subject: tc.fact.Subject, Field: tc.field, Value: tc.value}
			if _, outcome, _ := groundClaimAt([]CanonicalFact{tc.fact}, claim); (outcome == claimGrounded) != tc.want {
				t.Fatalf("by field: grounded = %v, want %v", outcome == claimGrounded, tc.want)
			}
			claim.FactID = tc.fact.ID
			if _, outcome, _ := groundClaimAt([]CanonicalFact{tc.fact}, claim); (outcome == claimGrounded) != tc.want {
				t.Fatalf("by fact id: grounded = %v, want %v", outcome == claimGrounded, tc.want)
			}
		})
	}
}

// A grounded null claim on a detail table reaches the attach step as its own
// fact's claim and carries that table.
func TestCHAOS12554AGroundedNullClaimCarriesTheDetailTable(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	claim := ClaimedFact{ClaimID: "claim_detail", Kind: FactInvestment, Subject: fact.Subject, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}, FactID: fact.ID}
	if _, outcome := groundClaim([]CanonicalFact{fact}, claim); outcome != claimGrounded {
		t.Fatalf("outcome = %v, want grounded", outcome)
	}
	attached, _, _, _ := attachCanonicalRows([]ClaimedFact{claim}, []CanonicalFact{fact})
	if attached[0].Table == nil || attached[0].Table.Field != "subcategory_breakdown" {
		t.Fatalf("table = %+v, want the detail table", attached[0].Table)
	}
}

// CHAOS-12587 (codex r1 P3, lead ruling): the detail-tables line names the
// table each claim carries, read off the attached table, so a pick of the
// wrong one of two equal-size tables shows.
func TestCHAOS12587TheLineNamesTheTableAClaimCarries(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	second := fact.Fields["subcategory_breakdown"]
	fact.Fields["repository_breakdown"] = second
	alpha := ClaimedFact{ClaimID: "claim_alpha", Kind: FactInvestment, Subject: fact.Subject, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}}
	beta := ClaimedFact{ClaimID: "claim_beta", Kind: FactInvestment, Subject: fact.Subject, Field: "repository_breakdown", Value: ScalarValue{Null: true}}
	share := 0.5
	plain := ClaimedFact{ClaimID: "claim_plain", Kind: FactInvestment, Subject: fact.Subject, Field: "theme_quality", Value: ScalarValue{Number: &share}}
	attached, _, _, _ := attachCanonicalRows([]ClaimedFact{alpha, beta, plain}, []CanonicalFact{fact})
	event := attachedDetailTables(attached, []CanonicalFact{fact})
	if event.Claims != 2 || !slices.Equal(event.Tables, []string{"repository_breakdown", "subcategory_breakdown"}) || event.Omitted != 0 {
		t.Fatalf("event = %+v, want 2 claims over both tables", event)
	}
	// The wrong table on a claim is logged as the table it carries.
	wrong := attached[0]
	wrong.Table = attached[1].Table
	if got := attachedDetailTables([]ClaimedFact{wrong}, []CanonicalFact{fact}); !slices.Equal(got.Tables, []string{"repository_breakdown"}) {
		t.Fatalf("wrong pick: tables = %v, want the table the claim carries", got.Tables)
	}
	// A pick table is no detail table, and no claim with one makes no line.
	if got := attachedDetailTables(attached[2:], []CanonicalFact{fact}); got.Claims != 0 || got.Tables != nil {
		t.Fatalf("pick table: event = %+v, want none", got)
	}
}

func TestCHAOS12587TheLineCapsItsNames(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	table := fact.Fields["subcategory_breakdown"]
	claims := make([]ClaimedFact, 0, MaxLoggedDetailTables+3)
	for index := range MaxLoggedDetailTables + 3 {
		name := fmt.Sprintf("detail_%02d", index)
		fact.Fields[name] = table
		claims = append(claims, ClaimedFact{ClaimID: "claim_" + name, Kind: FactInvestment, Subject: fact.Subject, Field: name, Value: ScalarValue{Null: true}})
	}
	attached, _, _, _ := attachCanonicalRows(claims, []CanonicalFact{fact})
	event := attachedDetailTables(attached, []CanonicalFact{fact})
	if event.Claims != MaxLoggedDetailTables+3 || len(event.Tables) != MaxLoggedDetailTables || event.Omitted != 3 {
		t.Fatalf("event = %d claims, %d names, %d omitted; want %d, %d, 3", event.Claims, len(event.Tables), event.Omitted, MaxLoggedDetailTables+3, MaxLoggedDetailTables)
	}
	if event.Tables[0] != "detail_00" || !slices.IsSorted(event.Tables) {
		t.Fatalf("names = %v, want the first %d in order", event.Tables, MaxLoggedDetailTables)
	}
}

// Synthesize writes the line once when a claim carries a detail table, and
// not at all when none does.
func TestCHAOS12587SynthesizeWritesTheLineOnlyForADetailTable(t *testing.T) {
	t.Parallel()
	for _, named := range []bool{true, false} {
		input, draft := closureFixture()
		source := detailFact(true)
		input.Facts.Facts[0].Fields["subcategory_breakdown"] = source.Fields["subcategory_breakdown"]
		if named {
			draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
				ClaimID: "claim_detail_table", Kind: input.Facts.Facts[0].Kind, Subject: input.Facts.Facts[0].Subject,
				Field: "subcategory_breakdown", Value: ScalarValue{Null: true},
			})
		}
		telemetry := &recordingTelemetry{}
		synthesizer := RuntimeAnswerSynthesizer{
			Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
			Sink:      &fakeReceiptSink{},
			Telemetry: telemetry,
		}
		if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
			t.Fatalf("named %v: Synthesize() error = %v", named, err)
		}
		switch {
		case named && (len(telemetry.detailTablesAttached) != 1 || !slices.Equal(telemetry.detailTablesAttached[0].Tables, []string{"subcategory_breakdown"})):
			t.Fatalf("named: lines = %+v, want one naming subcategory_breakdown", telemetry.detailTablesAttached)
		case !named && len(telemetry.detailTablesAttached) != 0:
			t.Fatalf("not named: lines = %+v, want none", telemetry.detailTablesAttached)
		}
	}
}
