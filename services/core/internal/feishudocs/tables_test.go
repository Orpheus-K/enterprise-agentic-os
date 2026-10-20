package feishudocs

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func rawBlock(fields map[string]any) json.RawMessage {
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

func tableSnapshot() Snapshot {
	in := Snapshot{DocumentID: "doxcTest", Revision: 4, Title: "价格表",
		Blocks: []json.RawMessage{textBlock("root", "", 1, "page", "", "table")}}
	cells := []string{"c0", "c1", "c2", "c3", "c4", "c5"}
	values := []string{"产品", "价格（元）", "A | \"标准\"\n含服务", "100.00", "B", ""}
	spans := []any{}
	for i, cell := range cells {
		id := "text" + strconv.Itoa(i)
		in.Blocks = append(in.Blocks, rawBlock(map[string]any{"block_id": cell, "parent_id": "table", "block_type": 32,
			"table_cell": map[string]any{}, "children": []string{id}}), textBlock(id, cell, 2, "text", values[i]))
		spans = append(spans, map[string]int{"row_span": 1, "col_span": 1})
	}
	in.Blocks = append(in.Blocks, rawBlock(map[string]any{"block_id": "table", "parent_id": "root", "block_type": 31,
		"children": cells, "table": map[string]any{"cells": cells, "property": map[string]any{
			"row_size": 3, "column_size": 2, "merge_info": spans, "header_row": true,
		}}}))
	return in
}

func TestTableKeepsRelationshipsAsOneEvidenceUnit(t *testing.T) {
	in := tableSnapshot()
	// Deliberately scramble transport order, including the table's own block.
	for i, j := 0, len(in.Blocks)-1; i < j; i, j = i+1, j-1 {
		in.Blocks[i], in.Blocks[j] = in.Blocks[j], in.Blocks[i]
	}
	out, err := Normalize(uuid.New(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].ID != "table" || out.Chunks[0].Anchor != "feishu-docx:doxcTest:4:table" {
		t.Fatal("table cells became independent evidence")
	}
	var body struct {
		HeaderRow bool       `json:"header_row"`
		Rows      [][]string `json:"rows"`
	}
	want := [][]string{{"产品", "价格（元）"}, {"A | \"标准\"\n含服务", "100.00"}, {"B", ""}}
	if json.Unmarshal([]byte(out.Chunks[0].Body), &body) != nil || !body.HeaderRow || !reflect.DeepEqual(body.Rows, want) {
		t.Fatal("lost header, empty cell, quoting or row/column relationships")
	}
}

func TestTableRejectsAmbiguousGeometry(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"missing_spans": func(_ map[string]any, p map[string]any) { delete(p, "merge_info") },
		"merged": func(_ map[string]any, p map[string]any) {
			p["merge_info"].([]any)[0].(map[string]any)["col_span"] = 2
		},
		"dimensions": func(_ map[string]any, p map[string]any) { p["row_size"] = 4 },
		"overflow":   func(_ map[string]any, p map[string]any) { p["column_size"] = 1e18 },
		"order": func(table map[string]any, _ map[string]any) {
			c := table["cells"].([]any)
			c[0], c[1] = c[1], c[0]
		},
		"unknown_content": func(table map[string]any, _ map[string]any) { table["future_content"] = "secret" },
	} {
		t.Run(name, func(t *testing.T) {
			in := tableSnapshot()
			var block map[string]any
			_ = json.Unmarshal(in.Blocks[len(in.Blocks)-1], &block)
			table := block["table"].(map[string]any)
			mutate(table, table["property"].(map[string]any))
			in.Blocks[len(in.Blocks)-1] = rawBlock(block)
			out, err := Normalize(uuid.New(), in)
			if err == nil || !reflect.DeepEqual(out, knowledge.Content{}) || strings.Contains(err.Error(), "secret") {
				t.Fatal("ambiguous table returned partial evidence")
			}
		})
	}
}

func TestTableContextAndLimits(t *testing.T) {
	in := tableSnapshot()
	// The table is subordinate to a prohibitive list item.
	in.Blocks[0] = textBlock("root", "", 1, "page", "", "rule")
	in.Blocks = append(in.Blocks, listBlock("rule", "root", 12, "bullet", "禁止使用以下价格", nil, "table"))
	var table map[string]any
	_ = json.Unmarshal(in.Blocks[len(in.Blocks)-2], &table)
	table["parent_id"] = "rule"
	in.Blocks[len(in.Blocks)-2] = rawBlock(table)
	out, err := Normalize(uuid.New(), in)
	if err != nil || len(out.Chunks) != 2 || !strings.HasPrefix(out.Chunks[1].Body, "• 禁止使用以下价格\n") {
		t.Fatal("table lost parent constraint", err)
	}
	in = tableSnapshot()
	in.Blocks[2] = textBlock("text0", "c0", 2, "text", strings.Repeat("\"", 9000))
	if out, err := Normalize(uuid.New(), in); err == nil || len(out.Chunks) != 0 {
		t.Fatal("serialized table overflow not rejected")
	}
	in = tableSnapshot()
	in.Blocks[1] = rawBlock(map[string]any{"block_id": "c0", "parent_id": "table", "block_type": 2,
		"text": map[string]any{"elements": []any{}}, "children": []string{"text0"}})
	if _, err := Normalize(uuid.New(), in); err == nil {
		t.Fatal("non-cell table child accepted")
	}
}
