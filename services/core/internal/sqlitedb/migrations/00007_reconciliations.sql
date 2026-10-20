-- +goose Up
CREATE TABLE channel_reconciliations (
 tenant_id TEXT NOT NULL,
 app_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 expected_version INTEGER NOT NULL CHECK (expected_version>0),
 decision TEXT NOT NULL CHECK (decision IN ('unresolved','not_executed','delivered')),
 evidence_id TEXT NOT NULL,
 result_state TEXT NOT NULL,
 result_version INTEGER NOT NULL CHECK (result_version>0),
 stage TEXT NOT NULL,
 trace_id TEXT NOT NULL,
 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(tenant_id,app_id,task_id,request_id),
 FOREIGN KEY(tenant_id,task_id) REFERENCES channel_tasks(tenant_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
