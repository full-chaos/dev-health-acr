package main

import (
	"context"
	"fmt"
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

// CHAOS-12244: the retention job runs as the runtime role, acr-api's own,
// holding exactly dbacl.RuntimeGrants (granted by grant-runtime-acl). It runs
// on rows its producers wrote: an expired thread, a deleted thread with its
// event log, a record with no thread, a claim and two selections on a removed
// result. Then, for each declared privilege in turn, with that one privilege
// revoked: the grant probe fails naming the table (on a database with nothing
// due as well) and a pass fails. So every declared privilege is needed, and
// with all of them the whole job runs.
func TestRuntimeRoleRunsTheAskDevRetention(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_runtime_retention_job_test"})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := start
	// The rows are seeded as the owner: the test revokes the runtime role's
	// retention privileges one by one, and admission needs some of them too.
	seedStore, err := pginvestigation.NewStore(database.Owner, pginvestigation.WithClock(func() time.Time { return clock }))
	require.NoError(t, err)
	retention, err := pginvestigation.NewStore(database.Runtime, pginvestigation.WithUnthreadedRecordRetention(true))
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
			_, err := database.Owner.ExecContext(ctx, statement, "selection_job_"+org, org, clock, strings.Repeat("a", 64), expired.ReservedResultID)
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
	role := dbacl.QuoteIdent("acr_runtime_retention_job_test")

	require.NoError(t, retention.ProbeRetention(ctx), "the probe on an empty database, as the runtime role")
	for index, grant := range dbacl.RetentionPrivileges {
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
		require.NoError(t, err, "a pass as the runtime role")
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
	for _, grant := range dbacl.RetentionPrivileges {
		orgs += len(grant.Privileges)
	}
	require.Equal(t, orgs, count("context_fabric_threads"), "one closure record per organization")
	var unerased int
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE left(reserved_result_id, 7) <> 'erased_' OR result_id IS NOT NULL`).Scan(&unerased))
	require.Zero(t, unerased, "every kept record is erased")
}
