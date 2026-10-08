package directread

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The units page cursor leaves the server sealed. The position a provider
// reports is the last row it READ, which may be a row the caller may not see;
// sealing keeps that row's id, share and repository from the caller, and the
// authentication binds the cursor to the organization, the subject and the
// window it was issued for.

// UnitsCursorKeyLabel domain-separates the units cursor key from every other
// use of the evidence keyring.
const UnitsCursorKeyLabel = "acr-data.v1/read_facts/units-cursor"

const unitsCursorVersion = 1

type unitsCursorPayload struct {
	Version int    `json:"v"`
	Org     string `json:"o"`
	Request string `json:"q"`
	// Position is the provider's keyset token, empty for the start.
	Position string `json:"p"`
}

// WithCursorKeyring lets the reader serve the units argument. Without a usable
// keyring a units request answers unavailable: an unsealed cursor is never
// served.
func (r *FactsReader) WithCursorKeyring(keyring CursorKeyring) *FactsReader {
	if r == nil {
		return nil
	}
	sealer, err := newLabeledCursorSealer(keyring, UnitsCursorKeyLabel)
	if err != nil {
		r.unitsSealer = nil
		return r
	}
	r.unitsSealer = sealer
	return r
}

// unitsRequestDigest names what a cursor is bound to: the one subject and the
// window as the caller asked for it (a trailing window moves with the clock, so
// the effective bounds cannot be part of it).
func unitsRequestDigest(request FactsRequest, plan readPlan) string {
	h := sha256.New()
	fmt.Fprintf(h, "units\x00%s\x00%s", plan.subjects[0].Kind, plan.subjects[0].CanonicalID)
	if w := request.Window; w != nil {
		fmt.Fprintf(h, "\x00%s\x00%d", w.Mode, w.Days)
		for _, t := range []*time.Time{w.AsOf, w.Start, w.End} {
			if t == nil {
				fmt.Fprint(h, "\x00-")
				continue
			}
			fmt.Fprintf(h, "\x00%s", t.UTC().Format(time.RFC3339Nano))
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (r *FactsReader) sealUnitsCursor(orgID, digest, position string) (string, error) {
	raw, err := json.Marshal(unitsCursorPayload{Version: unitsCursorVersion, Org: orgDigest(orgID), Request: digest, Position: position})
	if err != nil {
		return "", err
	}
	return r.unitsSealer.seal(raw)
}

// openUnitsCursor returns the provider position a token issued for this
// organization and request holds ("" is the start).
func (r *FactsReader) openUnitsCursor(token, orgID, digest string) (string, error) {
	raw, err := r.unitsSealer.open(token)
	if err != nil {
		return "", err
	}
	var payload unitsCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Version != unitsCursorVersion {
		return "", &cursorError{CursorInvalid}
	}
	if payload.Org != orgDigest(orgID) {
		return "", &cursorError{CursorForeignOrg}
	}
	if payload.Request != digest {
		return "", &cursorError{CursorStale}
	}
	return payload.Position, nil
}

// unitFactKind returns the unit_kind of a served investment fact, or "".
func unitFactKind(fact ServedFact) string {
	if fact.Kind != string(contextfabric.FactInvestment) {
		return ""
	}
	kind, _ := fact.Fields["unit_kind"].(string)
	return kind
}

// unitPositionOf is the keyset position of a served unit row.
func unitPositionOf(fact ServedFact) (string, bool) {
	share, ok := fact.Fields["share_in_scope"].(float64)
	id, okID := fact.Fields["work_unit_id"].(string)
	repo, okRepo := fact.Fields["repository_id"].(string)
	if !ok || !okID || !okRepo || id == "" || repo == "" {
		return "", false
	}
	return contextfabric.EncodeInvestmentUnitsCursor(contextfabric.InvestmentUnitsCursor{Share: share, WorkUnitID: id, RepoID: repo}), true
}

// finishUnitsPage replaces the provider's plain cursor with a sealed one and
// keeps the cursor honest when the byte budget dropped unit rows after the
// provider chose it: the cursor then resumes after the last row actually
// served. It returns how many unit rows the budget dropped.
func (r *FactsReader) finishUnitsPage(response *FactsResponse, orgID, digest, incoming string, rowsBeforeBudget int) (budgetDropped int, err error) {
	pageIndex, kept := -1, 0
	var last *ServedFact
	for i := range response.Facts {
		switch unitFactKind(response.Facts[i]) {
		case contextfabric.InvestmentUnitPageKind:
			pageIndex = i
		case contextfabric.InvestmentUnitKind:
			kept++
			last = &response.Facts[i]
		}
	}
	budgetDropped = rowsBeforeBudget - kept
	if pageIndex < 0 {
		return budgetDropped, nil
	}
	fields := response.Facts[pageIndex].Fields
	position, more := "", false
	if token, ok := fields["next_cursor"].(string); ok && token != "" {
		position, more = token, true
	}
	if budgetDropped > 0 {
		more = true
		position = incoming
		if last != nil {
			if p, ok := unitPositionOf(*last); ok {
				position = p
			}
		}
		fields["units_returned"] = strconv.Itoa(kept)
	}
	if !more {
		delete(fields, "next_cursor")
		return budgetDropped, nil
	}
	sealed, err := r.sealUnitsCursor(orgID, digest, position)
	if err != nil {
		delete(fields, "next_cursor")
		return budgetDropped, err
	}
	fields["next_cursor"] = sealed
	return budgetDropped, nil
}

func countUnitRows(facts []ServedFact) int {
	n := 0
	for _, fact := range facts {
		if unitFactKind(fact) == contextfabric.InvestmentUnitKind {
			n++
		}
	}
	return n
}

// orderUnitsFacts puts the units listing in the order the cursor walks: the
// other facts first, then the page fact, then the unit rows by share
// descending, work unit, repository. The registry sorts facts by its own key,
// so the order is restored here; it makes the byte budget (which drops from
// the end) cut the keyset tail, never the page fact and never a row in the
// middle.
func orderUnitsFacts(facts []ServedFact) []ServedFact {
	var others, pages, rows []ServedFact
	for _, fact := range facts {
		switch unitFactKind(fact) {
		case contextfabric.InvestmentUnitPageKind:
			pages = append(pages, fact)
		case contextfabric.InvestmentUnitKind:
			rows = append(rows, fact)
		default:
			others = append(others, fact)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		si, _ := rows[i].Fields["share_in_scope"].(float64)
		sj, _ := rows[j].Fields["share_in_scope"].(float64)
		if si != sj {
			return si > sj
		}
		ii, _ := rows[i].Fields["work_unit_id"].(string)
		ij, _ := rows[j].Fields["work_unit_id"].(string)
		if ii != ij {
			return ii < ij
		}
		ri, _ := rows[i].Fields["repository_id"].(string)
		rj, _ := rows[j].Fields["repository_id"].(string)
		return ri < rj
	})
	out := make([]ServedFact, 0, len(facts))
	out = append(out, others...)
	out = append(out, pages...)
	return append(out, rows...)
}
