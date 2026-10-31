package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

// The credential rotation rollback reads its own audit rows (two SELECTs on
// audit_events) while the compose and Kubernetes grants before the declaration
// gave the runtime role INSERT only. This runs create, rotate and rollback as
// the runtime role on the declaration's grants; without SELECT on audit_events
// it fails with permission denied.
func TestRuntimeRoleCanRotateAndRollBackACredential(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_rotation_runtime_test"})
	// The credential store reads the real clock, so the rollback deadline is relative to it.
	now := time.Now().UTC().Truncate(time.Second)
	audit, err := storagepostgres.NewAuditStore(database.Runtime)
	require.NoError(t, err)
	lifecycle, err := storagepostgres.NewCredentialStore(database.Runtime, audit)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)

	const orgID, actorID = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	source, err := service.Create(ctx, auth.CreateCredentialRequest{OrgID: orgID, Name: "rotation", RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: actorID})
	require.NoError(t, err)
	successor, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: orgID, CredentialID: source.Credential.CredentialID, CreatedBy: actorID, Overlap: time.Minute})
	require.NoError(t, err)
	revoked, err := lifecycle.RollbackCredentialRotation(ctx, storage.CredentialRotationRollbackInput{
		OrgID: orgID, SourceCredentialID: source.Credential.CredentialID, SuccessorCredentialID: successor.Credential.CredentialID,
		ActorID: actorID, RollbackUntil: now.Add(time.Minute),
	})
	require.NoError(t, err, "the rollback reads audit_events as the runtime role")
	require.Equal(t, successor.Credential.CredentialID, revoked.CredentialID)
}

// The OAuth flow on the narrowed grants: register a client, create and bind an
// authorization request, issue and consume its code, store a device grant, then
// run the purge the runtime runs. OAuth clients carry no UPDATE, because
// nothing updates them.
func TestRuntimeRoleCanRunTheWholeOAuthFlow(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_oauth_runtime_test"})
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	audit, err := storagepostgres.NewAuditStore(database.Runtime)
	require.NoError(t, err)
	devices, err := storagepostgres.NewDeviceAuthorizationStoreWithOptions(database.Runtime, audit, storagepostgres.DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := storagepostgres.NewOAuthStoreWithOptions(database.Runtime, storagepostgres.OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)

	var random [16]byte
	for i := range random {
		random[i] = 0xcd
	}
	clientID := storage.NewDynamicOAuthClientID(random)
	_, err = store.RegisterClient(ctx, storage.OAuthClient{ClientID: clientID, ClientName: "runtime role client", RedirectURIs: []string{"https://example.com/callback"}, CreatedAt: now})
	require.NoError(t, err, "register")
	device, err := devices.Create(ctx, storage.DeviceAuthorizationCreateInput{DeviceCodeHash: storage.HashDeviceCode("runtime-role-device"), UserCodeHash: storage.HashUserCode("RUNTIMER")})
	require.NoError(t, err, "device authorization")
	request, err := store.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
		HandleHash: storage.HashOAuthSecret("runtime-role-handle"), DeviceCodeHash: device.DeviceCodeHash, ClientID: clientID,
		ClientKind: storage.OAuthClientKindDynamic, RedirectURI: "https://example.com/callback", CodeChallenge: strings.Repeat("A", 43),
		Resource: "https://example.com/resource", Scope: "context:read", State: "state", CreatedAt: now, ExpiresAt: now.Add(storage.OAuthAuthorizationCodeTTL),
	})
	require.NoError(t, err, "authorize")
	require.NoError(t, store.BindAuthorizationRequestUser(ctx, request.HandleHash, "11111111-1111-1111-1111-111111111111", "subject"), "bind")
	code := storage.HashOAuthSecret("runtime-role-code")
	_, err = store.IssueAuthorizationCode(ctx, request.HandleHash, code, now.Add(storage.OAuthAuthorizationCodeTTL))
	require.NoError(t, err, "issue code")
	_, err = store.ConsumeAuthorizationCode(ctx, code)
	require.NoError(t, err, "token")
	_, err = store.CreateDeviceGrant(ctx, storage.OAuthDeviceGrant{
		DeviceCodeHash: device.DeviceCodeHash, ClientID: clientID, ClientKind: storage.OAuthClientKindDynamic,
		Resource: "https://example.com/resource", Scope: "context:read", CreatedAt: now, ExpiresAt: now.Add(storage.DeviceAuthorizationTTL),
	})
	require.NoError(t, err, "device grant")

	result, err := store.PurgeExpired(ctx, now.Add(240*time.Hour), time.Hour, time.Hour, 100)
	require.NoError(t, err, "purge as the runtime role")
	require.NotZero(t, result, "the purge removed the expired rows")
}

// The purges the API runs on a timer, as the runtime role. The packet purge
// locks rows with FOR UPDATE, which Postgres checks as UPDATE, so a role
// without UPDATE on the snapshot table is refused even when nothing expired.
func TestRuntimeRoleCanRunThePurgesTheRuntimeRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	enabled := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_purges_on_test", Features: dbacl.Features{dbacl.WorkloadTokenExchange: true}})
	packets, err := storagepostgres.NewPacketStore(enabled.Runtime, func() time.Time { return now })
	require.NoError(t, err)
	_, err = packets.PurgeExpiredWithAudit(ctx, now, 10)
	require.NoError(t, err, "packet purge")
	_, err = storagepostgres.NewWorkloadCredentialPurger(enabled.Runtime)(ctx, now, 10)
	require.NoError(t, err, "workload credential purge with workload token exchange on")

	// With the feature off the runtime does not run that purge, and the role
	// does not hold the privilege: the refusal names the class and the table.
	disabled := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_purges_off_test"})
	_, err = storagepostgres.NewWorkloadCredentialPurger(disabled.Runtime)(ctx, now, 10)
	require.Error(t, err)
	var class *storage.DependencyErrorClass
	require.ErrorAs(t, err, &class)
	require.Equal(t, "insufficient_privilege", class.Class)
	require.Equal(t, "client_credentials", class.Table)
}

// CHAOS-12140: the browser family purge, as a runtime role granted exactly the
// declaration with only the browser client on, runs (both statements and both
// remaining probes); with the feature off the role holds no DELETE on
// acr.client_credentials and the purge is refused there.
func TestRuntimeRoleCanRunTheBrowserFamilyPurge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	purge := func(db *sql.DB) error {
		audit, err := storagepostgres.NewAuditStore(db)
		require.NoError(t, err)
		store, err := storagepostgres.NewBrowserRefreshStore(db, audit)
		require.NoError(t, err)
		if _, err := store.PurgeEndedFamilies(ctx, now, time.Hour, 10); err != nil {
			return err
		}
		_, err = store.CountEndedFamiliesRemaining(ctx, now, time.Hour, 10)
		return err
	}

	enabled := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_browser_purge_on_test", Features: dbacl.Features{dbacl.BrowserClient: true}})
	require.NoError(t, purge(enabled.Runtime), "browser family purge with the browser client on")

	disabled := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_browser_purge_off_test"})
	err := purge(disabled.Runtime)
	require.Error(t, err)
	var class *storage.DependencyErrorClass
	require.ErrorAs(t, err, &class)
	require.Equal(t, "insufficient_privilege", class.Class)
	require.Equal(t, "client_credentials", class.Table)
}

// CHAOS-11952: the Ask Dev retention job, as a runtime role granted exactly
// the declaration, removes an expired thread's whole backing set (the
// cascades to the event log and the feedback run as the owner), erases a
// deleted thread's records and removes a record with no thread. Without the
// DELETE grant on the results the same pass is refused there (SQLSTATE 42501
// on that table): the grant is what lets the job run, and a missing one fails
// the first pass at startup.
func TestRuntimeRoleCanRunTheAskDevRetention(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	seedAndSweep := func(db *sql.DB) (contextfabric.RetentionSweep, error) {
		clock := start
		store, err := pginvestigation.NewStore(db, pginvestigation.WithClock(func() time.Time { return clock }))
		require.NoError(t, err)
		owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-retention-role", OrgID: "org-retention-role"}
		month := contractsv1.ContextFabricThreadRetentionMonth
		for _, turn := range []struct {
			name, thread string
			retention    *contractsv1.ContextFabricThreadRetentionDays
		}{{"expired", "thread-role-expired", nil}, {"deleted", "thread-role-deleted", &month}, {"lone", "", nil}} {
			key, err := contextfabric.SubmissionKeyFor(owner, "submission-role-"+turn.name)
			require.NoError(t, err)
			admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + strings.Repeat("0", 64), ReservedResultID: "result_role_" + turn.name,
				ThreadID: turn.thread, ExpiresAt: clock.Add(time.Minute), ThreadRetention: turn.retention}
			if turn.thread != "" {
				admission.Question = "role question " + turn.name
			}
			_, admitted, err := store.AdmitSubmission(ctx, admission, clock)
			require.NoError(t, err)
			require.True(t, admitted)
			require.NoError(t, store.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), owner, paritytest.ValidResult(admission.ReservedResultID, "role "+turn.name), nil, nil,
				contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
				contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
				contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
			appended, err := store.AppendRunEvent(ctx, contextfabric.RunKey{OrgID: owner.OrgID, ReservedResultID: admission.ReservedResultID},
				contextfabric.RunEvent{Seq: 1, Type: contractsv1.ContextFabricInvestigationEventResult, Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalResult, ResultID: admission.ReservedResultID}}, 0, clock)
			require.NoError(t, err)
			require.True(t, appended)
			_, err = store.SetResultFeedback(ctx, owner, admission.ReservedResultID, contractsv1.ContextFabricResultFeedbackYes, contractsv1.ContextFabricResultFeedbackNo, clock)
			require.NoError(t, err)
		}
		deletedKey, err := contextfabric.ThreadKeyFor(owner, "thread-role-deleted")
		require.NoError(t, err)
		closed, err := store.CloseThread(ctx, deletedKey, clock)
		require.NoError(t, err)
		require.True(t, closed)
		clock = start.Add(25 * time.Hour)
		return store.SweepRetention(ctx, clock, 0)
	}

	granted := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_retention_on_test"})
	sweep, err := seedAndSweep(granted.Runtime)
	require.NoError(t, err, "the retention pass as the runtime role")
	require.Equal(t, contextfabric.RetentionSweep{ThreadsRemoved: 1, ThreadsErased: 1, RecordsRemoved: 1, ResultsRemoved: 3}, sweep)

	revoked := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_retention_off_test"})
	_, err = revoked.Owner.ExecContext(ctx, `REVOKE DELETE ON acr.context_fabric_investigation_results FROM `+dbacl.QuoteIdent(revoked.Role))
	require.NoError(t, err)
	_, err = seedAndSweep(revoked.Runtime)
	require.Error(t, err)
	// The investigation store names the refusal by its SQLSTATE and table
	// (sanitizeError keeps the server's message, never a DSN).
	require.Contains(t, err.Error(), "permission denied for table context_fabric_investigation_results")
	require.Contains(t, err.Error(), "SQLSTATE 42501")
}
