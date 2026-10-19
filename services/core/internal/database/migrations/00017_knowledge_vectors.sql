-- +goose Up
-- The extension is installed by a database administrator (deploy/postgres/vector.sql);
-- the migrator never gains superuser rights and a missing or different driver fails here.
-- +goose StatementBegin
DO $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
  WHERE e.extname='vector' AND e.extversion='0.8.7' AND n.nspname='public'
 ) THEN
  RAISE EXCEPTION 'pgvector 0.8.7 must be installed in schema public by an administrator before schema 17';
 END IF;
END $$;
-- +goose StatementEnd

-- Administrators provision embedding spaces; runtime can only read enabled ones.
-- dimensions is immutable while vectors reference it (composite foreign key below).
CREATE TABLE knowledge.embedding_spaces (
 tenant_id uuid NOT NULL, app_id text NOT NULL, id uuid NOT NULL,
 model text NOT NULL CHECK(model<>'' AND length(model)<=256),
 model_revision text NOT NULL CHECK(model_revision<>'' AND length(model_revision)<=256),
 dimensions integer NOT NULL CHECK(dimensions BETWEEN 1 AND 4096),
 enabled boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant_id,app_id,id),
 UNIQUE(tenant_id,app_id,id,dimensions),
 FOREIGN KEY(tenant_id) REFERENCES iam.tenants(id)
);
ALTER TABLE knowledge.embedding_spaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.embedding_spaces FORCE ROW LEVEL SECURITY;
CREATE POLICY space_scope ON knowledge.embedding_spaces FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
 AND app_id=current_setting('agentos.app_id',true) AND enabled
);
GRANT SELECT ON knowledge.embedding_spaces TO agentos_runtime;
-- A different model, revision or dimension is a new space; existing vectors are never reinterpreted.
-- +goose StatementBegin
CREATE FUNCTION knowledge.freeze_embedding_space() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.tenant_id,NEW.app_id,NEW.id,NEW.model,NEW.model_revision,NEW.dimensions) IS DISTINCT FROM (OLD.tenant_id,OLD.app_id,OLD.id,OLD.model,OLD.model_revision,OLD.dimensions) THEN
  RAISE EXCEPTION 'embedding space identity is immutable; create a new space';
 END IF;
 RETURN NEW;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION knowledge.freeze_embedding_space() FROM PUBLIC;
CREATE TRIGGER freeze_space BEFORE UPDATE ON knowledge.embedding_spaces FOR EACH ROW EXECUTE FUNCTION knowledge.freeze_embedding_space();

-- One complete vector set per document, space and content revision.
CREATE TABLE knowledge.vector_sets (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, document_id text NOT NULL, space_id uuid NOT NULL,
 content_revision bigint NOT NULL CHECK(content_revision>0), request_hash text NOT NULL,
 PRIMARY KEY(tenant_id,app_id,source_id,document_id,space_id),
 FOREIGN KEY(tenant_id,app_id,source_id,document_id) REFERENCES knowledge.documents(tenant_id,app_id,source_id,id),
 FOREIGN KEY(tenant_id,app_id,space_id) REFERENCES knowledge.embedding_spaces(tenant_id,app_id,id)
);
ALTER TABLE knowledge.vector_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.vector_sets FORCE ROW LEVEL SECURITY;
CREATE POLICY vector_set_read ON knowledge.vector_sets FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.documents d WHERE (d.tenant_id,d.app_id,d.source_id,d.id)=(vector_sets.tenant_id,vector_sets.app_id,vector_sets.source_id,vector_sets.document_id))
);
CREATE POLICY vector_set_sync ON knowledge.vector_sets FOR ALL USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,UPDATE ON knowledge.vector_sets TO agentos_runtime;

-- Replacing chunks for a new content revision cascades here, so stale vectors cannot survive.
CREATE TABLE knowledge.chunk_vectors (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL, document_id text NOT NULL, chunk_id text NOT NULL,
 space_id uuid NOT NULL, dimensions integer NOT NULL, content_revision bigint NOT NULL CHECK(content_revision>0),
 embedding public.vector NOT NULL CHECK(public.vector_dims(embedding)=dimensions),
 PRIMARY KEY(tenant_id,app_id,source_id,document_id,space_id,chunk_id),
 FOREIGN KEY(tenant_id,app_id,source_id,document_id,chunk_id) REFERENCES knowledge.chunks(tenant_id,app_id,source_id,document_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,app_id,source_id,document_id,space_id) REFERENCES knowledge.vector_sets(tenant_id,app_id,source_id,document_id,space_id),
 FOREIGN KEY(tenant_id,app_id,space_id,dimensions) REFERENCES knowledge.embedding_spaces(tenant_id,app_id,id,dimensions)
);
CREATE INDEX chunk_vectors_space ON knowledge.chunk_vectors(tenant_id,app_id,space_id);
ALTER TABLE knowledge.chunk_vectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.chunk_vectors FORCE ROW LEVEL SECURITY;
CREATE POLICY chunk_vector_read ON knowledge.chunk_vectors FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.chunks c WHERE (c.tenant_id,c.app_id,c.source_id,c.document_id,c.id)=(chunk_vectors.tenant_id,chunk_vectors.app_id,chunk_vectors.source_id,chunk_vectors.document_id,chunk_vectors.chunk_id)
  AND c.content_revision=chunk_vectors.content_revision)
);
CREATE POLICY chunk_vector_sync ON knowledge.chunk_vectors FOR ALL USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,DELETE ON knowledge.chunk_vectors TO agentos_runtime;

-- Search spans sources, so it is audited separately; query vectors and candidate text are never stored.
CREATE TABLE knowledge.search_audit (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL, app_id text NOT NULL, space_id uuid NOT NULL,
 member_id uuid NOT NULL, requested integer NOT NULL, returned integer NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE knowledge.search_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.search_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY search_audit_scope ON knowledge.search_audit USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
);
GRANT INSERT ON knowledge.search_audit TO agentos_runtime;
