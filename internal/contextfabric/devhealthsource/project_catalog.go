package devhealthsource

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12410: what the membership refresh reads of the project catalog.
//
// Membership resolution depends only on (provider, id, project_key)
// (resolvedProjectsSubquery), but projects.last_synced moves on every catalog
// write, whether or not the row changed. Stamping subjects with it re-emitted
// nearly every subject on every project sync. That stamp keeps its meaning
// ("a sync saw this row"), and the ownership cursor keeps reading it, so its
// self-healing stays. Only the refresh selection changes: a per-organization
// digest of those tuples, carried on the cursor, says whether the catalog
// changed at all.
//
//   - Digest unchanged: no refresh, and no presence read.
//   - Digest changed, or absent on a cursor that has a position: every subject
//     of the organization is stamped at refreshAt, one fixed time just past
//     the cursor. The digest cannot name the changed project, so this is the
//     whole set, once.
//   - From zero: the main tables state every subject, so nothing is owed.
//
// The digest and the position are one cursor value, so they commit together.
// A crash before the write leaves the old cursor: its digest mismatches and the
// refresh runs again. Everything that loses the digest (a cursor with no room,
// a cursor from before this field) reads as absent: a full refresh, never a
// skip. The failure direction is more work, never a missing change.

// projectCatalogMarker names the digest read.
const projectCatalogMarker = "AS project_catalog_digest"

// projectCatalogStatement is one aggregate row in bounded memory: an XOR of
// row hashes over the current rows, folded with their count, and the server's
// time, so refreshAt is on the clock the stamps are on.
const projectCatalogStatement = `SELECT toString(cityHash64(groupBitXor(sipHash64(provider, id, ifNull(project_key, ''))), count())) ` + projectCatalogMarker + `, now64(3, 'UTC')
FROM projects FINAL
WHERE org_id = {org_id:String}`

// projectCatalog is the cursor's record of the catalog the refresh followed.
type projectCatalog struct {
	digest    uint64
	refreshAt time.Time
}

// encode is the cursor field: as short as it can be, because a cursor is
// bounded and its row key needs the room.
func (c projectCatalog) encode() string {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], c.digest)
	encoded := base64.RawURLEncoding.EncodeToString(raw[:])
	if c.refreshAt.IsZero() {
		return encoded
	}
	return encoded + "." + strconv.FormatInt(c.refreshAt.UnixMilli(), 36)
}

// decodeProjectCatalog reports false for a field it cannot read: absent.
func decodeProjectCatalog(field string) (projectCatalog, bool) {
	head, tail, stamped := strings.Cut(field, ".")
	raw, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil || len(raw) != 8 {
		return projectCatalog{}, false
	}
	decoded := projectCatalog{digest: binary.BigEndian.Uint64(raw)}
	if stamped {
		milli, err := strconv.ParseInt(tail, 36, 64)
		if err != nil {
			return projectCatalog{}, false
		}
		decoded.refreshAt = time.UnixMilli(milli).UTC()
	}
	return decoded, true
}

// decideProjectCatalog is the whole rule. position is the cursor's position;
// fromZero says the main tables are stating every subject in this walk.
func decideProjectCatalog(field string, position time.Time, fromZero bool, digest uint64, now time.Time) (decided projectCatalog, reason string) {
	if fromZero {
		return projectCatalog{digest: digest}, ""
	}
	stored, ok := decodeProjectCatalog(field)
	if ok && stored.digest == digest {
		return stored, ""
	}
	reason = "catalog_changed"
	if !ok {
		reason = "digest_absent"
	}
	// Strictly past the position, or the keyset would leave the rows behind
	// the cursor and the refresh would be a skip.
	if !now.After(position) {
		now = position.Add(time.Millisecond)
	}
	return projectCatalog{digest: digest, refreshAt: now.Truncate(time.Millisecond).Add(time.Millisecond)}, reason
}

type projectRefreshKey struct{}

// withProjectRefresh gives this call's refresh reads the refresh stamp, so
// every read of the call sees it, whichever cursor value it runs from.
func withProjectRefresh(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, projectRefreshKey{}, at)
}

func projectRefreshFrom(ctx context.Context) time.Time {
	at, _ := ctx.Value(projectRefreshKey{}).(time.Time)
	return at
}

// projectRefreshArm is the arm of the membership change subquery: every
// subject of the organization at the refresh stamp. Empty when no refresh is
// owed, or when the cursor is already past it.
func projectRefreshArm(cursor cursorState, refreshAt time.Time) string {
	if refreshAt.IsZero() || cursor.Since.After(refreshAt) {
		return ""
	}
	return fmt.Sprintf(`
    UNION ALL
    SELECT m.repo_id AS repo_id, m.subject_id AS subject_id, fromUnixTimestamp64Milli(toInt64(%d), 'UTC') AS stamp
    FROM project_membership_presence AS m
    WHERE m.org_id = {org_id:String} AND m.subject_kind = 'work_item'`, refreshAt.UnixMilli())
}

func readProjectCatalogDigest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) (uint64, time.Time, error) {
	rows, err := client.Query(ctx, projectCatalogStatement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return 0, time.Time{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, time.Time{}, err
		}
		return 0, time.Time{}, fmt.Errorf("devhealthsource: the project catalog digest read returned no row")
	}
	var text string
	var now time.Time
	if err := rows.Scan(&text, &now); err != nil {
		return 0, time.Time{}, err
	}
	digest, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("devhealthsource: the project catalog digest does not parse: %w", err)
	}
	return digest, now.UTC(), rows.Err()
}

// projectCatalogFor reads the digest and decides this call's catalog. A failed
// read fails the call: nothing is skipped and nothing moves.
func (p sourcePlan) projectCatalogFor(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (projectCatalog, error) {
	orgID := strings.TrimSpace(checkpoint.OrgID)
	digest, now, err := readProjectCatalogDigest(ctx, p.client, orgID)
	if err != nil {
		return projectCatalog{}, fmt.Errorf("devhealthsource: read the project catalog digest: %w", err)
	}
	state, err := decodeCursor(checkpoint.Cursor)
	if err != nil {
		return projectCatalog{}, err
	}
	fromZero := state.Since.IsZero() || state.Dim != nil || state.Space != p.cursorSpace()
	decided, reason := decideProjectCatalog(state.ProjectCatalog, state.Since, fromZero, digest, now)
	if reason != "" {
		slog.InfoContext(ctx, "context fabric: membership refresh of every subject owed", "reason", reason)
	}
	return decided, nil
}

// carried writes the catalog on a cursor this call hands back. A cursor with
// no room keeps its position and drops the field, which reads as absent: one
// more full refresh, never a skip. A dimension cursor has no room by design.
func (c projectCatalog) carried(ctx context.Context, cursor string) string {
	state, err := decodeCursor(cursor)
	if err != nil || state.Dim != nil || state.Space == "" {
		return cursor
	}
	state.ProjectCatalog = c.encode()
	next, err := encodeCursorIn(state.Space, state)
	if err != nil {
		return cursor
	}
	if len(next) > contractsv1.ContextFabricProjectionCursorMaxLength {
		slog.WarnContext(ctx, "context fabric: no cursor room for the project catalog digest, a full membership refresh follows")
		return cursor
	}
	return next
}
