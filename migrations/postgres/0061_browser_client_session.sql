-- CHAOS-12123: the first-party browser OAuth client.
--
-- 1. The web session an approval was made in. The consent assertion signs
--    it (sid); the approval stores it on the device authorization, and the
--    browser-class credential redeemed from that authorization takes it from
--    the locked row (session_id, migration 0058), never from the caller.
-- 2. A browser-class credential names the client it was issued to and never
--    outlives its token family (0058 already requires its session and
--    family). A release without the browser rotation guard -- a rollback --
--    cannot mint a long-lived browser credential by rotating one: the row is
--    refused.
-- 3. The submission canonical-owner backfill of migration 0059, repeated:
--    rows a replica on the release before 0059 wrote during that rollout
--    carry no canonical owner, and the runner applies 0059 once. The
--    statements are 0059's own and skip every row already classified.
--
-- Rollback: the column is nullable and the previous release neither reads
-- nor writes it. The new CHECK holds for every row the previous release
-- writes except one: its rotation of a browser credential (it has no browser
-- guard and gives the successor 30 days), which the CHECK refuses -- the
-- rotation fails closed. No inline BEGIN/COMMIT (the runner wraps the file).
-- Idempotent.

ALTER TABLE acr.device_authorizations
    ADD COLUMN IF NOT EXISTS approving_session_id TEXT;

ALTER TABLE acr.device_authorizations
    DROP CONSTRAINT IF EXISTS ck_acr_device_authorizations_approving_session;
ALTER TABLE acr.device_authorizations
    ADD CONSTRAINT ck_acr_device_authorizations_approving_session CHECK (
        approving_session_id IS NULL OR char_length(approving_session_id) BETWEEN 1 AND 256
    );

COMMENT ON COLUMN acr.device_authorizations.approving_session_id IS
    'CHAOS-12123: web session (sid) the approving web assertion was signed in; NULL when it named none.';

ALTER TABLE acr.client_credentials
    DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_browser_client;
ALTER TABLE acr.client_credentials
    ADD CONSTRAINT ck_acr_client_credentials_browser_client CHECK (
        client_class IS DISTINCT FROM 'browser'
        OR (client_id IS NOT NULL AND expires_at IS NOT NULL AND expires_at <= family_expires_at)
    );

COMMENT ON COLUMN acr.client_credentials.session_id IS
    'CHAOS-12123: browser class only -- the web session (sid) of the approval, from the device authorization; immutable once set.';

-- Backfill the submissions. A web assertion owner is its subject on the
-- browser; a credential owner takes its credential's approving user, with the
-- surface its client class names. When two legacy records map to one
-- canonical key, only the earliest is classified: the others keep their legacy
-- owner and stay readable by it.
WITH canonical AS (
    SELECT s.org_id, s.owner_auth_method, s.owner_subject, s.submission_id, s.admitted_at,
           CASE WHEN s.owner_auth_method = 'web_assertion' THEN s.owner_subject
                ELSE credential.approving_user_id END AS user_id,
           CASE WHEN s.owner_auth_method = 'web_assertion' THEN 'browser'
                WHEN credential.client_class = 'browser' THEN 'browser'
                WHEN credential.client_class IN ('oauth_user', 'native_device') THEN 'mcp' END AS surface
      FROM acr.context_fabric_investigation_submissions AS s
      LEFT JOIN acr.client_credentials AS credential
             ON s.owner_auth_method = 'credential'
            AND credential.credential_id = s.owner_subject
            AND credential.org_id::text = s.org_id
     WHERE s.owner_user_id IS NULL
), ranked AS (
    SELECT canonical.*,
           row_number() OVER (PARTITION BY org_id, user_id, surface, submission_id ORDER BY admitted_at, owner_auth_method, owner_subject) AS rank
      FROM canonical
     WHERE user_id IS NOT NULL AND surface IS NOT NULL AND char_length(user_id) BETWEEN 1 AND 200
)
UPDATE acr.context_fabric_investigation_submissions AS target
   SET owner_user_id = ranked.user_id, owner_surface = ranked.surface
  FROM ranked
 WHERE ranked.rank = 1
   AND target.org_id = ranked.org_id
   AND target.owner_auth_method = ranked.owner_auth_method
   AND target.owner_subject = ranked.owner_subject
   AND target.submission_id = ranked.submission_id
   AND NOT EXISTS (
       SELECT 1 FROM acr.context_fabric_investigation_submissions AS taken
        WHERE taken.org_id = ranked.org_id AND taken.submission_id = ranked.submission_id
          AND taken.owner_user_id = ranked.user_id AND taken.owner_surface = ranked.surface
   );

-- Backfill the results: the same mapping over each result's legacy owner.
UPDATE acr.context_fabric_investigation_results AS result
   SET submission_owner_user_id = mapped.user_id, submission_owner_surface = mapped.surface
  FROM (
    SELECT r.result_id,
           CASE WHEN r.submission_owner_auth_method = 'web_assertion' THEN r.submission_owner_subject
                ELSE credential.approving_user_id END AS user_id,
           CASE WHEN r.submission_owner_auth_method = 'web_assertion' THEN 'browser'
                WHEN credential.client_class = 'browser' THEN 'browser'
                WHEN credential.client_class IN ('oauth_user', 'native_device') THEN 'mcp' END AS surface
      FROM acr.context_fabric_investigation_results AS r
      LEFT JOIN acr.client_credentials AS credential
             ON r.submission_owner_auth_method = 'credential'
            AND credential.credential_id = r.submission_owner_subject
            AND credential.org_id::text = r.org_id
     WHERE r.submission_owner_subject IS NOT NULL AND r.submission_owner_user_id IS NULL
  ) AS mapped
 WHERE result.result_id = mapped.result_id
   AND mapped.user_id IS NOT NULL AND mapped.surface IS NOT NULL
   AND char_length(mapped.user_id) BETWEEN 1 AND 200;
