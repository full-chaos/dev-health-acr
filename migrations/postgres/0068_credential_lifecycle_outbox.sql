-- Credential lifecycle outbox: one row per lifecycle event of an acr
-- credential (acr-credential-lifecycle-event.v1, the ops-owned contract
-- vendored under contracts/auth/v1/ops-vendored). A row is written in the
-- transaction of the credential mutation it describes (issue, device redeem,
-- browser refresh successor, rotate, revoke, browser family revoke, device ack
-- sweep, rotation rollback), by the one writer in
-- internal/storage/postgres/outbox.go, so a rolled-back mutation leaves no row
-- and a committed one always has its row.
--
-- payload is the complete wire event. It names the credential by its public id
-- and carries no token, prefix, hash or fingerprint.
--
-- (aggregate_id, revision) is the stream key: revision is per credential,
-- 1 for the event that opens it, the next integer for every later event. The
-- writer reads the highest revision under the credential row lock it already
-- holds; the unique key is the backstop that makes a lost race an error and
-- never a duplicate.
--
-- A credential that predates this table has no revision 1. The writer adds an
-- `issued` revision 1 at the first mutation it touches, in the same
-- transaction; nothing in this file writes data.
--
-- published_at is for the publisher (CHAOS W9-6), which also owns reaping;
-- nothing here sets or deletes it. No foreign key: the purge jobs delete
-- credentials and the rows outlive them.
--
-- Runtime grant: SELECT and INSERT (internal/dbacl/runtime_grants.go).
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.credential_lifecycle_outbox (
    outbox_id    UUID        PRIMARY KEY,
    org_id       UUID        NOT NULL,
    aggregate_id TEXT        NOT NULL,
    revision     INTEGER     NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    CONSTRAINT uq_acr_credential_lifecycle_outbox_revision UNIQUE (aggregate_id, revision),
    CONSTRAINT ck_acr_credential_lifecycle_outbox_revision CHECK (revision >= 1),
    CONSTRAINT ck_acr_credential_lifecycle_outbox_event_type CHECK (event_type IN (
        'acr_credential.issued', 'acr_credential.device_redeemed', 'acr_credential.rotated',
        'acr_credential.revoked', 'acr_credential.expired')),
    CONSTRAINT ck_acr_credential_lifecycle_outbox_aggregate CHECK (aggregate_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    CONSTRAINT ck_acr_credential_lifecycle_outbox_payload CHECK (jsonb_typeof(payload) = 'object')
);

-- The publisher reads what it has not published, oldest first.
CREATE INDEX IF NOT EXISTS ix_acr_credential_lifecycle_outbox_unpublished
    ON acr.credential_lifecycle_outbox (created_at, aggregate_id, revision)
    WHERE published_at IS NULL;
