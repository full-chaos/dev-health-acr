ALTER TABLE acr.device_authorizations
    ADD COLUMN organization_id_hint TEXT,
    ADD COLUMN repository_hints JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE acr.device_authorizations
    ADD CONSTRAINT ck_acr_device_authorizations_repository_hints_array
        CHECK (jsonb_typeof(repository_hints) = 'array'),
    ADD CONSTRAINT ck_acr_device_authorizations_organization_id_hint_length
        CHECK (organization_id_hint IS NULL OR char_length(organization_id_hint) <= 128);
