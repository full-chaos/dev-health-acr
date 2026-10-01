-- A build whose target epoch can never be activated is aborted
-- (pglifecycle.Store.AbortBuild): the lifecycle row returns to 'serving' and,
-- in the same transaction, the target epoch gets a retirement record so its
-- graph key and checkpoint set have a path to deletion. That record carries a
-- third reason, 'build_aborted'.
--
-- 0019 is a shipped migration and is not edited. The inline CHECK it declared
-- on reason is unnamed, so PostgreSQL named it
-- context_fabric_graph_epoch_retirements_reason_check; it is dropped and
-- re-added here with the wider vocabulary. Re-running is a no-op.
ALTER TABLE acr.context_fabric_graph_epoch_retirements
    DROP CONSTRAINT IF EXISTS context_fabric_graph_epoch_retirements_reason_check;

ALTER TABLE acr.context_fabric_graph_epoch_retirements
    ADD CONSTRAINT context_fabric_graph_epoch_retirements_reason_check
    CHECK (reason IN ('grace_expired', 'rollback_abandoned', 'build_aborted'));
