-- CHAOS-4577 seed: CURRENT team_repo_ownership rows for the documented local
-- trial org (AGENTS.md's "local" org, 70d529e0-3c06-4597-8480-794fd02328b6),
-- rendered by `trial-data.sh seed-team-repo-ownership`.
--
-- Why this exists: internal/contextfabric/devhealthsource/teams_projects.go's
-- queryTeams stamps every Team node with authorization_repositories =
-- [acr-context-fabric:no-team-repository-ownership] (the CHAOS-4390
-- fail-closed sentinel) whenever this org's team_repo_ownership carries no
-- CURRENT row for that team. On the trial/kiac plane -- seeded ONCE from the
-- ORIGINAL dev-health/backups/ dump, a 2026-08-14 snapshot with zero
-- team_repo_ownership rows -- that sentinel fires for every team, so a
-- repository-scoped Ask Dev principal (its only kind) matches nothing and
-- "Which teams are struggling, and why?" always terminates no_match. This is
-- an input-data gap, not a code defect (CHAOS-4577); CHAOS-4390's
-- fail-closed posture is correct and stays as-is.
--
-- Values: the three teams and their real, currently-open repository
-- ownership for org 70d529e0, as read back off GW's 2026-08-30 prod
-- restore (dh_0830) during CHAOS-4577's investigation
-- (.remember/lanes/lane-kiac-askdev/handoff-2026-08-29.md §15) --
-- team_id/provider/repo_full_name are the org's real identifiers, not
-- synthetic placeholders. source='manual' (not 'inferred'): this is a
-- deliberate, hand-seeded assertion, never claimed to come from the
-- inferred-ownership derivation pipeline.
--
-- CURRENT predicate this must satisfy (teams_projects.go's
-- ownedRepositoriesJoinSQL): valid_from <= now64(3) AND the latest version
-- per (team_id, repo_full_name) has valid_to IS NULL. A fixed past
-- valid_from with valid_to left NULL satisfies that indefinitely -- no
-- re-seed needed as calendar time advances.
--
-- Idempotent to re-run: team_repo_ownership is
-- ReplacingMergeTree(updated_at) ORDER BY (org_id, provider, repo_full_name,
-- team_id, source, valid_from); a re-run inserts a new part with the same
-- key and a newer updated_at, which FINAL (the only way this table is ever
-- read) collapses to a single row per key regardless of how many times this
-- file has been applied.
INSERT INTO team_repo_ownership
  (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
VALUES
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/cloudymccloudflare', 'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/dev-health-acr',     'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/dev-health-deploy',  'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/dev-health-ops',     'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/dev-health-web',     'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'CHAOS', NULL, 'full-chaos/script-manifest',    'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'gitlab', 'gl:full.chaos', NULL, 'full.chaos/chaos-ops',      'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'gitlab', 'gl:full.chaos', NULL, 'full.chaos/dev-health-ops', 'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3)),
  ('__ORG_ID__', 'github', 'gh:ops-team', NULL, 'full-chaos/dev-health-acr', 'exact', 'manual', 1, 1, 1, '2026-01-01 00:00:00', NULL, now64(3));
