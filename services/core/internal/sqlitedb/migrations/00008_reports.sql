-- +goose Up
CREATE TABLE channel_audit_reads_v8 (
 id TEXT PRIMARY KEY NOT NULL,
 tenant_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 app_id TEXT NOT NULL,
 task_id TEXT,
 row_count INTEGER NOT NULL CHECK(row_count>=0),
 trace_id TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 scope TEXT NOT NULL DEFAULT 'task' CHECK(scope IN ('task','tasks','stats')),
 CHECK((scope='task' AND task_id IS NOT NULL) OR (scope IN ('tasks','stats') AND task_id IS NULL)),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
INSERT INTO channel_audit_reads_v8(id,tenant_id,member_id,app_id,task_id,row_count,trace_id,recorded_at)
SELECT id,tenant_id,member_id,app_id,task_id,row_count,trace_id,recorded_at FROM channel_audit_reads;
DROP TABLE channel_audit_reads;
ALTER TABLE channel_audit_reads_v8 RENAME TO channel_audit_reads;
CREATE INDEX channel_tasks_report ON channel_tasks(tenant_id,app_id,created_at,id);
