package contextfabric

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestEveryGroupStageLineCarriesTheRequestID pins the JOIN KEY of the group
// stage's Info lines.
//
// The decision graph is rebuilt from the trace by joining a turn's lines on
// request_id: the frame-validation line, the fact reads and the model
// decisions all carry it. The four lines this change adds did not, and on the
// private pair that made one `group_read_refusal=no_read_requirement` line
// unattributable -- six group-read lines from one corpus run, and nothing on
// any of them saying which question it decided. A line that cannot be joined
// to its turn cannot be part of that turn's decision graph.
//
// One turn drives all four: six members in two groups under a budget that
// clamps the allowance, so stage 2 narrows and retention runs.
//
// NOT t.Parallel(): it installs the process default logger.
func TestEveryGroupStageLineCarriesTheRequestID(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := groupReadServing("team_security", "team_platform")
	memberFacts := make([]CanonicalFact, 0, 6)
	cohortMembers := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		team := "team_security"
		if index >= 3 {
			team = "team_platform"
		}
		memberFacts = append(memberFacts, teamScopedFact(id, team, team))
		cohortMembers = append(cohortMembers, CohortMember{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1, InclusionReasons: []string{"matched"}})
	}
	serving := recorder.facts
	recorder.facts = func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := serving(request)
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				return bundle
			}
		}
		bundle.Facts = memberFacts
		return bundle
	}
	options := EngineOptions{MaxItems: 6, SynthesisDeadlineReserve: time.Second}
	engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, cohortMembers, nil, SubjectProject, &options, nil)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	const want = "req_0123456789abcdef0123456789abcdef"
	for _, message := range []string{
		"context fabric cohort group read",
		"context fabric group read coverage state",
		"context fabric cohort member allowance",
		"context fabric fact retention",
	} {
		lines := linesWithMessage(t, logs.configured.String(), message)
		if len(lines) == 0 {
			t.Errorf("%q: no line on this turn, so its join key cannot be checked -- the fixture must reach it", message)
			continue
		}
		for _, line := range lines {
			if got := line["request_id"]; got != want {
				t.Errorf("%q: request_id = %v, want %q -- a line that cannot be joined to its turn is not part of that turn's decision graph", message, got, want)
			}
		}
		t.Logf("%q: %d line(s), request_id=%v", message, len(lines), lines[0]["request_id"])
	}
}
