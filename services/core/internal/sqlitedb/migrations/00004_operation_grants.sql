-- +goose Up
CREATE TABLE iam_operation_grants (
 tenant_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 app_id TEXT NOT NULL,
 capability TEXT NOT NULL CHECK (capability IN ('audit.read','status.read','task.reconcile','task.approve')),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 PRIMARY KEY (tenant_id,member_id,app_id,capability),
 FOREIGN KEY (tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
