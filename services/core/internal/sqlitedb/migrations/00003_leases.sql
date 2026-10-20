-- +goose Up
CREATE TABLE channel_tasks_v3 (
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
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','claimed','model_running','delivery_pending','delivery_running','succeeded','failed','unknown')),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 response TEXT,
 reason TEXT NOT NULL DEFAULT '',
 lease_owner TEXT,
 lease_until REAL,
 lease_generation INTEGER NOT NULL DEFAULT 0 CHECK (lease_generation>=0),
 step_version INTEGER NOT NULL DEFAULT 1 CHECK (step_version>0),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,app_id,event_id),
 UNIQUE (tenant_id,app_id,message_id),
 FOREIGN KEY (tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
CREATE TABLE channel_audit_v3 (
 id INTEGER PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 state TEXT NOT NULL,
 reason TEXT NOT NULL,
 trace_id TEXT NOT NULL DEFAULT '',
 lease_generation INTEGER NOT NULL DEFAULT 0,
 step_version INTEGER NOT NULL DEFAULT 1,
 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 FOREIGN KEY (tenant_id,task_id) REFERENCES channel_tasks_v3(tenant_id,id)
) STRICT;
INSERT INTO channel_tasks_v3(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,ingress_trace_id,ingress_span_id,state,created_at)
SELECT tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,ingress_trace_id,ingress_span_id,state,created_at FROM channel_tasks;
INSERT INTO channel_audit_v3(id,tenant_id,task_id,state,reason,trace_id,recorded_at)
SELECT id,tenant_id,task_id,state,reason,trace_id,recorded_at FROM channel_audit;
DROP TABLE channel_audit;
DROP TABLE channel_tasks;
ALTER TABLE channel_tasks_v3 RENAME TO channel_tasks;
ALTER TABLE channel_audit_v3 RENAME TO channel_audit;
CREATE UNIQUE INDEX channel_audit_step ON channel_audit(tenant_id,task_id,step_version);
CREATE INDEX channel_pending ON channel_tasks(tenant_id,app_id,model_revision,created_at,id);
