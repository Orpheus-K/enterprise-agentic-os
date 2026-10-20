-- +goose Up
ALTER TABLE iam.operation_grants DROP CONSTRAINT operation_grants_capability_check;
ALTER TABLE iam.operation_grants ADD CONSTRAINT operation_grants_capability_check
CHECK(capability IN ('audit.read','status.read','task.reconcile','task.approve','knowledge.reconcile'));

CREATE TABLE knowledge.sync_reconciliations (
 tenant_id uuid NOT NULL, app_id text NOT NULL, id uuid NOT NULL,
 source_id uuid NOT NULL, document_id text NOT NULL, request_revision bigint NOT NULL,
 generation bigint NOT NULL, request_hash text NOT NULL, outcome text NOT NULL,
 evidence_hash text NOT NULL, member_id uuid NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,app_id,id),
 FOREIGN KEY(tenant_id,app_id,source_id,document_id)
 REFERENCES knowledge.sync_jobs(tenant_id,app_id,source_id,document_id),
 CHECK(request_revision>0 AND generation>0),
 CHECK(outcome IN ('not_sent','completed')),
 CHECK(request_hash ~ '^[0-9a-f]{64}$' AND evidence_hash ~ '^[0-9a-f]{64}$')
);
ALTER TABLE knowledge.sync_reconciliations ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.sync_reconciliations FORCE ROW LEVEL SECURITY;
CREATE POLICY sync_reconciliation_scope ON knowledge.sync_reconciliations USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
) WITH CHECK (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
GRANT SELECT,INSERT ON knowledge.sync_reconciliations TO agentos_runtime;
