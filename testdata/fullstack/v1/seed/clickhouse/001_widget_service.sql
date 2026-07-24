-- testdata/fullstack/v1/seed/clickhouse/001_widget_service.sql
--
-- CHAOS-3065 full-stack acceptance fixture: a deterministic projection of
-- testdata/evaluation/v1 (CHAOS-2918) into the current Dev Health ClickHouse
-- schema (ops/src/dev_health_ops/migrations/clickhouse).
--
-- Rules (see testdata/fullstack/v1/README.md and docs/fullstack-acceptance.md):
--   * Fixed UUIDs and fixed timestamps only. No generateUUIDv4(), no now()/now64().
--   * The literal token __ORG_ID__ stands in for the org UUID minted at
--     provisioning time by `dev-hops admin orgs create`. The orchestrator
--     performs a single textual substitution of __ORG_ID__ -> the real org
--     UUID before executing this file. Do not invent another mechanism.
--   * All identifiers/content are synthetic and public-safe (inherited from
--     testdata/evaluation/v1's clean-room notice).
--
-- Fixed identities:
--   repo 1 (in-scope):     example-org/widget-service
--                          00000000-3065-4000-8000-000000000001
--   repo 2 (out-of-scope): example-org/other-service
--                          00000000-3065-4000-8000-000000000002
--
-- Nothing is seeded on branch release/1.4-unindexed: task-003 must be
-- genuinely empty, not filtered.

-- ---------------------------------------------------------------------------
-- repos
-- ---------------------------------------------------------------------------

INSERT INTO repos
    (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'example-org/widget-service', 'main',
     '2026-01-01 00:00:00.000', NULL, NULL, '2026-01-14 12:00:00.000', '__ORG_ID__', 'synthetic'),
    ('00000000-3065-4000-8000-000000000002', 'example-org/other-service', 'main',
     '2026-01-01 00:00:00.000', NULL, NULL, '2026-01-14 12:00:00.000', '__ORG_ID__', 'synthetic');

-- ---------------------------------------------------------------------------
-- git_commits
-- ---------------------------------------------------------------------------
-- a1b2... projects ev-commit-checkout-001 (task-001, exact commit scope).
-- b2c3... projects ev-commit-auth-002 (task-002's corpus commit; see the
-- fixture manifest / oracle README for why branch-only scope never surfaces
-- this row via git_commits.v1 -- it is seeded anyway so the row exists for
-- traceability and for any future commit-scoped task against main).
-- c3d4... belongs to the OTHER repo and must never appear in a
-- widget-service packet.

INSERT INTO git_commits
    (repo_id, hash, message, author_name, author_email, author_when,
     committer_name, committer_email, committer_when, parents, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2',
     'checkout: add retry-safe wait for cart drawer animation',
     'Ada Merchant', 'ada@example.invalid', '2026-01-13 18:40:00.000',
     'Ada Merchant', 'ada@example.invalid', '2026-01-13 18:42:00.000',
     1, '2026-01-14 12:00:00.000', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000001', 'b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3',
     'auth: replace session cookie parsing with typed token struct',
     'Ada Merchant', 'ada@example.invalid', '2026-01-12 09:00:00.000',
     'Ada Merchant', 'ada@example.invalid', '2026-01-12 09:05:00.000',
     1, '2026-01-14 12:00:00.000', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000002', 'c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4',
     'chore: bump dependency pins',
     'Ben Otherservice', 'ben@example.invalid', '2026-01-11 09:00:00.000',
     'Ben Otherservice', 'ben@example.invalid', '2026-01-11 09:05:00.000',
     1, '2026-01-14 12:00:00.000', '__ORG_ID__');

-- ---------------------------------------------------------------------------
-- git_commit_stats
-- ---------------------------------------------------------------------------
-- Two file rows on the checkout commit so task-001 has >= 2 expandable
-- commit-file evidence refs (git_commit_files.v1).

INSERT INTO git_commit_stats
    (repo_id, commit_hash, file_path, additions, deletions, old_file_mode,
     new_file_mode, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2',
     'src/checkout/cart_drawer.ts', 12, 4, '100644', '100644', '2026-01-14 12:00:00.000', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000001', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2',
     'tests/e2e/checkout_flow.spec.ts', 8, 1, '100644', '100644', '2026-01-14 12:00:00.000', '__ORG_ID__');

-- ---------------------------------------------------------------------------
-- ci_pipeline_runs
-- ---------------------------------------------------------------------------
-- Projects ev-ci-checkout-001: the flaky checkout-e2e run pinned to the
-- checkout commit, on branch main.

INSERT INTO ci_pipeline_runs
    (repo_id, run_id, status, queued_at, started_at, finished_at,
     last_synced, pipeline_name, provider, retry_count, commit_hash,
     branch, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'checkout-e2e-run-4821', 'success',
     '2026-01-14 10:08:00.000', '2026-01-14 10:10:00.000', '2026-01-14 10:15:00.000',
     '2026-01-14 12:00:00.000',
     'checkout-e2e', 'synthetic', 2, 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2', 'main', '__ORG_ID__');

-- ---------------------------------------------------------------------------
-- git_pull_requests
-- ---------------------------------------------------------------------------
-- Projects ev-pr-auth-002: PR #1042, the typed session token refactor.

INSERT INTO git_pull_requests
    (repo_id, number, title, body, state, author_name, author_email,
     created_at, merged_at, closed_at, head_branch, base_branch, additions,
     deletions, changed_files, first_review_at, first_comment_at,
     changes_requested_count, reviews_count, comments_count, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 1042,
     'Typed session tokens for auth refactor',
     'Refactors session cookie parsing into a typed token struct with explicit expiry handling.',
     'open', 'Ada Merchant', 'ada@example.invalid', '2026-01-12 09:10:00.000',
     NULL, NULL, 'auth-refactor-typed-tokens', 'main', 140, 52, 6,
     '2026-01-12 14:30:00.000', '2026-01-12 13:50:00.000', 1, 1, 3,
     '2026-01-14 12:00:00.000', '__ORG_ID__');

-- ---------------------------------------------------------------------------
-- git_pull_request_reviews
-- ---------------------------------------------------------------------------
-- A changes_requested review on PR #1042, matching ev-pr-auth-002's summary
-- ("...should reject tokens with a missing expiry field...").

INSERT INTO git_pull_request_reviews
    (repo_id, number, review_id, reviewer, state, submitted_at, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 1042, 'review-1042-001', 'priya-reviewer',
     'changes_requested', '2026-01-12 14:30:00.000', '2026-01-14 12:00:00.000', '__ORG_ID__');

-- ---------------------------------------------------------------------------
-- Density rows: the remaining dev-health-source-catalog.v1 sources
-- ---------------------------------------------------------------------------
-- task-001 (commit_sha set, branch left empty) is the only scope shape under
-- which internal/contextpacket/source_catalog.go's ExecuteCatalog does not
-- unconditionally skip an entire scope class (EvidenceScopeRepo queries are
-- skipped outright whenever branch is set; EvidenceScopeCommit queries are
-- skipped outright whenever commit_sha is empty -- see README.md). These
-- rows exist so every EvidenceScopeRepo/EvidenceScopeBranch source that CAN
-- return data for task-001 does, closing the gap toward PacketComplete as
-- far as seeding alone can. They are all bystander rows: they satisfy each
-- query's own WHERE clause for org/repo (and, for file_complexity.v1, ref)
-- but are NOT part of any task's required_evidence -- see fixture-manifest.json
-- and README.md's "background density, not required evidence" note.
--
-- NOTE: `incidents.v1` (source_queries.go) still queries a table named
-- `incidents`, which ops migration 068_drop_legacy_incidents.sql drops in
-- favor of `operational_incidents`. That table no longer exists after a full
-- migration run, so incidents.v1 always fails with source_unavailable no
-- matter what is seeded here -- there is nothing to insert for it. See the
-- delivery report / README for this finding.

-- work_items.v1 (EvidenceScopeRepo)
INSERT INTO work_items
    (repo_id, work_item_id, provider, title, description, type, status, status_raw,
     project_key, project_id, assignees, reporter, created_at, updated_at, started_at,
     completed_at, closed_at, labels, story_points, sprint_id, sprint_name, parent_id,
     epic_id, url, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'WIDGET-101', 'jira',
     'Investigate checkout flake', 'Fixture-only synthetic work item for CHAOS-3065.',
     'bug', 'in_progress', 'In Progress', 'WIDGET', '10001',
     ['ada@example.invalid'], 'ada@example.invalid',
     '2026-01-13 09:00:00.000', '2026-01-14 09:00:00.000', '2026-01-13 09:30:00.000',
     NULL, NULL, ['checkout', 'flaky-test'], 3, 'sprint-12', 'Sprint 12', '', '',
     'https://example.invalid/jira/WIDGET-101', '2026-01-14 12:00:00.000', '__ORG_ID__');

-- work_item_dependencies.v1 (EvidenceScopeRepo; joined to work_items on
-- source_work_item_id, so only the source side must match a seeded work item)
INSERT INTO work_item_dependencies
    (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id)
VALUES
    ('WIDGET-101', 'WIDGET-099', 'blocks', 'is blocked by', '2026-01-14 12:00:00.000', '__ORG_ID__');

-- work_graph.v1 (EvidenceScopeRepo; task-001 filters on commit_sha, so this
-- edge's source_id is the checkout commit hash)
INSERT INTO work_graph_edges
    (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id,
     provider, provenance, confidence, evidence, discovered_at, last_synced, org_id)
VALUES
    ('edge-checkout-commit-ci-001', 'commit', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2',
     'ci_run', 'checkout-e2e-run-4821', 'touches', '00000000-3065-4000-8000-000000000001',
     'synthetic', 'native', 0.9, 'commit triggered the checkout-e2e pipeline run',
     '2026-01-13 18:42:00.000', '2026-01-14 12:00:00.000', '__ORG_ID__');

-- ai_workflow_runs.v1 (EvidenceScopeRepo; org_id column is UUID here, not
-- String -- the placeholder is still '__ORG_ID__', cast implicitly)
INSERT INTO ai_workflow_runs
    (run_id, org_id, provider, run_kind, status, tool, model, actor, repo_id,
     prompts_redacted, prompt_hash, prompt_length, started_at, completed_at,
     observed_at, metadata, computed_at)
VALUES
    ('ai-run-checkout-001', '__ORG_ID__', 'anthropic', 'code_review', 'completed',
     'opencode', 'deterministic-fixture-model', 'ada@example.invalid',
     '00000000-3065-4000-8000-000000000001', 1, NULL, NULL,
     '2026-01-14 08:00:00.000', '2026-01-14 08:05:00.000', '2026-01-14 08:05:00.000',
     '{}', '2026-01-14 12:00:00.000');

-- ai_workflow_artifacts.v1 (EvidenceScopeRepo)
INSERT INTO ai_workflow_artifact_edges
    (edge_id, org_id, run_id, artifact_type, artifact_id, provider, repo_id,
     confidence, source, evidence, observed_at, computed_at)
VALUES
    ('edge-ai-artifact-checkout-001', '__ORG_ID__', 'ai-run-checkout-001',
     'commit_annotation', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2', 'anthropic',
     '00000000-3065-4000-8000-000000000001', 0.9, 'native',
     'AI-assisted review note on the checkout flake fix',
     '2026-01-14 08:05:00.000', '2026-01-14 12:00:00.000');

-- ai_review_outcomes.v1 (EvidenceScopeRepo)
INSERT INTO work_graph_pr_review_outcome_edges
    (edge_id, org_id, pr_id, review_outcome_id, outcome, provider, repo_id,
     confidence, source, evidence, observed_at, computed_at)
VALUES
    ('edge-review-outcome-1042-001', '__ORG_ID__', '1042', 'outcome-1042-001',
     'changes_requested_addressed', 'github', '00000000-3065-4000-8000-000000000001',
     0.95, 'native', 'PR #1042 changes_requested addressed by a follow-up commit',
     '2026-01-14 09:00:00.000', '2026-01-14 12:00:00.000');

-- deployments.v1 (EvidenceScopeRepo)
INSERT INTO deployments
    (repo_id, deployment_id, status, environment, started_at, finished_at,
     deployed_at, merged_at, pull_request_number, release_ref,
     release_ref_confidence, last_synced, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', 'deploy-widget-2026-01-14-01', 'success',
     'production', '2026-01-14 11:00:00.000', '2026-01-14 11:05:00.000', '2026-01-14 11:05:00.000',
     NULL, NULL, 'v2026.01.14-1', 0.8, '2026-01-14 12:00:00.000', '__ORG_ID__');

-- deployment_incident_provenance.v1 (EvidenceScopeRepo). No corresponding row
-- in an "incidents" table is required or possible -- see the note above about
-- incidents.v1 querying a table ops migration 068 dropped; this edge only
-- needs to satisfy its own WHERE clause (org_id + repo_id).
INSERT INTO work_graph_deployment_incident_edges
    (edge_id, org_id, deployment_id, incident_id, provider, repo_id, confidence,
     source, evidence, observed_at, computed_at)
VALUES
    ('edge-deploy-incident-2026-01-14-01', '__ORG_ID__', 'deploy-widget-2026-01-14-01',
     'none', 'synthetic', '00000000-3065-4000-8000-000000000001', 0.4, 'heuristic',
     'no operational incident correlated with this deployment', '2026-01-14 11:10:00.000',
     '2026-01-14 12:00:00.000');

-- file_hotspots.v1 (EvidenceScopeRepo)
INSERT INTO file_hotspot_daily
    (repo_id, day, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total,
     cyclomatic_avg, blame_concentration, risk_score, computed_at, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'src/checkout/cart_drawer.ts',
     220, 9, 34, 2.8, 0.62, 41.5, '2026-01-14 12:00:00', '__ORG_ID__');

-- file_complexity.v1 (EvidenceScopeBranch; ref='main' so it also matches
-- task-002's branch=main request, in addition to task-001's empty branch)
INSERT INTO file_complexity_snapshots
    (repo_id, as_of_day, ref, file_path, language, loc, functions_count, cyclomatic_total,
     cyclomatic_avg, high_complexity_functions, very_high_complexity_functions, computed_at, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'main', 'src/checkout/cart_drawer.ts',
     'typescript', 180, 12, 34, 2.8, 1, 0, '2026-01-14 12:00:00', '__ORG_ID__');
