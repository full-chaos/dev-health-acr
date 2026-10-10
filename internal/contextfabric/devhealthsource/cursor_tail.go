package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// cursorSentinelKey sorts after every row key a source can mint. A cursor
// (since, After=cursorSentinelKey) passes the whole timestamp: a row key is
// real text, and no key begins with four U+10FFFF characters. It is a plain
// string, so a projector that predates it decodes and keysets it the same way.
const cursorSentinelKey = "\U0010FFFF\U0010FFFF\U0010FFFF\U0010FFFF"

// cursorAckHeadroom reserves the bytes an overlap batch's Ack adds to the same
// cursor, so one limit holds for every cursor this package encodes.
const cursorAckHeadroom = 16

const quarantineOversizeCursorKey = "oversize_cursor_key"

// errCursorKeyTooLong is the unreachable guard: a page tail whose key cannot
// be carried by the cursor and was neither passed over nor cut.
var errCursorKeyTooLong = fmt.Errorf("devhealthsource: the last row key exceeds the %d byte cursor bound", contractsv1.ContextFabricProjectionCursorMaxLength)

// cursorKeyFits reports whether a cursor at (at, key) is within the contract's
// cursor bound, measured with the encoder that produces it.
func cursorKeyFits(space string, at time.Time, key string) bool {
	encoded, err := encodeCursorIn(space, cursorState{Since: at, After: key, Ack: strings.Repeat("0", cursorAckHeadroom)})
	return err == nil && len(encoded) <= contractsv1.ContextFabricProjectionCursorMaxLength
}

// maxCursorKeyBytes is the longest plain ASCII row key that still fits.
func maxCursorKeyBytes(space string, at time.Time) int {
	low, high := 0, contractsv1.ContextFabricProjectionCursorMaxLength
	for low < high {
		mid := (low + high + 1) / 2
		if cursorKeyFits(space, at, strings.Repeat("a", mid)) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

// encodeTailCursor encodes the cursor that lands on last: a row marked passOver
// lands after its whole timestamp.
func encodeTailCursor(space string, last candidate) (string, error) {
	state := cursorState{Since: last.position(), After: last.sortKey}
	if last.passOver {
		state.After = cursorSentinelKey
	} else if !cursorKeyFits(space, state.Since, state.After) {
		return "", errCursorKeyTooLong
	}
	return encodeCursorIn(space, state)
}

// fitCursorTail makes the page's last row cursor-safe.
//
// A page that was read to its end (ahead false) holds every row after its
// starting cursor, so a last row whose key cannot be carried is passed over:
// it is marked and its cursor lands after its whole timestamp, with nothing
// quarantined. A page with rows beyond it cuts the trailing over-long rows
// instead; the next page starts at them and they are no longer its tail.
// cut counts the candidates removed.
func fitCursorTail(space string, all []candidate, ahead bool) (kept []candidate, cut int, passed bool) {
	n := len(all)
	if n == 0 || cursorKeyFits(space, all[n-1].position(), all[n-1].sortKey) {
		return all, 0, false
	}
	if !ahead {
		all[n-1].passOver = true
		return all, 0, true
	}
	end := n
	for end > 0 && !cursorKeyFits(space, all[end-1].position(), all[end-1].sortKey) {
		end--
	}
	return all[:end], n - end, false
}

func (p sourcePlan) logCursorKeyPass(ctx context.Context, orgID string, last candidate, action string) {
	if p.logger == nil {
		return
	}
	p.logger.WarnContext(ctx, "devhealthsource row key exceeds the projection cursor bound",
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"action", contextfabric.SanitizeLogAttr(action), "limit_bytes", contractsv1.ContextFabricProjectionCursorMaxLength,
		"max_key_bytes", maxCursorKeyBytes(p.cursorSpace(), last.position()), "key_bytes", len(last.sortKey),
		"key_digest", contextfabric.SanitizeLogAttr(keyDigest(last.sortKey)))
}

// quarantineOversizeCursorKeyRows reports every projectable item of rows as
// dropped for a key the cursor cannot carry. Progress markers carry no item.
func quarantineOversizeCursorKeyRows(rows []candidate, observe func(quarantineObservation)) {
	if observe == nil {
		return
	}
	for _, c := range rows {
		if kind, _ := validateCandidateItem(c); kind != "" {
			observe(quarantineObservation{Reason: quarantineOversizeCursorKey, Kind: kind})
		}
	}
}
