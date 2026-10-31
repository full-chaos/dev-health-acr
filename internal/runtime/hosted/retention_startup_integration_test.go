package hosted

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestOpenPostgres_retentionRunsAtStartupThroughTheRealConstructor drives the
// REAL openPostgres against a fresh migrated Postgres as a runtime role
// (CHAOS-11952): without DELETE on the results the first retention pass fails
// startup and removes nothing; with it, the startup pass removes a record
// with no thread past its retention-0 window, with its result, and logs its
// counts-only line.
func TestOpenPostgres_retentionRunsAtStartupThroughTheRealConstructor(t *testing.T) {
	h := newOAuthPurgeStartupHarness(t)
	ctx, owner := h.ctx, h.owner
	// Long past any window against the real clock the loop uses.
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store, err := pginvestigation.NewStore(owner)
	require.NoError(t, err)
	principal := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-startup-retention", OrgID: "org-startup-retention"}
	key, err := contextfabric.SubmissionKeyFor(principal, "submission-startup-retention")
	require.NoError(t, err)
	admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
		ReservedResultID: "result_startup_retention", ExpiresAt: t0.Add(time.Minute)}
	_, admitted, err := store.AdmitSubmission(ctx, admission, t0)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), principal,
		paritytest.ValidResult(admission.ReservedResultID, "startup retention"), nil, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_submissions"))
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_results"))

	cfg := h.config()
	cfg.OAuthIssuer, cfg.OAuthResources, cfg.OAuthConsentURL = "", nil, ""

	// No DELETE on the results: the startup pass fails startup, naming the
	// retention job, and removes nothing.
	_, err = owner.ExecContext(ctx, `REVOKE DELETE ON acr.context_fabric_investigation_results FROM acr_purge_rt`)
	require.NoError(t, err)
	logger, _ := newOAuthPurgeStartupLogger()
	_, err = openPostgres(ctx, cfg, logger)
	require.Error(t, err, "a runtime role without DELETE on the backing set must fail startup")
	require.Contains(t, err.Error(), "enforce Ask Dev retention")
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_submissions"))
	require.Equal(t, 1, h.count(t, "context_fabric_investigation_results"))

	// Granted: the startup pass removes the record and its result.
	_, err = owner.ExecContext(ctx, `GRANT DELETE ON acr.context_fabric_investigation_results TO acr_purge_rt`)
	require.NoError(t, err)
	logger, logs := newOAuthPurgeStartupLogger()
	components, err := openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 0, h.count(t, "context_fabric_investigation_submissions"), "the startup pass removes the record past its window")
	require.Equal(t, 0, h.count(t, "context_fabric_investigation_results"), "with its result")
	require.Contains(t, logs.String(), `msg="`+RetentionPurgeLogMessage+`"`)
	require.Contains(t, logs.String(), "records_removed=1")
	require.Contains(t, logs.String(), "results_removed=1")
	require.NotContains(t, logs.String(), "result_startup_retention", "the line carries counts only")
}
