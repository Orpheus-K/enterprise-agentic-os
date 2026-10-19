-- +goose Up
CREATE TABLE iam.asset_grants (
 tenant_id uuid NOT NULL, member_id uuid NOT NULL, app_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('skill','tool')),
 name text NOT NULL CHECK(name ~ '^[a-z][a-z0-9_-]{0,63}$'),
 revision text NOT NULL CHECK(revision ~ '^[0-9a-f]{64}$'),
 enabled boolean NOT NULL DEFAULT true,
 PRIMARY KEY(tenant_id,member_id,app_id,kind,name,revision),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE iam.asset_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.asset_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_grant_scope ON iam.asset_grants USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND enabled AND EXISTS(SELECT 1 FROM iam.members WHERE tenant_id=asset_grants.tenant_id AND id=member_id)
);
GRANT SELECT ON iam.asset_grants TO agentos_runtime;
