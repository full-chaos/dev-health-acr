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
	// A position behind the stamp of a refresh that is older than a day: the
	// cursor lags, so the stamp stays and only the digest follows.
	lagging := now.Add(-48 * time.Hour)
	pendingADayOld := projectCatalog{digest: 7, refreshAt: now.Add(-projectCatalogFullRefreshEvery), owed: true}.encode()
	passedADayOld := projectCatalog{digest: 7, refreshAt: lagging.Add(-time.Hour), owed: true}.encode()
	// The pass has begun at the stamp, so a new catalog needs a new stamp.
	onItsStamp := projectCatalog{digest: 7, refreshAt: position, owed: true}.encode()
	walkedAhead := projectCatalog{digest: 7, refreshAt: position.Add(time.Minute)}.encode()
	for name, tc := range map[string]struct {
		field      string
		fromZero   bool
		digest     uint64
		wantReason string
		wantOwed   bool
	}{
		"from zero owes nothing":                           {"", true, 7, "", false},
		"from zero ignores an old field":                   {walked, true, 8, "", false},
		"same digest":                                      {walked, false, 7, "", false},
		"same digest keeps what is owed":                   {owed, false, 7, "", true},
		"a changed digest":                                 {walked, false, 8, "catalog_changed", true},
		"an absent digest":                                 {"", false, 7, "digest_absent", true},
		"an absent digest of zero":                         {"", false, 0, "digest_absent", true},
		"an unreadable digest":                             {"x", false, 7, "digest_absent", true},
		"a day old":                                        {aDayOld, false, 7, "daily", true},
		"not yet a day old":                                {nearlyADayOld, false, 7, "", false},
		"a walk ahead of the position is not a refresh":    {walkedAhead, false, 8, "catalog_changed", true},
		"a pending refresh, a day old":                     {pendingADayOld, false, 7, "", true},
		"a pending refresh, a new digest":                  {pendingADayOld, false, 8, "", true},
		"a refresh the cursor stands on, a new digest":     {onItsStamp, false, 8, "catalog_changed", true},
		"an owed refresh the cursor has passed, a day old": {passedADayOld, false, 7, "daily", true},
	} {
		at := position
		if tc.field == pendingADayOld || tc.field == passedADayOld {
			at = lagging
		}
		got, reason := decideProjectCatalog(tc.field, at, tc.fromZero, tc.digest, now)
		if reason != tc.wantReason || got.digest != tc.digest || got.owed != tc.wantOwed || got.refreshAt.IsZero() {
			t.Errorf("%s: %+v %q", name, got, reason)
		}
		if got.owedAt().IsZero() == tc.wantOwed {
			t.Errorf("%s: owedAt = %v", name, got.owedAt())
		}
	}
	// The stamp of a pending refresh does not move: a new one would chase the
	// clock.
	if got, _ := decideProjectCatalog(pendingADayOld, lagging, false, 8, now); !got.refreshAt.Equal(now.Add(-projectCatalogFullRefreshEvery)) {
		t.Errorf("a pending refresh moved to %v", got.refreshAt)
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
		got := projectRefreshArm(cursorState{Since: since}, at)
		if !strings.Contains(got, "project_membership_presence") {
			t.Errorf("since %v: no arm", since)
		}
		// A final REMOVE leaves no presence row, so the history must be read.
		if !strings.Contains(got, "FROM project_membership_transitions") {
			t.Errorf("since %v: the refresh does not read the transition history", since)
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
	// The longest key a cursor takes still has room for the field, so it does
	// not drop and restart a pass.
	reserved, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: at, After: strings.Repeat("k", maxCursorKeyBytes(cursorSpaceIngest, at))})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := decodeCursor(catalog.carried(ctx, reserved)); err != nil || state.ProjectCatalog == "" {
		t.Fatalf("the longest key lost the field: %+v, %v", state, err)
	}
	// With no room all the same, the position survives and the field is gone,
	// which reads as absent, so one more full refresh follows.
	long := ""
	for extra := 1; long == ""; extra++ {
		key := strings.Repeat("k", maxCursorKeyBytes(cursorSpaceIngest, at)+extra)
		plain, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: at, After: key})
		if err != nil || len(plain) > contractsv1.ContextFabricProjectionCursorMaxLength {
			t.Fatalf("premise: no key fits without the field and not with it: %d, %v", len(plain), err)
		}
		if with, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: at, After: key, ProjectCatalog: catalog.encode()}); err == nil && len(with) > contractsv1.ContextFabricProjectionCursorMaxLength {
			long = plain
		}
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
