SELECT o.repo_key,
       argMax(o.repo_full_name, (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))) AS latest_repo_full_name,
       max(o.repo_slug) AS resolved_repo_slug,
       o.team_id AS owning_team_id, o.source_name, o.provider,
       argMax(o.match_type_name, (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))) AS latest_match_type,
       argMax(o.is_primary_flag, (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))) AS latest_is_primary,
       argMax(o.specificity_value, (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))) AS latest_specificity,
       argMax(o.priority_value, (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))) AS latest_priority,
       min(o.valid_from) AS first_valid_from,
       argMax(tuple(o.valid_to), (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))).1 IS NULL AS latest_is_open,
       ifNull(argMax(tuple(o.valid_to), (o.valid_to IS NULL, if(o.valid_to IS NULL, -toUnixTimestamp64Micro(o.valid_from), toUnixTimestamp64Micro(o.valid_to)), toUnixTimestamp64Micro(o.valid_from))).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       greatest(max(o.updated_at), max(o.repo_synced_at)) AS observed_at
FROM (
	SELECT rto.provider AS provider, rto.repo_key AS repo_key,
	       rto.repo_full_name AS repo_full_name, ifNull(r.repo, '') AS repo_slug,
	       rto.team_id AS team_id, toString(rto.source) AS source_name,
	       toString(rto.match_type) AS match_type_name, toUInt8(rto.is_primary) AS is_primary_flag,
	       toInt64(rto.specificity) AS specificity_value, toInt64(rto.priority) AS priority_value,
	       rto.valid_from AS valid_from, rto.valid_to AS valid_to, rto.updated_at AS updated_at,
	       ifNull(r.last_synced, toDateTime64(0, 3, 'UTC')) AS repo_synced_at
	FROM (
	SELECT o.team_id AS team_id,
		if((o.repo_id IS NOT NULL OR r.matched = 1), coalesce(toString(o.repo_id), toString(r.id)), '') AS repo_key,
		o.repo_full_name AS repo_full_name,
		o.org_id AS org_id,
		o.provider AS provider,
		o.source AS source,
		o.match_type AS match_type,
		o.is_primary AS is_primary,
		o.specificity AS specificity,
		o.priority AS priority,
		o.valid_from AS valid_from,
		o.valid_to AS valid_to,
		o.updated_at AS updated_at
	FROM (
		SELECT org_id, provider, team_id, repo_id, repo_full_name, source, match_type, is_primary, specificity, priority, valid_from, valid_to, updated_at
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND valid_from <= now64(3)
	) AS o
	LEFT JOIN (
		SELECT org_id, provider, id, repo, 1 AS matched
		FROM repos FINAL
		WHERE org_id = {org_id:String}
	) AS r
		ON r.org_id = o.org_id
		   AND r.provider = o.provider
		   AND lower(r.repo) = lower(o.repo_full_name)
) AS rto
	LEFT JOIN repos AS r FINAL ON toString(r.id) = rto.repo_key AND r.org_id = rto.org_id
	INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND is_active = 1) AS t ON t.id = rto.team_id
	WHERE rto.org_id = {org_id:String}
) AS o
GROUP BY o.provider, o.repo_key, o.team_id, o.source_name