-- Used web-assertion ids, shared by every acr-api pod so a replayed assertion
-- is refused whichever pod receives it. One row per (issuer, jti): the first
-- INSERT wins, a conflict is a replay. expires_at is the assertion's own exp.
--
-- Operational note: this repository never grants runtime-role table
-- privileges from a migration (see 0042). The runtime role needs SELECT,
-- INSERT and DELETE on acr.web_assertion_replays; deploy/compose/acr-db-init.sh
-- and cmd/acr-migrate's grant-runtime-acl apply them.
--
-- Additive: an older acr-api image does not read this table.

CREATE TABLE IF NOT EXISTS acr.web_assertion_replays (
    issuer TEXT NOT NULL,
    jti TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (issuer, jti)
);

CREATE INDEX IF NOT EXISTS ix_acr_web_assertion_replays_expires_at
    ON acr.web_assertion_replays (expires_at);
