package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/quotation"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	larkcard "github.com/larksuite/oapi-sdk-go/v3/card"
)

func approvalCard(n conversation.ApprovalNotice) (string, error) {
	a := n.Approval
	sum := sha256.Sum256([]byte(a.Response))
	if a.ID == uuid.Nil || a.TaskID == uuid.Nil || a.Version < 1 || a.Revision == "" || a.Status != "pending" || a.ExpiresAt.IsZero() || a.Response == "" || a.ContentHash != hex.EncodeToString(sum[:]) {
		return "", errors.New("invalid approval notice")
	}
	body := a.Response
	if a.QuoteVersion > 0 {
		var draft quotation.Draft
		if json.Unmarshal([]byte(a.Response), &draft) != nil || draft.ID != a.TaskID || draft.Version != a.QuoteVersion {
			return "", errors.New("invalid notice quotation")
		}
		var err error
		body, err = quotation.RenderText(draft)
		if err != nil {
			return "", err
		}
		if draft.Version > 1 {
			old := n.PreviousQuote
			if old == nil || quotation.Verify(*old) != nil || old.ID != draft.ID || old.Version+1 != draft.Version || old.Hash != draft.PreviousHash {
				return "", errors.New("invalid quotation predecessor")
			}
			var diff strings.Builder
			fmt.Fprintf(&diff, "\n版本差异：V%d → V%d（金额单位：分）\n合计：%d → %d\n", old.Version, draft.Version, old.TotalMinor, draft.TotalMinor)
			for i, line := range draft.Lines {
				if i >= len(old.Lines) || old.Lines[i].SKU != line.SKU {
					return "", errors.New("quotation line mismatch")
				}
				if old.Lines[i].UnitMinor != line.UnitMinor {
					fmt.Fprintf(&diff, "%s 单价：%d → %d\n", line.SKU, old.Lines[i].UnitMinor, line.UnitMinor)
				}
			}
			body += diff.String()
		}
	}
	plain := func(s string) *larkcard.MessageCardPlainText { return larkcard.NewMessageCardPlainText().Content(s) }
	button := func(label, decision string, style larkcard.MessageCardButtonType) *larkcard.MessageCardEmbedButton {
		return larkcard.NewMessageCardEmbedButton().Text(plain(label)).Type(style).Value(map[string]interface{}{
			"task_id": a.TaskID.String(), "approval_id": a.ID.String(), "revision": a.Revision, "version": a.Version, "content_hash": a.ContentHash, "decision": decision,
		}).Confirm(larkcard.NewMessageCardActionConfirm().Title(plain("确认审批决定")).Text(plain("仅适用于卡片所列版本。修改报价请先通过受控改单入口生成新版本。")))
	}
	card := larkcard.NewMessageCard().Config(larkcard.NewMessageCardConfig().EnableForward(false)).Header(larkcard.NewMessageCardHeader().Title(plain("待审批 · 内部初稿"))).Elements([]larkcard.MessageCardElement{
		larkcard.NewMessageCardDiv().Text(plain(fmt.Sprintf("任务：%s\n审批：%s\n版本：%d\n有效至：%s\n摘要：%s", a.TaskID, a.ID, a.Version, a.ExpiresAt.UTC().Format(time.RFC3339), a.ContentHash))),
		larkcard.NewMessageCardDiv().Text(plain(body)),
		larkcard.NewMessageCardDiv().Text(plain("请核对完整内容再决定。审批不授予正式对外发送或用章权限；系统在点击及交付前重新检查授权。")),
		larkcard.NewMessageCardAction().Actions([]larkcard.MessageCardActionElement{button("批准此版本", "approve", larkcard.MessageCardButtonTypePrimary), button("驳回", "reject", larkcard.MessageCardButtonTypeDanger)}),
	})
	content, err := card.String()
	if err != nil {
		return "", err
	}
	// Never attach approval buttons to a truncated business document.
	if len(content) > 24000 {
		return "", errors.New("approval card exceeds local size limit")
	}
	return content, nil
}
