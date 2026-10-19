-- +goose Up
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA iam;
REVOKE ALL ON SCHEMA iam FROM PUBLIC;

CREATE TABLE iam.tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    enabled boolean NOT NULL DEFAULT true
);
CREATE TABLE iam.members (
    tenant_id uuid NOT NULL REFERENCES iam.tenants(id),
    id uuid NOT NULL,
    employee_id text NOT NULL CHECK (length(btrim(employee_id)) > 0),
    enabled boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, employee_id)
);
CREATE TABLE iam.identity_links (
    tenant_id uuid NOT NULL,
    provider text NOT NULL CHECK (length(btrim(provider)) > 0),
    issuer text NOT NULL CHECK (length(btrim(issuer)) > 0),
    subject text NOT NULL CHECK (length(btrim(subject)) > 0),
    member_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, provider, issuer, subject),
    FOREIGN KEY (tenant_id, member_id) REFERENCES iam.members(tenant_id, id)
);

ALTER TABLE iam.tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE iam.members ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.members FORCE ROW LEVEL SECURITY;
ALTER TABLE iam.identity_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE iam.identity_links FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_visibility ON iam.tenants
    USING (id = nullif(current_setting('agentos.tenant_id', true), '')::uuid AND enabled);
CREATE POLICY member_visibility ON iam.members
    USING (
        tenant_id = nullif(current_setting('agentos.tenant_id', true), '')::uuid
        AND id = nullif(current_setting('agentos.member_id', true), '')::uuid
        AND enabled
        AND EXISTS (SELECT 1 FROM iam.tenants WHERE id = tenant_id)
    );
CREATE POLICY identity_visibility ON iam.identity_links
    USING (
        tenant_id = nullif(current_setting('agentos.tenant_id', true), '')::uuid
        AND provider = current_setting('agentos.provider', true)
        AND issuer = current_setting('agentos.issuer', true)
        AND subject = current_setting('agentos.subject', true)
        AND enabled
        AND EXISTS (SELECT 1 FROM iam.tenants WHERE id = tenant_id)
    );

GRANT USAGE ON SCHEMA iam TO agentos_runtime;
GRANT SELECT ON iam.tenants, iam.members, iam.identity_links TO agentos_runtime;
GRANT SELECT ON public.goose_db_version TO agentos_runtime;
