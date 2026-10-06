-- A device-flow credential the client has not yet acknowledged. ack_required
-- is set when the credential was minted for the acr-mcp device-code poll, which
-- acknowledges the credential once it is stored; credential_acked_at is that
-- acknowledgement. A redeemed row with ack_required and no credential_acked_at
-- is "unacknowledged": a poll retry inside the ack window replaces the
-- credential, and the purge loop revokes it once the window has elapsed.
--
-- Operational note: no new runtime-role privilege. The runtime role already
-- holds SELECT, INSERT, UPDATE, DELETE on acr.device_authorizations and UPDATE
-- on acr.client_credentials (0042 / acr-db-init.sh).
--
-- Additive: an older acr-api image ignores both columns, but it never sets
-- ack_required, so it never revokes a credential for want of an ack.

ALTER TABLE acr.device_authorizations
    ADD COLUMN IF NOT EXISTS ack_required BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS credential_acked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS ix_acr_device_authorizations_unacked
    ON acr.device_authorizations (redeemed_at)
    WHERE state = 'redeemed' AND ack_required AND credential_acked_at IS NULL;
