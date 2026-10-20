package quotation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/signintech/gopdf"
)

const PDFLayout = "quotation-pdf-v1"

var ErrPDFFont = errors.New("quotation PDF font missing, mismatched or incomplete")
var ErrPDFLimit = errors.New("quotation PDF exceeds resource limit")

// PDFRenderer renders verified data, but does not grant publication or seal authority.
// A trusted caller supplies licensed TTF bytes pinned by SHA-256. Fonts are embedded.
type PDFRenderer struct {
	font     []byte
	revision string
}

func NewPDFRenderer(font []byte, expectedSHA256 string) (*PDFRenderer, error) {
	sum := sha256.Sum256(font)
	if len(font) == 0 || len(font) > 32<<20 || hex.EncodeToString(sum[:]) != expectedSHA256 {
		return nil, ErrPDFFont
	}
	binding := sha256.Sum256([]byte(PDFLayout + ":" + expectedSHA256))
	return &PDFRenderer{font: bytes.Clone(font), revision: hex.EncodeToString(binding[:])}, nil
}

func (r *PDFRenderer) Revision() string { return r.revision }

func (r *PDFRenderer) Render(ctx context.Context, d Draft) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Verify(d); err != nil {
		return nil, err
	}
	if r == nil || len(r.font) == 0 {
		return nil, ErrPDFFont
	}
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	missing := false
	if err := pdf.AddTTFFontDataWithOption("quote", r.font, gopdf.TtfOption{OnGlyphNotFound: func(rune) { missing = true }}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPDFFont, err)
	}
	if err := pdf.SetFont("quote", "", 10); err != nil {
		return nil, err
	}
	page := 0
	y := 0.0
	newPage := func() error {
		page++
		if page > 40 {
			return ErrPDFLimit
		}
		pdf.AddPage()
		pdf.SetXY(42, 805)
		if err := pdf.Cell(nil, fmt.Sprintf("V%d | %s | %d", d.Version, d.ID, page)); err != nil {
			return err
		}
		y = 48
		return nil
	}
	if err := newPage(); err != nil {
		return nil, err
	}
	write := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		lines, err := pdf.SplitText(text, 510)
		if err != nil {
			return err
		}
		if missing {
			return ErrPDFFont
		}
		for _, line := range lines {
			if y > 770 {
				if err := newPage(); err != nil {
					return err
				}
			}
			pdf.SetXY(42, y)
			if err := pdf.Cell(nil, line); err != nil {
				return err
			}
			y += 16
		}
		y += 6
		return nil
	}
	lines := []string{
		fmt.Sprintf("正式报价单 / V%d", d.Version),
		"未用章；生成文件不代表获准对外发送。",
		"客户：" + d.Customer,
		"币种：CNY；金额单位：元；不含税费。",
	}
	for _, line := range d.Lines {
		lines = append(lines, fmt.Sprintf("产品：%s   数量：%d", line.SKU, line.Quantity),
			fmt.Sprintf("标准单价：%s   报价单价：%s   行金额：%s", money(line.StandardUnitMinor), money(line.UnitMinor), money(line.TotalMinor)))
	}
	lines = append(lines, "标准总额："+money(d.StandardTotalMinor), "报价总额："+money(d.TotalMinor),
		"证据引用："+strings.Join(d.Evidence, ", "), "报价编号："+d.ID.String(),
		"内容摘要："+d.Hash, "目录版本："+d.CatalogueRevision, "规则版本："+d.Policy.Revision,
		"排版版本："+r.revision)
	for _, line := range lines {
		if err := write(line); err != nil {
			return nil, err
		}
	}
	if missing {
		return nil, ErrPDFFont
	}
	var output bytes.Buffer
	if _, err := pdf.WriteTo(&output); err != nil {
		return nil, err
	}
	if output.Len() > 8<<20 {
		return nil, ErrPDFLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
