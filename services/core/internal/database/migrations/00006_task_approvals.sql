-- +goose Up
ALTER TABLE iam.operation_grants DROP CONSTRAINT operation_grants_capability_check;
ALTER TABLE iam.operation_grants ADD CONSTRAINT operation_grants_capability_check CHECK (capability IN ('audit.read','status.read','task.reconcile','task.approve'));
ALTER TABLE channel.tasks DROP CONSTRAINT tasks_state_check;
ALTER TABLE channel.tasks ADD CONSTRAINT tasks_state_check CHECK (state IN ('pending','claimed','model_running','delivery_pending','delivery_running','succeeded','failed','unknown','waiting_approval','rejected','expired'));
ALTER TABLE channel.tasks DROP CONSTRAINT task_lease_shape;
ALTER TABLE channel.tasks ADD CONSTRAINT task_lease_shape CHECK (
 (state IN ('claimed','model_running','delivery_pending','delivery_running') AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
 OR (state IN ('pending','succeeded','failed','unknown','waiting_approval','rejected','expired') AND lease_owner IS NULL AND lease_until IS NULL)
);
ALTER TABLE channel.tasks ADD COLUMN approval_member uuid;
ALTER TABLE channel.tasks ADD COLUMN approval_ttl integer;
ALTER TABLE channel.tasks ADD CONSTRAINT approval_policy_shape CHECK ((approval_member IS NULL AND approval_ttl IS NULL) OR (approval_member IS NOT NULL AND approval_ttl IS NOT NULL AND approval_ttl BETWEEN 1 AND 604800));
ALTER TABLE channel.tasks ADD FOREIGN KEY(tenant_id,approval_member) REFERENCES iam.members(tenant_id,id);
CREATE TABLE channel.approvals (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, id uuid NOT NULL,
 member_id uuid NOT NULL, revision text NOT NULL, content_hash text NOT NULL,
 waiting_version bigint NOT NULL, expires_at timestamptz NOT NULL,
 status text NOT NULL CHECK(status IN ('pending','approved','rejected','expired')),
 request_id uuid, result_version bigint, actor_provider text, actor_issuer text, actor_subject text,
 PRIMARY KEY(tenant_id,task_id), UNIQUE(tenant_id,id),
 FOREIGN KEY(tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY approval_scope ON channel.approvals USING (tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON channel.approvals TO agentos_runtime;
CREATE INDEX pending_approval_expiry ON channel.approvals(expires_at) WHERE status='pending';
