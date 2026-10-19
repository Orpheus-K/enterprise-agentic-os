-- +goose Up
CREATE TABLE iam.operation_grants (
 tenant_id uuid NOT NULL,
 member_id uuid NOT NULL,
 app_id text NOT NULL,
 capability text NOT NULL CHECK (capability IN ('audit.read','status.read')),
 enabled boolean NOT NULL DEFAULT true,
 PRIMARY KEY(tenant_id,member_id,app_id,capability),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE iam.operation_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.operation_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY operation_grant_scope ON iam.operation_grants USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND enabled AND EXISTS(SELECT 1 FROM iam.members WHERE tenant_id=operation_grants.tenant_id AND id=member_id)
);
GRANT SELECT ON iam.operation_grants TO agentos_runtime;

ALTER TABLE channel.tasks ADD COLUMN ingress_trace_id text NOT NULL DEFAULT '';
ALTER TABLE channel.tasks ADD COLUMN ingress_span_id text NOT NULL DEFAULT '';
ALTER TABLE channel.audit ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY;
ALTER TABLE channel.audit ADD PRIMARY KEY(tenant_id,id);
ALTER TABLE channel.audit ADD COLUMN trace_id text NOT NULL DEFAULT '';
CREATE INDEX audit_task_page ON channel.audit(tenant_id,task_id,id);
GRANT USAGE ON SEQUENCE channel.audit_id_seq TO agentos_runtime;

CREATE TABLE channel.audit_reads (
 id uuid PRIMARY KEY,
 tenant_id uuid NOT NULL,
 member_id uuid NOT NULL,
 app_id text NOT NULL,
 task_id uuid NOT NULL,
 row_count integer NOT NULL,
 trace_id text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.audit_reads ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.audit_reads FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_reader_scope ON channel.audit_reads USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
GRANT SELECT,INSERT ON channel.audit_reads TO agentos_runtime;
