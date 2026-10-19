-- +goose Up
ALTER TABLE channel.tasks DROP CONSTRAINT tasks_state_check;
ALTER TABLE channel.tasks ADD CONSTRAINT tasks_state_check CHECK (state IN ('pending','claimed','model_running','delivery_pending','delivery_running','succeeded','failed','unknown'));
ALTER TABLE channel.tasks ADD COLUMN lease_owner uuid;
ALTER TABLE channel.tasks ADD COLUMN lease_until timestamptz;
ALTER TABLE channel.tasks ADD COLUMN lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0);
ALTER TABLE channel.tasks ADD COLUMN step_version bigint NOT NULL DEFAULT 1 CHECK (step_version > 0);
ALTER TABLE channel.audit ADD COLUMN lease_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE channel.audit ADD COLUMN step_version bigint NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX audit_step_version ON channel.audit(tenant_id,task_id,step_version) WHERE step_version > 0;

-- Legacy attempts have no owner or reliable external result. Never reclaim them.
-- The migration owner must see all tenants. DDL locks and one migration transaction
-- prevent runtime access until FORCE RLS is restored before commit.
ALTER TABLE channel.tasks NO FORCE ROW LEVEL SECURITY;
ALTER TABLE channel.audit NO FORCE ROW LEVEL SECURITY;
WITH interrupted AS (
 UPDATE channel.tasks SET state='unknown',reason='migration_unowned_attempt',step_version=step_version+1,updated_at=clock_timestamp()
 WHERE state IN ('model_running','delivery_running')
 RETURNING tenant_id,id,state,reason,step_version
)
INSERT INTO channel.audit(tenant_id,task_id,state,reason,step_version)
 SELECT tenant_id,id,state,reason,step_version FROM interrupted;
ALTER TABLE channel.tasks FORCE ROW LEVEL SECURITY;
ALTER TABLE channel.audit FORCE ROW LEVEL SECURITY;

ALTER TABLE channel.tasks ADD CONSTRAINT task_lease_shape CHECK (
 (state IN ('claimed','model_running','delivery_pending','delivery_running') AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
 OR (state IN ('pending','succeeded','failed','unknown') AND lease_owner IS NULL AND lease_until IS NULL)
);
CREATE INDEX expired_task_leases ON channel.tasks(tenant_id,app_id,model_revision,lease_until) WHERE lease_until IS NOT NULL;
