SELECT a.work_item_id, ifNull(a.team_id, '') AS owning_team_id,
       argMinIf(tuple(toString(a.source), toString(a.confidence), toString(a.repo_id)), tuple(toUInt8(a.is_primary != 1), -toUnixTimestamp64Micro(a.computed_at), toString(a.source), toString(a.repo_id)), (a.is_primary IN (1, 2))).1 AS current_source,
       argMinIf(tuple(toString(a.source), toString(a.confidence), toString(a.repo_id)), tuple(toUInt8(a.is_primary != 1), -toUnixTimestamp64Micro(a.computed_at), toString(a.source), toString(a.repo_id)), (a.is_primary IN (1, 2))).2 AS current_confidence,
       toString(w.repo_id) AS work_item_repo_id, ifNull(r.repo, '') AS work_item_repo_slug,
       max(a.computed_at) AS observed_at,
       argMinIf(tuple(toString(a.source), toString(a.confidence), toString(a.repo_id)), tuple(toUInt8(a.is_primary != 1), -toUnixTimestamp64Micro(a.computed_at), toString(a.source), toString(a.repo_id)), (a.is_primary IN (1, 2))).3 AS attribution_repo_id,
       countIf((a.is_primary IN (1, 2))) AS group_rows
FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.org_id = a.org_id
INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND is_active = 1) AS t ON t.id = ifNull(a.team_id, '')
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND ifNull(a.team_id, '') != ''
GROUP BY toString(w.repo_id), ifNull(r.repo, ''), a.work_item_id, ifNull(a.team_id, '') ORDER BY max(a.computed_at) ASC, toString(concat(replaceAll(replaceAll(toString(w.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(a.work_item_id, '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(ifNull(a.team_id, ''), '%', '%25'), ':', '%3A'))) ASC LIMIT {row_limit:UInt32}