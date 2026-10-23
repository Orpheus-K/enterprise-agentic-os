-- +goose Up
-- Tasks created before this migration were all received through Feishu, the only channel then implemented.
ALTER TABLE channel.tasks ADD COLUMN provider text NOT NULL DEFAULT 'feishu' CHECK (length(btrim(provider)) > 0);
ALTER TABLE channel.tasks ALTER COLUMN provider DROP DEFAULT;
