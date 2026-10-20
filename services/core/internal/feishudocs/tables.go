package feishudocs

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

type documentTable struct {
	Cells    []string `json:"cells"`
	Property *struct {
		Rows        int   `json:"row_size"`
		Columns     int   `json:"column_size"`
		ColumnWidth []int `json:"column_width"`
		MergeInfo   []struct {
			Rows    int `json:"row_span"`
			Columns int `json:"col_span"`
		} `json:"merge_info"`
		HeaderRow    bool `json:"header_row"`
		HeaderColumn bool `json:"header_column"`
	} `json:"property"`
}

func parseTable(raw json.RawMessage, children []string) (*documentTable, error) {
	var table documentTable
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&table) != nil || table.Property == nil {
		return nil, ErrInvalid
	}
	p := table.Property
	if p.Rows < 1 || p.Columns < 1 || p.Rows > maxBlocks || p.Columns > maxBlocks ||
		p.Rows > maxBlocks/p.Columns || len(table.Cells) != p.Rows*p.Columns ||
		!slices.Equal(table.Cells, children) {
		return nil, ErrInvalid
	}
	// A rectangle without explicit unit spans is not evidence that merged
	// cells are absent. Merge layouts require their own certified decoder.
	if len(p.MergeInfo) != len(table.Cells) {
		return nil, ErrUnsupported
	}
	for _, span := range p.MergeInfo {
		if span.Rows != 1 || span.Columns != 1 {
			return nil, ErrUnsupported
		}
	}
	return &table, nil
}

func tableText(table normalizedBlock, nodes map[string]normalizedBlock) (string, error) {
	p := table.table.Property
	// JSON keeps embedded newlines, pipes and quotes inside their cells; it
	// does not invent a header row or flatten adjacent monetary values.
	out := struct {
		HeaderRow    bool       `json:"header_row"`
		HeaderColumn bool       `json:"header_column"`
		Rows         [][]string `json:"rows"`
	}{HeaderRow: p.HeaderRow, HeaderColumn: p.HeaderColumn, Rows: make([][]string, p.Rows)}
	total := 0
	for i, id := range table.table.Cells {
		cell := nodes[id]
		if cell.Type != 32 || cell.Parent != table.ID {
			return "", ErrInvalid
		}
		type pending struct{ id, context string }
		stack := []pending{}
		for j := len(cell.Children) - 1; j >= 0; j-- {
			stack = append(stack, pending{id: cell.Children[j]})
		}
		var parts []string
		for len(stack) > 0 {
			item := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n := nodes[item.id]
			if n.Type == 31 || n.Type == 32 || n.Type == 1 {
				return "", ErrUnsupported
			}
			if n.body != "" {
				part := item.context + n.body
				total += len(part)
				if total > 16000 {
					return "", ErrLimit
				}
				parts = append(parts, part)
			}
			prefix := item.context
			if n.Type == 12 || n.Type == 13 || n.Type == 17 {
				prefix += n.body + "\n"
			}
			for j := len(n.Children) - 1; j >= 0; j-- {
				stack = append(stack, pending{id: n.Children[j], context: prefix})
			}
		}
		row := i / p.Columns
		out.Rows[row] = append(out.Rows[row], strings.Join(parts, "\n"))
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", ErrInvalid
	}
	if len(raw) > 16000 {
		return "", ErrLimit
	}
	return string(raw), nil
}
