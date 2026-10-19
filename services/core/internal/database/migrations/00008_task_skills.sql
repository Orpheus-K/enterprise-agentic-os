-- +goose Up
ALTER TABLE channel.tasks ADD COLUMN skill_name text;
ALTER TABLE channel.tasks ADD COLUMN skill_revision text;
ALTER TABLE channel.tasks ADD CONSTRAINT task_skill_ref CHECK (
 (skill_name IS NULL AND skill_revision IS NULL) OR
 (skill_name IS NOT NULL AND skill_revision IS NOT NULL AND skill_name ~ '^[a-z][a-z0-9_-]{0,63}$' AND skill_revision ~ '^[0-9a-f]{64}$')
);
