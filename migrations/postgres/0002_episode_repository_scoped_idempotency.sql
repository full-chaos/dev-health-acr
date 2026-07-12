ALTER TABLE acr.agent_episodes
    DROP CONSTRAINT IF EXISTS agent_episodes_org_id_idempotency_key_key,
    DROP CONSTRAINT IF EXISTS agent_episodes_org_id_client_episode_id_key;

ALTER TABLE acr.agent_episodes
    ADD CONSTRAINT agent_episodes_org_repo_idempotency_key_key UNIQUE (org_id, repo_id, idempotency_key),
    ADD CONSTRAINT agent_episodes_org_repo_client_episode_id_key UNIQUE (org_id, repo_id, client_episode_id);
