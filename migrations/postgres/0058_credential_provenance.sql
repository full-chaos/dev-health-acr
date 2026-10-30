-- CHAOS-12083: immutable issuance provenance on every credential row.
--
-- Today a credential row cannot say how, or for whom, it was issued:
-- created_by is the approving user at issuance but a self-rotation writes the
-- rotating credential's own id there, and nothing records the issuance class.
-- These columns record it once, at issuance, and every rotation copies them
-- from the source row (internal/storage/{postgres,memory}).
--
--   client_class      closed vocabulary, derived by the credential lifecycle
--                     from the issuance path (credentiallifecycle
--                     deriveClientClass): oauth_user, native_device,
--                     operator, workload; browser is reserved for the
--                     first-party web client (CHAOS-12070). NULL only on a
--                     legacy row whose class the backfill below cannot prove.
--   approving_user_id the web-session subject that approved the device or
--                     OAuth authorization (the device authorization row's
--                     approving_subject). NULL for operator and workload.
--   client_id         the OAuth client the credential was issued to.
--   session_id,       reserved for the browser class (CHAOS-12070): no
--   family_id,        issuance path writes them yet; rotation copies them.
--   family_expires_at
--
-- Immutable once set: a trigger refuses any UPDATE that changes a non-NULL
-- value, so a later code path cannot re-attribute a credential. NULL -> value
-- stays allowed so this backfill (and a re-run of it) can classify legacy
-- rows; no application path writes these columns after INSERT.
--
-- Rollback: the columns are nullable and the previous release never reads
-- them; its INSERTs leave them NULL (allowed) and its UPDATEs touch only
-- lifecycle columns (allowed by the trigger). No inline BEGIN/COMMIT (the
-- runner wraps the file). Idempotent.

ALTER TABLE acr.client_credentials
    ADD COLUMN IF NOT EXISTS client_class TEXT,
    ADD COLUMN IF NOT EXISTS approving_user_id TEXT,
    ADD COLUMN IF NOT EXISTS client_id TEXT,
    ADD COLUMN IF NOT EXISTS session_id TEXT,
    ADD COLUMN IF NOT EXISTS family_id TEXT,
    ADD COLUMN IF NOT EXISTS family_expires_at TIMESTAMPTZ;

ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_client_class;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_client_class CHECK (
    client_class IS NULL
    OR client_class IN ('browser', 'oauth_user', 'native_device', 'operator', 'workload')
);

ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_provenance_lengths;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_provenance_lengths CHECK (
    (approving_user_id IS NULL OR char_length(approving_user_id) BETWEEN 1 AND 200)
    AND (client_id IS NULL OR char_length(client_id) BETWEEN 1 AND 256)
    AND (session_id IS NULL OR char_length(session_id) BETWEEN 1 AND 256)
    AND (family_id IS NULL OR char_length(family_id) BETWEEN 1 AND 256)
);

-- A user-approved class names its approving user; operator and workload
-- credentials have none and are issued to no OAuth client.
ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_approving_user;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_approving_user CHECK (
    client_class IS NULL
    OR (client_class IN ('browser', 'oauth_user', 'native_device') AND approving_user_id IS NOT NULL)
    OR (client_class IN ('operator', 'workload') AND approving_user_id IS NULL AND client_id IS NULL)
);

-- Session and family fields belong to the browser class only, and a browser
-- credential carries all three.
ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_browser_family;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_browser_family CHECK (
    (client_class = 'browser' AND session_id IS NOT NULL AND family_id IS NOT NULL AND family_expires_at IS NOT NULL)
    OR (client_class IS DISTINCT FROM 'browser' AND session_id IS NULL AND family_id IS NULL AND family_expires_at IS NULL)
);

COMMENT ON COLUMN acr.client_credentials.client_class IS
    'CHAOS-12083: issuance class (browser|oauth_user|native_device|operator|workload), derived from the issuance path; immutable once set. NULL only on an unproven legacy row.';
COMMENT ON COLUMN acr.client_credentials.approving_user_id IS
    'CHAOS-12083: web-session subject that approved the authorization behind the credential; immutable once set. NULL for operator and workload.';
COMMENT ON COLUMN acr.client_credentials.client_id IS
    'CHAOS-12083: OAuth client the credential was issued to; immutable once set.';
COMMENT ON COLUMN acr.client_credentials.session_id IS
    'CHAOS-12083: reserved for the browser class (CHAOS-12070); immutable once set.';
COMMENT ON COLUMN acr.client_credentials.family_id IS
    'CHAOS-12083: reserved for the browser class (CHAOS-12070); immutable once set.';
COMMENT ON COLUMN acr.client_credentials.family_expires_at IS
    'CHAOS-12083: reserved for the browser class (CHAOS-12070); immutable once set.';

-- Backfill, proven rows only. A row the steps below cannot prove keeps a NULL
-- class (and with it credential-id ownership, CHAOS-12091).
--
-- 1. Workload exchange: the row names its workload binding.
UPDATE acr.client_credentials
SET client_class = 'workload'
WHERE client_class IS NULL AND workload_binding_id IS NOT NULL;

-- 2. Device and OAuth redemption: the device authorization that redeemed the
--    row names its approver; a resource marks an OAuth exchange, whose client
--    is on its authorization request or device grant while that row exists.
UPDATE acr.client_credentials AS credential
SET client_class = CASE WHEN credential.resource IS NULL THEN 'native_device' ELSE 'oauth_user' END,
    approving_user_id = device_auth.approving_subject,
    client_id = CASE WHEN credential.resource IS NULL THEN NULL
                     ELSE COALESCE(request.client_id, grant_row.client_id) END
FROM acr.device_authorizations AS device_auth
LEFT JOIN acr.oauth_authorization_requests AS request
       ON request.device_code_hash = device_auth.device_code_hash
LEFT JOIN acr.oauth_device_grants AS grant_row
       ON grant_row.device_code_hash = device_auth.device_code_hash
WHERE credential.client_class IS NULL
  AND credential.workload_binding_id IS NULL
  AND device_auth.redeemed_credential_id = credential.credential_id
  AND device_auth.approving_authentication_method = 'web_assertion'
  AND device_auth.approving_subject IS NOT NULL
  AND char_length(device_auth.approving_subject) BETWEEN 1 AND 200;

-- 3. Rotation successors inherit their source's provenance. The rotation
--    edges are read once from the audit log (credential_rotated names the
--    successor), then walked from every classified row.
WITH RECURSIVE edges AS MATERIALIZED (
    SELECT audit_row.org_id, audit_row.resource_id AS source_id,
           audit_row.metadata->>'replacement_credential_id' AS successor_id
    FROM acr.audit_events AS audit_row
    WHERE audit_row.action = 'credential_rotated'
      AND audit_row.metadata ? 'replacement_credential_id'
), chain AS (
    SELECT credential.org_id, credential.credential_id, credential.client_class,
           credential.approving_user_id, credential.client_id
    FROM acr.client_credentials AS credential
    WHERE credential.client_class IS NOT NULL
    UNION
    SELECT chain.org_id, edges.successor_id, chain.client_class,
           chain.approving_user_id, chain.client_id
    FROM chain
    JOIN edges ON edges.org_id = chain.org_id AND edges.source_id = chain.credential_id
)
UPDATE acr.client_credentials AS successor
SET client_class = chain.client_class,
    approving_user_id = chain.approving_user_id,
    client_id = chain.client_id
FROM chain
WHERE successor.org_id = chain.org_id
  AND successor.credential_id = chain.credential_id
  AND successor.client_class IS NULL;

-- Immutability. SECURITY INVOKER: it reads only NEW and OLD. EXECUTE is revoked
-- from PUBLIC: a trigger fires without that privilege.
CREATE OR REPLACE FUNCTION acr.client_credentials_provenance_immutable() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF (OLD.client_class IS NOT NULL AND NEW.client_class IS DISTINCT FROM OLD.client_class)
       OR (OLD.approving_user_id IS NOT NULL AND NEW.approving_user_id IS DISTINCT FROM OLD.approving_user_id)
       OR (OLD.client_id IS NOT NULL AND NEW.client_id IS DISTINCT FROM OLD.client_id)
       OR (OLD.session_id IS NOT NULL AND NEW.session_id IS DISTINCT FROM OLD.session_id)
       OR (OLD.family_id IS NOT NULL AND NEW.family_id IS DISTINCT FROM OLD.family_id)
       OR (OLD.family_expires_at IS NOT NULL AND NEW.family_expires_at IS DISTINCT FROM OLD.family_expires_at) THEN
        RAISE EXCEPTION 'credential provenance is immutable'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION acr.client_credentials_provenance_immutable() FROM PUBLIC;

DROP TRIGGER IF EXISTS trg_client_credentials_provenance_immutable ON acr.client_credentials;
CREATE TRIGGER trg_client_credentials_provenance_immutable
    BEFORE UPDATE ON acr.client_credentials
    FOR EACH ROW
    EXECUTE FUNCTION acr.client_credentials_provenance_immutable();
