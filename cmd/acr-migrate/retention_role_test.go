package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-11952 (lead ruling: least privilege): grant-runtime-acl gives the Ask
// Dev retention job's own role exactly dbacl.RetentionGrants, and nothing of
// it reaches the runtime role.
func TestRetentionRoleHoldsExactlyTheRetentionDeclaration(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_runtime_for_retention_test", RetentionRole: "acr_retention_exact_test"})
	want := map[string]map[string]bool{}
	for _, statement := range dbacl.RetentionGrantStatements(dbacl.QuoteIdent("acr_retention_exact_test")) {
		if want[statement.Table] == nil {
			want[statement.Table] = map[string]bool{}
		}
		for _, privilege := range statement.Privileges {
			want[statement.Table][privilege] = true
		}
	}
	var mismatches []string
	for _, table := range append(migrationTables(t), "schema_migrations") {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			var has bool
			require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, "acr_retention_exact_test", "acr."+table, privilege).Scan(&has))
			if has != want[table][privilege] {
				mismatches = append(mismatches, privilege+" on "+table+": holds "+strconv.FormatBool(has)+", declared "+strconv.FormatBool(want[table][privilege]))
			}
		}
	}
	require.Empty(t, mismatches)
	for _, table := range []string{"context_fabric_investigation_results", "context_fabric_threads", "context_fabric_investigation_submissions"} {
		var runtimeCanDelete bool
		require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, 'DELETE')`, "acr_runtime_for_retention_test", "acr."+table).Scan(&runtimeCanDelete))
		require.False(t, runtimeCanDelete, "the runtime role must not be able to delete %s", table)
	}
}

// grant-runtime-acl refuses to give the retention privileges to a role the
// runtime, the migrations or the purge runs as.
func TestRetentionGrantTargetsAreARoleOfTheirOwn(t *testing.T) {
	const runtimeDSN = "postgres://acr_runtime:x@h/acr"
	const migrationDSN = "postgres://acr_owner:x@h/acr"
	const purgeDSN = "postgres://acr_purge:x@h/acr"
	for name, tc := range map[string]struct {
		retention, purge string
		want             []string
		refused          string
	}{
		"distinct roles":                  {retention: "postgres://acr_retention:x@h/acr", purge: purgeDSN, want: []string{"acr_retention"}},
		"no purge role":                   {retention: "postgres://acr_retention:x@h/acr", want: []string{"acr_retention"}},
		"role parameter":                  {retention: "postgres://acr_login:x@h/acr?role=acr_retention", purge: purgeDSN, want: []string{"acr_login", "acr_retention"}},
		"the runtime role":                {retention: runtimeDSN, purge: purgeDSN, refused: "the runtime runs as"},
		"the runtime role by parameter":   {retention: "postgres://acr_login:x@h/acr?role=acr_runtime", refused: "the runtime runs as"},
		"the purge role":                  {retention: purgeDSN, purge: purgeDSN, refused: "the purge runs as"},
		"the purge role by parameter":     {retention: "postgres://acr_login:x@h/acr?role=acr_purge", purge: purgeDSN, refused: "the purge runs as"},
		"login shared with the runtime":   {retention: "postgres://acr_runtime:x@h/acr?role=acr_retention", refused: "the runtime runs as"},
		"the migration role":              {retention: migrationDSN, refused: "the migrations runs as"},
		"the migration role by parameter": {retention: "postgres://acr_login:x@h/acr?role=acr_owner", refused: "the migrations runs as"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := retentionGrantTargets(tc.retention, runtimeDSN, migrationDSN, tc.purge)
			if tc.refused != "" {
				require.ErrorContains(t, err, tc.refused)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRetentionRoleNeedsEveryGrantAndNothingMore runs the retention job as a
// role holding exactly dbacl.RetentionGrants (granted by grant-runtime-acl),
// on rows its producers wrote: an expired thread, a deleted thread with its
// event log, a record with no thread, a claim and two selections on a removed
// result. Then, for each declared privilege in turn, with that one privilege
// revoked: the grant probe fails naming the table (on a database with nothing
// due as well) and a pass fails. So every declared privilege is needed, and
// with all of them the whole job runs.
func TestRetentionRoleNeedsEveryGrantAndNothingMore(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_runtime_for_retention_job", RetentionRole: "acr_retention_job_test"})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := start
	seedStore, err := pginvestigation.NewStore(database.Runtime, pginvestigation.WithClock(func() time.Time { return clock }))
	require.NoError(t, err)
	retention, err := pginvestigation.NewStore(database.Retention, pginvestigation.WithUnthreadedRecordRetention(true))
	require.NoError(t, err)
	month := contractsv1.ContextFabricThreadRetentionMonth

	seed := func(org string) {
		owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-retention-job", OrgID: org}
		clock = start
		answer := func(name, thread string, retentionDays *contractsv1.ContextFabricThreadRetentionDays, result func(id string) contextfabric.InvestigationResult) contextfabric.SubmissionAdmission {
			key, err := contextfabric.SubmissionKeyFor(owner, "submission-job-"+name)
			require.NoError(t, err)
			admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + strings.Repeat("0", 64), ReservedResultID: "result_job_" + org + "_" + name,
				ThreadID: thread, ExpiresAt: clock.Add(time.Minute), ThreadRetention: retentionDays}
			if thread != "" {
				admission.Question = "job question " + name
			}
			_, admitted, err := seedStore.AdmitSubmission(ctx, admission, clock)
			require.NoError(t, err)
			require.True(t, admitted)
			require.NoError(t, seedStore.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), owner, result(admission.ReservedResultID), nil, nil,
				contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
				contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
				contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
			appended, err := seedStore.AppendRunEvent(ctx, contextfabric.RunKey{OrgID: org, ReservedResultID: admission.ReservedResultID},
				contextfabric.RunEvent{Seq: 1, Type: contractsv1.ContextFabricInvestigationEventResult, Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalResult, ResultID: admission.ReservedResultID}}, 0, clock)
			require.NoError(t, err)
			require.True(t, appended)
			return admission
		}
		plain := func(id string) contextfabric.InvestigationResult { return paritytest.ValidResult(id, "job "+id) }
		expired := answer("expired", "thread-job-expired", nil, plain)
		answer("claim", "thread-job-expired", nil, func(id string) contextfabric.InvestigationResult {
			return paritytest.ClaimBearingResult(id, "job claim", expired.ReservedResultID, "receipt_job")
		})
		answer("deleted", "thread-job-deleted", &month, plain)
		answer("lone", "", nil, plain)
		for _, statement := range []string{
			`INSERT INTO acr.context_fabric_clarification_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id,
    selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
VALUES ($1 || '_clar', $2, $3, $4, $5, 'receipt_job', 'repository', 'repo_job', 'web_assertion', '[]', '{}')`,
			`INSERT INTO acr.context_fabric_structure_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id,
    selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
VALUES ($1 || '_struct', $2, $3, $4, $5, 'expected_kind', 'receipt_job', 'pull_request', true, 'human_panel', 'web_assertion', '[]', '{}')`,
		} {
			_, err := database.Runtime.ExecContext(ctx, statement, "selection_job_"+org, org, clock, strings.Repeat("a", 64), expired.ReservedResultID)
			require.NoError(t, err)
		}
		key, err := contextfabric.ThreadKeyFor(owner, "thread-job-deleted")
		require.NoError(t, err)
		closed, err := seedStore.CloseThread(ctx, key, clock)
		require.NoError(t, err)
		require.True(t, closed)
	}
	sweepAt := start.Add(48 * time.Hour)
	count := func(table string) int {
		var n int
		require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT count(*) FROM acr.`+table).Scan(&n))
		return n
	}
	role := dbacl.QuoteIdent("acr_retention_job_test")

	require.NoError(t, retention.ProbeRetention(ctx), "the probe on an empty database, as the declared role")
	for index, grant := range dbacl.RetentionGrants {
		for _, privilege := range grant.Privileges {
			name := grant.Table + "/" + privilege
			_, err := database.Owner.ExecContext(ctx, `REVOKE `+privilege+` ON acr.`+grant.Table+` FROM `+role)
			require.NoError(t, err)
			err = retention.ProbeRetention(ctx)
			require.Error(t, err, "the probe without %s", name)
			require.Contains(t, err.Error(), "permission denied for table "+grant.Table, "the probe without %s", name)
			seed(fmt.Sprintf("org-retention-job-%02d-%s", index, strings.ToLower(privilege)))
			_, err = retention.SweepRetention(ctx, sweepAt, 0)
			require.Error(t, err, "a pass without %s", name)
			_, err = database.Owner.ExecContext(ctx, `GRANT `+privilege+` ON acr.`+grant.Table+` TO `+role)
			require.NoError(t, err)
		}
	}
	require.NoError(t, retention.ProbeRetention(ctx), "the probe with every grant back")
	seed("org-retention-job-final")
	for {
		sweep, err := retention.SweepRetention(ctx, sweepAt, 0)
		require.NoError(t, err, "a pass as the declared role")
		if !sweep.Changed() {
			break
		}
	}
	// Every unit is gone except each deleted thread's content-free closure
	// record (its row and its erased record) until its 30-day expiry.
	require.Zero(t, count("context_fabric_investigation_results"))
	require.Zero(t, count("context_fabric_result_feedback"))
	require.Zero(t, count("context_fabric_investigation_run_events"))
	require.Zero(t, count("context_fabric_clarification_selections"))
	require.Zero(t, count("context_fabric_structure_selections"))
	require.Zero(t, count("context_fabric_structure_supersession_claims"))
	orgs := 1
	for _, grant := range dbacl.RetentionGrants {
		orgs += len(grant.Privileges)
	}
	require.Equal(t, orgs, count("context_fabric_threads"), "one closure record per organization")
	var unerased int
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE left(reserved_result_id, 7) <> 'erased_' OR result_id IS NOT NULL`).Scan(&unerased))
	require.Zero(t, unerased, "every kept record is erased")
}

// grant-runtime-acl, run as the deployment runs it, refuses a retention DSN
// that is the migration DSN (the schema owner): the job never runs as the
// owner (codex #1808 r2).
func TestGrantRuntimeACLRefusesTheMigrationRoleAsTheRetentionRole(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_runtime_for_owner_refusal", SkipGrant: true})
	env := map[string]string{
		migrationDSNEnvironment: database.DSN,
		runtimeDSNEnvironment:   "postgres://acr_runtime_for_owner_refusal:unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
		retentionDSNEnvironment: database.DSN,
	}
	err := run(ctx, []string{"grant-runtime-acl"}, environment(env), &bytes.Buffer{})
	require.ErrorContains(t, err, "the migrations runs as")
}
