-- +goose Up
-- NULL means no retrieval checkpoint; [] means retrieval completed without evidence.
-- Bodies and anchors are deliberately excluded: consumers must re-read current authorization.
ALTER TABLE channel.tasks ADD COLUMN knowledge_refs jsonb;
ALTER TABLE channel.tasks ADD CONSTRAINT task_knowledge_refs_shape CHECK (
  knowledge_refs IS NULL OR
  CASE WHEN jsonb_typeof(knowledge_refs)='array'
    THEN jsonb_array_length(knowledge_refs)<=50
    ELSE false
  END
);
