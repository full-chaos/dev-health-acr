-- Presence of a row means: a rebuild has purged (or is purging) this
-- organization's backend graph state and at least one source's checkpoint
-- has not yet been confirmed reset to the empty cursor. The coordinator
-- must refuse ordinary incremental projection for an organization while its
-- row is present -- see internal/contextfabric/projectionrun.Coordinator's
-- rebuild-marker invariant -- because an incremental batch applied against
-- a purged-but-not-reset graph silently loses the organization's
-- previously projected history instead of replaying it from a full
-- snapshot. The row is removed only after every source's checkpoint is
-- confirmed reset, so a crash between purge and reset leaves the marker in
-- place and the next tick (or a re-invoked `acr-projector rebuild`) resumes
-- rather than silently proceeding.
CREATE TABLE IF NOT EXISTS acr.context_fabric_projection_rebuild_markers (
    org_id     TEXT PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ck_acr_cf_projection_rebuild_markers_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256)
);
