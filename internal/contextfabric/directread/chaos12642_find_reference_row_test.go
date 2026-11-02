package directread

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12642 (codex r1 P2): find_subjects handle mode reads a GitHub
// owner/repo#N reference by stored rows too: a pull request row with that
// repository and number is found as the pull request, not as its ghpr work item
// mirror; else the work item reading stands. A "!" reference is never read as a
// pull request.
func TestFindSubjectsResolvesAReferenceToTheRowItNames(t *testing.T) {
	pr := subject(contractsv1.ContextFabricSubjectPullRequest, "pull_request:r-widget:8")
	mirror := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:r-widget:ghpr:acme/widget#8")
	issue := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:r-widget:gh:acme/widget#7")
	merge := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:r-group:gitlab:group/project!8")
	rows := map[string]string{
		string(contractsv1.ContextFabricSubjectPullRequest) + "|acme/widget#8": pr.CanonicalID,
		string(contractsv1.ContextFabricSubjectWorkItem) + "|acme/widget#8":    mirror.CanonicalID,
		string(contractsv1.ContextFabricSubjectWorkItem) + "|acme/widget#7":    issue.CanonicalID,
		string(contractsv1.ContextFabricSubjectWorkItem) + "|group/project!8":  merge.CanonicalID,
	}
	for _, tc := range []struct {
		handle, kind, id string
		reads            []graphrank.CensusKind
		row              string
	}{
		{"acme/widget#8", string(contractsv1.ContextFabricSubjectPullRequest), pr.CanonicalID, []graphrank.CensusKind{contextfabric.SubjectPullRequest, contextfabric.SubjectPullRequest}, "pull_request_row"},
		{"acme/widget#7", string(contractsv1.ContextFabricSubjectWorkItem), issue.CanonicalID, []graphrank.CensusKind{contextfabric.SubjectPullRequest, contextfabric.SubjectWorkItem}, "work_item_reading"},
		{"acme/widget#9", string(contractsv1.ContextFabricSubjectWorkItem), "", []graphrank.CensusKind{contextfabric.SubjectPullRequest, contextfabric.SubjectWorkItem}, "work_item_reading"},
		{"group/project!8", string(contractsv1.ContextFabricSubjectWorkItem), merge.CanonicalID, []graphrank.CensusKind{contextfabric.SubjectWorkItem}, ""},
	} {
		t.Run(tc.handle, func(t *testing.T) {
			g := handleGraph()
			for _, s := range []contextfabric.SubjectRef{pr, mirror, issue, merge} {
				g.nodes[graphrank.SubjectKey(s)] = repos("acme/widget")
			}
			var reads []graphrank.CensusKind
			census := func(_ context.Context, _ string, kind graphrank.CensusKind, value string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (graphrank.CensusOutcome, error) {
				reads = append(reads, kind)
				if id, ok := rows[string(kind)+"|"+value]; ok {
					return graphrank.CensusOutcome{Count: 1, SatisfierCanonicalID: id}, nil
				}
				return graphrank.CensusOutcome{}, nil
			}
			recorder := &findRowRecorder{}
			lookup := newModesLookup(g, census)
			lookup.recorder = recorder
			response, err := lookup.Find(relCtx("12642-"+tc.handle), unrestricted, FindRequest{Handle: tc.handle})
			if err != nil {
				t.Fatal(err)
			}
			if got := foundIDs(response); got != tc.id {
				t.Fatalf("found = %q, want %q", got, tc.id)
			}
			if kinds := recorder.last.Kinds; len(kinds) != 1 || kinds[0] != tc.kind {
				t.Fatalf("kinds = %v, want [%s]", kinds, tc.kind)
			}
			if !equalKinds(reads, tc.reads) {
				t.Fatalf("census reads = %v, want %v", reads, tc.reads)
			}
			if recorder.last.ReferenceRow != tc.row {
				t.Fatalf("reference_row = %q, want %q", recorder.last.ReferenceRow, tc.row)
			}
		})
	}
}

// A failed pull request read decides nothing: the work item reading stands and
// the line says unread.
func TestFindSubjectsKeepsTheWorkItemReadingOnAFailedReferenceRead(t *testing.T) {
	for name, open := range map[string]graphrank.CensusOutcome{
		"a count that did not close":         {Count: 1, ClosureMismatch: true},
		"a satisfier set that did not close": {Count: 2, SatisfierSetClosureMismatch: true},
	} {
		t.Run(name, func(t *testing.T) {
			census := func(_ context.Context, _ string, kind graphrank.CensusKind, _ string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (graphrank.CensusOutcome, error) {
				if kind == contextfabric.SubjectPullRequest {
					return open, nil
				}
				return graphrank.CensusOutcome{}, nil
			}
			recorder := &findRowRecorder{}
			lookup := newModesLookup(handleGraph(), census)
			lookup.recorder = recorder
			response, err := lookup.Find(relCtx("12642-open-"+name), unrestricted, FindRequest{Handle: "acme/widget#8"})
			if err != nil {
				t.Fatal(err)
			}
			if kinds := recorder.last.Kinds; len(kinds) != 1 || kinds[0] != string(contractsv1.ContextFabricSubjectWorkItem) || recorder.last.ReferenceRow != "unread" || response.Status != FindEmpty {
				t.Fatalf("kinds %v row %q status %s, want work_item, unread, empty", kinds, recorder.last.ReferenceRow, response.Status)
			}
		})
	}
}

type findRowRecorder struct{ last FindTelemetry }

func (r *findRowRecorder) RecordFindSubjects(_ context.Context, _ storage.Principal, telemetry FindTelemetry) {
	r.last = telemetry
}

func equalKinds(a, b []graphrank.CensusKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
