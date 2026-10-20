-- +goose Up
ALTER TABLE channel.oauth_audit ADD COLUMN evidence_hash text NOT NULL DEFAULT '';
ALTER TABLE channel.oauth_audit DROP CONSTRAINT oauth_audit_action_check;
ALTER TABLE channel.oauth_audit ADD CONSTRAINT oauth_audit_action_check
 CHECK(action IN ('authorize','exchange','activate','refresh','revoke','revoked','reconciled'));
ALTER TABLE channel.oauth_audit ADD CONSTRAINT oauth_reconciliation_evidence
 CHECK((action='reconciled' AND evidence_hash ~ '^[0-9a-f]{64}$')
 OR (action<>'reconciled' AND evidence_hash=''));
