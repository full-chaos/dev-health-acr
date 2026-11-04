-- The ownership extract of the owner-share pair (CHAOS-12691) for one
-- organization: SELECT statements only, no other kind of statement.
--
-- This file is the rendering of ownershipExtractTables in
-- owner_share_extract_file.go; TestOwnerShareVenueExtractSQLIsTheRendering
-- holds it to that rendering. Do not edit it by hand.
--
-- Run it in the venue ClickHouse pod with the organization bound as a
-- parameter, and keep the output in a file only you can read:
--
--   umask 077
--   kubectl exec -i -n <namespace> <clickhouse pod> -- clickhouse-client \
--     --database <ops analytics database> --multiquery \
--     --param_org_id=<organization id> \
--     < owner_share_venue_extract.sql > owner_share_extract.jsonl
--
-- Then, as the user that owns that file and within 15 minutes, run
-- make o4-owner-share-live with the ACR_O4_* variables of the target and
-- ACR_O4_OWNERSHIP_EXTRACT=<path of owner_share_extract.jsonl>
-- (ACR_O4_OWNERSHIP_EXTRACT_MAX_AGE changes the bound).
--
-- The output is JSONEachRow: one JSON object per line, its table named in
-- extract_table. The first line (extract_table 'snapshot') holds the
-- organization, the server time before the reads and the row count of each
-- table; a file whose rows do not match those counts is refused.

SELECT
    'snapshot' AS extract_table,
    {org_id:String} AS org,
    now64(3, 'UTC') AS taken_at,
    (SELECT count() FROM (SELECT org_id, id, provider, repo, last_synced FROM repos WHERE org_id = {org_id:String})) AS rows_repos,
    (SELECT count() FROM (SELECT org_id, id, is_active, updated_at, last_synced FROM teams WHERE org_id = {org_id:String})) AS rows_teams,
    (SELECT count() FROM (SELECT org_id, provider, team_id, repo_id, repo_full_name, source, is_primary, valid_from, valid_to, updated_at, share FROM team_repo_ownership WHERE org_id = {org_id:String} LIMIT 1 BY provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_to, share)) AS rows_team_repo_ownership
FORMAT JSONEachRow;

SELECT 'repos' AS extract_table, org_id, id, provider, repo, last_synced
FROM repos
WHERE org_id = {org_id:String}
FORMAT JSONEachRow;

SELECT 'teams' AS extract_table, org_id, id, is_active, updated_at, last_synced
FROM teams
WHERE org_id = {org_id:String}
FORMAT JSONEachRow;

SELECT 'team_repo_ownership' AS extract_table, org_id, provider, team_id, repo_id, repo_full_name, source, is_primary, valid_from, valid_to, updated_at, share
FROM team_repo_ownership
WHERE org_id = {org_id:String}
ORDER BY valid_from ASC, updated_at DESC
LIMIT 1 BY provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_to, share
FORMAT JSONEachRow;
