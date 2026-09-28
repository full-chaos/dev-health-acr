package directread

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
)

// CursorOutcome re-exports the cursor vocabulary.
type CursorOutcome = gatevocab.CursorOutcome

const (
	CursorIssued     = gatevocab.CursorIssued
	CursorAccepted   = gatevocab.CursorAccepted
	CursorExpired    = gatevocab.CursorExpired
	CursorStale      = gatevocab.CursorStale
	CursorInvalid    = gatevocab.CursorInvalid
	CursorForeignOrg = gatevocab.CursorForeignOrg
)

// relationshipsCursorVersion is the only cursor version this build reads.
const relationshipsCursorVersion = 1

// RelationshipsCursorTTL is how long a read_relationships cursor may be
// presented after its page was served.
const RelationshipsCursorTTL = 15 * time.Minute

// relationshipsCursor is the position of the next page. It is NOT a
// permission: every page runs the subject gate on the root and on every end
// node again (design E.1: no step uses a cursor as permission). So it is not
// signed; a tampered cursor can only move the position inside a walk the
// caller is already allowed to read. It is bound to the organization, to the
// request (so a cursor cannot continue a different walk), to a version and to
// an issue time.
type relationshipsCursor struct {
	Version       int     `json:"v"`
	Hop           int     `json:"h"`
	After         EdgeKey `json:"a"`
	OrgDigest     string  `json:"o"`
	RequestDigest string  `json:"q"`
	IssuedAtUnix  int64   `json:"t"`
}

// cursorError carries the outcome a refused cursor records.
type cursorError struct{ outcome CursorOutcome }

func (e *cursorError) Error() string { return "read_relationships cursor " + string(e.outcome) }

func cursorOutcomeOf(err error) (CursorOutcome, bool) {
	var ce *cursorError
	if errors.As(err, &ce) {
		return ce.outcome, true
	}
	return "", false
}

func orgDigest(orgID string) string {
	sum := sha256.Sum256([]byte("acr-data.v1/org\x00" + orgID))
	return hex.EncodeToString(sum[:12])
}

func encodeRelationshipsCursor(cursor relationshipsCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		// A struct of strings and ints always marshals.
		panic("read_relationships cursor encode: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeRelationshipsCursor checks, in this order: shape and version, the
// organization, the request, the expiry, the hop. Each refusal has its own
// outcome for the trace; the caller sees invalid_request.
func decodeRelationshipsCursor(token, orgID, requestDigest string, depth int, now time.Time) (relationshipsCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return relationshipsCursor{}, &cursorError{CursorInvalid}
	}
	var cursor relationshipsCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != relationshipsCursorVersion {
		return relationshipsCursor{}, &cursorError{CursorInvalid}
	}
	if cursor.OrgDigest != orgDigest(orgID) {
		return relationshipsCursor{}, &cursorError{CursorForeignOrg}
	}
	if cursor.RequestDigest != requestDigest {
		return relationshipsCursor{}, &cursorError{CursorStale}
	}
	issued := time.Unix(cursor.IssuedAtUnix, 0)
	if !now.Before(issued.Add(RelationshipsCursorTTL)) || issued.After(now.Add(time.Minute)) {
		return relationshipsCursor{}, &cursorError{CursorExpired}
	}
	// A hop-1 cursor always carries a position; a hop-2 cursor may be the
	// start of hop 2 (no position yet).
	if cursor.Hop < 1 || cursor.Hop > depth || (cursor.Hop == 1 && cursor.After.RelationshipID == "") {
		return relationshipsCursor{}, &cursorError{CursorInvalid}
	}
	return cursor, nil
}
