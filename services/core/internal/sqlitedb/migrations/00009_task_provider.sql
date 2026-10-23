-- +goose Up
-- Tasks created before this migration were all received through Feishu, the only channel then implemented.
-- SQLite cannot drop a column default; the runtime inbox always writes the provider explicitly.
ALTER TABLE channel_tasks ADD COLUMN provider TEXT NOT NULL DEFAULT 'feishu' CHECK (length(trim(provider)) > 0);
