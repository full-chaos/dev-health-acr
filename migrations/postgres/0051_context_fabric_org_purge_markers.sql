-- Presence of a row means: `acr-projector purge` removed this organization's
-- projected state and Dev Health no longer lists it. The coordinator refuses
-- to project the organization while the row is present, checked under the
-- per-organization lock, so a tick that selected the organization before the
-- purge cannot recreate checkpoint rows or an empty graph afterwards. The row
-- is cleared when the organization returns to discovery. The purge writes it
-- in the same transaction as its deletes and never deletes it.
CREATE TABLE IF NOT EXISTS acr.context_fabric_org_purge_markers (
    org_id    TEXT PRIMARY KEY,
    purged_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ck_acr_cf_org_purge_markers_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256)
);
