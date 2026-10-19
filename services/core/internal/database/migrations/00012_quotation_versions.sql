-- +goose Up
ALTER TABLE channel.tasks ADD COLUMN quote_version bigint;
CREATE TABLE channel.quote_versions (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, version bigint NOT NULL CHECK(version>0),
 content_hash text NOT NULL, draft jsonb NOT NULL,
 PRIMARY KEY(tenant_id,task_id,version),
 FOREIGN KEY(tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id)
);
CREATE TABLE channel.quote_edits (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, request_id uuid NOT NULL,
 member_id uuid NOT NULL, request_hash text NOT NULL,
 old_approval jsonb NOT NULL, result jsonb NOT NULL,
 PRIMARY KEY(tenant_id,task_id,request_id),
 FOREIGN KEY(tenant_id,task_id) REFERENCES channel.tasks(tenant_id,id)
);
ALTER TABLE channel.quote_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_versions FORCE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_edits ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_edits FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_version_scope ON channel.quote_versions USING(tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
CREATE POLICY quote_edit_scope ON channel.quote_edits USING(tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid);
GRANT SELECT,INSERT ON channel.quote_versions,channel.quote_edits TO agentos_runtime;
ALTER TABLE channel.tasks ADD FOREIGN KEY(tenant_id,id,quote_version) REFERENCES channel.quote_versions(tenant_id,task_id,version);
