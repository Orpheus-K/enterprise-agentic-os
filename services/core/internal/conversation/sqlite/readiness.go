package sqlite

import (
	"context"
	"errors"

	"agentos.local/core/internal/sqlitedb"
)

func (s *Inbox) Ready(ctx context.Context) error {
	var ready bool
	err := s.db.QueryRowContext(ctx, `SELECT
EXISTS(SELECT 1 FROM iam_tenants WHERE id=? AND enabled=1)
AND (SELECT max(version_id) FROM goose_db_version WHERE is_applied=1)=?`,
		s.tenant.String(), sqlitedb.SchemaVersion).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("installation storage unavailable")
	}
	return nil
}
