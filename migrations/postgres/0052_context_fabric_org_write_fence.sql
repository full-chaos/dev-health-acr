-- The write fence of a purged organization. A row in
-- acr.context_fabric_org_purge_markers means the organization was purged and
-- has not returned to discovery; until then nothing may write a row keyed by
-- it. A check made before the write, or a NOT EXISTS inside the write
-- statement, races the purge: the purge's delete can pass a table before the
-- writer's row is visible and commit before the writer does.
--
-- So the check is made here, in the transaction of the write itself:
--   1. take the organization's write-fence advisory lock in SHARED mode, held
--      to the end of the transaction;
--   2. then read the marker, in a later statement, so READ COMMITTED gives it
--      a snapshot newer than the lock wait;
--   3. let the row through only when no marker is present.
-- The purge takes the same lock in EXCLUSIVE mode as the first statement of
-- its delete transaction and writes the marker in that transaction. The two
-- modes exclude each other, so a writer either commits before the purge
-- starts deleting (its row is deleted) or reads the marker after the purge
-- committed (its row is refused).
--
-- The lock class 20261014 is this fence's own. It is not the projection lock
-- (class 20260812), which a projector holds for a whole pass.
--
-- The lock key and the marker both use the canonical lower-case UUID. A TEXT
-- organization column can hold another spelling of the same UUID (upper case,
-- braces, no hyphens, a urn:uuid: prefix); it is folded the way the purge's
-- own predicate folds it. A value that is not a UUID cannot have been purged.
--
-- A snapshot isolation level would read the marker from a snapshot older than
-- the lock wait, so a write to a fenced table outside READ COMMITTED is
-- refused.
--
-- The function is SECURITY DEFINER, owned by the role that runs migrations,
-- which owns the marker table. So the fence needs no privilege of the
-- writer's role: it cannot be switched off, or turned into a failure of every
-- write, by a deployment that did not grant the marker table to that role
-- (runtime grants are applied out of band and differ between deployments).
-- The body reads one table by its schema-qualified name, runs no dynamic SQL
-- and has a fixed search_path. EXECUTE is revoked from PUBLIC: a trigger
-- fires without that privilege, and no role needs to call the function.
CREATE OR REPLACE FUNCTION acr.context_fabric_refuse_purged_org() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    candidates text[];
    candidate  text;
    folded     text;
    canonical  text;
BEGIN
    IF TG_TABLE_NAME = 'device_authorizations' THEN
        candidates := ARRAY[NEW.authorized_org_id::text, NEW.organization_id_hint];
    ELSIF TG_TABLE_NAME = 'oauth_authorization_requests' THEN
        candidates := ARRAY[NEW.bound_org_id];
    ELSE
        candidates := ARRAY[NEW.org_id::text];
    END IF;
    FOREACH candidate IN ARRAY candidates LOOP
        IF candidate IS NULL THEN
            CONTINUE;
        END IF;
        folded := lower(regexp_replace(btrim(candidate), '^urn:uuid:|[{}-]', '', 'gi'));
        IF folded !~ '^[0-9a-f]{32}$' THEN
            CONTINUE;
        END IF;
        canonical := folded::uuid::text;
        IF current_setting('transaction_isolation') <> 'read committed' THEN
            RAISE EXCEPTION 'organization write fence requires read committed isolation'
                USING ERRCODE = 'ACR02';
        END IF;
        PERFORM pg_advisory_xact_lock_shared(20261014, hashtext(canonical));
        IF EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = canonical) THEN
            RAISE EXCEPTION 'organization was purged'
                USING ERRCODE = 'ACR01';
        END IF;
    END LOOP;
    RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION acr.context_fabric_refuse_purged_org() FROM PUBLIC;

-- Every insert is checked. An update is checked only when it gives the row an
-- organization it did not have: an update of a row that already belongs to the
-- organization cannot recreate purged data, because the purge's delete and
-- that update meet on the row lock.
DO $$
DECLARE
    fenced record;
BEGIN
    FOR fenced IN
        SELECT * FROM (VALUES
            ('device_authorizations', 'authorized_org_id, organization_id_hint',
             'NEW.authorized_org_id IS DISTINCT FROM OLD.authorized_org_id OR NEW.organization_id_hint IS DISTINCT FROM OLD.organization_id_hint'),
            ('oauth_authorization_requests', 'bound_org_id', 'NEW.bound_org_id IS DISTINCT FROM OLD.bound_org_id'),
            ('client_credentials', 'org_id', NULL),
            ('workload_bindings', 'org_id', NULL),
            ('context_packet_snapshots', 'org_id', NULL),
            ('agent_episodes', 'org_id', NULL),
            ('context_fabric_projection_checkpoints', 'org_id', NULL),
            ('context_fabric_projection_rebuild_markers', 'org_id', NULL),
            ('context_fabric_investigation_results', 'org_id', NULL),
            ('context_fabric_model_execution_receipts', 'org_id', NULL),
            ('context_fabric_org_model_config', 'org_id', NULL),
            ('context_fabric_reuse_invalidations', 'org_id', NULL),
            ('context_fabric_clarification_selections', 'org_id', NULL),
            ('context_fabric_structure_selections', 'org_id', NULL),
            ('context_fabric_structure_supersession_claims', 'org_id', NULL),
            ('context_fabric_structure_prior_pointer_history', 'org_id', NULL),
            ('context_fabric_structure_prior_pointer', 'org_id', NULL),
            ('context_fabric_structure_prior_revocations', 'org_id', NULL),
            ('context_fabric_structure_priors', 'org_id', NULL),
            ('context_fabric_graph_build_source_progress', 'org_id', NULL),
            ('context_fabric_graph_epoch_retirements', 'org_id', NULL),
            ('context_fabric_graph_lifecycle', 'org_id', NULL)
        ) AS t(table_name, org_columns, changed)
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.%I', fenced.table_name);
        EXECUTE format('DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.%I', fenced.table_name);
        EXECUTE format(
            'CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.%I FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org()',
            fenced.table_name);
        EXECUTE format(
            'CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF %s ON acr.%I FOR EACH ROW WHEN (%s) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org()',
            fenced.org_columns, fenced.table_name,
            coalesce(fenced.changed, 'NEW.org_id IS DISTINCT FROM OLD.org_id'));
    END LOOP;
END
$$;
