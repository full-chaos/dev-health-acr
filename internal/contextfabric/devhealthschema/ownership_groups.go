package devhealthschema

// The stored project -> team ownership groups (ops migration 147,
// internal/chmigrate/sql/147_project_team_ownership_groups.sql in ops). Its two
// tables are declared in ProductionColumns and EngineFull like every other
// table; its two views have no mechanical rendering, so they are copied here
// statement by statement from the migration (testdata holds the file as
// merged and TestOwnershipGroupsDDLMirrorsMigration147 holds both to it).

// OwnershipGroupsCopyViewDDL copies every log insert into current.
const OwnershipGroupsCopyViewDDL = `CREATE MATERIALIZED VIEW IF NOT EXISTS project_team_ownership_groups_current_mv
TO project_team_ownership_groups_current
AS SELECT org_id, row_key, project_id, team_id, source_name, provider, first_valid_from, latest_is_open, latest_valid_to,
    observed_at, edge_suppressed, conflict_identities, conflicting_identity_present, content_hash, stored_at, is_deleted
FROM project_team_ownership_groups_log`

// OwnershipGroupsRefreshViewDDL is the refresh view: every 60 seconds it
// builds every group of every organization, compares it with current and
// appends to the log only what changed.
const OwnershipGroupsRefreshViewDDL = `CREATE MATERIALIZED VIEW IF NOT EXISTS project_team_ownership_groups_refresh
REFRESH EVERY 60 SECOND APPEND TO project_team_ownership_groups_log
AS SELECT
    if(g_row_key != '', g_org_id, c_org_id) AS org_id,
    if(g_row_key != '', g_row_key, c_row_key) AS row_key,
    if(g_row_key != '', g_project_id, c_project_id) AS project_id,
    if(g_row_key != '', g_team_id, c_team_id) AS team_id,
    if(g_row_key != '', g_source_name, c_source_name) AS source_name,
    if(g_row_key != '', g_provider, c_provider) AS provider,
    if(g_row_key != '', g_first_valid_from, c_first_valid_from) AS first_valid_from,
    if(g_row_key != '', g_latest_is_open, c_latest_is_open) AS latest_is_open,
    if(g_row_key != '', g_latest_valid_to, c_latest_valid_to) AS latest_valid_to,
    if(g_row_key != '', g_observed_at, c_observed_at) AS observed_at,
    if(g_row_key != '', g_edge_suppressed, c_edge_suppressed) AS edge_suppressed,
    if(g_row_key != '', g_conflict_identities, c_conflict_identities) AS conflict_identities,
    if(g_row_key != '', g_conflicting_identity_present, c_conflicting_identity_present) AS conflicting_identity_present,
    if(g_row_key != '', g_content_hash, c_content_hash) AS content_hash,
    now64(6, 'UTC') AS stored_at,
    toUInt8(g_row_key = '') AS is_deleted
FROM (
    SELECT org_id AS g_org_id, row_key AS g_row_key, project_id AS g_project_id, team_id AS g_team_id,
        source_name AS g_source_name, provider AS g_provider, first_valid_from AS g_first_valid_from,
        latest_is_open AS g_latest_is_open, latest_valid_to AS g_latest_valid_to, observed_at AS g_observed_at,
        edge_suppressed AS g_edge_suppressed, conflict_identities AS g_conflict_identities,
        conflicting_identity_present AS g_conflicting_identity_present,
        cityHash64(project_id, team_id, source_name, provider, first_valid_from, latest_is_open, latest_valid_to,
            observed_at, edge_suppressed, conflict_identities, conflicting_identity_present) AS g_content_hash
    FROM (
        SELECT gg.*,
            concat(replaceAll(replaceAll(gg.provider, '%', '%25'), ':', '%3A'), ':',
                   replaceAll(replaceAll(gg.project_id, '%', '%25'), ':', '%3A'), ':',
                   replaceAll(replaceAll(gg.team_id, '%', '%25'), ':', '%3A'), ':',
                   replaceAll(replaceAll(gg.source_name, '%', '%25'), ':', '%3A')) AS row_key
        FROM (
            SELECT o.org_id AS org_id, o.project_id AS project_id, o.team_id AS team_id, o.source_name AS source_name,
                minIf(o.first_valid_from, o.unassertable = 0) AS first_valid_from,
                toUInt8(argMaxIf(tuple(o.valid_to), (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from)), o.unassertable = 0).1 IS NULL) AS latest_is_open,
                ifNull(argMaxIf(tuple(o.valid_to), (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from)), o.unassertable = 0).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
                max(o.row_watermark) AS observed_at, o.provider AS provider,
                toUInt8(countIf(o.unassertable = 0) = 0) AS edge_suppressed,
                arraySort(groupUniqArrayIf(concat(o.ownership_ref, '\0', o.ownership_key, '\0', o.team_id, '\0', o.source_name), o.unassertable = 1 AND o.retraction_only = 0)) AS conflict_identities,
                toUInt8(countIf(o.unassertable = 1 AND o.retraction_only = 0) > 0) AS conflicting_identity_present
            FROM (
                SELECT org_id, project_id, provider, ownership_ref, ownership_key, team_id, source_name, first_valid_from, valid_from, valid_to, retraction_only,
                    greatest(updated_at, max(project_updated_at) OVER (PARTITION BY org_id, provider, ownership_ref, ownership_key, retraction_only)) AS row_watermark,
                    toUInt8(retraction_only = 1 OR min(project_id) OVER (PARTITION BY org_id, provider, ownership_ref, ownership_key, retraction_only) != max(project_id) OVER (PARTITION BY org_id, provider, ownership_ref, ownership_key, retraction_only)) AS unassertable
                FROM (
                    SELECT p.org_id AS org_id, p.id AS project_id, p.provider AS provider,
                        o.project_ref AS ownership_ref, o.project_key AS ownership_key,
                        o.team_id AS team_id, o.source_name AS source_name, o.first_valid_from AS first_valid_from, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at,
                        p.project_updated_at AS project_updated_at, toUInt8(0) AS retraction_only
                    FROM (
                        SELECT pi.org_id AS org_id, pi.provider AS provider, pi.id AS id, pi.project_key AS project_key, pi.scope AS scope, pi.scope_kind AS scope_kind,
                            w.project_updated_at AS project_updated_at
                        FROM (
                            SELECT org_id, provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
                            FROM (
                                SELECT org_id, provider, id, project_key,
                                    key_resolution_count AS project_key_resolution_count,
                                    (project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
                                FROM (
                                    SELECT org_id, id, provider, ifNull(project_key, '') AS project_key,
                                        countIf(ifNull(project_key, '') != '') OVER (PARTITION BY org_id, provider, ifNull(project_key, '')) AS key_resolution_count
                                    FROM projects FINAL
                                )
                            )
                            ARRAY JOIN
                                if(key_scope_emitted, [id, project_key], [id]) AS scope,
                                if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
                                if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
                            GROUP BY org_id, provider, id, project_key, key_resolution_count, scope
                        ) AS pi
                        INNER JOIN (
                            SELECT org_id, provider, id, updated_at AS project_updated_at
                            FROM projects FINAL
                        ) AS w ON w.org_id = pi.org_id AND w.provider = pi.provider AND w.id = pi.id
                    ) AS p
                    INNER JOIN (
                        SELECT org_id, provider, project_ref, project_key, team_id, source_name, first_valid_from, valid_from, valid_to, updated_at,
                            scope_match.1 AS scope_value, scope_match.2 AS required_scope_kind
                        FROM (
                            SELECT org_id, provider, project_ref, project_key, team_id, source_name,
                                min(vf) AS first_valid_from,
                                argMax(tuple(vf, vt), ord) AS latest_assertion,
                                latest_assertion.1 AS valid_from, latest_assertion.2 AS valid_to,
                                max(upd) AS updated_at
                            FROM (
                                SELECT org_id, provider, project_id AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name,
                                    valid_from AS vf, valid_to AS vt, updated_at AS upd,
                                    (valid_to IS NULL, if(valid_to IS NULL, -toUnixTimestamp64Micro(valid_from), toUnixTimestamp64Micro(valid_to)), toUnixTimestamp64Micro(valid_from)) AS ord
                                FROM team_project_ownership FINAL
                            )
                            GROUP BY org_id, provider, project_ref, project_key, team_id, source_name
                        )
                        ARRAY JOIN [(project_ref, ''), (project_key, 'key')] AS scope_match
                    ) AS o ON o.org_id = p.org_id AND o.provider = p.provider AND o.scope_value = p.scope
                    INNER JOIN (SELECT org_id, id FROM teams FINAL WHERE is_active = 1) AS t ON t.org_id = o.org_id AND t.id = o.team_id
                    WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind

                    UNION ALL

                    SELECT p.org_id AS org_id, p.id AS project_id, p.provider AS provider,
                        o.project_ref AS ownership_ref, o.project_key AS ownership_key,
                        o.team_id AS team_id, o.source_name AS source_name, o.first_valid_from AS first_valid_from, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at,
                        p.project_updated_at AS project_updated_at, toUInt8(1) AS retraction_only
                    FROM (
                        SELECT pa.org_id AS org_id, pa.provider AS provider, pa.id AS id, pa.project_key AS project_key, pa.project_updated_at AS project_updated_at,
                            count() OVER (PARTITION BY pa.org_id, pa.provider, pa.project_key) AS key_project_count
                        FROM (
                            SELECT pi.org_id AS org_id, pi.provider AS provider, pi.id AS id, pi.project_key AS project_key, pi.scope_kind AS scope_kind,
                                w.project_updated_at AS project_updated_at
                            FROM (
                                SELECT org_id, provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
                                FROM (
                                    SELECT org_id, provider, id, project_key,
                                        key_resolution_count AS project_key_resolution_count,
                                        (project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
                                    FROM (
                                        SELECT org_id, id, provider, ifNull(project_key, '') AS project_key,
                                            countIf(ifNull(project_key, '') != '') OVER (PARTITION BY org_id, provider, ifNull(project_key, '')) AS key_resolution_count
                                        FROM projects FINAL
                                    )
                                )
                                ARRAY JOIN
                                    if(key_scope_emitted, [id, project_key], [id]) AS scope,
                                    if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
                                    if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
                                GROUP BY org_id, provider, id, project_key, key_resolution_count, scope
                            ) AS pi
                            INNER JOIN (
                                SELECT org_id, provider, id, updated_at AS project_updated_at
                                FROM projects FINAL
                            ) AS w ON w.org_id = pi.org_id AND w.provider = pi.provider AND w.id = pi.id
                        ) AS pa
                        WHERE pa.scope_kind = 'id' AND pa.project_key != ''
                    ) AS p
                    INNER JOIN (
                        SELECT org_id, provider, project_ref, project_key, team_id, source_name, first_valid_from, valid_from, valid_to, updated_at,
                            if(project_key != '', project_key, project_ref) AS match_value
                        FROM (
                            SELECT org_id, provider, project_ref, project_key, team_id, source_name,
                                min(vf) AS first_valid_from,
                                argMax(tuple(vf, vt), ord) AS latest_assertion,
                                latest_assertion.1 AS valid_from, latest_assertion.2 AS valid_to,
                                max(upd) AS updated_at
                            FROM (
                                SELECT org_id, provider, project_id AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name,
                                    valid_from AS vf, valid_to AS vt, updated_at AS upd,
                                    (valid_to IS NULL, if(valid_to IS NULL, -toUnixTimestamp64Micro(valid_from), toUnixTimestamp64Micro(valid_to)), toUnixTimestamp64Micro(valid_from)) AS ord
                                FROM team_project_ownership FINAL
                            )
                            GROUP BY org_id, provider, project_ref, project_key, team_id, source_name
                        )
                    ) AS o ON o.org_id = p.org_id AND o.provider = p.provider AND o.match_value = p.project_key
                    INNER JOIN (SELECT org_id, id FROM teams FINAL WHERE is_active = 1) AS t ON t.org_id = o.org_id AND t.id = o.team_id
                    WHERE p.key_project_count > 1
                )
            ) AS o
            GROUP BY o.org_id, o.project_id, o.provider, o.team_id, o.source_name
        ) AS gg
    )
) AS g
FULL OUTER JOIN (
    SELECT org_id AS c_org_id, row_key AS c_row_key, project_id AS c_project_id, team_id AS c_team_id,
        source_name AS c_source_name, provider AS c_provider, first_valid_from AS c_first_valid_from,
        latest_is_open AS c_latest_is_open, latest_valid_to AS c_latest_valid_to, observed_at AS c_observed_at,
        edge_suppressed AS c_edge_suppressed, conflict_identities AS c_conflict_identities,
        conflicting_identity_present AS c_conflicting_identity_present, content_hash AS c_content_hash, is_deleted AS c_is_deleted
    FROM project_team_ownership_groups_current FINAL
) AS c ON g.g_org_id = c.c_org_id AND g.g_row_key = c.c_row_key
WHERE (g_row_key != '' AND (c_row_key = '' OR c_is_deleted = 1 OR c_content_hash != g_content_hash))
   OR (g_row_key = '' AND c_row_key != '' AND c_is_deleted = 0)`

// OwnershipGroupsTables are the two tables the views write, in the order a
// fixture creates them (the copy view needs both, the refresh view needs both
// plus projects, team_project_ownership and teams).
var OwnershipGroupsTables = []string{"project_team_ownership_groups_log", "project_team_ownership_groups_current"}

// OwnershipGroupsViewsDDL is the two view statements in creation order: the
// copy view before the refresh view, whose first refresh runs at creation.
func OwnershipGroupsViewsDDL() []string {
	return []string{OwnershipGroupsCopyViewDDL, OwnershipGroupsRefreshViewDDL}
}
