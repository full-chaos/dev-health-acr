package pgprojection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OrgPurgeMarkerStore is the production projectionrun.OrgPurgeMarker. A row
// in context_fabric_org_purge_markers means `acr-projector purge` removed the
// organization and it has not returned to discovery since.
type OrgPurgeMarkerStore struct {
	db *sql.DB
}

func NewOrgPurgeMarkerStore(db *sql.DB) (*OrgPurgeMarkerStore, error) {
	if db == nil {
		return nil, errors.New("pgprojection: org purge marker store requires a database")
	}
	return &OrgPurgeMarkerStore{db: db}, nil
}

// canonicalOrg maps any UUID spelling to the lowercase hyphenated form the
// purge writes. A non-UUID id cannot have been purged, so ok is false.
func canonicalOrg(orgID string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(orgID))
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func (s *OrgPurgeMarkerStore) IsPurged(ctx context.Context, orgID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("pgprojection: org purge marker store is not configured")
	}
	canonical, ok := canonicalOrg(orgID)
	if !ok {
		return false, nil
	}
	var purged bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = $1)`, canonical).Scan(&purged); err != nil {
		return false, fmt.Errorf("read org purge marker: %w", sanitizeError(err))
	}
	return purged, nil
}

// DiscoveryClock reads the database clock. The purge stamps its marker with
// the same clock, so comparing the two never depends on host clock skew.
func (s *OrgPurgeMarkerStore) DiscoveryClock(ctx context.Context) (time.Time, error) {
	if s == nil || s.db == nil {
		return time.Time{}, errors.New("pgprojection: org purge marker store is not configured")
	}
	var now time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("read discovery clock: %w", sanitizeError(err))
	}
	return now, nil
}

// ClearPurged removes the markers of the given organizations that were written
// before discoveryStarted and returns the canonical ids whose marker was
// removed. A marker written after the discovery began stays.
func (s *OrgPurgeMarkerStore) ClearPurged(ctx context.Context, orgIDs []string, discoveryStarted time.Time) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pgprojection: org purge marker store is not configured")
	}
	canonical := make([]string, 0, len(orgIDs))
	for _, orgID := range orgIDs {
		if id, ok := canonicalOrg(orgID); ok {
			canonical = append(canonical, id)
		}
	}
	if len(canonical) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `DELETE FROM acr.context_fabric_org_purge_markers WHERE org_id = ANY($1) AND purged_at < $2 RETURNING org_id`, canonical, discoveryStarted)
	if err != nil {
		return nil, fmt.Errorf("clear org purge markers: %w", sanitizeError(err))
	}
	defer rows.Close()
	var cleared []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cleared org purge marker: %w", sanitizeError(err))
		}
		cleared = append(cleared, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clear org purge markers: %w", sanitizeError(err))
	}
	return cleared, nil
}
