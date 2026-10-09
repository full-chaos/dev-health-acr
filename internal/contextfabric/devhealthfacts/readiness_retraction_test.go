package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-go/readers"
)

func TestDropInactiveTeamReadinessRows(t *testing.T) {
	live := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "team:linear:a", EstimatedCount: 3, UnestimatedCount: 1, BacklogSize: 4, HasRatio: 1, Ratio: 0.75}
	retraction := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "a"}
	measuredEmpty := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "team:linear:b"}
	unattributed := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 0}
	rows := []readers.ReadinessProjectRow{live, retraction, measuredEmpty, unattributed}
	got := dropInactiveTeamReadinessRows(rows, map[string]bool{"a": true})
	if len(got) != 3 || got[0].TeamID != "team:linear:a" || got[1].TeamID != "team:linear:b" || got[2].HasTeam != 0 {
		t.Fatalf("kept %+v, want the live row, the measured empty backlog of an active team, and the unattributed row", got)
	}
	if got := dropInactiveTeamReadinessRows(rows, nil); len(got) != 4 {
		t.Fatalf("without an inactive set kept %d rows, want 4", len(got))
	}
}
