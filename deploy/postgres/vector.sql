-- Run as database administrator before vector-dependent migrations.
-- Keep extension ownership separate from the application migration/runtime roles.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public VERSION '0.8.7';
DO $$
BEGIN
 IF NOT EXISTS (
   SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
   WHERE e.extname='vector' AND e.extversion='0.8.7' AND n.nspname='public'
 ) THEN
   RAISE EXCEPTION 'required pgvector 0.8.7 in public; explicit extension upgrade required';
 END IF;
END $$;
