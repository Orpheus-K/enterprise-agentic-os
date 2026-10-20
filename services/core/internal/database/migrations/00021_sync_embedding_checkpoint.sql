-- +goose Up
ALTER TABLE knowledge.sync_jobs
 ADD COLUMN embedding_pending boolean NOT NULL DEFAULT false,
 ADD COLUMN embedding_input jsonb,
 ADD COLUMN embedding_vectors jsonb;
ALTER TABLE knowledge.sync_jobs ADD CONSTRAINT sync_embedding_shape CHECK (
 (embedding_input IS NULL AND embedding_vectors IS NULL AND NOT embedding_pending)
 OR (embedding_input IS NOT NULL AND jsonb_typeof(embedding_input)='object'
     AND pg_column_size(embedding_input)<=16777216
     AND (embedding_vectors IS NULL OR
          (jsonb_typeof(embedding_vectors)='object' AND pg_column_size(embedding_vectors)<=16777216))
     AND (NOT embedding_pending OR embedding_vectors IS NULL))
);
