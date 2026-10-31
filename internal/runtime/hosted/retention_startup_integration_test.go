package hosted

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestOpenPostgres_retentionRunsOnlyBehindItsSwitchAsItsOwnRole drives the
// REAL openPostgres against a fresh migrated Postgres (CHAOS-11952):
//   - switch off: no retention pool, no probe, nothing removed, one line that
//     retention is off;
//   - switch on, the retention role missing one grant, nothing due: the grant
//     probe fails startup (an empty first pass would prove nothing);
//   - switch on, the role holding exactly dbacl.RetentionGrants: the startup
//     pass removes a record with no thread past its retention-0 window, with
//     its result, through that role, and logs its counts-only line.
func TestOpenPostgres_retentionRunsOnlyBehindItsSwitchAsItsOwnRole(t *testing.T) {
	h := newOAuthPurgeStartupHarness(t)
	ctx, owner := h.ctx, h.owner
	for _, statement := range []string{
		`CREATE ROLE acr_retention_rt LOGIN PASSWORD 'rt'`,
		`GRANT USAGE ON SCHEMA acr TO acr_retention_rt`,
	} {
		_, err := owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	for _, statement := range dbacl.RetentionGrantStatements(dbacl.QuoteIdent("acr_retention_rt")) {
		_, err := owner.ExecContext(ctx, statement.SQL)
		require.NoError(t, err, statement.SQL)
	}
	parsed, err := url.Parse(h.runtimeDSN)
	require.NoError(t, err)
	parsed.User = url.UserPassword("acr_retention_rt", "rt")
	retentionDSN := parsed.String()

	cfg := h.config()
	cfg.OAuthIssuer, cfg.OAuthResources, cfg.OAuthConsentURL = "", nil, ""

	// Switch on, one grant missing, nothing due: the probe fails startup.
	_, err = owner.ExecContext(ctx, `REVOKE DELETE ON acr.context_fabric_threads FROM acr_retention_rt`)
	require.NoError(t, err)
	on := cfg
	on.AskDevRetentionEnabled, on.AskDevRetentionPostgresDSN = true, retentionDSN
	logger, _ := newOAuthPurgeStartupLogger()
	_, err = openPostgres(ctx, on, logger)
	require.Error(t, err, "a retention role without one of its grants must fail startup, with nothing due")
	require.Contains(t, err.Error(), "enforce Ask Dev retention")
	require.Contains(t, err.Error(), "permission denied for table context_fabric_threads")
	_, err = owner.ExecContext(ctx, `GRANT DELETE ON acr.context_fabric_threads TO acr_retention_rt`)
	require.NoError(t, err)

	// A record with no thread, long past its window against the real clock.
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seed, err := pginvestigation.NewStore(owner)
	require.NoError(t, err)
	principal := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-startup-retention", OrgID: "org-startup-retention"}
	key, err := contextfabric.SubmissionKeyFor(principal, "submission-startup-retention")
	require.NoError(t, err)
	admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + strings.Repeat("0", 64),
		ReservedResultID: "result_startup_retention", ExpiresAt: t0.Add(time.Minute)}
	_, admitted, err := seed.AdmitSubmission(ctx, admission, t0)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, seed.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), principal,
		paritytest.ValidResult(admission.ReservedResultID, "startup retention"), nil, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))

	// Switch off: nothing removed, the line says so.
	logger, logs := newOAuthPurgeStartupLogger()
	components, err := openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_submissions"), "with the switch off no row is removed")
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_results"))
	require.Contains(t, logs.String(), `msg="`+RetentionPurgeLogMessage+`"`)
	require.Contains(t, logs.String(), "outcome=disabled")

	// Switch on with every grant: the startup pass removes the record and
	// its result through the retention role.
	logger, logs = newOAuthPurgeStartupLogger()
	components, err = openPostgres(ctx, on, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 0, h.count(t, "context_fabric_investigation_submissions"), "the startup pass removes the record past its window")
	require.Equal(t, 0, h.count(t, "context_fabric_investigation_results"), "with its result")
	require.Contains(t, logs.String(), "records_removed=1")
	require.Contains(t, logs.String(), "results_removed=1")
	require.NotContains(t, logs.String(), "result_startup_retention", "the line carries counts only")
}
