-- +goose Up
CREATE TABLE knowledge.sync_jobs (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, document_id text NOT NULL,
 request_revision bigint NOT NULL CHECK(request_revision>0),
 config_hash text NOT NULL CHECK(config_hash ~ '^[0-9a-f]{64}$'),
 state text NOT NULL CHECK(state IN ('pending','running','completed')),
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 lease_token uuid, lease_until timestamptz NOT NULL DEFAULT '-infinity',
 publication_hash text NOT NULL DEFAULT '',
 PRIMARY KEY(tenant_id,app_id,source_id,document_id),
 FOREIGN KEY(tenant_id,app_id,source_id) REFERENCES knowledge.sources(tenant_id,app_id,id),
 CHECK(state<>'running' OR lease_token IS NOT NULL)
);
ALTER TABLE knowledge.sync_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.sync_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY sync_job_scope ON knowledge.sync_jobs USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,UPDATE ON knowledge.sync_jobs TO agentos_runtime;
CREATE INDEX sync_job_claim ON knowledge.sync_jobs(tenant_id,app_id,source_id,state,lease_until);
