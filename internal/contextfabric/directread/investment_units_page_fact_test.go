package directread

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func pageFactUnitsProvider(mixPad, rows int) *stubProvider {
	return &stubProvider{capability: unitsInvestmentCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		team := query.Subjects[0]
		facts := []contextfabric.CanonicalFact{
			{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{
				"unit_kind": strValue("mix"), "work_unit_id": strValue(strings.Repeat("m", mixPad)),
			}, EvidenceRefIDs: []string{"acr:v1:team:t"}},
			{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{
				"unit_kind": strValue(contextfabric.InvestmentUnitPageKind), "units_returned": intValue(int64(rows)), "units_refs_unresolved": intValue(3),
				"next_cursor": strValue(hiddenRowCursor),
			}, EvidenceRefIDs: []string{"acr:v1:team:t"}},
		}
		for i := 0; i < rows; i++ {
			facts = append(facts, unitRowFact(team, "wu-"+strings.Repeat("x", 40)+fmt.Sprintf("%02d", i), "a", float64(100-i)))
		}
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: facts, Reason: contextfabric.InvestmentUnitsCutReason, Truncated: true}, nil
	}}
}

func readPageFact(t *testing.T, mixPad, maxBytes int) FactsResponse {
	t.Helper()
	reader := newUnitsReader(t, pageFactUnitsProvider(mixPad, 12))
	response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
		Units: &RequestUnits{MaxUnits: 12}, MaxBytes: maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func hasPageFact(response FactsResponse) (ServedFact, bool) {
	return findUnitsPageFact(response.Facts)
}

func hasMixFact(response FactsResponse) bool {
	for _, f := range response.Facts {
		if k, _ := f.Fields["unit_kind"].(string); k == "mix" {
			return true
		}
	}
	return false
}

func investmentReason(response FactsResponse) string {
	var reasons []string
	for _, row := range response.Coverage {
		reasons = append(reasons, row.Reason)
	}
	return strings.Join(reasons, ";")
}

// sizeWithout is the byte size of a full read with the named unit kinds removed.
func sizeWithout(t *testing.T, mixPad int, drop ...string) int {
	t.Helper()
	full := readPageFact(t, mixPad, 262144)
	kept := full.Facts[:0:0]
	for _, f := range full.Facts {
		k := unitFactKind(f)
		skip := false
		for _, d := range drop {
			skip = skip || k == d
		}
		if !skip {
			kept = append(kept, f)
		}
	}
	full.Facts = kept
	return len(mustJSON(t, full))
}

// mixPadFilling returns the largest mix padding whose mix-only document fits
// the minimum budget.
func mixPadFilling(t *testing.T) int {
	t.Helper()
	pad := 0
	for step := 1024; step > 0; step /= 2 {
		for sizeWithout(t, pad+step, contextfabric.InvestmentUnitPageKind, contextfabric.InvestmentUnitKind) <= MinMaxBytes {
			pad += step
		}
	}
	return pad - 300
}

// A mix that fills the budget outranks the page fact. The page fact is not
// dropped silently: coverage says no units were served and what max_bytes the
// page fact needs, and no cursor is served.
func TestInvestmentUnitsPageFactNotFittingBesideTheMixIsALimitation(t *testing.T) {
	response := readPageFact(t, mixPadFilling(t), MinMaxBytes)
	if !hasMixFact(response) {
		t.Fatal("the mix fact was dropped for the page fact")
	}
	page, ok := hasPageFact(response)
	if !ok {
		t.Fatal("no work_unit_page fact: the limitation must be a structural row")
	}
	if got := page.Fields["units_returned"]; got != "0" {
		t.Fatalf("units_returned = %v, want 0", got)
	}
	if _, has := page.Fields["next_cursor"]; has {
		t.Fatal("limitation row carries a cursor")
	}
	if text, _ := page.Fields["units_limitation"].(string); !strings.Contains(text, "units not served under max_bytes 4096; minimum ") {
		t.Fatalf("units_limitation = %q", text)
	}
	if n := countUnitRows(response.Facts); n != 0 {
		t.Fatalf("served %d unit rows", n)
	}
	reason := investmentReason(response)
	if strings.Contains(reason, "pass the next_cursor") {
		t.Fatalf("coverage still promises a cursor: %q", reason)
	}
	if !strings.Contains(reason, "units not served under max_bytes 4096; minimum ") || !strings.Contains(reason, "raise max_bytes") {
		t.Fatalf("coverage reason = %q, want the limitation text", reason)
	}
	var minimum int
	if _, err := fmt.Sscanf(reason[strings.Index(reason, "; minimum ")+len("; minimum "):], "%d", &minimum); err != nil {
		t.Fatalf("no minimum in %q: %v", reason, err)
	}
	if minimum < MinMaxBytes-300 {
		t.Fatalf("minimum %d is not above the budget %d that failed", minimum, MinMaxBytes)
	}
	if response.Truncation == nil || !response.Truncation.CoverageOverBudget {
		t.Fatal("a document over max_bytes must say so in truncation.coverage_over_budget")
	}
	retry := readPageFact(t, mixPadFilling(t), minimum)
	retryPage, served := hasPageFact(retry)
	if !served {
		t.Fatal("no page fact at the stated minimum")
	}
	if _, limited := retryPage.Fields["units_limitation"]; limited {
		t.Fatalf("still a limitation row at the stated minimum %d", minimum)
	}
	if size := len(mustJSON(t, retry)); size > minimum {
		t.Fatalf("document is %d bytes at the stated minimum %d", size, minimum)
	}
	if retry.Truncation != nil && retry.Truncation.CoverageOverBudget {
		t.Fatal("flagged over budget at the stated minimum")
	}
	below := readPageFact(t, mixPadFilling(t), minimum-1)
	if _, ok := hasPageFact(below); !ok {
		t.Fatal("no page fact one byte below the minimum")
	}
	if size := len(mustJSON(t, below)); size > minimum-1 && (below.Truncation == nil || !below.Truncation.CoverageOverBudget) {
		t.Fatalf("document is %d bytes over max_bytes %d without the coverage_over_budget flag", size, minimum-1)
	}
}

// Just above mix + page fact + one row the page fact is served with a cursor.
func TestInvestmentUnitsPageFactIsReservedBeforeRows(t *testing.T) {
	pad := mixPadFilling(t)
	base := sizeWithout(t, pad, contextfabric.InvestmentUnitKind)
	oneRow := sizeWithout(t, pad) - base
	perRow := oneRow / 12
	response := readPageFact(t, pad, base+perRow+500)
	page, ok := hasPageFact(response)
	if !ok {
		t.Fatal("page fact not served although mix + page fit")
	}
	rows := countUnitRows(response.Facts)
	if rows == 0 || rows >= 12 {
		t.Fatalf("served %d rows, want a cut page", rows)
	}
	if _, has := page.Fields["next_cursor"]; !has {
		t.Fatal("cut page without next_cursor")
	}
	if !hasMixFact(response) {
		t.Fatal("mix fact missing")
	}
}
