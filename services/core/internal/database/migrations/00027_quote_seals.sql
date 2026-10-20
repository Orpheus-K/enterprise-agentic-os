-- +goose Up
-- Policies are provisioned by administrators; runtime can never grant itself a seal.
CREATE TABLE channel.seal_policies (
 tenant_id uuid NOT NULL REFERENCES iam.tenants(id), app_id text NOT NULL,
 seal_id text NOT NULL, revision text NOT NULL CHECK(revision ~ '^[0-9a-f]{64}$'),
 policy jsonb NOT NULL, enabled boolean NOT NULL DEFAULT true,
 PRIMARY KEY(tenant_id,app_id,seal_id)
);
ALTER TABLE channel.seal_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.seal_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY seal_policy_scope ON channel.seal_policies FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM iam.operation_grants g WHERE g.tenant_id=seal_policies.tenant_id
 AND g.member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND g.app_id=seal_policies.app_id AND g.capability='quotation.seal' AND g.enabled)
);
GRANT SELECT ON channel.seal_policies TO agentos_runtime;

CREATE TABLE channel.quote_seals (
 tenant_id uuid NOT NULL, app_id text NOT NULL, task_id uuid NOT NULL, version bigint NOT NULL,
 seal_id text NOT NULL, attempt_id uuid NOT NULL, binding jsonb NOT NULL,
 binding_hash text NOT NULL CHECK(binding_hash ~ '^[0-9a-f]{64}$'),
 state text NOT NULL CHECK(state IN ('reserved','ready')),
 object_ref text, output_hash text, output_bytes bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,task_id,version,seal_id), UNIQUE(tenant_id,attempt_id),
 FOREIGN KEY(tenant_id,task_id,version) REFERENCES channel.quote_documents(tenant_id,task_id,version),
 FOREIGN KEY(tenant_id,app_id,seal_id) REFERENCES channel.seal_policies(tenant_id,app_id,seal_id),
 CHECK((state='reserved' AND object_ref IS NULL AND output_hash IS NULL AND output_bytes IS NULL)
 OR (state='ready' AND length(object_ref) BETWEEN 1 AND 256 AND output_hash ~ '^[0-9a-f]{64}$' AND output_bytes BETWEEN 1 AND 8388608))
);
ALTER TABLE channel.quote_seals ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_seals FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_seal_scope ON channel.quote_seals USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM channel.seal_policies p WHERE p.tenant_id=quote_seals.tenant_id
 AND p.app_id=quote_seals.app_id AND p.seal_id=quote_seals.seal_id AND p.enabled)
 AND EXISTS(SELECT 1 FROM channel.quote_documents d WHERE d.tenant_id=quote_seals.tenant_id
 AND d.task_id=quote_seals.task_id AND d.version=quote_seals.version)
);
GRANT SELECT,INSERT,UPDATE ON channel.quote_seals TO agentos_runtime;

CREATE TABLE channel.quote_seal_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id uuid NOT NULL, attempt_id uuid NOT NULL, member_id uuid NOT NULL,
 event text NOT NULL CHECK(event IN ('reserved','ready','read')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(tenant_id,attempt_id) REFERENCES channel.quote_seals(tenant_id,attempt_id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id)
);
ALTER TABLE channel.quote_seal_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel.quote_seal_events FORCE ROW LEVEL SECURITY;
CREATE POLICY quote_seal_event_scope ON channel.quote_seal_events USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
 AND EXISTS(SELECT 1 FROM channel.quote_seals s WHERE s.tenant_id=quote_seal_events.tenant_id
 AND s.attempt_id=quote_seal_events.attempt_id)
);
GRANT SELECT,INSERT ON channel.quote_seal_events TO agentos_runtime;
GRANT USAGE ON SEQUENCE channel.quote_seal_events_id_seq TO agentos_runtime;
