-- +goose Up
ALTER TABLE channel.approval_notices ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0);
CREATE TABLE channel.notice_reconciliations (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, notice_id uuid NOT NULL, request_id uuid NOT NULL,
 member_id uuid NOT NULL, request_hash text NOT NULL, result jsonb NOT NULL,
 evidence_id uuid NOT NULL, decision text NOT NULL, expected_version bigint NOT NULL,
 PRIMARY KEY(tenant_id,task_id,notice_id,request_id),
 FOREIGN KEY(tenant_id,notice_id) REFERENCES channel.approval_notices(tenant_id,id)
);
ALTER TABLE channel.notice_reconciliations ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.notice_reconciliations FORCE ROW LEVEL SECURITY;
CREATE POLICY notice_reconcile_scope ON channel.notice_reconciliations USING(tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT SELECT,INSERT ON channel.notice_reconciliations TO agentos_runtime;
