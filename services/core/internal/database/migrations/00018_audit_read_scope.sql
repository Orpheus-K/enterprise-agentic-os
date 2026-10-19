-- +goose Up
-- Installation-wide audit queries (task list, statistics) are recorded like single-task reads.
-- Existing rows are single-task reads; a task id is required exactly for that scope.
ALTER TABLE channel.audit_reads ADD COLUMN scope text NOT NULL DEFAULT 'task' CHECK(scope IN ('task','tasks','stats'));
ALTER TABLE channel.audit_reads ALTER COLUMN task_id DROP NOT NULL;
ALTER TABLE channel.audit_reads ADD CONSTRAINT audit_read_scope_shape CHECK((scope='task')=(task_id IS NOT NULL));
CREATE INDEX task_report_window ON channel.tasks(tenant_id,app_id,created_at,id);
