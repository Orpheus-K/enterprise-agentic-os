-- +goose Up
CREATE SCHEMA knowledge;
REVOKE ALL ON SCHEMA knowledge FROM PUBLIC;
GRANT USAGE ON SCHEMA knowledge TO agentos_runtime;

CREATE TABLE knowledge.sources (
 tenant_id uuid NOT NULL, app_id text NOT NULL, id uuid NOT NULL, sync_member uuid NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant_id,app_id,id),
 FOREIGN KEY(tenant_id,sync_member) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE knowledge.sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.sources FORCE ROW LEVEL SECURITY;
CREATE POLICY source_scope ON knowledge.sources USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND enabled
 AND EXISTS(SELECT 1 FROM iam.members WHERE tenant_id=sources.tenant_id)
 AND EXISTS(SELECT 1 FROM iam.identity_links WHERE tenant_id=sources.tenant_id AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid)
);
GRANT SELECT ON knowledge.sources TO agentos_runtime;

CREATE FUNCTION knowledge.can_sync(source uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM knowledge.sources WHERE id=source AND sync_member=nullif(current_setting('agentos.member_id',true),'')::uuid)
$$;
REVOKE ALL ON FUNCTION knowledge.can_sync(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION knowledge.can_sync(uuid) TO agentos_runtime;

CREATE TABLE knowledge.documents (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, id text NOT NULL,
 content_revision bigint NOT NULL DEFAULT 0 CHECK(content_revision>=0), content_hash text NOT NULL DEFAULT '', title text NOT NULL DEFAULT '', deleted boolean NOT NULL DEFAULT false,
 acl_revision bigint NOT NULL DEFAULT 0 CHECK(acl_revision>=0), acl_hash text NOT NULL DEFAULT '', acl_content_revision bigint NOT NULL DEFAULT 0,
 allow_members uuid[] NOT NULL DEFAULT '{}', deny_members uuid[] NOT NULL DEFAULT '{}',
 chunk_acl jsonb NOT NULL DEFAULT '{}', valid_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(tenant_id,app_id,source_id,id),
 FOREIGN KEY(tenant_id,app_id,source_id) REFERENCES knowledge.sources(tenant_id,app_id,id)
);
ALTER TABLE knowledge.documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.documents FORCE ROW LEVEL SECURITY;
CREATE POLICY document_read ON knowledge.documents FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.sources WHERE id=source_id)
 AND content_revision>0 AND acl_revision>0 AND acl_content_revision=content_revision AND NOT deleted AND valid_until>clock_timestamp()
 AND nullif(current_setting('agentos.member_id',true),'')::uuid=ANY(allow_members)
 AND NOT nullif(current_setting('agentos.member_id',true),'')::uuid=ANY(deny_members)
);
CREATE POLICY document_sync ON knowledge.documents FOR ALL USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,UPDATE ON knowledge.documents TO agentos_runtime;

CREATE TABLE knowledge.chunks (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, document_id text NOT NULL,
 id text NOT NULL, content_revision bigint NOT NULL, body text NOT NULL, anchor text NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,document_id,id),
 FOREIGN KEY(tenant_id,app_id,source_id,document_id) REFERENCES knowledge.documents(tenant_id,app_id,source_id,id)
);
ALTER TABLE knowledge.chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY chunk_read ON knowledge.chunks FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.documents d WHERE (d.tenant_id,d.app_id,d.source_id,d.id)=(chunks.tenant_id,chunks.app_id,chunks.source_id,chunks.document_id)
 AND d.content_revision=chunks.content_revision
 AND coalesce((d.chunk_acl->chunks.id->'allow') ? current_setting('agentos.member_id',true),true)
 AND NOT coalesce((d.chunk_acl->chunks.id->'deny') ? current_setting('agentos.member_id',true),false))
);
CREATE POLICY chunk_sync ON knowledge.chunks FOR ALL USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,DELETE ON knowledge.chunks TO agentos_runtime;

CREATE TABLE knowledge.audit (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, document_id text NOT NULL,
 member_id uuid NOT NULL, action text NOT NULL, revision bigint NOT NULL, recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE knowledge.audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.audit FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_scope ON knowledge.audit USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
GRANT INSERT ON knowledge.audit TO agentos_runtime;
