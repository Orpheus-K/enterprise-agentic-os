-- +goose Up
CREATE TABLE channel.oauth_credentials (
 tenant_id uuid NOT NULL, app_id text NOT NULL, tenant_key text NOT NULL, open_id text NOT NULL,
 member_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 key_id text NOT NULL CHECK(key_id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 ciphertext bytea NOT NULL CHECK(octet_length(ciphertext) BETWEEN 29 AND 65564),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,app_id,tenant_key,open_id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.oauth_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.oauth_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_owner ON channel.oauth_credentials USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND current_setting('agentos.provider',true)='feishu'
 AND current_setting('agentos.issuer',true)='feishu:'||app_id||':'||tenant_key
 AND open_id=current_setting('agentos.subject',true)
 AND EXISTS(SELECT 1 FROM iam.members m WHERE m.tenant_id=oauth_credentials.tenant_id AND m.id=oauth_credentials.member_id)
 AND EXISTS(SELECT 1 FROM iam.identity_links l WHERE l.tenant_id=oauth_credentials.tenant_id AND l.member_id=oauth_credentials.member_id)
);
GRANT SELECT,INSERT,UPDATE ON channel.oauth_credentials TO agentos_runtime;

CREATE TABLE channel.oauth_audit (
 tenant_id uuid NOT NULL, app_id text NOT NULL, tenant_key text NOT NULL, open_id text NOT NULL,
 member_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 action text NOT NULL CHECK(action IN ('authorize','exchange','activate','refresh','revoke','revoked')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,app_id,tenant_key,open_id,revision),
 FOREIGN KEY(tenant_id,app_id,tenant_key,open_id) REFERENCES channel.oauth_credentials(tenant_id,app_id,tenant_key,open_id)
);
ALTER TABLE channel.oauth_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.oauth_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_audit_owner ON channel.oauth_audit USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND EXISTS(SELECT 1 FROM channel.oauth_credentials c WHERE (c.tenant_id,c.app_id,c.tenant_key,c.open_id)=(oauth_audit.tenant_id,oauth_audit.app_id,oauth_audit.tenant_key,oauth_audit.open_id))
);
GRANT SELECT,INSERT ON channel.oauth_audit TO agentos_runtime;
