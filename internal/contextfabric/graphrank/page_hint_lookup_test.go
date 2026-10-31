package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12230: the page_hint_lookup line. Web names the Ask Dev page's
// entities as subject hints with source ask_dev_page_context, ids built in
// acr's canonical form. The line counts, once per resolveSubjects call, how
// those hints resolved -- and carries nothing that names them.

const (
	pageHintHiddenTeamID = "team:hidden-platform"
	pageHintHiddenLabel  = "Hidden Platform"
	pageHintStaleRepoID  = "repository:5a1d7e44-stale-0000-0000-000000000001"
	pageHintRepoLabel    = "dev-health-acr-page"
	pageHintGoneRepoID   = "repository:5a1d7e44-gone-0000-0000-000000000002"
)

func pageHint(kind contextfabric.SubjectKind, id, label string) contextfabric.SubjectHint {
	return contextfabric.SubjectHint{Kind: kind, ID: id, Label: label, Source: hintsource.AskDevPageContext}
}

// threeOutcomeRequest carries one page hint per outcome over two kinds:
//
//   - a team the exact lookup FINDS but the principal may not see, so it is
//     an exact hit that commits nothing and the call goes on to search;
//   - a repository whose id misses and whose label is searched;
//   - a repository whose id misses and which has no label.
func threeOutcomeRequest() (contextfabric.InvestigationRequest, *fakeGraphBackend, storage.Principal) {
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		pageHint(contextfabric.SubjectTeam, pageHintHiddenTeamID, pageHintHiddenLabel),
		pageHint(contextfabric.SubjectRepository, pageHintStaleRepoID, pageHintRepoLabel),
		pageHint(contextfabric.SubjectRepository, pageHintGoneRepoID, ""),
	}
	hidden := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: pageHintHiddenTeamID}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(hidden): candidateNode(contextfabric.SubjectTeam, pageHintHiddenTeamID, pageHintHiddenLabel, 1, []string{"other/private"}),
	}}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	return request, backend, principal
}

func pageHintEvents(t *testing.T, tracer *captureResolutionTracer) []ResolutionTraceEvent {
	t.Helper()
	return tracer.eventsForStage("page_hint_lookup")
}

// (i) all three outcomes in one request, over two kinds, with exact counts.
func TestPageHintLookupCountsEachOutcomeOncePerHint(t *testing.T) {
	t.Parallel()
	request, backend, principal := threeOutcomeRequest()
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed %v; the hidden team must not commit, or the fixture no longer reaches search", resolution.Committed)
	}
	if !slices.Contains(backend.searchCalls, pageHintRepoLabel) {
		t.Fatalf("search calls %q do not include the labelled hint's label; label_fallback would claim a search that did not happen", backend.searchCalls)
	}
	events := pageHintEvents(t, tracer)
	if len(events) != 1 {
		t.Fatalf("captured %d page_hint_lookup events, want exactly 1", len(events))
	}
	got := events[0]
	if got.RequestID != request.RequestID || got.PageHintCount != 3 || got.PageHintExactHit != 1 || got.PageHintLabelFallback != 1 || got.PageHintUnresolved != 1 || !got.PageHintSearchFallback {
		t.Fatalf("page_hint_lookup = %+v, want 3 hints: 1 exact_hit, 1 label_fallback, 1 unresolved, search_fallback=true", got)
	}
	team, repository := string(contextfabric.SubjectTeam), string(contextfabric.SubjectRepository)
	if !reflect.DeepEqual(got.PageHintExactHitByKind, map[string]int{team: 1}) ||
		!reflect.DeepEqual(got.PageHintLabelFallbackByKind, map[string]int{repository: 1}) ||
		!reflect.DeepEqual(got.PageHintUnresolvedByKind, map[string]int{repository: 1}) {
		t.Fatalf("by kind: exact_hit=%v label_fallback=%v unresolved=%v, want {team:1} {repository:1} {repository:1}",
			got.PageHintExactHitByKind, got.PageHintLabelFallbackByKind, got.PageHintUnresolvedByKind)
	}
}

// A labelled miss is a label_fallback only when the search actually ran: an
// exact hit the principal may see commits through the caller-hint short
// circuit, no search runs, and the labelled miss beside it is unresolved.
func TestPageHintLookupCountsALabelledMissUnresolvedWhenNoSearchRan(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		pageHint(contextfabric.SubjectTeam, "team:platform", "Platform"),
		pageHint(contextfabric.SubjectRepository, pageHintStaleRepoID, pageHintRepoLabel),
	}
	platform := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(platform): candidateNode(contextfabric.SubjectTeam, "team:platform", "Platform", 1, "*"),
	}}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Committed) != 1 || len(backend.searchCalls) != 0 {
		t.Fatalf("committed %v after search calls %q; want the short circuit (one commit, no search)", resolution.Committed, backend.searchCalls)
	}
	events := pageHintEvents(t, tracer)
	if len(events) != 1 {
		t.Fatalf("captured %d page_hint_lookup events, want exactly 1", len(events))
	}
	got := events[0]
	if got.PageHintCount != 2 || got.PageHintExactHit != 1 || got.PageHintLabelFallback != 0 || got.PageHintUnresolved != 1 || got.PageHintSearchFallback {
		t.Fatalf("page_hint_lookup = %+v, want 2 hints: 1 exact_hit, 0 label_fallback, 1 unresolved, search_fallback=false", got)
	}
}

// (ii) the emitted slog line carries the counts and nothing that names a hint.
func TestPageHintLookupLineCarriesCountsAndNoHintIdentity(t *testing.T) {
	t.Parallel()
	request, backend, principal := threeOutcomeRequest()
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted(), deps, nil, nil, nil, ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	team, repository := string(contextfabric.SubjectTeam), string(contextfabric.SubjectRepository)
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.PageHintLookup,
		Want: map[string]any{
			"request_id": request.RequestID, "page_hints": 3,
			"exact_hit": 1, "label_fallback": 1, "unresolved": 1, "search_fallback": true,
			"exact_hit_by_kind":      map[string]any{team: 1},
			"label_fallback_by_kind": map[string]any{repository: 1},
			"unresolved_by_kind":     map[string]any{repository: 1},
		},
	}); err != nil {
		t.Fatalf("Certify(PageHintLookup) error = %v", err)
	}

	var raw []string
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		var decoded map[string]any
		if json.Unmarshal(line, &decoded) == nil && decoded["msg"] == eventspec.PageHintLookup.Msg {
			raw = append(raw, string(line))
		}
	}
	if len(raw) != 1 {
		t.Fatalf("found %d page hint lookup lines, want 1", len(raw))
	}
	for _, forbidden := range []string{
		pageHintHiddenTeamID, pageHintStaleRepoID, pageHintGoneRepoID,
		pageHintHiddenLabel, pageHintRepoLabel, request.Question,
	} {
		if strings.Contains(raw[0], forbidden) {
			t.Errorf("the page hint lookup line carries %q: %s", forbidden, raw[0])
		}
	}
}

// (iii) a request with no page hint writes no line.
func TestPageHintLookupWritesNoLineWithoutAPageHint(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	request := testRequest()

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("acr"), deps, nil, nil, nil, ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.PageHintLookup, map[string]any{"request_id": request.RequestID}); err != nil {
		t.Fatalf("CertifyAbsent(PageHintLookup) error = %v", err)
	}
	if lines := log.LinesWithMsg(eventspec.PageHintLookup.Msg); len(lines) != 0 {
		t.Fatalf("captured %d page hint lookup lines for a request with no hint, want 0", len(lines))
	}
}

// (iv) a hint of another source is not counted, and a request whose hints are
// all of other sources writes no line.
func TestPageHintLookupCountsOnlyPageContextHints(t *testing.T) {
	t.Parallel()
	other := contextfabric.SubjectHint{Kind: contextfabric.SubjectProject, ID: "project.v2:jira:OPS", Label: "Ops board", Source: "web_scope_selector"}

	t.Run("mixed sources", func(t *testing.T) {
		t.Parallel()
		request := testRequest()
		request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
			other,
			pageHint(contextfabric.SubjectRepository, pageHintStaleRepoID, pageHintRepoLabel),
		}
		tracer := &captureResolutionTracer{}
		deps := (&fakeGraphBackend{}).deps()
		deps.ResolutionTracer = tracer
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		events := pageHintEvents(t, tracer)
		if len(events) != 1 {
			t.Fatalf("captured %d page_hint_lookup events, want exactly 1", len(events))
		}
		got := events[0]
		repository := string(contextfabric.SubjectRepository)
		if got.PageHintCount != 1 || got.PageHintLabelFallback != 1 || got.PageHintExactHit != 0 || got.PageHintUnresolved != 0 ||
			!reflect.DeepEqual(got.PageHintLabelFallbackByKind, map[string]int{repository: 1}) || len(got.PageHintUnresolvedByKind) != 0 {
			t.Fatalf("page_hint_lookup = %+v, want only the one page hint counted (a label_fallback repository)", got)
		}
	})

	t.Run("no page source", func(t *testing.T) {
		t.Parallel()
		request := testRequest()
		request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{other}
		tracer := &captureResolutionTracer{}
		deps := (&fakeGraphBackend{}).deps()
		deps.ResolutionTracer = tracer
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		if events := pageHintEvents(t, tracer); len(events) != 0 {
			t.Fatalf("captured %v for a request with no page hint, want none", events)
		}
	})
}

// The deferred emit is what makes the line survive an early return: an exact
// lookup that fails ends the call with an error, and the line is still written,
// each page hint the loop did not find counted unresolved (no search ran).
func TestPageHintLookupIsWrittenWhenTheExactLookupFails(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		pageHint(contextfabric.SubjectTeam, "team:platform", "Platform"),
		pageHint(contextfabric.SubjectRepository, pageHintStaleRepoID, pageHintRepoLabel),
	}
	tracer := &captureResolutionTracer{}
	deps := (&fakeGraphBackend{}).deps()
	deps.ExactHint = func(_ context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
		if subject.Kind == contextfabric.SubjectRepository {
			return CandidateNode{}, false, errors.New("graph read failed")
		}
		return CandidateNode{}, false, nil
	}
	deps.ResolutionTracer = tracer
	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, ""); err == nil {
		t.Fatal("ResolveSubjectsWithCommitBasis() error = nil, want the exact lookup's failure")
	}
	events := pageHintEvents(t, tracer)
	if len(events) != 1 {
		t.Fatalf("captured %d page_hint_lookup events on the error return, want exactly 1", len(events))
	}
	if got := events[0]; got.PageHintCount != 2 || got.PageHintUnresolved != 2 || got.PageHintExactHit != 0 || got.PageHintLabelFallback != 0 || got.PageHintSearchFallback {
		t.Fatalf("page_hint_lookup = %+v, want 2 hints, both unresolved, search_fallback=false", got)
	}
}
