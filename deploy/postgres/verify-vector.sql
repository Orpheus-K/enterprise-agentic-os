-- Driver certification only: disposable fixture, no product tables or customer data.
-- psql -v ON_ERROR_STOP=1 -f deploy/postgres/verify-vector.sql
BEGIN;
DO $$
BEGIN
 IF current_setting('server_version_num')<>'170011' THEN
   RAISE EXCEPTION 'driver certification requires PostgreSQL 17.11';
 END IF;
END $$;
SET LOCAL ROLE agentos_migrator;
CREATE SCHEMA vector_probe;
CREATE TABLE vector_probe.items (
 id integer PRIMARY KEY, tenant text NOT NULL, allowed boolean NOT NULL,
 embedding public.vector(3) NOT NULL
);
INSERT INTO vector_probe.items VALUES
 (1,'a',false,'[1,0,0]'), (2,'b',true,'[1,0,0]'),
 (3,'a',true,'[0.9,0.1,0]'), (4,'a',true,'[0,1,0]');
ALTER TABLE vector_probe.items ENABLE ROW LEVEL SECURITY;
ALTER TABLE vector_probe.items FORCE ROW LEVEL SECURITY;
CREATE POLICY scope ON vector_probe.items USING (
 tenant=current_setting('agentos.tenant_id',true) AND allowed
);
GRANT USAGE ON SCHEMA vector_probe TO agentos_runtime;
GRANT SELECT ON vector_probe.items TO agentos_runtime;
SET LOCAL ROLE agentos_runtime;
SET LOCAL agentos.tenant_id='a';
DO $$
DECLARE ids integer[];
BEGIN
 SELECT array_agg(id ORDER BY distance,id) INTO ids FROM (
   SELECT id,embedding <=> '[1,0,0]'::public.vector AS distance
   FROM vector_probe.items ORDER BY distance,id LIMIT 2
 ) candidates;
 IF ids IS DISTINCT FROM ARRAY[3,4] THEN
   RAISE EXCEPTION 'exact retrieval lost authorized candidates or returned denied rows';
 END IF;
 IF (SELECT count(*) FROM vector_probe.items)<>2 THEN
   RAISE EXCEPTION 'count crossed row authorization';
 END IF;
END $$;
SET LOCAL agentos.tenant_id='absent';
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM vector_probe.items) THEN
   RAISE EXCEPTION 'tenant boundary failed';
 END IF;
END $$;
ROLLBACK;
