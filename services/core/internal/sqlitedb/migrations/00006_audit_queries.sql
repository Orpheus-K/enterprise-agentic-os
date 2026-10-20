-- +goose Up
-- Reads of nonexistent or out-of-scope tasks return empty pages and still leave
-- an access receipt. The requested task ID therefore cannot have a foreign key.
CREATE TABLE channel_audit_reads_v6 (
 id TEXT PRIMARY KEY NOT NULL,
 tenant_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 app_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 row_count INTEGER NOT NULL CHECK (row_count>=0),
 trace_id TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
INSERT INTO channel_audit_reads_v6 SELECT * FROM channel_audit_reads;
DROP TABLE channel_audit_reads;
ALTER TABLE channel_audit_reads_v6 RENAME TO channel_audit_reads;
CREATE INDEX channel_audit_task_page ON channel_audit(tenant_id,task_id,id);
