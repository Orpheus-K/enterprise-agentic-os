-- +goose Up
ALTER TABLE iam.operation_grants DROP CONSTRAINT operation_grants_capability_check;
ALTER TABLE iam.operation_grants ADD CONSTRAINT operation_grants_capability_check CHECK (capability IN ('audit.read','status.read','task.reconcile'));
CREATE TABLE channel.reconciliations (
 tenant_id uuid NOT NULL, app_id text NOT NULL, task_id uuid NOT NULL, request_id uuid NOT NULL,
 member_id uuid NOT NULL, expected_version bigint NOT NULL, decision text NOT NULL,
 evidence_id uuid NOT NULL, result_state text NOT NULL, result_version bigint NOT NULL, stage text NOT NULL,
 trace_id text NOT NULL, recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,app_id,task_id,request_id),
 FOREIGN KEY(tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.reconciliations ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.reconciliations FORCE ROW LEVEL SECURITY;
CREATE POLICY reconciliation_scope ON channel.reconciliations USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
GRANT SELECT,INSERT ON channel.reconciliations TO agentos_runtime;
