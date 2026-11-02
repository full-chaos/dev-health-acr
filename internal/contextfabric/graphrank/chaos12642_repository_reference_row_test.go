package graphrank

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12642: on GitHub, issues and pull requests share one number space, so
// owner/repo#N names an issue or a pull request and the text cannot tell which.
// The stored rows decide, in this order: a pull request row with that repository
// and number, else a work item with that key, else nothing.
//
// The fixture is a repository that holds issue #7 and pull request #8, with the
// pull request also mirrored as a work item ghpr:acme/widget#8, as the sync
// stores GitHub pull requests on askdev-1.
const (
	referenceRowsPullRequestID = "pull_request:r-widget:8"
	referenceRowsIssueID       = "work_item.v2:r-widget:gh:acme/widget#7"
	referenceRowsMirrorID      = "work_item.v2:r-widget:ghpr:acme/widget#8"
	referenceRowsMergeID       = "work_item.v2:r-group:gitlab:group/project!8"
)

func referenceRows() []grantRow {
	return []grantRow{
		{contextfabric.SubjectPullRequest, "acme/widget#8", referenceRowsPullRequestID, "acme/widget"},
		{contextfabric.SubjectWorkItem, "acme/widget#7", referenceRowsIssueID, "acme/widget"},
		{contextfabric.SubjectWorkItem, "acme/widget#8", referenceRowsMirrorID, "acme/widget"},
		{contextfabric.SubjectWorkItem, "group/project!8", referenceRowsMergeID, "group/project"},
	}
}

func resolveReference(t *testing.T, reference string, census CensusFunc) (contextfabric.SubjectResolution, *captureResolutionTracer) {
	t.Helper()
	hints := map[string]CandidateNode{}
	for kind, nodes := range map[contextfabric.SubjectKind][]CandidateNode{
		contextfabric.SubjectPullRequest: {candidateNode(contextfabric.SubjectPullRequest, referenceRowsPullRequestID, "PR #8", 0.5, []string{"acme/widget"})},
		contextfabric.SubjectWorkItem: {
			candidateNode(contextfabric.SubjectWorkItem, referenceRowsIssueID, "acme/widget#7", 0.5, []string{"acme/widget"}),
			candidateNode(contextfabric.SubjectWorkItem, referenceRowsMirrorID, "acme/widget#8", 0.5, []string{"acme/widget"}),
			candidateNode(contextfabric.SubjectWorkItem, referenceRowsMergeID, "group/project!8", 0.5, []string{"group/project"}),
		},
	} {
		for _, node := range nodes {
			id, _ := node.Attributes["canonical_id"].(string)
			hints[SubjectKey(contextfabric.SubjectRef{Kind: kind, CanonicalID: id})] = node
		}
	}
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{}, searchTruncated: true, exactHints: hints}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = census
	deps.HandleGrammarChecker = func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
		column, ok := HandleSourceColumn(kind, patternID)
		return column, ok && ValidateHandleGrammar(kind, patternID, value)
	}
	request := testRequest()
	request.Question = "what is the status of " + reference + "?"
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(reference), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolution, tracer
}

// The ticket's proof, through the real resolver: #8 commits the pull request
// (not its work item mirror), #7 commits the issue, #9 is not found, and a
// GitLab merge request reference keeps its reading.
func TestARepositoryReferenceCommitsTheRowItNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reference string
		want      string
		outcome   ShadowOutcome
	}{
		{"acme/widget#8", referenceRowsPullRequestID, ShadowWouldCommit},
		{"acme/widget#7", referenceRowsIssueID, ShadowWouldCommit},
		{"acme/widget#9", "", ShadowWouldNoMatch},
		{"group/project!8", referenceRowsMergeID, ShadowWouldCommit},
	} {
		t.Run(tc.reference, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: referenceRows()}
			resolution, tracer := resolveReference(t, tc.reference, spy.census)
			if round := onlyRound(t, tracer); round.ShadowOutcome != string(tc.outcome) {
				t.Fatalf("round = %q/%q, want %q", round.ShadowOutcome, round.ShadowReason, tc.outcome)
			}
			ids := scopeAnchorCommittedIDs(resolution)
			switch {
			case tc.want == "" && len(ids) != 0:
				t.Fatalf("committed = %v, want nothing", ids)
			case tc.want != "" && (len(ids) != 1 || ids[0] != tc.want):
				t.Fatalf("committed = %v, want %s", ids, tc.want)
			}
		})
	}
}

// The bind decision itself: what each reference binds as, and that a "!"
// reference is never read as a pull request.
func TestTheStoredRowsDecideWhatAReferenceBindsAs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reference string
		want      string
		prRead    bool
	}{
		{"acme/widget#8", "pull_request_repo_reference:acme/widget#8", true},
		{"acme/widget#7", "work_item_repo_reference:acme/widget#7", true},
		{"acme/widget#9", "work_item_repo_reference:acme/widget#9", true},
		{"group/project!8", "work_item_repo_reference:group/project!8", false},
	} {
		t.Run(tc.reference, func(t *testing.T) {
			t.Parallel()
			spy := &grantCensusSpy{rows: referenceRows()}
			deps := ResolveDeps{CensusFunc: spy.census, WorkItemKeyPrefix: everyKeyPrefixHeld}
			request := testRequest()
			request.Question = "what is the status of " + tc.reference + "?"
			bound := bindQuestionHandles(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps)
			if got := grammarsOf(bound); got != tc.want {
				t.Fatalf("bound = %q, want %q", got, tc.want)
			}
			if len(bound) == 1 && bound[0].Grammar == "pull_request_repo_reference" && bound[0].Kind != contextfabric.SubjectPullRequest {
				t.Fatalf("kind = %q, want pull_request", bound[0].Kind)
			}
			prRead := false
			for _, call := range spy.calls {
				prRead = prRead || call.kind == contextfabric.SubjectPullRequest
			}
			if prRead != tc.prRead {
				t.Fatalf("pull request read = %v, want %v (calls %+v)", prRead, tc.prRead, spy.calls)
			}
		})
	}
}

// The decision line names which rule decided each reference, counts only, and a
// read that failed is unread: that reference keeps its work item reading.
func TestTheReferenceRowLineNamesEachRule(t *testing.T) {
	t.Parallel()
	failing := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{}, errors.New("boom")
	}
	open := func(outcome CensusOutcome) CensusFunc {
		return func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
			return outcome, nil
		}
	}
	unread := map[string]any{"references": 3, "pull_request_row": 0, "work_item_row": 0, "not_found": 0, "unread": 3}
	everyWorkItem := "work_item_repo_reference:acme/widget#8 work_item_repo_reference:acme/widget#7 work_item_repo_reference:acme/widget#9 work_item_repo_reference:group/project!8"
	for _, tc := range []struct {
		name   string
		census CensusFunc
		want   map[string]any
		bound  string
	}{
		{"a count that did not close", open(CensusOutcome{Count: 1, ClosureMismatch: true}), unread, everyWorkItem},
		{"a satisfier set that did not close", open(CensusOutcome{Count: 1, SatisfierSetClosureMismatch: true}), unread, everyWorkItem},
		{"every rule", (&grantCensusSpy{rows: referenceRows()}).census,
			map[string]any{"references": 3, "pull_request_row": 1, "work_item_row": 1, "not_found": 1, "unread": 0},
			"pull_request_repo_reference:acme/widget#8 work_item_repo_reference:acme/widget#7 work_item_repo_reference:acme/widget#9 work_item_repo_reference:group/project!8"},
		{"a failed read", failing,
			map[string]any{"references": 3, "pull_request_row": 0, "work_item_row": 0, "not_found": 0, "unread": 3},
			"work_item_repo_reference:acme/widget#8 work_item_repo_reference:acme/widget#7 work_item_repo_reference:acme/widget#9 work_item_repo_reference:group/project!8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			deps := ResolveDeps{CensusFunc: tc.census, WorkItemKeyPrefix: everyKeyPrefixHeld, ResolutionTracer: tracer}
			request := testRequest()
			request.Question = "compare acme/widget#8, acme/widget#7, acme/widget#9 and group/project!8"
			if got := grammarsOf(bindQuestionHandles(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps)); got != tc.bound {
				t.Fatalf("bound = %q, want %q", got, tc.bound)
			}
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"request_id": request.RequestID, "stage": "repository_reference_row"}
			maps.Copy(want, tc.want)
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.RepositoryReferenceRow, Want: want}); err != nil {
				t.Fatalf("Certify = %v", err)
			}
			if strings.Contains(buf.String(), "widget") || strings.Contains(buf.String(), "group/project") {
				t.Fatalf("the line names a reference: %s", buf.String())
			}
		})
	}
}

// A turn reads each reference once: a later resolver call of the same turn reads
// the memo, makes no census call, and writes no second line.
func TestATurnReadsAReferenceOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	spy := &grantCensusSpy{rows: referenceRows()}
	deps := ResolveDeps{CensusFunc: spy.census, WorkItemKeyPrefix: everyKeyPrefixHeld, ResolutionTracer: tracer}
	request := testRequest()
	request.Question = "what is the status of acme/widget#7?"
	ctx := contextfabric.WithWorkItemKeyPrefixMemo(context.Background())
	first := grammarsOf(bindQuestionHandles(ctx, storage.Principal{OrgID: "org_1"}, request, deps))
	calls := len(spy.calls)
	second := grammarsOf(bindQuestionHandles(ctx, storage.Principal{OrgID: "org_1"}, request, deps))
	if first != second || first != "work_item_repo_reference:acme/widget#7" {
		t.Fatalf("bound %q then %q", first, second)
	}
	if calls != 2 || len(spy.calls) != calls {
		t.Fatalf("census calls = %d then %d, want 2 then none", calls, len(spy.calls))
	}
	if n := strings.Count(buf.String(), eventspec.RepositoryReferenceRow.Msg); n != 1 {
		t.Fatalf("lines = %d, want 1", n)
	}
}

// The turn's census reads for references stay inside the same budget as its
// prefix reads: a reference whose read does not fit is unread and keeps its work
// item reading (codex r1 P2: eight references made sixteen census calls).
func TestReferenceReadsStayInsideTheReadBudget(t *testing.T) {
	t.Parallel()
	spy := &grantCensusSpy{}
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	deps := ResolveDeps{CensusFunc: spy.census, WorkItemKeyPrefix: everyKeyPrefixHeld, ResolutionTracer: tracer}
	request := testRequest()
	request.Question = "compare acme/w#1 acme/w#2 acme/w#3 acme/w#4 acme/w#5 acme/w#6 acme/w#7 acme/w#8 acme/w#9"
	bindQuestionHandles(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps)
	if len(spy.calls) > maxWorkItemKeyPrefixReads {
		t.Fatalf("census calls = %d, want at most %d", len(spy.calls), maxWorkItemKeyPrefixReads)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// Four references read in full (a pull request read and a work item read
	// each); the other five are unread.
	want := map[string]any{"request_id": request.RequestID, "stage": "repository_reference_row", "references": 9, "pull_request_row": 0, "work_item_row": 0, "not_found": 4, "unread": 5}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.RepositoryReferenceRow, Want: want}); err != nil {
		t.Fatalf("Certify = %v", err)
	}
}
