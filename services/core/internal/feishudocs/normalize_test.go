package feishudocs

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func textBlock(id, parent string, kind int, key, body string, children ...string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{"block_id": id, "parent_id": parent, "block_type": kind,
		"children": children, key: map[string]any{"elements": []any{map[string]any{"text_run": map[string]any{"content": body}}}}})
	if err != nil {
		panic(err)
	}
	return raw
}

func textSnapshot() Snapshot {
	return Snapshot{DocumentID: "doxcTest", Revision: 7, Title: "会议纪要", Blocks: []json.RawMessage{
		textBlock("second", "root", 2, "text", "第二项"),
		textBlock("root", "", 1, "page", "会议纪要", "first", "second"),
		textBlock("first", "root", 3, "heading1", "产品\n与价格", "nested"),
		textBlock("nested", "first", 2, "text", "每件 ¥100；折扣不得低于 80%。"),
	}}
}

func TestNormalizeOrderAnchorsAndVersions(t *testing.T) {
	source := uuid.New()
	in := textSnapshot()
	got, err := Normalize(source, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceID != source || got.DocumentID != in.DocumentID || got.Revision != 7 || got.Title != in.Title || got.Deleted {
		t.Fatal("lost document binding")
	}
	want := []knowledge.Chunk{
		{ID: "root", Body: "会议纪要", Anchor: "feishu-docx:doxcTest:7:root"},
		{ID: "first", Body: "产品\n与价格", Anchor: "feishu-docx:doxcTest:7:first"},
		{ID: "nested", Body: "每件 ¥100；折扣不得低于 80%。", Anchor: "feishu-docx:doxcTest:7:nested"},
		{ID: "second", Body: "第二项", Anchor: "feishu-docx:doxcTest:7:second"},
	}
	if !reflect.DeepEqual(got.Chunks, want) {
		t.Fatal("source order, text or anchors changed")
	}
	in.Blocks[0], in.Blocks[3] = in.Blocks[3], in.Blocks[0]
	reordered, err := Normalize(source, in)
	if err != nil || !reflect.DeepEqual(got, reordered) {
		t.Fatal("pagination order changed normalized result")
	}
	in.Revision++
	newer, err := Normalize(source, in)
	if err != nil || newer.Chunks[0].ID != got.Chunks[0].ID || newer.Chunks[0].Anchor == got.Chunks[0].Anchor {
		t.Fatal("unstable block ID or unbound anchor version")
	}
}

func TestNormalizeRejectsEntireSnapshot(t *testing.T) {
	for name, replace := range map[string]string{
		"image":            `{"block_id":"second","parent_id":"root","block_type":27,"image":{"token":"secret"}}`,
		"mention":          `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"mention_user":{"user_id":"ou_secret"}}]}}`,
		"unknown_inline":   `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"future":{"content":"secret"}}]}}`,
		"ambiguous_inline": `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":"safe"},"future":{}}]}}`,
		"unknown_payload":  `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[]},"future":{"content":"secret"}}`,
		"null_content":     `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":null}}]}}`,
		"missing_payload":  `{"block_id":"second","parent_id":"root","block_type":2}`,
		"struck_out":       `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":"approved","text_element_style":{"strikethrough":true}}}]}}`,
		"link":             `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":"policy","text_element_style":{"link":{"url":"https://example.com"}}}}]}}`,
		"nul":              `{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":"a\u0000b"}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			in := textSnapshot()
			in.Blocks[0] = json.RawMessage(replace)
			got, err := Normalize(uuid.New(), in)
			if err == nil || !reflect.DeepEqual(got, knowledge.Content{}) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsupported or invalid content returned partial evidence")
			}
		})
	}
}

func TestNormalizeSizeAndTreeBoundaries(t *testing.T) {
	in := textSnapshot()
	in.Blocks[0] = textBlock("second", "root", 2, "text", strings.Repeat("中", 5333))
	if _, err := Normalize(uuid.New(), in); err != nil {
		t.Fatal(err)
	}
	in.Blocks[0] = textBlock("second", "root", 2, "text", strings.Repeat("中", 5334))
	if _, err := Normalize(uuid.New(), in); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized UTF-8 body not rejected")
	}
	in = textSnapshot()
	in.Blocks = in.Blocks[:3]
	if _, err := Normalize(uuid.New(), in); err == nil {
		t.Fatal("missing child accepted")
	}
	in = textSnapshot()
	in.Title = " title "
	if _, err := Normalize(uuid.New(), in); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid storage title accepted")
	}
}

func TestNormalizeChunkLimitAndBlankBlocks(t *testing.T) {
	in := Snapshot{DocumentID: "doxcTest", Revision: 1, Title: "标题"}
	children := make([]string, 256)
	for i := range children {
		children[i] = "b" + strconv.Itoa(i)
		in.Blocks = append(in.Blocks, textBlock(children[i], "root", 2, "text", "内容"))
	}
	in.Blocks = append(in.Blocks, textBlock("root", "", 1, "page", "", children...))
	if out, err := Normalize(uuid.New(), in); err != nil || len(out.Chunks) != 256 {
		t.Fatal("256 chunks or blank root rejected", err)
	}
	in.Blocks[len(in.Blocks)-1] = textBlock("root", "", 1, "page", "标题", children...)
	if out, err := Normalize(uuid.New(), in); !errors.Is(err, ErrLimit) || len(out.Chunks) != 0 {
		t.Fatal("chunk overflow truncated instead of rejected")
	}
	in.Blocks = []json.RawMessage{textBlock("root", "", 1, "page", " \n\t")}
	if _, err := Normalize(uuid.New(), in); !errors.Is(err, ErrUnsupported) {
		t.Fatal("empty evidence published")
	}
}

func TestNormalizeTextRunsPreserveBoundaries(t *testing.T) {
	in := textSnapshot()
	in.Blocks[0] = json.RawMessage(`{"block_id":"second","parent_id":"root","block_type":2,"text":{"elements":[{"text_run":{"content":"  折扣不得","text_element_style":{"bold":true}}},{"text_run":{"content":"低于\n80%。  ","text_element_style":{"strikethrough":false}}}]}}`)
	out, err := Normalize(uuid.New(), in)
	if err != nil || out.Chunks[3].Body != "折扣不得低于\n80%。" {
		t.Fatal("formatting run boundaries changed source words", err)
	}
}
