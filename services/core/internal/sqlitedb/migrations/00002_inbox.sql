-- +goose Up
CREATE TABLE channel_tasks (
 tenant_id TEXT NOT NULL,
 id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 app_id TEXT NOT NULL,
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 event_id TEXT NOT NULL,
 message_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 model_revision TEXT NOT NULL,
 prompt TEXT NOT NULL,
 ingress_trace_id TEXT NOT NULL DEFAULT '',
 ingress_span_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending')),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,app_id,event_id),
 UNIQUE (tenant_id,app_id,message_id),
 FOREIGN KEY (tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
CREATE TABLE channel_audit (
 id INTEGER PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 state TEXT NOT NULL,
 reason TEXT NOT NULL,
 trace_id TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 FOREIGN KEY (tenant_id,task_id) REFERENCES channel_tasks(tenant_id,id)
) STRICT;
