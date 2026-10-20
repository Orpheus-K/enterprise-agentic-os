package quotation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"os"
	"testing"
)

func fixtureRenderer(t *testing.T) *PDFRenderer {
	t.Helper()
	font, err := os.ReadFile("testdata/quote-fixture.ttf")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(font)
	renderer, err := NewPDFRenderer(font, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

func TestPDFVerifiedChineseDraft(t *testing.T) {
	renderer := fixtureRenderer(t)
	r, c, p := inputs()
	r.Customer = "合成测试公司"
	r.Items[0].SKU = "设备甲"
	c.Prices["设备甲"] = 10000
	d, err := New(uuid.MustParse("84d6558b-93b1-4c12-8fdc-ef984558bf48"), r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := renderer.Render(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("/FontFile2")) {
		t.Fatal("missing PDF or embedded font")
	}
	if output := os.Getenv("AGENTOS_QUOTE_PDF_SAMPLE"); output != "" {
		if err := os.WriteFile(output, pdf, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d.TotalMinor++
	if _, err := renderer.Render(context.Background(), d); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered quote accepted", err)
	}
}

func TestPDFFontAndCancellationFailures(t *testing.T) {
	renderer := fixtureRenderer(t)
	if _, err := NewPDFRenderer(renderer.font, "wrong"); !errors.Is(err, ErrPDFFont) {
		t.Fatal(err)
	}
	r, c, p := inputs()
	r.Customer = "missing-😀"
	d, err := New(uuid.New(), r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.Render(context.Background(), d); !errors.Is(err, ErrPDFFont) {
		t.Fatal("silently replaced missing glyph", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := renderer.Render(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
