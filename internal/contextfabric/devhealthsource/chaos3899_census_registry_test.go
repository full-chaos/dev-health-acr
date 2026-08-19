package devhealthsource

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// workItemHandleMatches is the pure-Go transliteration of
// workItemTicketKeyPredicate's SQL semantics: ClickHouse's position(s, ':')
// returns the 1-based index of the FIRST ':' (0 if absent), and
// substring(s, n) returns everything from position n onward -- together,
// "position(...)+1" as substring's start argument is the exact SQL
// equivalent of Go's strings.Cut(s, ":") remainder. This function exists
// ONLY so a unit test can prove that SQL semantics, expressed in Go,
// equals ticketKeyAlias's own remainder -- it is test-only logic, not a
// second production implementation of the predicate.
func workItemHandleMatches(workItemID, handleValue string) bool {
	idx := -1
	for i, r := range workItemID {
		if r == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	return workItemID[idx+1:] == handleValue
}

// TestWorkItemTicketKeyPredicateIsExactTicketKeyAliasInverse cross-tests
// the SQL predicate's Go-equivalent semantics (workItemHandleMatches)
// against the REAL production function (ticketKeyAlias, embed_fields.go)
// over live id shapes (design brief v5 §1.2's load-bearing example).
func TestWorkItemTicketKeyPredicateIsExactTicketKeyAliasInverse(t *testing.T) {
	t.Parallel()
	ids := []string{
		"linear:CHAOS-3896", "a:b:c", "no-colon-here", "linear:CHAOS-1",
		":", "x:", ":y", "jira:PROJ-42:extra", "", "linear:CHAOS-0001",
	}
	// "" is deliberately excluded: workItemTicketKeyPredicate itself
	// rejects an empty handle value outright (its own guard), so the SQL
	// this cross-test pins is never actually issued with handle="" in
	// production -- comparing against it here would just be testing an
	// artificial input outside the real domain (id ":" has a genuine empty
	// REMAINDER, which is a different thing from an empty HANDLE).
	handles := []string{"CHAOS-3896", "b:c", "CHAOS-1", "y", "PROJ-42:extra", "CHAOS-0001", "nonexistent"}
	for _, id := range ids {
		alias := ticketKeyAlias(id)
		for _, handle := range handles {
			want := alias == handle
			got := workItemHandleMatches(id, handle)
			if got != want {
				t.Fatalf("workItemHandleMatches(%q, %q) = %v, want %v (ticketKeyAlias(%q) = %q)", id, handle, got, want, id, alias)
			}
		}
	}
}

// TestPullRequestNumberPredicate pins the pull_request handle registry
// entry's parse behavior.
func TestPullRequestNumberPredicate(t *testing.T) {
	t.Parallel()
	predicate, err := pullRequestNumberPredicate("532")
	if err != nil {
		t.Fatalf("pullRequestNumberPredicate(532): %v", err)
	}
	if predicate.SQL != "p.number = {census_handle_pr_number:UInt32}" {
		t.Fatalf("SQL = %q", predicate.SQL)
	}
	if len(predicate.Bindings) != 1 || predicate.Bindings[0].Value != uint32(532) {
		t.Fatalf("Bindings = %#v", predicate.Bindings)
	}
	if _, err := pullRequestNumberPredicate("not-a-number"); err == nil {
		t.Fatalf("pullRequestNumberPredicate(not-a-number): want error, got nil")
	}
	// "PR 532" never binds "PR 53" (R3) is the GRAMMAR's job
	// (graphrank.BindHandles); this predicate operates on an
	// already-extracted value and must reject a non-numeric one outright
	// rather than silently truncating it.
	if _, err := pullRequestNumberPredicate("53x"); err == nil {
		t.Fatalf("pullRequestNumberPredicate(53x): want error, got nil")
	}
}

func TestCanonicalIDValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind contextfabric.SubjectKind
		in   string
		want string
	}{
		// SubjectRepository's id shape is untouched by CHAOS-3898, so the
		// legacy single-Cut fallback still applies.
		{contextfabric.SubjectRepository, "repository:abc-123", "abc-123"},
		{contextfabric.SubjectRepository, "no-colon", "no-colon"},
		// A kind with no identity.Registry entry at all (team) also falls
		// through to the legacy Cut, unaffected either way.
		{"team", "team:t-1", "t-1"},
		// CHAOS-3898 regression: project's canonical id gained a repo_id-
		// shaped extra segment ("project.v2:<provider>:<id>"). A naive
		// single-Cut on the first ':' would return "<provider>:<id>" here
		// -- which can never equal work_items.project_id (this file's own
		// registry comment: "carries no provider") -- so this is exactly
		// the false would_no_match class the census package forbids.
		// identity.Segments must be used instead, to recover just <id>.
		{contextfabric.SubjectProject, "project.v2:github:p-1", "p-1"},
		// A not-yet-migrated (pre-flip) project id has no identity.Segments
		// match and must still fall through to the legacy Cut -- the same
		// total-function guarantee strings.Cut alone always provided.
		{contextfabric.SubjectProject, "project:p-1", "p-1"},
	}
	for _, tc := range cases {
		if got := canonicalIDValue(tc.kind, tc.in); got != tc.want {
			t.Fatalf("canonicalIDValue(%q, %q) = %q, want %q", tc.kind, tc.in, got, tc.want)
		}
	}
}

// TestBuildCensusDiscriminator_JoinedColumnDiscriminator pins brief
// §1.3(1)'s refusal: an anchor kind with no FK column on a census kind's
// base table must refuse, never silently join.
func TestBuildCensusDiscriminator_JoinedColumnDiscriminator(t *testing.T) {
	t.Parallel()
	_, err := BuildCensusDiscriminator(contractsv1.ContextFabricSubjectCIRun, "", false, contextfabric.SubjectProject, "project:p-1", true)
	if err == nil {
		t.Fatalf("BuildCensusDiscriminator(ci_pipeline_run, anchor=project): want joined_column_discriminator error, got nil")
	}
}

// TestBuildCensusDiscriminator_NoHandleGrammarForKind pins that
// pull_request_review (no registered handle grammar) refuses a handle
// predicate rather than silently no-op'ing.
func TestBuildCensusDiscriminator_NoHandleGrammarForKind(t *testing.T) {
	t.Parallel()
	_, err := BuildCensusDiscriminator(contractsv1.ContextFabricSubjectPullRequestReview, "532", true, "", "", false)
	if err == nil {
		t.Fatalf("BuildCensusDiscriminator(pull_request_review, handle bound): want error, got nil")
	}
}

// TestBuildCensusDiscriminator_ComposesHandleAndAnchor pins that both
// classes AND together when both are bound.
func TestBuildCensusDiscriminator_ComposesHandleAndAnchor(t *testing.T) {
	t.Parallel()
	predicate, err := BuildCensusDiscriminator(contextfabric.SubjectPullRequest, "532", true, contextfabric.SubjectRepository, "repository:r-1", true)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	wantSQL := "p.number = {census_handle_pr_number:UInt32} AND toString(p.repo_id) = {census_anchor_id:String}"
	if predicate.SQL != wantSQL {
		t.Fatalf("SQL = %q, want %q", predicate.SQL, wantSQL)
	}
	if len(predicate.Bindings) != 2 {
		t.Fatalf("Bindings = %#v, want 2 entries", predicate.Bindings)
	}
}

// TestWorkItemAnchorColumns pins work_item's ONE anchor FK column
// (project_id) and, just as importantly, that repository is REFUSED
// (adversarial review finding: a Linear-sourced work item's repo_id is the
// zero UUID at ingest -- tables.go's queryWorkItems doc comment -- so a
// repository-anchored work_item census would return 0 for nearly every
// real Linear item, a near-certain false would_no_match).
func TestWorkItemAnchorColumns(t *testing.T) {
	t.Parallel()
	if _, err := BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectProject, "project:p-1", true); err != nil {
		t.Fatalf("work_item anchor=project: %v", err)
	}
	if _, err := BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectRepository, "repository:r-1", true); err == nil {
		t.Fatalf("work_item anchor=repository: want joined_column_discriminator error, got nil")
	}
}

// TestKindHasAnchorFKMatchesCensusRegistry cross-tests
// graphrank.KindHasAnchorFK (the shadow round's own copy of "which anchor
// kinds does this census kind's base table have an FK column for") against
// THIS package's censusKindRegistryEntries.anchorColumns -- the actual SQL
// registry -- over every census kind x every alias-lookup-scoped anchor
// kind. The two must never silently drift (adversarial review finding 7):
// devhealthsource can import graphrank (chaos3884_identity_universe.go
// already does), so this direction of the cross-check is free; the reverse
// would cycle, which is why graphrank keeps its own mirror at all.
func TestKindHasAnchorFKMatchesCensusRegistry(t *testing.T) {
	t.Parallel()
	anchorKinds := []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam}
	for kind, entry := range censusKindRegistryEntries {
		for _, anchorKind := range anchorKinds {
			_, registryHasColumn := entry.anchorColumns[anchorKind]
			mirrorSaysYes := graphrank.KindHasAnchorFK(kind, anchorKind)
			if registryHasColumn != mirrorSaysYes {
				t.Fatalf("kind=%s anchorKind=%s: censusKindRegistryEntries has column=%v, graphrank.KindHasAnchorFK=%v -- the two registries have drifted", kind, anchorKind, registryHasColumn, mirrorSaysYes)
			}
		}
	}
}

// TestIdentityColumnIsTheFullCompositeNaturalKey is the v6 stamp's own
// directive, pinned as a test: every census kind's identityColumn (used
// BOTH as the row statement's SELECT and, wrapped in min(...), as the
// aggregate statement's identity-witness expression) must be the base
// table's FULL TYPED composite natural key -- exactly its own
// devhealthschema ORDER BY sort key -- never a lossy single-column
// serialization. A lossy witness reopens the injectivity trap: two
// legitimately-different rows could collide under it. This asserts every
// registry entry's identityColumn references org_id AND every other
// sort-key column devhealthschema.go declares for that table, so a future
// edit that narrows it back down to one column fails loudly.
func TestIdentityColumnIsTheFullCompositeNaturalKey(t *testing.T) {
	t.Parallel()
	// wantColumnRefs are the exact devhealthschema ORDER BY sort-key
	// columns (schema.go) for each census base table, alias-qualified as
	// this registry's own entries reference them.
	wantColumnRefs := map[contextfabric.SubjectKind][]string{
		contextfabric.SubjectPullRequest:                  {"p.org_id", "p.repo_id", "p.number"},
		contextfabric.SubjectWorkItem:                     {"w.org_id", "w.repo_id", "w.work_item_id"},
		contractsv1.ContextFabricSubjectCIRun:             {"c.org_id", "c.repo_id", "c.run_id"},
		contractsv1.ContextFabricSubjectPullRequestReview: {"r.org_id", "r.repo_id", "r.number", "r.review_id"},
	}
	if len(wantColumnRefs) != len(censusKindRegistryEntries) {
		t.Fatalf("wantColumnRefs covers %d kinds, censusKindRegistryEntries has %d -- this test's own table is stale", len(wantColumnRefs), len(censusKindRegistryEntries))
	}
	for kind, want := range wantColumnRefs {
		entry, ok := censusKindRegistryEntries[kind]
		if !ok {
			t.Fatalf("no censusKindRegistryEntries entry for %s", kind)
		}
		for _, column := range want {
			if !strings.Contains(entry.identityColumn, column) {
				t.Fatalf("kind=%s identityColumn=%q is missing sort-key column %q -- a lossy witness reopens the injectivity trap (v6 stamp)", kind, entry.identityColumn, column)
			}
		}
	}
}
