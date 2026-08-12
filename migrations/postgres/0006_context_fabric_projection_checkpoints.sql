CREATE TABLE IF NOT EXISTS acr.context_fabric_projection_checkpoints (
    org_id            TEXT NOT NULL,
    source            TEXT NOT NULL,
    cursor            TEXT NOT NULL DEFAULT '',
    source_version    TEXT NOT NULL DEFAULT '',
    backend_watermark TEXT NOT NULL DEFAULT '',
    updated_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, source),
    CONSTRAINT ck_acr_cf_projection_checkpoints_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_projection_checkpoints_source_length CHECK (char_length(source) BETWEEN 1 AND 128),
    CONSTRAINT ck_acr_cf_projection_checkpoints_cursor_length CHECK (char_length(cursor) <= 512),
    CONSTRAINT ck_acr_cf_projection_checkpoints_source_version_length CHECK (char_length(source_version) <= 256),
    CONSTRAINT ck_acr_cf_projection_checkpoints_backend_watermark_length CHECK (char_length(backend_watermark) <= 512)
);
