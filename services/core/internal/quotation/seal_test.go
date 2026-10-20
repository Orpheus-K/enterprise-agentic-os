package quotation

import (
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestSealBindsExactVersionAndCurrentPolicy(t *testing.T) {
	r, c, p := inputs()
	d, err := New(uuid.New(), r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	tenant, member := uuid.New(), uuid.New()
	policy, err := NewSealPolicy(SealPolicy{TenantID: tenant, AppID: "app", SealID: "synthetic-seal", Members: []uuid.UUID{member}, MaximumMinor: d.TotalMinor, NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	pdf, evidence := strings.Repeat("a", 64), strings.Repeat("b", 64)
	binding, err := BindSeal(policy, tenant, member, "app", d, pdf, evidence, now)
	if err != nil || CheckSealBinding(policy, binding, d, pdf, now) != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*SealBinding){
		func(b *SealBinding) { b.QuoteVersion++ }, func(b *SealBinding) { b.PDFHash = strings.Repeat("c", 64) },
		func(b *SealBinding) { b.SealID = "another" }, func(b *SealBinding) { b.MemberID = uuid.New() },
		func(b *SealBinding) { b.QuoteHash = strings.Repeat("c", 64) }, func(b *SealBinding) { b.ExpiresAt = b.ExpiresAt.Add(time.Second) },
	} {
		other := binding
		edit(&other)
		if CheckSealBinding(policy, other, d, pdf, now) == nil {
			t.Fatal("changed binding accepted")
		}
	}
	revised, err := Revise(d, d.Hash, []PriceChange{{SKU: "A", UnitMinor: 8500}})
	if err != nil {
		t.Fatal(err)
	}
	if CheckSealBinding(policy, binding, revised, pdf, now) == nil {
		t.Fatal("old seal authorization accepted new quote")
	}
	if CheckSealBinding(policy, binding, d, pdf, policy.ExpiresAt) == nil {
		t.Fatal("expiry accepted")
	}
	policy.MaximumMinor++
	if CheckSealBinding(policy, binding, d, pdf, now) == nil {
		t.Fatal("mutated policy accepted")
	}
	policy.MaximumMinor = d.TotalMinor - 1
	limited, err := NewSealPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindSeal(limited, tenant, member, "app", d, pdf, evidence, now); err == nil {
		t.Fatal("amount limit bypassed")
	}
	policy.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewSealPolicy(policy); err == nil {
		t.Fatal("unencodable policy accepted")
	}
}
