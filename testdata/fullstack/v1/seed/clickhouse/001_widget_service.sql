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
-- incidents.v1 reaches canonical incidents through active service-to-repository mappings.
INSERT INTO operational_service_repository_mappings
    (org_id, provider, provider_instance_id, source_entity_type, external_id,
     source_version_at, id, observed_at, last_synced, service_id, repo_id,
     repo_full_name, repo_provider, mapping_kind, rule_id, valid_from, valid_to, is_active,
     relationship_provenance, relationship_confidence,
     source_revision, source_conflict_key, ingest_revision, ordering_contract)
VALUES
    ('__ORG_ID__', 'synthetic', 'fixture', 'service_repository_mapping', 'widget-service',
     '2026-01-14 11:00:00.000000', 'mapping-widget-service', '2026-01-14 11:00:00.000000',
     '2026-01-14 12:00:00.000000', 'service-widget', '00000000-3065-4000-8000-000000000001',
     'example-org/widget-service', 'synthetic', 'admin_configuration_exact',
     'service_repository_mapping.admin.v1', '2026-01-01 00:00:00.000000', NULL, 1,
     'admin_configuration', 1.0,
     32621008237716716150192299327304567, '6f7065726174696f6e616c2d636f6e666c6963742d76310000000d656e746974795f66616d696c790006737472696e670100000000000000266f7065726174696f6e616c5f736572766963655f7265706f7369746f72795f6d617070696e67000000066f72675f69640006737472696e6701000000000000000a5f5f4f52475f49445f5f0000000870726f76696465720006737472696e6701000000000000000973796e7468657469630000001470726f76696465725f696e7374616e63655f69640006737472696e670100000000000000076669787475726500000012736f757263655f656e746974795f747970650006737472696e6701000000000000001a736572766963655f7265706f7369746f72795f6d617070696e670000000b65787465726e616c5f69640006737472696e6701000000000000000e7769646765742d7365727669636500000011736f757263655f76657273696f6e5f617400086461746574696d6501000000000000001b323032362d30312d31345431313a30303a30302e3030303030305a0000000a736572766963655f69640006737472696e6701000000000000000e736572766963652d776964676574000000077265706f5f696400047575696401000000000000002430303030303030302d333036352d343030302d383030302d3030303030303030303030310000000e7265706f5f66756c6c5f6e616d650006737472696e6701000000000000001a6578616d706c652d6f72672f7769646765742d736572766963650000000d7265706f5f70726f76696465720006737472696e6701000000000000000973796e7468657469630000000c6d617070696e675f6b696e640006737472696e6701000000000000001961646d696e5f636f6e66696775726174696f6e5f65786163740000000772756c655f69640006737472696e67010000000000000023736572766963655f7265706f7369746f72795f6d617070696e672e61646d696e2e76310000000a76616c69645f66726f6d00086461746574696d6501000000000000001b323032362d30312d30315430303a30303a30302e3030303030305a0000000876616c69645f746f00046e756c6c0000000000000000000000000969735f6163746976650004626f6f6c010000000000000001010000001772656c6174696f6e736869705f70726f76656e616e63650006737472696e6701000000000000001361646d696e5f636f6e66696775726174696f6e0000001772656c6174696f6e736869705f636f6e666964656e63650007666c6f617436340100000000000000083ff0000000000000', 32621074645995381403089860400000000, 2),
    ('__ORG_ID__', 'synthetic', 'fixture', 'service_repository_mapping', 'other-service',
     '2026-01-14 11:00:00.000000', 'mapping-other-service', '2026-01-14 11:00:00.000000',
     '2026-01-14 12:00:00.000000', 'service-other', '00000000-3065-4000-8000-000000000002',
     'example-org/other-service', 'synthetic', 'admin_configuration_exact',
     'service_repository_mapping.admin.v1', '2026-01-01 00:00:00.000000', NULL, 1,
     'admin_configuration', 1.0,
     32621008237716716135512990110377834, '6f7065726174696f6e616c2d636f6e666c6963742d76310000000d656e746974795f66616d696c790006737472696e670100000000000000266f7065726174696f6e616c5f736572766963655f7265706f7369746f72795f6d617070696e67000000066f72675f69640006737472696e6701000000000000000a5f5f4f52475f49445f5f0000000870726f76696465720006737472696e6701000000000000000973796e7468657469630000001470726f76696465725f696e7374616e63655f69640006737472696e670100000000000000076669787475726500000012736f757263655f656e746974795f747970650006737472696e6701000000000000001a736572766963655f7265706f7369746f72795f6d617070696e670000000b65787465726e616c5f69640006737472696e6701000000000000000d6f746865722d7365727669636500000011736f757263655f76657273696f6e5f617400086461746574696d6501000000000000001b323032362d30312d31345431313a30303a30302e3030303030305a0000000a736572766963655f69640006737472696e6701000000000000000d736572766963652d6f74686572000000077265706f5f696400047575696401000000000000002430303030303030302d333036352d343030302d383030302d3030303030303030303030320000000e7265706f5f66756c6c5f6e616d650006737472696e670100000000000000196578616d706c652d6f72672f6f746865722d736572766963650000000d7265706f5f70726f76696465720006737472696e6701000000000000000973796e7468657469630000000c6d617070696e675f6b696e640006737472696e6701000000000000001961646d696e5f636f6e66696775726174696f6e5f65786163740000000772756c655f69640006737472696e67010000000000000023736572766963655f7265706f7369746f72795f6d617070696e672e61646d696e2e76310000000a76616c69645f66726f6d00086461746574696d6501000000000000001b323032362d30312d30315430303a30303a30302e3030303030305a0000000876616c69645f746f00046e756c6c0000000000000000000000000969735f6163746976650004626f6f6c010000000000000001010000001772656c6174696f6e736869705f70726f76656e616e63650006737472696e6701000000000000001361646d696e5f636f6e66696775726174696f6e0000001772656c6174696f6e736869705f636f6e666964656e63650007666c6f617436340100000000000000083ff0000000000000', 32621074645995381403089860400000000, 2);

INSERT INTO operational_incidents
    (org_id, provider, provider_instance_id, source_entity_type, external_id,
     source_version_at, id, source_id, source_url, source_event_at, source_event_id,
     observed_at, last_synced, raw_status, raw_severity, raw_priority,
     normalized_status, normalized_severity, normalized_priority,
     relationship_provenance, relationship_confidence, service_id,
     service_external_id, escalation_policy_id, title, description, started_at,
     resolved_at, is_deleted, deleted_at,
     source_revision, source_conflict_key, ingest_revision, ordering_contract)
VALUES
    ('__ORG_ID__', 'synthetic', 'fixture', 'incident', 'widget-incident-001',
     '2026-01-14 11:10:00.000000', 'incident-widget-001', NULL,
     'https://example.invalid/incidents/widget-incident-001', '2026-01-14 11:10:00.000000',
     'widget-incident-001', '2026-01-14 11:10:00.000000', '2026-01-14 12:00:00.000000',
     'open', 'low', NULL, 'open', 'low', NULL, 'native', 1.0, 'service-widget', NULL, NULL,
     'Synthetic widget-service incident', 'Fixture-only canonical operational incident.',
     '2026-01-14 11:10:00.000000', NULL, 0, NULL,
     32621019305763160353547467306627794, '6f7065726174696f6e616c2d636f6e666c6963742d76310000000d656e746974795f66616d696c790006737472696e670100000000000000146f7065726174696f6e616c5f696e636964656e74000000066f72675f69640006737472696e6701000000000000000a5f5f4f52475f49445f5f0000000870726f76696465720006737472696e6701000000000000000973796e7468657469630000001470726f76696465725f696e7374616e63655f69640006737472696e670100000000000000076669787475726500000012736f757263655f656e746974795f747970650006737472696e67010000000000000008696e636964656e740000000b65787465726e616c5f69640006737472696e670100000000000000137769646765742d696e636964656e742d30303100000011736f757263655f76657273696f6e5f617400086461746574696d6501000000000000001b323032362d30312d31345431313a31303a30302e3030303030305a00000009736f757263655f696400046e756c6c0000000000000000000000000a736f757263655f75726c0006737472696e6701000000000000003568747470733a2f2f6578616d706c652e696e76616c69642f696e636964656e74732f7769646765742d696e636964656e742d3030310000000f736f757263655f6576656e745f617400086461746574696d6501000000000000001b323032362d30312d31345431313a31303a30302e3030303030305a0000000f736f757263655f6576656e745f69640006737472696e670100000000000000137769646765742d696e636964656e742d3030310000000a7261775f7374617475730006737472696e670100000000000000046f70656e0000000c7261775f73657665726974790006737472696e670100000000000000036c6f770000000c7261775f7072696f7269747900046e756c6c000000000000000000000000116e6f726d616c697a65645f7374617475730006737472696e670100000000000000046f70656e000000136e6f726d616c697a65645f73657665726974790006737472696e670100000000000000036c6f77000000136e6f726d616c697a65645f7072696f7269747900046e756c6c0000000000000000000000001772656c6174696f6e736869705f70726f76656e616e63650006737472696e670100000000000000066e61746976650000001772656c6174696f6e736869705f636f6e666964656e63650007666c6f617436340100000000000000083ff00000000000000000000a736572766963655f69640006737472696e6701000000000000000e736572766963652d77696467657400000013736572766963655f65787465726e616c5f696400046e756c6c00000000000000000000000014657363616c6174696f6e5f706f6c6963795f696400046e756c6c000000000000000000000000057469746c650006737472696e6701000000000000002153796e746865746963207769646765742d7365727669636520696e636964656e740000000b6465736372697074696f6e0006737472696e6701000000000000002c466978747572652d6f6e6c792063616e6f6e6963616c206f7065726174696f6e616c20696e636964656e742e0000000a737461727465645f617400086461746574696d6501000000000000001b323032362d30312d31345431313a31303a30302e3030303030305a0000000b7265736f6c7665645f617400046e756c6c0000000000000000000000000a69735f64656c657465640004626f6f6c010000000000000001000000000a64656c657465645f617400046e756c6c000000000000000000', 32621074645995381403089861000000000, 2),
    ('__ORG_ID__', 'synthetic', 'fixture', 'incident', 'other-incident-001',
     '2026-01-14 11:20:00.000000', 'incident-other-001', NULL,
     'https://example.invalid/incidents/other-incident-001', '2026-01-14 11:20:00.000000',
     'other-incident-001', '2026-01-14 11:20:00.000000', '2026-01-14 12:00:00.000000',
     'open', 'low', NULL, 'open', 'low', NULL, 'native', 1.0, 'service-other', NULL, NULL,
     'Synthetic other-service incident', 'Foreign canonical operational incident.',
     '2026-01-14 11:20:00.000000', NULL, 0, NULL,
     32621030373809604639609751810068915, '6f7065726174696f6e616c2d636f6e666c6963742d76310000000d656e746974795f66616d696c790006737472696e670100000000000000146f7065726174696f6e616c5f696e636964656e74000000066f72675f69640006737472696e6701000000000000000a5f5f4f52475f49445f5f0000000870726f76696465720006737472696e6701000000000000000973796e7468657469630000001470726f76696465725f696e7374616e63655f69640006737472696e670100000000000000076669787475726500000012736f757263655f656e746974795f747970650006737472696e67010000000000000008696e636964656e740000000b65787465726e616c5f69640006737472696e670100000000000000126f746865722d696e636964656e742d30303100000011736f757263655f76657273696f6e5f617400086461746574696d6501000000000000001b323032362d30312d31345431313a32303a30302e3030303030305a00000009736f757263655f696400046e756c6c0000000000000000000000000a736f757263655f75726c0006737472696e6701000000000000003468747470733a2f2f6578616d706c652e696e76616c69642f696e636964656e74732f6f746865722d696e636964656e742d3030310000000f736f757263655f6576656e745f617400086461746574696d6501000000000000001b323032362d30312d31345431313a32303a30302e3030303030305a0000000f736f757263655f6576656e745f69640006737472696e670100000000000000126f746865722d696e636964656e742d3030310000000a7261775f7374617475730006737472696e670100000000000000046f70656e0000000c7261775f73657665726974790006737472696e670100000000000000036c6f770000000c7261775f7072696f7269747900046e756c6c000000000000000000000000116e6f726d616c697a65645f7374617475730006737472696e670100000000000000046f70656e000000136e6f726d616c697a65645f73657665726974790006737472696e670100000000000000036c6f77000000136e6f726d616c697a65645f7072696f7269747900046e756c6c0000000000000000000000001772656c6174696f6e736869705f70726f76656e616e63650006737472696e670100000000000000066e61746976650000001772656c6174696f6e736869705f636f6e666964656e63650007666c6f617436340100000000000000083ff00000000000000000000a736572766963655f69640006737472696e6701000000000000000d736572766963652d6f7468657200000013736572766963655f65787465726e616c5f696400046e756c6c00000000000000000000000014657363616c6174696f6e5f706f6c6963795f696400046e756c6c000000000000000000000000057469746c650006737472696e6701000000000000002053796e746865746963206f746865722d7365727669636520696e636964656e740000000b6465736372697074696f6e0006737472696e67010000000000000027466f726569676e2063616e6f6e6963616c206f7065726174696f6e616c20696e636964656e742e0000000a737461727465645f617400086461746574696d6501000000000000001b323032362d30312d31345431313a32303a30302e3030303030305a0000000b7265736f6c7665645f617400046e756c6c0000000000000000000000000a69735f64656c657465640004626f6f6c010000000000000001000000000a64656c657465645f617400046e756c6c000000000000000000', 32621074645995381403089861600000000, 2);

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
--
-- Confidence must stay >= 0.5. internal/contextpacket/source_executor.go filters rows with
-- `{include_low_confidence:UInt8} = 1 OR confidence >= 0.5`, and the MCP context_for_task tool
-- (internal/mcp) never sets IncludeLowConfidence, so it defaults to false -- unlike the direct
-- HTTP context-packets endpoint, which every fullstack-opencode.sh caller invokes with
-- include_low_confidence:true. A prior 0.4 value passed the direct-HTTP capture used by
-- capture_api_packet but was silently dropped from the live packet OpenCode's MCP session
-- actually sees, so deployment_incident_provenance.v1 came back "unavailable:no_evidence"
-- only through the real agent-facing path -- exactly the surface task-001's oracle requires
-- to be complete (see task-001-checkout-flake-exact-commit.oracle.json's
-- expected_unavailable_sources_note). Caught the first time this gate ran for real in CI.
INSERT INTO work_graph_deployment_incident_edges
    (edge_id, org_id, deployment_id, incident_id, provider, repo_id, confidence,
     source, evidence, observed_at, computed_at)
VALUES
    ('edge-deploy-incident-2026-01-14-01', '__ORG_ID__', 'deploy-widget-2026-01-14-01',
     'none', 'synthetic', '00000000-3065-4000-8000-000000000001', 0.6, 'heuristic',
     'no operational incident correlated with this deployment', '2026-01-14 11:10:00.000',
     '2026-01-14 12:00:00.000');

-- file_hotspots.v1 (EvidenceScopeRepo)
INSERT INTO file_hotspot_daily
    (repo_id, day, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total,
     cyclomatic_avg, blame_concentration, risk_score, computed_at, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'src/checkout/cart_drawer.ts',
     180, 8, 30, 2.5, 0.58, 37.0, '2026-01-14 11:00:00', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'src/checkout/replaced.ts',
     80, 4, 19, 2.1, 0.44, 20.0, '2026-01-14 11:00:00', '__ORG_ID__');

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
     'typescript', 170, 11, 30, 2.7, 1, 0, '2026-01-14 11:00:00', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'main', 'src/checkout/replaced.ts',
     'typescript', 90, 7, 19, 2.2, 1, 0, '2026-01-14 11:00:00', '__ORG_ID__');

INSERT INTO file_complexity_snapshots
    (repo_id, as_of_day, ref, file_path, language, loc, functions_count, cyclomatic_total,
     cyclomatic_avg, high_complexity_functions, very_high_complexity_functions, computed_at, org_id)
VALUES
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'main', 'src/checkout/cart_drawer.ts',
     'typescript', 180, 12, 34, 2.8, 1, 0, '2026-01-14 12:00:00', '__ORG_ID__'),
    ('00000000-3065-4000-8000-000000000001', '2026-01-14', 'zzz-tied-ref', 'src/checkout/other_ref.ts',
     'typescript', 80, 5, 11, 2.2, 0, 0, '2026-01-14 12:00:00', '__ORG_ID__');
