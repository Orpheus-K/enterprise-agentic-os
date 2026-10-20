-- +goose Up
ALTER TABLE iam.operation_grants DROP CONSTRAINT operation_grants_capability_check;
ALTER TABLE iam.operation_grants ADD CONSTRAINT operation_grants_capability_check
 CHECK(capability IN ('audit.read','status.read','task.reconcile','task.approve','knowledge.reconcile','knowledge.audit.read','quotation.publish','quotation.seal'));

CREATE TABLE channel.quote_documents (
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, version bigint NOT NULL,
 content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
 renderer_hash text NOT NULL CHECK(renderer_hash ~ '^[0-9a-f]{64}$'),
 attempt_id uuid NOT NULL, member_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('reserved','ready')),
 object_ref text, pdf_hash text, pdf_bytes bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,task_id,version), UNIQUE(tenant_id,attempt_id),
 FOREIGN KEY(tenant_id,task_id,version) REFERENCES channel.quote_versions(tenant_id,task_id,version),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id),
 CHECK((state='reserved' AND object_ref IS NULL AND pdf_hash IS NULL AND pdf_bytes IS NULL)
 OR (state='ready' AND length(object_ref) BETWEEN 1 AND 256 AND pdf_hash ~ '^[0-9a-f]{64}$' AND pdf_bytes BETWEEN 1 AND 8388608))
);
ALTER TABLE channel.quote_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_document_scope ON channel.quote_documents USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND EXISTS(SELECT 1 FROM iam.operation_grants g WHERE g.tenant_id=quote_documents.tenant_id
 AND g.member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND g.app_id=current_setting('agentos.app_id',true) AND g.capability='quotation.publish' AND g.enabled)
 AND EXISTS(SELECT 1 FROM channel.tasks t WHERE t.tenant_id=quote_documents.tenant_id
 AND t.id=quote_documents.task_id AND t.app_id=current_setting('agentos.app_id',true))
);
GRANT SELECT,INSERT,UPDATE ON channel.quote_documents TO agentos_runtime;

CREATE TABLE channel.quote_document_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id uuid NOT NULL, task_id uuid NOT NULL, version bigint NOT NULL,
 attempt_id uuid NOT NULL, member_id uuid NOT NULL,
 event text NOT NULL CHECK(event IN ('reserved','ready','read')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(tenant_id,task_id,version) REFERENCES channel.quote_documents(tenant_id,task_id,version),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.quote_document_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_document_events FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_document_event_scope ON channel.quote_document_events USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND EXISTS(SELECT 1 FROM channel.quote_documents d WHERE d.tenant_id=quote_document_events.tenant_id
 AND d.task_id=quote_document_events.task_id AND d.version=quote_document_events.version)
);
GRANT SELECT,INSERT ON channel.quote_document_events TO agentos_runtime;
GRANT USAGE ON SEQUENCE channel.quote_document_events_id_seq TO agentos_runtime;
