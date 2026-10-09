package devhealthfacts

import (
	"testing"

	"github.com/full-chaos/dev-health-go/readers"
)

func TestDropRetractionReadinessRows(t *testing.T) {
	live := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "team:linear:a", EstimatedCount: 3, UnestimatedCount: 1, BacklogSize: 4, HasRatio: 1, Ratio: 0.75}
	retraction := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "a"}
	unattributed := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 0}
	liveZeroWithRatio := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "b", HasRatio: 1}
	retired := readers.ReadinessProjectRow{ProjectSubjectKey: "p", HasTeam: 1, TeamID: "old", EstimatedCount: 7, HasRatio: 1, Ratio: 1}
	rows := []readers.ReadinessProjectRow{live, retraction, unattributed, liveZeroWithRatio, retired}
	got := dropRetractionReadinessRows(rows, map[string]bool{"old": true})
	if len(got) != 3 || got[0].TeamID != "team:linear:a" || got[1].HasTeam != 0 || got[2].TeamID != "b" {
		t.Fatalf("kept %+v, want the live, unattributed and ratio-bearing rows only", got)
	}
	if got := dropRetractionReadinessRows(rows, nil); len(got) != 4 {
		t.Fatalf("without an inactive set kept %d rows, want 4 (only the all-zero row drops)", len(got))
	}
}
