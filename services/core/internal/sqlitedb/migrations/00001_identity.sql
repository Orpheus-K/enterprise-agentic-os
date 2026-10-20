-- +goose Up
CREATE TABLE iam_tenants (
 id TEXT PRIMARY KEY NOT NULL,
 name TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1))
) STRICT;
CREATE TABLE iam_members (
 tenant_id TEXT NOT NULL REFERENCES iam_tenants(id),
 id TEXT NOT NULL,
 employee_id TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,employee_id)
) STRICT;
CREATE TABLE iam_identity_links (
 tenant_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 member_id TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 PRIMARY KEY (tenant_id,provider,issuer,subject),
 FOREIGN KEY (tenant_id,member_id) REFERENCES iam_members(tenant_id,id)
) STRICT;
