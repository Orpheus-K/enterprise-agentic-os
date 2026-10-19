// Package quotation computes versioned drafts from an authorized price catalogue.
// It grants neither approval nor permission to publish a quote.
package quotation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrInvalid = errors.New("invalid or unsupported quotation")
var ErrVersion = errors.New("quotation version mismatch")

// Bound JSON integer amounts below the exact integer range of JavaScript.
const MaxMinor int64 = 9_000_000_000_000_000
const Calculation = "cny-minor-exact-v1"

type Policy struct {
	Revision         string `json:"revision"`
	Currency         string `json:"currency"`
	Basis            string `json:"basis"`
	ApprovalBelowBPS int64  `json:"approval_below_bps"`
}
type Catalogue struct {
	Revision string           `json:"revision"`
	Currency string           `json:"currency"`
	Basis    string           `json:"basis"`
	Prices   map[string]int64 `json:"prices"`
}
type Item struct {
	SKU       string `json:"sku"`
	Quantity  int64  `json:"quantity"`
	UnitMinor int64  `json:"unit_minor"`
}
type Request struct {
	Customer string   `json:"customer"`
	Evidence []string `json:"evidence"`
	Items    []Item   `json:"items"`
}
type Line struct {
	Item
	StandardUnitMinor  int64 `json:"standard_unit_minor"`
	StandardTotalMinor int64 `json:"standard_total_minor"`
	TotalMinor         int64 `json:"total_minor"`
}
type Draft struct {
	ID                 uuid.UUID `json:"id"`
	Version            int64     `json:"version"`
	PreviousHash       string    `json:"previous_hash"`
	Hash               string    `json:"hash"`
	Calculation        string    `json:"calculation"`
	CatalogueRevision  string    `json:"catalogue_revision"`
	Policy             Policy    `json:"policy"`
	Customer           string    `json:"customer"`
	Evidence           []string  `json:"evidence"`
	Lines              []Line    `json:"lines"`
	StandardTotalMinor int64     `json:"standard_total_minor"`
	TotalMinor         int64     `json:"total_minor"`
	RequiresApproval   bool      `json:"requires_approval"`
}

func revision(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && strings.ToLower(s) == s
}
func label(s string) bool {
	return s != "" && len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

func New(id uuid.UUID, r Request, c Catalogue, p Policy) (Draft, error) {
	if id == uuid.Nil || !revision(c.Revision) || !revision(p.Revision) || p.Currency != "CNY" || c.Currency != p.Currency || p.Basis != "net_no_fees" || c.Basis != p.Basis || p.ApprovalBelowBPS < 1 || p.ApprovalBelowBPS > 10000 || !label(r.Customer) || len(r.Items) < 1 || len(r.Items) > 100 || len(r.Evidence) < 1 || len(r.Evidence) > 32 {
		return Draft{}, ErrInvalid
	}
	d := Draft{ID: id, Version: 1, Calculation: Calculation, CatalogueRevision: c.Revision, Policy: p, Customer: r.Customer, Evidence: append([]string(nil), r.Evidence...)}
	sort.Strings(d.Evidence)
	for i, e := range d.Evidence {
		if !label(e) || (i > 0 && e == d.Evidence[i-1]) {
			return Draft{}, ErrInvalid
		}
	}
	items := append([]Item(nil), r.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].SKU < items[j].SKU })
	for i, item := range items {
		standard, exists := c.Prices[item.SKU]
		if !label(item.SKU) || (i > 0 && item.SKU == items[i-1].SKU) || !exists || standard <= 0 || standard > MaxMinor || item.Quantity < 1 || item.Quantity > 1_000_000 || item.UnitMinor <= 0 || item.UnitMinor > standard || standard > MaxMinor/item.Quantity {
			return Draft{}, ErrInvalid
		}
		line := Line{Item: item, StandardUnitMinor: standard, StandardTotalMinor: standard * item.Quantity, TotalMinor: item.UnitMinor * item.Quantity}
		if d.StandardTotalMinor > MaxMinor-line.StandardTotalMinor {
			return Draft{}, ErrInvalid
		}
		d.StandardTotalMinor += line.StandardTotalMinor
		d.TotalMinor += line.TotalMinor
		d.Lines = append(d.Lines, line)
	}
	// Compare the exact weighted ratio; never round a displayed percentage for policy.
	offered := new(big.Int).Mul(big.NewInt(d.TotalMinor), big.NewInt(10000))
	threshold := new(big.Int).Mul(big.NewInt(d.StandardTotalMinor), big.NewInt(p.ApprovalBelowBPS))
	d.RequiresApproval = offered.Cmp(threshold) < 0
	d.Hash = digest(d)
	return d, nil
}

func digest(d Draft) string {
	d.Hash = ""
	raw, _ := json.Marshal(d) // Only concrete JSON scalar fields and bounded slices.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Verify recomputes a stored draft; the hash is integrity metadata, not authorization.
func Verify(d Draft) error {
	if d.Version < 1 || d.Version > MaxMinor || (d.Version == 1 && d.PreviousHash != "") || (d.Version > 1 && !revision(d.PreviousHash)) || d.Calculation != Calculation {
		return ErrInvalid
	}
	r := Request{Customer: d.Customer, Evidence: d.Evidence}
	c := Catalogue{Revision: d.CatalogueRevision, Currency: d.Policy.Currency, Basis: d.Policy.Basis, Prices: map[string]int64{}}
	for _, line := range d.Lines {
		r.Items = append(r.Items, line.Item)
		c.Prices[line.SKU] = line.StandardUnitMinor
	}
	computed, err := New(d.ID, r, c, d.Policy)
	if err != nil {
		return err
	}
	computed.Version = d.Version
	computed.PreviousHash = d.PreviousHash
	if digest(computed) != d.Hash || digest(d) != d.Hash {
		return ErrInvalid
	}
	return nil
}

type PriceChange struct {
	SKU       string `json:"sku"`
	UnitMinor int64  `json:"unit_minor"`
}

// Revise preserves source prices and policy. New evidence/catalogues require a new draft.
func Revise(old Draft, expectedHash string, changes []PriceChange) (Draft, error) {
	if err := Verify(old); err != nil {
		return Draft{}, err
	}
	if old.Hash != expectedHash || old.Version >= MaxMinor {
		return Draft{}, ErrVersion
	}
	if len(changes) == 0 || len(changes) > len(old.Lines) {
		return Draft{}, ErrInvalid
	}
	edits := map[string]int64{}
	for _, change := range changes {
		if _, exists := edits[change.SKU]; exists {
			return Draft{}, ErrInvalid
		}
		edits[change.SKU] = change.UnitMinor
	}
	r := Request{Customer: old.Customer, Evidence: old.Evidence}
	c := Catalogue{Revision: old.CatalogueRevision, Currency: old.Policy.Currency, Basis: old.Policy.Basis, Prices: map[string]int64{}}
	changed := false
	for _, line := range old.Lines {
		item := line.Item
		c.Prices[item.SKU] = line.StandardUnitMinor
		if price, exists := edits[item.SKU]; exists {
			changed = changed || price != item.UnitMinor
			item.UnitMinor = price
			delete(edits, item.SKU)
		}
		r.Items = append(r.Items, item)
	}
	if len(edits) > 0 || !changed {
		return Draft{}, ErrInvalid
	}
	d, err := New(old.ID, r, c, old.Policy)
	if err != nil {
		return Draft{}, err
	}
	d.Version = old.Version + 1
	d.PreviousHash = old.Hash
	d.Hash = digest(d)
	return d, nil
}
