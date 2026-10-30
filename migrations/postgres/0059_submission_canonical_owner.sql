-- CHAOS-12091: the canonical owner of a submission and of the result it
-- completes -- the approving user on a surface -- beside the legacy owner pair.
--
-- The legacy owner is (owner_auth_method, owner_subject): for a credential it
-- is the credential id, so every rotation (and every browser token refresh)
-- made the same user a new owner: the user's own submission read 404 and a
-- same-key retry ran a second time. The canonical owner is the credential's
-- immutable approving user (migration 0058) or the web assertion's subject,
-- with the surface it acts through (browser | mcp). An owner matches when the
-- legacy pair is the same OR the canonical user and surface are the same
-- (contextfabric.SameSubmissionOwner); a user's browser records and MCP
-- records stay apart.
--
-- No column is repurposed: the legacy pair keeps its meaning and its primary
-- key, so a replica still running the previous release reads and writes
-- exactly as before during a rolling deploy. The previous release ignores the
-- new columns (nullable). Rows it writes during the rollout carry no
-- canonical owner and are matched by their legacy pair only; re-running this
-- file (idempotent) classifies them.
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

ALTER TABLE acr.context_fabric_investigation_submissions
    ADD COLUMN IF NOT EXISTS owner_user_id TEXT,
    ADD COLUMN IF NOT EXISTS owner_surface TEXT;

ALTER TABLE acr.context_fabric_investigation_submissions
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_submissions_canonical_owner;
ALTER TABLE acr.context_fabric_investigation_submissions
    ADD CONSTRAINT ck_acr_cf_investigation_submissions_canonical_owner CHECK (
        (owner_user_id IS NULL AND owner_surface IS NULL)
        OR (char_length(owner_user_id) BETWEEN 1 AND 200 AND owner_surface IN ('browser', 'mcp'))
    );

-- One record per (organization, canonical owner, submission id): with the
-- primary key on the legacy pair, a rotated credential cannot admit its
-- user's key a second time.
CREATE UNIQUE INDEX IF NOT EXISTS ux_acr_cf_investigation_submissions_canonical_owner
    ON acr.context_fabric_investigation_submissions (org_id, owner_user_id, owner_surface, submission_id)
    WHERE owner_user_id IS NOT NULL;

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS submission_owner_user_id TEXT,
    ADD COLUMN IF NOT EXISTS submission_owner_surface TEXT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_submission_canonical_owner;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_submission_canonical_owner CHECK (
        (submission_owner_user_id IS NULL AND submission_owner_surface IS NULL)
        OR (submission_owner_subject IS NOT NULL
            AND char_length(submission_owner_user_id) BETWEEN 1 AND 200
            AND submission_owner_surface IN ('browser', 'mcp'))
    );

COMMENT ON COLUMN acr.context_fabric_investigation_submissions.owner_user_id IS
    'CHAOS-12091: canonical owner -- the approving user of a user-approved credential, or the web assertion subject. NULL when the owner has no user.';
COMMENT ON COLUMN acr.context_fabric_investigation_submissions.owner_surface IS
    'CHAOS-12091: browser | mcp, from the credential client class (web assertion: browser).';
COMMENT ON COLUMN acr.context_fabric_investigation_results.submission_owner_user_id IS
    'CHAOS-12091: canonical owner of the submission this result completed (copied from the submission record).';
COMMENT ON COLUMN acr.context_fabric_investigation_results.submission_owner_surface IS
    'CHAOS-12091: browser | mcp of the canonical owner.';

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
