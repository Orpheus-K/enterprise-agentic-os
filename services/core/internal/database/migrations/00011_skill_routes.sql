-- +goose Up
ALTER TABLE channel.tasks ADD COLUMN skill_routes jsonb NOT NULL DEFAULT '[]'
 CHECK(jsonb_typeof(skill_routes)='array' AND jsonb_array_length(skill_routes)<=32);
