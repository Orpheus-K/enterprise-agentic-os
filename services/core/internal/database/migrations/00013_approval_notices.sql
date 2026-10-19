-- +goose Up
ALTER TABLE channel.tasks ADD COLUMN approval_recipient text;
ALTER TABLE channel.audit ADD COLUMN operation_id uuid;
CREATE TABLE channel.approval_notices (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, id uuid NOT NULL,
 member_id uuid NOT NULL, recipient text NOT NULL, issuer text NOT NULL,
 revision text NOT NULL, waiting_version bigint NOT NULL, content_hash text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','sent','unknown','cancelled')),
 owner uuid, lease_until timestamptz,
 PRIMARY KEY(tenant_id,id), FOREIGN KEY(tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id),
 CHECK((state='sending' AND owner IS NOT NULL AND lease_until IS NOT NULL) OR (state<>'sending' AND owner IS NULL AND lease_until IS NULL))
);
ALTER TABLE channel.approval_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.approval_notices FORCE ROW LEVEL SECURITY;
CREATE POLICY notice_scope ON channel.approval_notices USING(tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON channel.approval_notices TO agentos_runtime;
