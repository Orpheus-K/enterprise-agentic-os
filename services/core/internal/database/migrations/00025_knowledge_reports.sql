-- +goose Up
ALTER TABLE iam.operation_grants DROP CONSTRAINT operation_grants_capability_check;
ALTER TABLE iam.operation_grants ADD CONSTRAINT operation_grants_capability_check
 CHECK(capability IN ('audit.read','status.read','task.reconcile','task.approve','knowledge.reconcile','knowledge.audit.read'));

CREATE FUNCTION knowledge.can_read_audit() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM iam.operation_grants g
 WHERE tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND capability='knowledge.audit.read' AND enabled
 AND EXISTS(SELECT 1 FROM iam.identity_links l WHERE l.tenant_id=g.tenant_id AND l.member_id=g.member_id))
$$;
REVOKE ALL ON FUNCTION knowledge.can_read_audit() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION knowledge.can_read_audit() TO agentos_runtime;

-- Do not retain the former ALL policy: granting SELECT would otherwise also
-- permit writers to read their own audit rows without the new capability.
DROP POLICY audit_scope ON knowledge.audit;
CREATE POLICY audit_writer ON knowledge.audit FOR INSERT WITH CHECK (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
CREATE POLICY audit_reader ON knowledge.audit FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND knowledge.can_read_audit()
);
DROP POLICY search_audit_scope ON knowledge.search_audit;
CREATE POLICY search_audit_writer ON knowledge.search_audit FOR INSERT WITH CHECK (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
CREATE POLICY search_audit_reader ON knowledge.search_audit FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND knowledge.can_read_audit()
);
GRANT SELECT ON knowledge.audit,knowledge.search_audit TO agentos_runtime;
CREATE INDEX knowledge_audit_window ON knowledge.audit(tenant_id,app_id,recorded_at,id);
CREATE INDEX knowledge_search_audit_window ON knowledge.search_audit(tenant_id,app_id,recorded_at,id);

CREATE TABLE knowledge.audit_reads (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL, app_id text NOT NULL, member_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('events','searches','stats')),
 window_from timestamptz NOT NULL, window_to timestamptz NOT NULL CHECK(window_to>window_from),
 row_count integer NOT NULL CHECK(row_count BETWEEN 0 AND 100),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE knowledge.audit_reads ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.audit_reads FORCE ROW LEVEL SECURITY;
CREATE POLICY knowledge_audit_read_trail ON knowledge.audit_reads FOR INSERT WITH CHECK (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid AND knowledge.can_read_audit()
);
GRANT INSERT ON knowledge.audit_reads TO agentos_runtime;
