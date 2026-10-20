//go:build integration

package knowledge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

func TestKnowledgeAuditGrantAndUsage(t *testing.T) {
	f := setup(t)
	f.publish(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f.read(t, true)
	}
	f.search(t, f.reader, space, 10)
	f.acl.Revision++
	f.acl.Allow = nil
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	f.read(t, false)
	if got := f.search(t, f.reader, space, 5); len(got) != 0 {
		t.Fatal("denied search returned content")
	}
	w := AuditWindow{From: time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond), To: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)}
	if _, err := f.store.Usage(t.Context(), f.reader, w); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("implicit reader audit authorization", err)
	}
	if err := f.store.within(t.Context(), f.reader, func(q database.Queries) error {
		var n int
		err := q.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM knowledge.audit)+(SELECT count(*) FROM knowledge.search_audit)`).Scan(&n)
		if n != 0 {
			t.Error("writer's old policy leaked own audit rows")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pg.Admin.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO iam.operation_grants VALUES($1,$2,'app','audit.read',true)`, f.reader.TenantID, f.readerID)
	if _, err := f.store.Usage(t.Context(), f.reader, w); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("task audit grant authorized knowledge", err)
	}
	exec(`INSERT INTO iam.operation_grants VALUES($1,$2,'app','knowledge.audit.read',true)`, f.reader.TenantID, f.readerID)
	got, err := f.store.Usage(t.Context(), f.reader, w)
	if err != nil || got.ReadChecks != 2 || got.Searches != 2 || got.EmptySearches != 1 || got.RequestedCandidates != 15 || got.ReturnedCandidates != 2 {
		t.Fatalf("usage %+v: %v", got, err)
	}
	f.read(t, false) // Metadata permission never grants content access.
	page, err := f.store.ReadAudit(t.Context(), f.reader, AuditQuery{AuditWindow: w, Kind: "searches", Limit: 1})
	if err != nil || len(page.Events) != 1 || page.NextAfter == nil {
		t.Fatal("search page", err)
	}
	next, err := f.store.ReadAudit(t.Context(), f.reader, AuditQuery{AuditWindow: w, Kind: "searches", Limit: 1, After: *page.NextAfter})
	if err != nil || len(next.Events) != 1 || next.NextAfter != nil || next.Events[0].ID == page.Events[0].ID {
		t.Fatal("search cursor", err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Synthetic evidence", "Restricted evidence", "member_id", "embedding"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("audit leaked body or identity")
		}
	}
	f.count(t, `SELECT count(*) FROM knowledge.audit_reads`, 3)
	exec(`ALTER TABLE knowledge.audit_reads ADD CONSTRAINT synthetic_read_failure CHECK(false) NOT VALID`)
	if out, err := f.store.Usage(t.Context(), f.reader, w); err == nil || out != (UsageStats{}) {
		t.Fatal("statistics released despite audit failure", err)
	}
	if out, err := f.store.ReadAudit(t.Context(), f.reader, AuditQuery{AuditWindow: w, Kind: "events", Limit: 5}); err == nil || out.Events != nil {
		t.Fatal("page released despite audit failure", err)
	}
	exec(`ALTER TABLE knowledge.audit_reads DROP CONSTRAINT synthetic_read_failure`)
	exec(`UPDATE iam.operation_grants SET enabled=false WHERE capability='knowledge.audit.read'`)
	if _, err := f.store.Usage(t.Context(), f.reader, w); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked grant retained", err)
	}
	exec(`UPDATE iam.operation_grants SET enabled=true WHERE capability='knowledge.audit.read'`)
	exec(`UPDATE iam.identity_links SET enabled=false WHERE tenant_id=$1 AND member_id=$2`, f.reader.TenantID, f.readerID)
	if _, err := f.store.Usage(t.Context(), f.reader, w); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("revoked identity retained", err)
	}
	exec(`UPDATE iam.identity_links SET enabled=true WHERE tenant_id=$1 AND member_id=$2`, f.reader.TenantID, f.readerID)
	if err := f.store.auditWithin(t.Context(), f.reader, func(q database.Queries) error {
		exec(`UPDATE iam.identity_links SET enabled=false WHERE tenant_id=$1 AND member_id=$2`, f.reader.TenantID, f.readerID)
		var n int
		if err := q.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM knowledge.audit)+(SELECT count(*) FROM knowledge.search_audit)`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Error("raw RLS retained revoked identity")
		}
		return nil
	}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("report gate did not revalidate identity", err)
	}
}

func TestKnowledgeReportWindowAndScope(t *testing.T) {
	f := setup(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := AuditWindow{From: from, To: from.Add(time.Hour)}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pg.Admin.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO iam.operation_grants VALUES($1,$2,'app','knowledge.audit.read',true)`, f.reader.TenantID, f.readerID)
	foreignID := uuid.New()
	for _, row := range []struct {
		id, tenant uuid.UUID
		app        string
		at         time.Time
	}{
		{uuid.New(), f.reader.TenantID, "app", w.From},
		{uuid.New(), f.reader.TenantID, "app", w.To.Add(-time.Microsecond)},
		{uuid.New(), f.reader.TenantID, "app", w.To},
		{foreignID, f.reader.TenantID, "other-app", w.From},
		{uuid.New(), uuid.New(), "app", w.From},
	} {
		exec(`INSERT INTO knowledge.audit VALUES($1,$2,$3,$4,'synthetic',$5,'read',1,$6)`, row.id, row.tenant, row.app, f.source, f.readerID, row.at)
	}
	got, err := f.store.Usage(t.Context(), f.reader, w)
	if err != nil || got.ReadChecks != 2 || got.Searches != 0 {
		t.Fatal("window or scope leaked", got, err)
	}
	page, err := f.store.ReadAudit(t.Context(), f.reader, AuditQuery{AuditWindow: w, Kind: "events", Limit: 10})
	if err != nil || len(page.Events) != 2 {
		t.Fatal("event scope", err)
	}
	page, err = f.store.ReadAudit(t.Context(), f.reader, AuditQuery{AuditWindow: w, Kind: "events", Limit: 10, After: foreignID})
	if err != nil || len(page.Events) != 0 {
		t.Fatal("foreign cursor probed rows", err)
	}
	foreign := f.reader
	foreign.TenantID = uuid.New()
	if _, err := f.store.Usage(t.Context(), foreign, w); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("cross-tenant actor accepted", err)
	}
	for _, invalid := range []AuditWindow{{}, {From: w.To, To: w.From}, {From: w.From, To: w.From.Add(93 * 24 * time.Hour)}, {From: w.From.Add(time.Nanosecond), To: w.To}} {
		if _, err := f.store.Usage(t.Context(), f.reader, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid window accepted", err)
		}
	}
}
