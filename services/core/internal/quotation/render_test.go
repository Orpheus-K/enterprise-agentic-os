package quotation

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestRenderExactDraftAndRejectTampering(t *testing.T) {
	r, c, p := inputs()
	d, err := New(uuid.New(), r, c, p)
	if err != nil {
		t.Fatal(err)
	}
	text, err := RenderText(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"报价初稿 · V1", "报价单价：75.00", "报价总额：150.00", "标准总额：200.00", d.Hash, "未用章", "触发特批"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s", part)
		}
	}
	if money(1) != "0.01" || money(MaxMinor) != "90000000000000.00" {
		t.Fatal("money formatting lost precision")
	}
	d.TotalMinor++
	if _, err = RenderText(d); err == nil {
		t.Fatal("tampered quote rendered")
	}
}
