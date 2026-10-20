package devhealthsource_test

import (
	"testing"
	"time"
)

func TestARemovedTeamCarriesTheTimeItWasProvedGoneAndStaysActive(t *testing.T) {
	t.Parallel()
	updated := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	const goneAt = "2026-10-09 17:30:00.000000"
	client := &fakeClient{tables: []fakeTable{
		{match: "ifNull(toString(tm.gone_at), '')", rows: [][]any{
			{"gh:gone-team", "Gone Team", "", "github", "gone-team", uint8(1), goneAt, updated, []string(nil), []string(nil)},
			{"gh:live-team", "Live Team", "", "github", "live-team", uint8(1), "", updated, []string(nil), []string(nil)},
		}},
	}}
	batch := teamsProjectsBatch(t, client)

	gone := entityByCanonicalID(t, batch, "team:gh:gone-team")
	if got := gone.Properties["gone_at"].String; got == nil || *got != goneAt {
		t.Fatalf("gone team gone_at property = %+v, want %q", gone.Properties["gone_at"], goneAt)
	}
	if active := gone.Properties["is_active"].Boolean; active == nil || !*active {
		t.Fatalf("a removed team keeps is_active=true, got %+v", gone.Properties["is_active"])
	}
	if gone.ValidTo != nil {
		t.Fatalf("a removed team's validity window stays open, got ValidTo=%v", gone.ValidTo)
	}
	live := entityByCanonicalID(t, batch, "team:gh:live-team")
	if !live.Properties["gone_at"].Null {
		t.Fatalf("a team not proved gone carries an explicit null gone_at so a re-projection clears a stale one, got %+v", live.Properties["gone_at"])
	}
}
