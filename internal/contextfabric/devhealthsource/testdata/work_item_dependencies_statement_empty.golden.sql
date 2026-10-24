SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, 'related_to'), toString(w.repo_id), ifNull(r.repo, ''), d.last_synced,
       w.created_at, isNotNull(coalesce(w.completed_at, w.closed_at)), ifNull(coalesce(w.completed_at, w.closed_at), toDateTime64(0, 6, 'UTC')),
       isNotNull(t.created_at), ifNull(t.created_at, toDateTime64(0, 6, 'UTC')), isNotNull(coalesce(t.completed_at, t.closed_at)), ifNull(coalesce(t.completed_at, t.closed_at), toDateTime64(0, 6, 'UTC')), toString(t.repo_id),
       toUInt8(ifNull(rr.ended, 0) = 1), if(ifNull(rr.ended, 0) = 1, rr.ended_at, d.last_synced), if(ifNull(rr.ended, 0) = 1, rr.ended_at, d.last_synced)
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS w FINAL ON w.org_id = d.org_id AND w.work_item_id = d.source_work_item_id
LEFT JOIN work_items AS t FINAL ON t.org_id = d.org_id AND t.work_item_id = d.target_work_item_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
LEFT JOIN (SELECT unhex(p[1]) AS s, unhex(p[2]) AS t, unhex(p[3]) AS rt, fromUnixTimestamp64Micro(toInt64(p[4]), 'UTC') AS ended_at, 1 AS ended
  FROM (SELECT splitByChar('.', e) AS p FROM (SELECT arrayJoin({ended_links:Array(String)}) AS e))) AS rr
  ON rr.s = d.source_work_item_id AND rr.t = d.target_work_item_id AND rr.rt = d.relationship_type
WHERE d.org_id = {org_id:String} ORDER BY if(ifNull(rr.ended, 0) = 1, rr.ended_at, d.last_synced) ASC, toString(concat(replaceAll(replaceAll(toString(w.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(d.source_work_item_id, '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(d.target_work_item_id, '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(ifNull(d.relationship_type, 'related_to'), '%', '%25'), ':', '%3A'))) ASC LIMIT {row_limit:UInt32}