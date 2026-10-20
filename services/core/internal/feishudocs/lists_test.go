package feishudocs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func listBlock(id, parent string, kind int, key, body string, style any, children ...string) json.RawMessage {
	var fields map[string]any
	_ = json.Unmarshal(textBlock(id, parent, kind, key, body, children...), &fields)
	fields[key].(map[string]any)["style"] = style
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestListsPreserveNumberingStatusAndParentMeaning(t *testing.T) {
	in := Snapshot{DocumentID: "doxcTest", Revision: 1, Title: "核价步骤", Blocks: []json.RawMessage{
		textBlock("root", "", 1, "page", "", "first", "second", "todo"),
		listBlock("second", "root", 13, "ordered", "复核价格", map[string]any{"sequence": "auto"}),
		listBlock("first", "root", 13, "ordered", "不得提供以下折扣", map[string]any{"sequence": "3"}, "bullet"),
		listBlock("bullet", "first", 12, "bullet", "七五折", nil, "detail"),
		textBlock("detail", "bullet", 2, "text", "未经审批"),
		listBlock("todo", "root", 17, "todo", "主管确认", map[string]any{"done": false}, "done"),
		listBlock("done", "todo", 17, "todo", "核对客户", map[string]any{"done": true}),
	}}
	out, err := Normalize(uuid.New(), in)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range out.Chunks {
		bodies = append(bodies, c.Body)
	}
	want := []string{"3. 不得提供以下折扣", "3. 不得提供以下折扣\n• 七五折",
		"3. 不得提供以下折扣\n• 七五折\n未经审批", "4. 复核价格",
		"[未完成] 主管确认", "[未完成] 主管确认\n[已完成] 核对客户"}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("lost list meaning: %q", bodies)
	}
	if out.Chunks[2].ID != "detail" || out.Chunks[2].Anchor != "feishu-docx:doxcTest:1:detail" {
		t.Fatal("context changed source anchor")
	}
}

func TestListAmbiguityNeverPublishesPartialEvidence(t *testing.T) {
	for name, block := range map[string]json.RawMessage{
		"missing_sequence": listBlock("item", "root", 13, "ordered", "secret", nil),
		"orphan_auto":      listBlock("item", "root", 13, "ordered", "secret", map[string]any{"sequence": "auto"}),
		"negative":         listBlock("item", "root", 13, "ordered", "secret", map[string]any{"sequence": "-1"}),
		"overflow":         listBlock("item", "root", 13, "ordered", "secret", map[string]any{"sequence": "999999999999999999999999"}),
		"noncanonical":     listBlock("item", "root", 13, "ordered", "secret", map[string]any{"sequence": "03"}),
		"missing_done":     listBlock("item", "root", 17, "todo", "secret", map[string]any{}),
		"wrong_done_type":  listBlock("item", "root", 17, "todo", "secret", map[string]any{"done": "false"}),
		"empty_bullet":     listBlock("item", "root", 12, "bullet", "", nil),
	} {
		t.Run(name, func(t *testing.T) {
			in := Snapshot{DocumentID: "doxcTest", Revision: 1, Title: "标题", Blocks: []json.RawMessage{
				textBlock("root", "", 1, "page", "标题", "item"), block,
			}}
			out, err := Normalize(uuid.New(), in)
			if err == nil || !reflect.DeepEqual(out, knowledge.Content{}) || strings.Contains(err.Error(), "secret") {
				t.Fatal("ambiguous list returned evidence")
			}
		})
	}
}

func TestOrderedListRestartsAndContextLimit(t *testing.T) {
	in := Snapshot{DocumentID: "doxcTest", Revision: 1, Title: "标题", Blocks: []json.RawMessage{
		textBlock("root", "", 1, "page", "", "a", "break", "b"),
		listBlock("a", "root", 13, "ordered", "a", map[string]any{"sequence": "5"}),
		textBlock("break", "root", 2, "text", "中断"),
		listBlock("b", "root", 13, "ordered", "b", map[string]any{"sequence": "auto"}),
	}}
	if _, err := Normalize(uuid.New(), in); err == nil {
		t.Fatal("auto numbering crossed non-list sibling")
	}
	in.Blocks[3] = listBlock("b", "root", 13, "ordered", "b", map[string]any{"sequence": "1"})
	out, err := Normalize(uuid.New(), in)
	if err != nil || out.Chunks[2].Body != "1. b" {
		t.Fatal("explicit restart lost")
	}
	in.Blocks = []json.RawMessage{
		textBlock("root", "", 1, "page", "", "a"),
		listBlock("a", "root", 12, "bullet", strings.Repeat("x", 9000), nil, "b"),
		listBlock("b", "a", 12, "bullet", strings.Repeat("y", 9000), nil),
	}
	if out, err := Normalize(uuid.New(), in); err == nil || len(out.Chunks) != 0 {
		t.Fatal("context overflow was truncated")
	}
}
