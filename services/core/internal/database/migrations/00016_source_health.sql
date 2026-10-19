-- +goose Up
CREATE TABLE knowledge.source_health (
 tenant_id uuid NOT NULL, app_id text NOT NULL, source_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0), epoch bigint NOT NULL DEFAULT 0 CHECK(epoch>=0),
 state text NOT NULL DEFAULT 'unavailable' CHECK(state IN ('healthy','unavailable')),
 valid_until timestamptz NOT NULL DEFAULT '-infinity', request_hash text NOT NULL DEFAULT '',
 PRIMARY KEY(tenant_id,app_id,source_id),
 FOREIGN KEY(tenant_id,app_id,source_id) REFERENCES knowledge.sources(tenant_id,app_id,id)
);
ALTER TABLE knowledge.source_health ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge.source_health FORCE ROW LEVEL SECURITY;
CREATE POLICY health_read ON knowledge.source_health FOR SELECT USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.sources WHERE id=source_id)
);
CREATE POLICY health_write ON knowledge.source_health FOR ALL USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true) AND knowledge.can_sync(source_id)
);
GRANT SELECT,INSERT,UPDATE ON knowledge.source_health TO agentos_runtime;
-- +goose StatementBegin
CREATE FUNCTION knowledge.invalidate_disabled_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.enabled AND NOT NEW.enabled THEN
  UPDATE knowledge.source_health SET state='unavailable',epoch=epoch+1,valid_until='-infinity',request_hash=''
   WHERE tenant_id=NEW.tenant_id AND app_id=NEW.app_id AND source_id=NEW.id;
 END IF;
 RETURN NEW;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION knowledge.invalidate_disabled_source() FROM PUBLIC;
CREATE TRIGGER invalidate_source AFTER UPDATE OF enabled ON knowledge.sources FOR EACH ROW EXECUTE FUNCTION knowledge.invalidate_disabled_source();
CREATE FUNCTION knowledge.source_ready(source uuid, generation bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM knowledge.source_health WHERE source_id=source AND epoch=generation AND state='healthy' AND valid_until>clock_timestamp())
$$;
REVOKE ALL ON FUNCTION knowledge.source_ready(uuid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION knowledge.source_ready(uuid,bigint) TO agentos_runtime;
ALTER TABLE knowledge.documents ADD COLUMN source_epoch bigint NOT NULL DEFAULT 0 CHECK(source_epoch>=0);
ALTER POLICY document_read ON knowledge.documents USING (
 tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid AND app_id=current_setting('agentos.app_id',true)
 AND EXISTS(SELECT 1 FROM knowledge.sources WHERE id=source_id)
 AND knowledge.source_ready(source_id,source_epoch)
 AND content_revision>0 AND acl_revision>0 AND acl_content_revision=content_revision AND NOT deleted AND valid_until>clock_timestamp()
 AND nullif(current_setting('agentos.member_id',true),'')::uuid=ANY(allow_members)
 AND NOT nullif(current_setting('agentos.member_id',true),'')::uuid=ANY(deny_members)
);
