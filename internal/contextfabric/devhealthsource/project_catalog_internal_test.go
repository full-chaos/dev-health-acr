package devhealthsource

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestTheCatalogFieldRoundTrips(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 12, 0, 0, 5_000_000, time.UTC)
	for _, c := range []projectCatalog{{digest: 0, refreshAt: at}, {digest: 1<<64 - 1, refreshAt: at}, {digest: 42, refreshAt: at, owed: true}} {
		got, ok := decodeProjectCatalog(c.encode())
		if !ok || got.digest != c.digest || !got.refreshAt.Equal(c.refreshAt) || got.owed != c.owed {
			t.Errorf("%+v came back as %+v, %v", c, got, ok)
		}
	}
	for _, field := range []string{"", "x", "AAAA", "AAAA.1", "AAAAAAAAAAA", "AAAAAAAAAAA.$$", "AAAAAAAAAAA~$$", "not base64!"} {
		if _, ok := decodeProjectCatalog(field); ok {
			t.Errorf("%q decoded", field)
		}
	}
}

func TestTheCatalogDecision(t *testing.T) {
	t.Parallel()
	position := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	now := position.Add(time.Hour)
	walked := projectCatalog{digest: 7, refreshAt: position.Add(-time.Minute)}.encode()
	owed := projectCatalog{digest: 7, refreshAt: position.Add(-time.Minute), owed: true}.encode()
	aDayOld := projectCatalog{digest: 7, refreshAt: now.Add(-projectCatalogFullRefreshEvery)}.encode()
	nearlyADayOld := projectCatalog{digest: 7, refreshAt: now.Add(-projectCatalogFullRefreshEvery + time.Millisecond)}.encode()
	for name, tc := range map[string]struct {
		field      string
		fromZero   bool
		digest     uint64
		wantReason string
		wantOwed   bool
	}{
		"from zero owes nothing":         {"", true, 7, "", false},
		"from zero ignores an old field": {walked, true, 8, "", false},
		"same digest":                    {walked, false, 7, "", false},
		"same digest keeps what is owed": {owed, false, 7, "", true},
		"a changed digest":               {walked, false, 8, "catalog_changed", true},
		"an absent digest":               {"", false, 7, "digest_absent", true},
		"an absent digest of zero":       {"", false, 0, "digest_absent", true},
		"an unreadable digest":           {"x", false, 7, "digest_absent", true},
		"a day old":                      {aDayOld, false, 7, "daily", true},
		"not yet a day old":              {nearlyADayOld, false, 7, "", false},
	} {
		got, reason := decideProjectCatalog(tc.field, position, tc.fromZero, tc.digest, now)
		if reason != tc.wantReason || got.digest != tc.digest || got.owed != tc.wantOwed || got.refreshAt.IsZero() {
			t.Errorf("%s: %+v %q", name, got, reason)
		}
		if got.owedAt().IsZero() == tc.wantOwed {
			t.Errorf("%s: owedAt = %v", name, got.owedAt())
		}
	}
	// A new refresh is strictly past the position, whatever the clock says: at
	// or behind it, the keyset would leave its rows behind the cursor.
	for _, at := range []time.Time{position.Add(-time.Hour), position, position.Add(time.Microsecond)} {
		got, _ := decideProjectCatalog("", position, false, 7, at)
		if !got.refreshAt.After(position) {
			t.Errorf("clock %v: refresh at %v is not past the position", at, got.refreshAt)
		}
	}
}

func TestTheRefreshArmRunsOnlyWhileARefreshIsOwed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, since := range []time.Time{{}, at} {
		if got := projectRefreshArm(cursorState{Since: since}, time.Time{}); got != "" {
			t.Errorf("no refresh owed, since %v: %q", since, got)
		}
	}
	if got := projectRefreshArm(cursorState{Since: at.Add(time.Millisecond)}, at); got != "" {
		t.Errorf("the cursor is past it: %q", got)
	}
	for _, since := range []time.Time{{}, at.Add(-time.Hour), at} {
		if got := projectRefreshArm(cursorState{Since: since}, at); !strings.Contains(got, "project_membership_presence") {
			t.Errorf("since %v: no arm", since)
		}
	}
	// With no refresh, the change subquery reads no presence row at all.
	if got := workItemMembershipChangesSubquery(cursorState{Since: at}, time.Time{}); strings.Contains(got, "project_membership_presence") || strings.Contains(got, "FROM projects") {
		t.Errorf("a quiet subquery still reads the catalog: %s", got)
	}
}

func TestACursorCarriesTheCatalogOrDropsItForRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	catalog := projectCatalog{digest: 9, refreshAt: at, owed: true}
	cursor, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: at, After: "k", Ack: "ack"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeCursor(catalog.carried(ctx, cursor))
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectCatalog != catalog.encode() || !state.Since.Equal(at) || state.After != "k" || state.Ack != "ack" || state.Space != cursorSpaceIngest {
		t.Fatalf("carried = %+v", state)
	}
	// No room: the position survives and the field is gone, which reads as
	// absent, so one more full refresh follows.
	long, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: at, After: strings.Repeat("k", maxCursorKeyBytes(cursorSpaceIngest, at))})
	if err != nil || len(long) > contractsv1.ContextFabricProjectionCursorMaxLength {
		t.Fatalf("premise: %d, %v", len(long), err)
	}
	if got := catalog.carried(ctx, long); got != long {
		t.Fatalf("a full cursor changed: %d bytes", len(got))
	}
	// A dimension cursor is never written.
	dimension, _, err := dimensionPosition{Since: at, After: "k"}.encode(cursorSpaceIngest)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.carried(ctx, dimension); got != dimension {
		t.Fatal("a dimension cursor changed")
	}
}
