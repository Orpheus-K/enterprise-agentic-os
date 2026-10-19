-- +goose Up
CREATE SCHEMA channel;
REVOKE ALL ON SCHEMA channel FROM PUBLIC;
CREATE TABLE channel.tasks (
 tenant_id uuid NOT NULL REFERENCES iam.tenants(id),
 id uuid NOT NULL,
 member_id uuid NOT NULL,
 app_id text NOT NULL,
 issuer text NOT NULL,
 subject text NOT NULL,
 event_id text NOT NULL,
 message_id text NOT NULL,
 fingerprint text NOT NULL,
 model_revision text NOT NULL,
 prompt text NOT NULL,
 response text,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','model_running','delivery_running','succeeded','failed','unknown')),
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,app_id,event_id),
 UNIQUE (tenant_id,app_id,message_id),
 FOREIGN KEY (tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
CREATE TABLE channel.audit (
 tenant_id uuid NOT NULL,
 task_id uuid NOT NULL,
 state text NOT NULL,
 reason text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id)
);
CREATE INDEX pending_tasks ON channel.tasks(tenant_id,app_id,created_at) WHERE state='pending';
ALTER TABLE channel.tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.tasks FORCE ROW LEVEL SECURITY;
ALTER TABLE channel.audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.audit FORCE ROW LEVEL SECURITY;
-- Trusted installation workers own the tenant scope. These tables are not exposed to users.
CREATE POLICY task_scope ON channel.tasks USING (tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
CREATE POLICY audit_scope ON channel.audit USING (tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT USAGE ON SCHEMA channel TO agentos_runtime;
GRANT SELECT,INSERT,UPDATE ON channel.tasks TO agentos_runtime;
GRANT SELECT,INSERT ON channel.audit TO agentos_runtime;
