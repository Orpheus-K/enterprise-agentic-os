-- +goose Up
CREATE TABLE channel.tool_operations (
 tenant_id uuid NOT NULL, app_id text NOT NULL, id uuid NOT NULL, member_id uuid NOT NULL,
 skill_name text NOT NULL, skill_revision text NOT NULL, tool_name text NOT NULL, tool_revision text NOT NULL,
 input_hash text NOT NULL, output_hash text NOT NULL DEFAULT '', trace_id text NOT NULL,
 state text NOT NULL CHECK(state IN ('started','succeeded','rejected','unknown','denied')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), finished_at timestamptz,
 PRIMARY KEY(tenant_id,app_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.tool_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.tool_operations FORCE ROW LEVEL SECURITY;
CREATE POLICY tool_operation_scope ON channel.tool_operations USING (tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT SELECT,INSERT,UPDATE ON channel.tool_operations TO agentos_runtime;
