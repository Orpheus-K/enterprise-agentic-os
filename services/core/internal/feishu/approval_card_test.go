package feishu

import (
	"agentos.local/core/internal/conversation"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func syntheticNotice() conversation.ApprovalNotice {
	text := `synthetic <at id=all>business</at> [link](https://example.invalid)`
	hash := sha256.Sum256([]byte(text))
	return conversation.ApprovalNotice{Approval: conversation.Approval{TaskID: uuid.New(), ID: uuid.New(), Revision: "approved-revision", Version: 3, ContentHash: hex.EncodeToString(hash[:]), ExpiresAt: time.Now().Add(time.Hour), Status: "pending", Response: text}}
}

// Decode the wire card, so callback tests consume values emitted by the renderer.
func cardDecision(t *testing.T, content string, index int) map[string]any {
	t.Helper()
	var card struct {
		Config struct {
			Forward bool `json:"enable_forward"`
		} `json:"config"`
		Elements []struct {
			Tag     string                        `json:"tag"`
			Text    struct{ Tag, Content string } `json:"text"`
			Actions []struct {
				Value map[string]any `json:"value"`
			} `json:"actions"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatal(err)
	}
	if card.Config.Forward || len(card.Elements) != 4 || card.Elements[1].Text.Tag != "plain_text" || len(card.Elements[3].Actions) != 2 {
		t.Fatal("card safety or actions missing")
	}
	return card.Elements[3].Actions[index].Value
}

func TestApprovalCardContentAndBinding(t *testing.T) {
	n := syntheticNotice()
	content, err := approvalCard(n)
	if err != nil {
		t.Fatal(err)
	}
	value := cardDecision(t, content, 0)
	if value["task_id"] != n.Approval.TaskID.String() || value["approval_id"] != n.Approval.ID.String() || value["content_hash"] != n.Approval.ContentHash || value["decision"] != "approve" || len(value) != 6 {
		t.Fatal("version binding missing")
	}
	if cardDecision(t, content, 1)["decision"] != "reject" {
		t.Fatal("reject missing")
	}
	n.Approval.Response = "tampered"
	if _, err = approvalCard(n); err == nil {
		t.Fatal("altered body accepted")
	}
	n = syntheticNotice()
	n.Approval.Response = strings.Repeat("x", 25000)
	hash := sha256.Sum256([]byte(n.Approval.Response))
	n.Approval.ContentHash = hex.EncodeToString(hash[:])
	if _, err = approvalCard(n); err == nil {
		t.Fatal("oversize approval silently truncated")
	}
}
