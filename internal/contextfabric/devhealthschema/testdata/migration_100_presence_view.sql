CREATE OR REPLACE VIEW project_membership_presence AS
WITH touched AS (
    SELECT
        org_id,
        subject_kind,
        repo_id,
        subject_id,
        provider,
        occurred_at,
        ingested_at,
        event_id,
        to_project_id,
        arrayJoin(arrayFilter(
            pair -> pair.1 != '',
            if(from_project_id = to_project_id,
               [(to_project_id, to_project_key)],
               [(to_project_id, to_project_key), (from_project_id, from_project_key)])
        )) AS touch
    FROM project_membership_transitions FINAL
),
latest_membership AS (
    SELECT
        org_id,
        subject_kind,
        repo_id,
        subject_id,
        touch.1 AS project_id,
        argMax(touch.2, (occurred_at, event_id)) AS project_key,
        argMax(provider, (occurred_at, event_id)) AS provider,
        argMax(to_project_id, (occurred_at, event_id)) AS latest_to_project_id,
        max(occurred_at) AS observed_at,
        max(ingested_at) AS max_ingested_at
    FROM touched
    GROUP BY org_id, subject_kind, repo_id, subject_id, project_id
),
subjects_with_history AS (
    SELECT DISTINCT org_id, subject_kind, repo_id, subject_id
    FROM project_membership_transitions FINAL
)
SELECT
    org_id,
    subject_kind,
    repo_id,
    subject_id,
    provider,
    project_id,
    project_key,
    observed_at,
    max_ingested_at AS last_synced,
    'transition' AS source
FROM latest_membership
WHERE latest_to_project_id = project_id
UNION ALL
SELECT
    w.org_id AS org_id,
    'work_item' AS subject_kind,
    w.repo_id AS repo_id,
    w.work_item_id AS subject_id,
    w.provider AS provider,
    w.project_id AS project_id,
    w.project_key AS project_key,
    w.updated_at AS observed_at,
    w.ingested_at AS last_synced,
    'work_item_column' AS source
FROM work_items AS w FINAL
WHERE w.project_id != ''
    AND w.provider != 'gitlab'
    AND (w.provider != 'github' OR startsWith(w.project_id, 'ghprojv2:'))
    AND (w.org_id, 'work_item', w.repo_id, w.work_item_id) NOT IN (
        SELECT org_id, subject_kind, repo_id, subject_id FROM subjects_with_history
    );
