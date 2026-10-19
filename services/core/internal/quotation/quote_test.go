package quotation

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func inputs() (Request, Catalogue, Policy) {
	return Request{Customer: "synthetic-customer", Evidence: []string{"synthetic-minute-v1"}, Items: []Item{{SKU: "A", Quantity: 2, UnitMinor: 7500}}},
		Catalogue{Revision: strings.Repeat("a", 64), Currency: "CNY", Basis: "net_no_fees", Prices: map[string]int64{"A": 10000, "B": 1000}},
		Policy{Revision: strings.Repeat("b", 64), Currency: "CNY", Basis: "net_no_fees", ApprovalBelowBPS: 8000}
}

func TestExactWeightedApprovalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		price    int64
		approval bool
	}{{"seventy_five", 7500, true}, {"below", 7999, true}, {"exact", 8000, false}, {"above", 8001, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r, c, p := inputs()
			r.Items[0].UnitMinor = tc.price
			d, err := New(uuid.New(), r, c, p)
			if err != nil {
				t.Fatal(err)
			}
			if d.StandardTotalMinor != 20000 || d.TotalMinor != 2*tc.price || d.RequiresApproval != tc.approval || Verify(d) != nil {
				t.Fatal("wrong exact total or approval")
			}
		})
	}
	r, c, p := inputs()
	r.Items = []Item{{SKU: "A", Quantity: 1, UnitMinor: 7000}, {SKU: "B", Quantity: 1, UnitMinor: 1000}}
	d, err := New(uuid.New(), r, c, p)
	if err != nil || d.TotalMinor != 8000 || d.StandardTotalMinor != 11000 || !d.RequiresApproval {
		t.Fatal("used average of line discounts instead of weighted totals", err)
	}
	// 4/5 equals the threshold; 3/4 is below it without any decimal conversion.
	c.Prices["A"] = 5
	r.Items = []Item{{SKU: "A", Quantity: 1, UnitMinor: 4}}
	d, err = New(uuid.New(), r, c, p)
	if err != nil || d.RequiresApproval {
		t.Fatal("small amount threshold", err)
	}
	c.Prices["A"] = MaxMinor
	r.Items[0].UnitMinor = MaxMinor*4/5 - 1
	d, err = New(uuid.New(), r, c, p)
	if err != nil || !d.RequiresApproval {
		t.Fatal("large total ratio overflowed or rounded", err)
	}
}

func TestDraftRevisionRecalculatesAndPreservesPrior(t *testing.T) {
	r, c, p := inputs()
	old, err := New(uuid.New(), r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	next, err := Revise(old, old.Hash, []PriceChange{{SKU: "A", UnitMinor: 8500}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != 2 || next.PreviousHash != old.Hash || next.Hash == old.Hash || next.RequiresApproval || next.TotalMinor != 17000 || !old.RequiresApproval || old.TotalMinor != 15000 {
		t.Fatal("revision inherited old calculation or mutated prior")
	}
	if Verify(next) != nil || Verify(old) != nil {
		t.Fatal("invalid revision hash")
	}
	if _, err = Revise(next, old.Hash, []PriceChange{{SKU: "A", UnitMinor: 9000}}); !errors.Is(err, ErrVersion) {
		t.Fatal("old approval/version accepted", err)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	var restored Draft
	if json.Unmarshal(raw, &restored) != nil || Verify(restored) != nil {
		t.Fatal("serialized version cannot be recomputed")
	}
	restored.Lines[0].TotalMinor++
	if Verify(restored) == nil {
		t.Fatal("tampered line accepted")
	}
	next.Policy.ApprovalBelowBPS = 9000
	if Verify(next) == nil {
		t.Fatal("policy mutation accepted")
	}
}

func TestDraftRejectsUndefinedPricingAndOverflow(t *testing.T) {
	cases := map[string]func(*Request, *Catalogue, *Policy){
		"missing_standard":     func(r *Request, c *Catalogue, p *Policy) { delete(c.Prices, "A") },
		"zero_standard":        func(r *Request, c *Catalogue, p *Policy) { c.Prices["A"] = 0 },
		"zero_offer":           func(r *Request, c *Catalogue, p *Policy) { r.Items[0].UnitMinor = 0 },
		"negative":             func(r *Request, c *Catalogue, p *Policy) { r.Items[0].Quantity = -1 },
		"above_standard":       func(r *Request, c *Catalogue, p *Policy) { r.Items[0].UnitMinor = 10001 },
		"currency":             func(r *Request, c *Catalogue, p *Policy) { c.Currency = "USD" },
		"unsupported_currency": func(r *Request, c *Catalogue, p *Policy) { c.Currency = "JPY"; p.Currency = "JPY" },
		"tax":                  func(r *Request, c *Catalogue, p *Policy) { c.Basis = "tax_inclusive" },
		"multiply_overflow":    func(r *Request, c *Catalogue, p *Policy) { c.Prices["A"] = MaxMinor },
		"sum_overflow": func(r *Request, c *Catalogue, p *Policy) {
			c.Prices["A"] = MaxMinor
			c.Prices["B"] = 1
			r.Items = []Item{{SKU: "A", Quantity: 1, UnitMinor: 1}, {SKU: "B", Quantity: 1, UnitMinor: 1}}
		},
		"duplicate_sku":     func(r *Request, c *Catalogue, p *Policy) { r.Items = append(r.Items, r.Items[0]) },
		"no_evidence":       func(r *Request, c *Catalogue, p *Policy) { r.Evidence = nil },
		"no_policy_version": func(r *Request, c *Catalogue, p *Policy) { p.Revision = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, c, p := inputs()
			change(&r, &c, &p)
			if _, err := New(uuid.New(), r, c, p); !errors.Is(err, ErrInvalid) {
				t.Fatal("undefined pricing accepted", err)
			}
		})
	}
}

func TestCanonicalHashBindsSourcesAndRejectsNoopEdits(t *testing.T) {
	r, c, p := inputs()
	r.Items = append(r.Items, Item{SKU: "B", Quantity: 1, UnitMinor: 1000})
	r.Evidence = append(r.Evidence, "other-source")
	id := uuid.New()
	d, err := New(id, r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	r.Items[0], r.Items[1] = r.Items[1], r.Items[0]
	r.Evidence[0], r.Evidence[1] = r.Evidence[1], r.Evidence[0]
	reordered, err := New(id, r, c, p)
	if err != nil || d.Hash != reordered.Hash {
		t.Fatal("unstable canonical hash", err)
	}
	c.Revision = strings.Repeat("c", 64)
	changed, err := New(id, r, c, p)
	if err != nil || changed.Hash == d.Hash {
		t.Fatal("catalogue version not bound", err)
	}
	for _, edits := range [][]PriceChange{nil, {{SKU: "missing", UnitMinor: 1}}, {{SKU: "A", UnitMinor: 7500}}, {{SKU: "A", UnitMinor: 8000}, {SKU: "A", UnitMinor: 9000}}} {
		if _, err := Revise(d, d.Hash, edits); err == nil {
			t.Fatal("invalid revision accepted")
		}
	}
	r.Evidence[0] = "changed-source"
	r.Items[0].UnitMinor = 1
	if Verify(d) != nil {
		t.Fatal("caller changed stored draft through input alias")
	}
}
