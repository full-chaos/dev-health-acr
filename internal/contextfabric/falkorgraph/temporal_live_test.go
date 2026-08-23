package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3781 temporal admission, proved against a real FalkorDB server
// rather than a fake: AC-3781-4 (an element whose validity window excludes
// the requested time is not returned) and AC-3781-7 (comparison uses the
// epoch-nanosecond properties, never a string comparison).
//
// A live proof matters more than usual here. The predicate compares
// int64 `_ns` properties that may be NULL, and how a given FalkorDB
// version treats `NULL <= $x` inside a WHERE clause is exactly the kind of
// assumption that reads correctly and behaves wrongly. These tests seed
// windows either side of a requested instant and assert what comes back.

// temporalLiveBatch projects three subjects with deliberately different
// windows, plus edges from an anchor to each, so one requested instant
// partitions them: one closed before it, one open across it, and one that
// carries no window at all.
func temporalLiveBatch(orgID string, before, across time.Time) contextfabric.ProjectionBatch {
	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_anchor", Label: "Temporal anchor project"}
	ended := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_ended", Label: "Work that ended early"}
	live := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_live", Label: "Work still open"}
	unbounded := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_unbounded", Label: "Work with no window"}

	authorization := contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}}
	windowStart := before.Add(-24 * time.Hour)

	entity := func(subject contextfabric.SubjectRef, validFrom, validTo *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Authorization: authorization,
			EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt:     observed, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	relationship := func(id string, to contextfabric.SubjectRef, validFrom, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: "BLOCKS", From: anchor, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_" + id},
			ObservedAt: observed, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}

	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_temporal_0001", OrgID: orgID,
		Source: "live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			// The anchor is open across everything, so it always resolves
			// and the test is never measuring the anchor's own window.
			entity(anchor, &windowStart, nil),
			// Closed BEFORE the requested instant.
			entity(ended, &windowStart, &before),
			// Open across the requested instant.
			entity(live, &windowStart, nil),
			// No window at all.
			entity(unbounded, nil, nil),
		},
		Relationships: []contextfabric.RelationshipProjection{
			relationship("relationship_ended_0001", ended, &windowStart, &before),
			relationship("relationship_live_0001", live, &windowStart, nil),
			relationship("relationship_unbounded_0001", unbounded, nil, nil),
		},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

func temporalLiveRequest(subject contextfabric.SubjectRef, timeContext contextfabric.TimeContext) (contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion) {
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_00000002",
		Question: "What is the anchor project blocked by?", TimeContext: timeContext,
		RequestedScope: contextfabric.RequestedScope{SubjectHints: []contextfabric.SubjectHint{
			{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "live-test"},
		}},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "blockers",
		SubjectTerms: []string{subject.Label}, TimeContext: timeContext,
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
	}
	return request, interpreted
}

// subjectsInPaths collects every subject reachable through the discovered
// relationship paths -- the observable answer to "what did this
// investigation see".
func subjectsInPaths(graphContext contextfabric.GraphContext) map[string]bool {
	seen := map[string]bool{}
	for _, path := range graphContext.Paths {
		for _, node := range path.Nodes {
			seen[node.CanonicalID] = true
		}
	}
	return seen
}

func TestLiveTemporalAdmissionExcludesClosedWindows(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	// The requested instant sits strictly after `before` and strictly
	// inside the live subject's open window.
	before := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	batch := temporalLiveBatch(orgID, before, asOf)
	anchor := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{
		Axis: contextfabric.TemporalValidTime, AsOf: &asOf,
	})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) == 0 {
		t.Fatal("the anchor subject, whose window spans the requested time, did not resolve")
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
		Request: request, Interpretation: interpreted, Resolution: resolution,
	})
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	seen := subjectsInPaths(graphContext)
	// AC-3781-4: the edge and subject that closed before the requested
	// time must not come back.
	if seen["work_ended"] {
		t.Error("a subject whose validity window closed before the requested time was returned")
	}
	// The other half: filtering must not empty the graph.
	if !seen["work_live"] {
		t.Error("a subject whose validity window spans the requested time was not returned")
	}
	// The unbounded element is admitted deliberately -- and disclosed.
	if !seen["work_unbounded"] {
		t.Error("a subject carrying no validity window was excluded; an unbounded element must be admitted at every requested time")
	}
	var disclosed bool
	for _, source := range graphContext.Coverage.Sources {
		if source.Source == "context-fabric:graph-validity-windows" {
			disclosed = true
			if source.State != contextfabric.SourceNotApplicable {
				t.Errorf("unbounded-window disclosure state = %q, want %q", source.State, contextfabric.SourceNotApplicable)
			}
		}
	}
	if !disclosed {
		t.Error("elements with no validity window were admitted but never disclosed in coverage")
	}
	// An unbounded element is not a failure, so it must not present as
	// degraded coverage.
	if graphContext.Coverage.Partial {
		t.Error("admitting an unbounded element must not mark coverage partial")
	}
}

// TestLiveTemporalAdmissionBeforeAnythingExisted is the AC-3781-3 shape at
// the graph layer: asking about a time before a subject's window opened
// must not fall through to its current state.
func TestLiveTemporalAdmissionBeforeAnythingExisted(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-early-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	before := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	batch := temporalLiveBatch(orgID, before, before.Add(48*time.Hour))
	anchor := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	// Strictly before every window in the batch opens.
	asOf := before.Add(-365 * 24 * time.Hour)
	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{
		Axis: contextfabric.TemporalValidTime, AsOf: &asOf,
	})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	for _, subject := range resolution.Committed {
		if subject.CanonicalID == "project_anchor" {
			t.Fatal("a subject was committed for a time before its validity window opened; a historical question must not fall through to current state")
		}
	}
}

// TestLiveTemporalAdmissionIsHalfOpen pins the boundary convention: an
// element whose window ends exactly AT the requested instant is excluded,
// while one that starts exactly at it is included. Adjacent intervals
// therefore partition with no gap and no double-count.
func TestLiveTemporalAdmissionIsHalfOpen(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-boundary-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	boundary := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	batch := temporalLiveBatch(orgID, boundary, boundary)
	anchor := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	// work_ended's valid_to is exactly `boundary`. Half-open [from, to)
	// means it is NOT valid at `boundary` itself.
	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{
		Axis: contextfabric.TemporalValidTime, AsOf: &boundary,
	})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
		Request: request, Interpretation: interpreted, Resolution: resolution,
	})
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if subjectsInPaths(graphContext)["work_ended"] {
		t.Error("an element whose window ends exactly at the requested instant was returned; the interval must be half-open [valid_from, valid_to)")
	}
}

// TestLiveTemporalRangeAdmitsOverlap proves the range axis is INTERVAL
// OVERLAP, not containment: an element that merely overlaps the requested
// window belongs in the answer.
func TestLiveTemporalRangeAdmitsOverlap(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-range-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	before := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	batch := temporalLiveBatch(orgID, before, before)
	anchor := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	// work_ended is valid over [before-24h, before). This window starts
	// before that and ends inside it, so the two overlap without either
	// containing the other.
	start := before.Add(-36 * time.Hour)
	end := before.Add(-12 * time.Hour)
	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{
		Axis: contextfabric.TemporalRange, Start: &start, End: &end,
	})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
		Request: request, Interpretation: interpreted, Resolution: resolution,
	})
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !subjectsInPaths(graphContext)["work_ended"] {
		t.Error("an element overlapping the requested range was excluded; the range axis is overlap, not containment")
	}
}

// TestLiveCurrentAxisIsUnaffected is the over-blocking guard: a
// current-axis investigation must behave exactly as it did before
// CHAOS-3781, including returning elements whose windows have closed.
func TestLiveCurrentAxisIsUnaffected(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-current-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	before := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	batch := temporalLiveBatch(orgID, before, before)
	anchor := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
		Request: request, Interpretation: interpreted, Resolution: resolution,
	})
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	seen := subjectsInPaths(graphContext)
	for _, canonicalID := range []string{"work_ended", "work_live", "work_unbounded"} {
		if !seen[canonicalID] {
			t.Errorf("current-axis discovery lost %q; the temporal filter must be inert on the current axis", canonicalID)
		}
	}
	// And no unbounded-window disclosure: there is no requested time for
	// an unbounded element to be admitted "at".
	for _, source := range graphContext.Coverage.Sources {
		if source.Source == "context-fabric:graph-validity-windows" {
			t.Error("a current-axis answer must not carry an unbounded-validity disclosure")
		}
	}
}

// TestLiveReferencedStubsCarryNoValidityWindow is round-1 F3, proved
// against a real FalkorDB.
//
// A referenced stub used to inherit the window of whatever record
// mentioned it: a relationship valid for one week stamped that week onto
// the work item it pointed at, and an episode that ran for an hour stamped
// that hour. A historical read then excluded the subject everywhere
// outside an unrelated record's interval -- and which interval won
// depended on projection ORDER, since the next referencing record
// overwrote it.
//
// Stubs now assert identity and nothing canonical, the same discipline
// CHAOS-3785 set. Only the authoritative entity write states validity.
func TestLiveReferencedStubsCarryNoValidityWindow(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-temporal-stub-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	// A relationship with a NARROW window, pointing at two subjects that
	// have no authoritative entity write of their own.
	windowStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_stub_anchor", Label: "Stub anchor"}
	referenced := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_referenced_only", Label: "Referenced only"}

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_stub_00001", OrgID: orgID,
		Source: "live-test", SourceVersion: "v1", Cursor: "c1", NextCursor: "c2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_stub_0001", Type: "BLOCKS", From: anchor, To: referenced,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_stub_0001"},
			ObservedAt:     observed, ValidFrom: &windowStart, ValidTo: &windowEnd, SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	principal := storage.Principal{OrgID: orgID}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	// Ask about a time WELL OUTSIDE the relationship's window. If the
	// stubs had inherited it, the anchor subject would not resolve at all.
	outside := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	request, interpreted := temporalLiveRequest(anchor, contextfabric.TimeContext{
		Axis: contextfabric.TemporalValidTime, AsOf: &outside,
	})
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	var found bool
	for _, subject := range resolution.Committed {
		if subject.CanonicalID == anchor.CanonicalID {
			found = true
		}
	}
	if !found {
		t.Fatal("a referenced stub was excluded outside an unrelated relationship's window; stubs must carry no validity window at all")
	}

	// The EDGE still carries its own window, so the association itself is
	// correctly excluded outside it -- the stub fix must not weaken
	// AC-3781-4.
	graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
		Request: request, Interpretation: interpreted, Resolution: resolution,
	})
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if subjectsInPaths(graphContext)[referenced.CanonicalID] {
		t.Error("the relationship was returned outside its own validity window; the edge must still be excluded")
	}
}
