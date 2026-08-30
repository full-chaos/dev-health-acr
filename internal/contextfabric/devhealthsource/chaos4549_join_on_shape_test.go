package devhealthsource_test

// CHAOS-4549 (chris, RULED 2026-08-29 "26"): raising acr's ClickHouse
// fixture pin from 24.8 to the 26 line removes the only CI gate that ever
// happened to reject a non-portable JOIN ON shape. CHAOS-4521b shipped a
// multi-arm `JOIN ... ON (... OR ...)` that is valid on prod's 26.7
// analyzer and rejected on 24.8 under every analyzer setting (`Code: 403
// Unsupported JOIN ON conditions`) -- caught only because acr's pinned
// fixtures ran it, after four review rounds of "executed against a real
// ClickHouse" proofs that had all run against 26.7. The equality-only-join
// rule that fix established is a STANDING RULE (cf-standing-rules.md),
// enforced from here on statically instead of by an old-engine fixture
// that only rejected the shape as a side effect.
//
// This is the acr twin of dev-health-go's own guard
// (readers/chaos4521b_project_work_scope_test.go) and generalizes this
// package's own narrower
// TestChaos4542_KeyArmSelectsTheKeyScopeRowAndTheScopeArmDoesNot
// (chaos4542_project_identity_test.go) into a full sweep.
//
// MECHANISM: run both of this package's projection sources
// (ClickHouseProjectionSource, TeamsProjectsSource) once each through
// chaos3898_d7_join_fix_test.go's existing statementRecordingClient
// (which wraps a fakeClient and records every statement it is asked to
// run -- no code changes needed there), seeded broadly enough
// (clickhouse_test.go's baseTables / teams_projects_test.go's
// liveShapedTeamsProjectsClient) that every entityTables/
// teamsProjectsTables producer actually issues its query -- an unseeded
// table still issues its statement, the fake just answers it empty, it
// never skips the call -- then check every JOIN ... ON in every captured
// statement.
//
// RULE: internal/chfixture.JoinONViolations requires every ON clause to be
// a top-level AND-conjunction of conjuncts, each EXACTLY "<operand> =
// <operand>", where an operand is a qualified column, a literal, or a
// function call of any arity of portable operands (recursively) -- so a
// literal conjunct (m.is_active = 1, tables.go) and a multi-argument cast
// (ifNull(a.team_id, ''), teams_projects_edges.go) are both portable; only
// OR, a comparator other than a single "=" per conjunct, and a bare
// boolean-function conjunct with no "=" fail. See chfixture.go's doc
// comment for the full rationale and team-lead's 2026-08-29 scope
// correction that relaxed this from an earlier, stricter draft.
//
// This also captures whatever dev-health-go's readers package renders
// where this package's producers delegate to it (queryProjectTeams ->
// readers.ProjectOwnershipJoinSQL et al.) -- the RECORDED statement is the
// fully rendered text regardless of which package composed which
// fragment, so that SQL is checked here too, not skipped as "someone
// else's guard". dev-health-go's own
// readers/chaos4521b_project_work_scope_test.go additionally pins it at
// the source.
import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestChaos4549AllJoinOnClausesArePortable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)

	entityRecorder := &statementRecordingClient{inner: &fakeClient{tables: baseTables(at)}}
	entitySource, err := devhealthsource.NewClickHouseProjectionSource(entityRecorder)
	if err != nil {
		t.Fatalf("new entity source: %v", err)
	}
	if _, _, err := entitySource.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("entity NextProjectionBatch: %v", err)
	}

	teamsRecorder := &statementRecordingClient{inner: liveShapedTeamsProjectsClient()}
	teamsSource, err := devhealthsource.NewTeamsProjectsSource(teamsRecorder, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	if _, _, err := teamsSource.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}); err != nil {
		t.Fatalf("teams/projects NextProjectionBatch: %v", err)
	}

	statements := make([]string, 0, len(entityRecorder.statements)+len(teamsRecorder.statements))
	statements = append(statements, entityRecorder.statements...)
	statements = append(statements, teamsRecorder.statements...)
	if len(statements) == 0 {
		t.Fatal("no statements captured -- the recording client is not wired to the sources")
	}

	totalClauses, totalConjuncts := 0, 0
	for _, statement := range statements {
		violations, clauses, conjuncts := chfixture.JoinONViolations(statement)
		totalClauses += clauses
		totalConjuncts += conjuncts
		for _, violation := range violations {
			t.Errorf("a JOIN ON conjunct is not exactly <operand> = <operand> (%q); the pre-26 ClickHouse analyzer rejects anything else in an ON clause (CHAOS-4549)\n%s", violation, statement)
		}
	}
	// codex review finding (devhealthfacts sibling, same class here): a
	// sweep that only checks "at least one statement was captured" still
	// passes if every captured statement happens to carry zero JOIN
	// clauses -- assert real structural coverage, not just query count.
	if totalClauses == 0 {
		t.Fatal("0 JOIN ON clauses found across all captured statements -- the ON/AND parser is not matching anything, not proof of portability")
	}
	t.Logf("CHAOS-4549: checked %d JOIN ON clauses (%d conjuncts) across %d captured statements (devhealthsource)", totalClauses, totalConjuncts, len(statements))
}
