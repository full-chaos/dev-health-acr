-- org_bearer credential class (D6530).
--
-- Migration 0058 closed the client_class vocabulary without org_bearer and the
-- credential lifecycle wrote operator for a credential issued by the operator
-- CLI (acr-api credentials). D6530 rules that credential is org_bearer with no
-- approving user and no client. This migration:
--
--   1. admits org_bearer in the class CHECK and in the approver CHECK, where it
--      behaves like operator and workload: approving_user_id and client_id NULL.
--      operator stays in the vocabulary and has no writer.
--   2. relabels the rows written as operator. Every one is a CLI credential or
--      a rotation successor of one: the 0058 backfill never writes operator,
--      and the CLI create is the only issuance path that did. The rows keep
--      their NULL approver and NULL client.
--
-- The immutability trigger refuses a class change, so the relabel disables it
-- for this one UPDATE and enables it again. The runner wraps the file in one
-- transaction: the trigger is never left disabled, and a failed run changes
-- nothing. The credential_created audit rows keep the class they recorded.
--
-- Rollback: the previous release still writes operator for a CLI credential,
-- which the widened CHECK admits; nothing reads client_class yet. Idempotent.
-- No inline BEGIN/COMMIT.

ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_client_class;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_client_class CHECK (
    client_class IS NULL
    OR client_class IN ('browser', 'oauth_user', 'native_device', 'org_bearer', 'operator', 'workload')
);

ALTER TABLE acr.client_credentials DROP CONSTRAINT IF EXISTS ck_acr_client_credentials_approving_user;
ALTER TABLE acr.client_credentials ADD CONSTRAINT ck_acr_client_credentials_approving_user CHECK (
    client_class IS NULL
    OR (client_class IN ('browser', 'oauth_user', 'native_device') AND approving_user_id IS NOT NULL)
    OR (client_class IN ('org_bearer', 'operator', 'workload') AND approving_user_id IS NULL AND client_id IS NULL)
);

COMMENT ON COLUMN acr.client_credentials.client_class IS
    'Issuance class (browser|oauth_user|native_device|org_bearer|operator|workload), derived from the issuance path; immutable once set. org_bearer is a credential from the operator CLI. operator has no writer. NULL only on an unproven legacy row.';
COMMENT ON COLUMN acr.client_credentials.approving_user_id IS
    'Web-session subject that approved the authorization behind the credential; immutable once set. NULL for org_bearer, operator and workload.';

ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable;
UPDATE acr.client_credentials SET client_class = 'org_bearer' WHERE client_class = 'operator';
ALTER TABLE acr.client_credentials ENABLE TRIGGER trg_client_credentials_provenance_immutable;
