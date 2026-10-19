package quotation

import (
	"fmt"
	"strings"
)

// RenderText is a presentation of the approved data, never a new pricing decision.
func RenderText(d Draft) (string, error) {
	if err := Verify(d); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "报价初稿 · V%d\n仅供内部确认；非正式对外文件，未用章。\n客户：%s\n币种：CNY；金额单位：元；不含税费。\n", d.Version, d.Customer)
	for _, line := range d.Lines {
		fmt.Fprintf(&b, "\n产品：%s\n数量：%d\n标准单价：%s\n报价单价：%s\n行金额：%s\n", line.SKU, line.Quantity, money(line.StandardUnitMinor), money(line.UnitMinor), money(line.TotalMinor))
	}
	fmt.Fprintf(&b, "\n标准总额：%s\n报价总额：%s\n", money(d.StandardTotalMinor), money(d.TotalMinor))
	if d.RequiresApproval {
		b.WriteString("折扣规则：触发特批；本文件不证明审批状态。\n")
	} else {
		b.WriteString("折扣规则：未触发本条特批；不代表获准对外发送。\n")
	}
	fmt.Fprintf(&b, "证据引用：%s\n目录版本：%s\n规则版本：%s\n报价编号：%s\n内容摘要：%s\n", strings.Join(d.Evidence, ", "), d.CatalogueRevision, d.Policy.Revision, d.ID, d.Hash)
	return b.String(), nil
}

func money(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }
