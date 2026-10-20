SELECT lr.team_id AS team_id,
	uniqExact(m.repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * m.effort), m.theme_distribution_json)) AS theme_effort,
	sum(m.bugfix_share * m.effort) AS bugfix_effort,
	arraySort(groupUniqArray(cityHash64(m.work_unit_id))) AS unit_hashes,
	arraySort(groupUniqArrayIf(cityHash64(m.work_unit_id), m.unit_issue_size > 0)) AS sized_unit_hashes
FROM (
	SELECT work_unit_id, repo_uuid, pr_number, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share
	FROM (
SELECT win, work_unit_id, rp.1 AS repo_uuid, rp.2 AS pr_number,
	1 AS c, n,
	unit_size_weight AS effort_value, unit_issue_size, theme_distribution_json, bugfix_share, span_from
FROM (
            SELECT sized_unit.*, if(unit_size.issue_size > 0, unit_size.issue_size, 1.0) AS unit_size_weight,
                if(unit_size.issue_size > 0, unit_size.issue_size, 0.0) AS unit_issue_size
            FROM (SELECT folded_units.*, {org_id:String} AS org_id FROM (SELECT work_unit_id,
		any(theme_distribution_json) AS theme_distribution_json,
		any(bugfix_share) AS bugfix_share,
		any(span_from) AS span_from,
		any(wins) AS wins,
		groupUniqArray((ref_repo, pr_number)) AS repo_prs,
		groupUniqArray((ref_repo, if(ref_repo = '', ref_text, concat(ref_repo, '#', pr_number)))) AS pairs,
		length(pairs) AS n
	FROM (
		SELECT parsed.work_unit_id AS work_unit_id,
			parsed.theme_distribution_json AS theme_distribution_json, parsed.bugfix_share AS bugfix_share,
			parsed.span_from AS span_from, parsed.from_ts AS from_ts, parsed.to_ts AS to_ts, parsed.wins AS wins,
			parsed.pr_number AS pr_number, parsed.ref_text AS ref_text,
			ifNull(if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')), '') AS ref_repo
		FROM (
			SELECT work_unit_id, theme_distribution_json, bugfix_share, from_ts AS span_from, from_ts, to_ts, wins,
				ref.1 AS uuid_direct, ref.2 AS lookup_provider, ref.3 AS lookup_repo, ref.4 AS pr_number, ref.5 AS ref_text
			FROM (
				SELECT work_unit_id, latest.1 AS repo_id, latest.2 AS from_ts, latest.3 AS to_ts,
					latest.4 AS theme_distribution_json, latest.5 AS bugfix_share, latest.6 AS pr_refs,
					arrayFilter(w -> w >= 0, [if(1, 0, -1)]) AS wins
				FROM (
					SELECT work_unit_id,
						argMaxIf(tuple(repo_id, from_ts, to_ts, theme_distribution_json, unit_bugfix_share, pr_refs), computed_at, reaches) AS latest
					FROM (
						SELECT work_unit_id, computed_at, repo_id, from_ts, to_ts, theme_distribution_json,
							ifNull(subcategory_distribution_json['quality.bugfix'], 0.0) AS unit_bugfix_share,
							arrayMap(r -> (substring(r, 1, 36), substring(r, 40)),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))) AS direct_refs,
							arrayConcat(
								arrayMap(r -> ('github', substring(r, 6, position(r, '#') - 6), substring(r, position(r, '#') + 1)),
									arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
								arrayMap(r -> ('gitlab', substring(r, 8, position(r, '!') - 8), substring(r, position(r, '!') + 1)),
									arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
							) AS lookup_refs,
							(hasAny(direct_refs, arrayMap(x -> (x.1, x.2), (SELECT groupUniqArray((toString(pr_repo_id), toString(pr_number), team_id)) FROM (
  SELECT DISTINCT o.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM (SELECT t.team_id AS team_id, p.provider AS provider, p.join_key AS join_key FROM (SELECT provider, id, team_id FROM (
	SELECT provider, id, team_id
	FROM (
	SELECT provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
	FROM (
		SELECT provider, id, project_key,
			key_resolution_count AS project_key_resolution_count,
			(project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
		FROM (
		SELECT id, provider, project_key, key_resolution_count
		FROM (
			SELECT id, provider, ifNull(project_key, '') AS project_key,
				countIf(ifNull(project_key, '') != '') OVER (PARTITION BY provider, ifNull(project_key, '')) AS key_resolution_count
			FROM projects FINAL
			WHERE org_id = {org_id:String}
		))
	)
	ARRAY JOIN
		if(key_scope_emitted, [id, project_key], [id]) AS scope,
		if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
		if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
	GROUP BY provider, id, project_key, key_resolution_count, scope
) AS p
	INNER JOIN (
		SELECT provider, project_id AS scope_value, team_id, '' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_id, team_id

		UNION ALL

		SELECT provider, ifNull(project_key, '') AS scope_value, team_id, 'key' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_key IS NOT NULL AND project_key != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_key, team_id
	) AS o ON o.provider = p.provider AND o.scope_value = p.scope
	WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind
	GROUP BY provider, id, team_id
) AS p) AS t
    INNER JOIN (
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
) AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1) AS o
  INNER JOIN (SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))))
      AND cw.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))) AND ifNull(cw.type, '') NOT IN ('pr', 'merge_request')) AS w ON w.provider = o.provider AND w.project_id = o.join_key
  INNER JOIN (
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number
  WHERE l.org_id = {org_id:String} AND cityHash64(l.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))
    AND (pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))) AND multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) > 0
) AS l ON l.work_item_id = w.work_item_id
))))
								OR hasAny(lookup_refs, (
						SELECT groupUniqArray((rl.provider, rl.repo, lx.lp_pr)) FROM (
							SELECT arrayJoin((SELECT groupUniqArray((toString(pr_repo_id), toString(pr_number), team_id)) FROM (
  SELECT DISTINCT o.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM (SELECT t.team_id AS team_id, p.provider AS provider, p.join_key AS join_key FROM (SELECT provider, id, team_id FROM (
	SELECT provider, id, team_id
	FROM (
	SELECT provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
	FROM (
		SELECT provider, id, project_key,
			key_resolution_count AS project_key_resolution_count,
			(project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
		FROM (
		SELECT id, provider, project_key, key_resolution_count
		FROM (
			SELECT id, provider, ifNull(project_key, '') AS project_key,
				countIf(ifNull(project_key, '') != '') OVER (PARTITION BY provider, ifNull(project_key, '')) AS key_resolution_count
			FROM projects FINAL
			WHERE org_id = {org_id:String}
		))
	)
	ARRAY JOIN
		if(key_scope_emitted, [id, project_key], [id]) AS scope,
		if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
		if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
	GROUP BY provider, id, project_key, key_resolution_count, scope
) AS p
	INNER JOIN (
		SELECT provider, project_id AS scope_value, team_id, '' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_id, team_id

		UNION ALL

		SELECT provider, ifNull(project_key, '') AS scope_value, team_id, 'key' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_key IS NOT NULL AND project_key != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_key, team_id
	) AS o ON o.provider = p.provider AND o.scope_value = p.scope
	WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind
	GROUP BY provider, id, team_id
) AS p) AS t
    INNER JOIN (
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
) AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1) AS o
  INNER JOIN (SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))))
      AND cw.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))) AND ifNull(cw.type, '') NOT IN ('pr', 'merge_request')) AS w ON w.provider = o.provider AND w.project_id = o.join_key
  INNER JOIN (
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number
  WHERE l.org_id = {org_id:String} AND cityHash64(l.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))
    AND (pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))) AND multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) > 0
) AS l ON l.work_item_id = w.work_item_id
))) AS lp, lp.1 AS lp_repo, lp.2 AS lp_pr
						) AS lx
						INNER JOIN (
				SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
					if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
				FROM repos
				WHERE org_id = {org_id:String}
				GROUP BY id
			) AS rl ON rl.repo_uuid = lx.lp_repo))) AS reaches,
							if(reaches, arrayConcat(
							arrayMap(r -> (splitByString('#pr', r)[1], '', '', splitByString('#pr', r)[2], r),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))),
							arrayMap(r -> ('', 'github', substring(splitByChar('#', r)[1], 6), splitByChar('#', r)[2], r),
								arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
							arrayMap(r -> ('', 'gitlab', substring(splitByChar('!', r)[1], 8), splitByChar('!', r)[2], r),
								arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
						), []) AS pr_refs
						FROM work_unit_investments
						WHERE org_id = {org_id:String}
              AND work_unit_id NOT IN (
                  SELECT superseded_work_unit_id
                  FROM work_unit_supersessions
                  WHERE org_id = {org_id:String}
              )
              AND work_unit_id IN (SELECT arrayJoin(JSONExtract({scope_json:String}, 'Array(String)')))
					)
					GROUP BY work_unit_id
					HAVING argMax(reaches, computed_at)
				)
			)
			ARRAY JOIN if(empty(pr_refs) AND repo_id IS NOT NULL, [(toString(repo_id), '', '', '', '')], pr_refs) AS ref
		) AS parsed
		LEFT JOIN (
				SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
					if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
				FROM repos
				WHERE org_id = {org_id:String}
				GROUP BY id
			) AS rl ON rl.provider = parsed.lookup_provider AND rl.repo = parsed.lookup_repo
	)
	GROUP BY work_unit_id) AS folded_units) AS sized_unit
            LEFT JOIN (
                SELECT org_id, work_unit_id, argMax(issue_size, computed_at) AS issue_size
                FROM work_unit_issue_size
                WHERE org_id = {org_id:String}
                GROUP BY org_id, work_unit_id
            ) AS unit_size
                ON unit_size.org_id = sized_unit.org_id
                AND unit_size.work_unit_id = sized_unit.work_unit_id
        ) AS sized_units
ARRAY JOIN repo_prs AS rp
ARRAY JOIN wins AS win
WHERE rp.1 != '' AND rp IN (SELECT (l.1, l.2) FROM (SELECT arrayJoin((SELECT groupUniqArray((toString(pr_repo_id), toString(pr_number), team_id)) FROM (
  SELECT DISTINCT o.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM (SELECT t.team_id AS team_id, p.provider AS provider, p.join_key AS join_key FROM (SELECT provider, id, team_id FROM (
	SELECT provider, id, team_id
	FROM (
	SELECT provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
	FROM (
		SELECT provider, id, project_key,
			key_resolution_count AS project_key_resolution_count,
			(project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
		FROM (
		SELECT id, provider, project_key, key_resolution_count
		FROM (
			SELECT id, provider, ifNull(project_key, '') AS project_key,
				countIf(ifNull(project_key, '') != '') OVER (PARTITION BY provider, ifNull(project_key, '')) AS key_resolution_count
			FROM projects FINAL
			WHERE org_id = {org_id:String}
		))
	)
	ARRAY JOIN
		if(key_scope_emitted, [id, project_key], [id]) AS scope,
		if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
		if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
	GROUP BY provider, id, project_key, key_resolution_count, scope
) AS p
	INNER JOIN (
		SELECT provider, project_id AS scope_value, team_id, '' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_id, team_id

		UNION ALL

		SELECT provider, ifNull(project_key, '') AS scope_value, team_id, 'key' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_key IS NOT NULL AND project_key != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_key, team_id
	) AS o ON o.provider = p.provider AND o.scope_value = p.scope
	WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind
	GROUP BY provider, id, team_id
) AS p) AS t
    INNER JOIN (
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
) AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1) AS o
  INNER JOIN (SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))))
      AND cw.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))) AND ifNull(cw.type, '') NOT IN ('pr', 'merge_request')) AS w ON w.provider = o.provider AND w.project_id = o.join_key
  INNER JOIN (
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number
  WHERE l.org_id = {org_id:String} AND cityHash64(l.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))
    AND (pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))) AND multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) > 0
) AS l ON l.work_item_id = w.work_item_id
))) AS l))
	)
) AS m
INNER JOIN (SELECT l.1 AS pr_repo, l.2 AS pr_num, l.3 AS team_id FROM (SELECT arrayJoin((SELECT groupUniqArray((toString(pr_repo_id), toString(pr_number), team_id)) FROM (
  SELECT DISTINCT o.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM (SELECT t.team_id AS team_id, p.provider AS provider, p.join_key AS join_key FROM (SELECT provider, id, team_id FROM (
	SELECT provider, id, team_id
	FROM (
	SELECT provider, id, project_key, key_resolution_count, scope, max(scope_kind) AS scope_kind
	FROM (
		SELECT provider, id, project_key,
			key_resolution_count AS project_key_resolution_count,
			(project_key != '' AND key_resolution_count = 1) AS key_scope_emitted
		FROM (
		SELECT id, provider, project_key, key_resolution_count
		FROM (
			SELECT id, provider, ifNull(project_key, '') AS project_key,
				countIf(ifNull(project_key, '') != '') OVER (PARTITION BY provider, ifNull(project_key, '')) AS key_resolution_count
			FROM projects FINAL
			WHERE org_id = {org_id:String}
		))
	)
	ARRAY JOIN
		if(key_scope_emitted, [id, project_key], [id]) AS scope,
		if(key_scope_emitted, ['id', 'key'], ['id']) AS scope_kind,
		if(key_scope_emitted, [toUInt64(1), project_key_resolution_count], [toUInt64(1)]) AS key_resolution_count
	GROUP BY provider, id, project_key, key_resolution_count, scope
) AS p
	INNER JOIN (
		SELECT provider, project_id AS scope_value, team_id, '' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_id, team_id

		UNION ALL

		SELECT provider, ifNull(project_key, '') AS scope_value, team_id, 'key' AS required_scope_kind
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String} AND project_key IS NOT NULL AND project_key != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
		GROUP BY provider, project_key, team_id
	) AS o ON o.provider = p.provider AND o.scope_value = p.scope
	WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind
	GROUP BY provider, id, team_id
) AS p) AS t
    INNER JOIN (
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
) AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1) AS o
  INNER JOIN (SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))))
      AND cw.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}))) AND ifNull(cw.type, '') NOT IN ('pr', 'merge_request')) AS w ON w.provider = o.provider AND w.project_id = o.join_key
  INNER JOIN (
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number
  WHERE l.org_id = {org_id:String} AND cityHash64(l.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))
    AND (pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN (SELECT cityHash64(work_item_id) FROM (SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN (SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})
      AND cp.project_id IN (SELECT v FROM (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})
    UNION ALL
    SELECT arrayJoin(if(ifNull(project_key, '') != '', [id, ifNull(project_key, '')], [id])) AS v FROM projects FINAL
    WHERE org_id = {org_id:String} AND (id IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}) OR ifNull(project_key, '') IN (SELECT project_id AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND project_id IS NOT NULL AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)}
    UNION ALL
    SELECT ifNull(project_key, '') AS v FROM team_project_ownership FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != '' AND valid_from <= now64(3) AND valid_to IS NULL AND team_id IN {ids:Array(String)})))))) AND multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0) > 0
) AS l ON l.work_item_id = w.work_item_id
))) AS l)) AS lr
	ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM (
	SELECT o.team_id AS team_id,
		coalesce(toString(o.repo_id), toString(r.id)) AS repo_key,
		o.repo_full_name AS repo_full_name
	FROM (
		SELECT org_id, provider, team_id, repo_id, repo_full_name
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND valid_from <= now64(3) AND valid_to IS NULL
	) AS o
	LEFT JOIN (
		SELECT org_id, provider, id, repo, 1 AS matched
		FROM repos FINAL
		WHERE org_id = {org_id:String}
	) AS r
		ON r.org_id = o.org_id
		   AND r.provider = o.provider
		   AND lower(r.repo) = lower(o.repo_full_name)
	WHERE (o.repo_id IS NOT NULL OR r.matched = 1)
	  AND coalesce(toString(o.repo_id), toString(r.id)) IN (
		  SELECT toString(id) AS id
		  FROM repos FINAL
		  WHERE org_id = {org_id:String}
	  )
))
GROUP BY lr.team_id
ORDER BY lr.team_id
LIMIT 101