package feishudocs

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

var ErrUnsupported = errors.New("unsupported Feishu document content")

type normalizedBlock struct {
	ID       string   `json:"block_id"`
	Parent   string   `json:"parent_id"`
	Children []string `json:"children"`
	Type     int      `json:"block_type"`
	body     string
	sequence string
	table    *documentTable
}

// Normalize produces version-bound plain text evidence, not rendered Markdown.
// It performs no I/O and grants no access. Unsupported content rejects the whole
// snapshot so that omitted evidence cannot silently change a document's meaning.
func Normalize(source uuid.UUID, snapshot Snapshot) (knowledge.Content, error) {
	fail := knowledge.Content{}
	if source == uuid.Nil || !identifier(snapshot.DocumentID) || snapshot.Revision < 1 ||
		!cleanText(snapshot.Title) || len(snapshot.Title) > 512 ||
		strings.TrimSpace(snapshot.Title) != snapshot.Title || snapshot.Title == "" {
		return fail, ErrInvalid
	}
	if len(snapshot.Blocks) > maxBlocks {
		return fail, ErrLimit
	}
	size := 0
	for _, raw := range snapshot.Blocks {
		size += len(raw)
		if size > maxSnapshot {
			return fail, ErrLimit
		}
		if !utf8.Valid(raw) {
			return fail, ErrInvalid
		}
	}
	if err := completeTree(snapshot.Blocks); err != nil {
		return fail, err
	}
	nodes := map[string]normalizedBlock{}
	root := ""
	for _, raw := range snapshot.Blocks {
		var n normalizedBlock
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &n) != nil || json.Unmarshal(raw, &fields) != nil {
			return fail, ErrInvalid
		}
		key := ""
		switch {
		case n.Type == 1:
			key = "page"
		case n.Type == 2:
			key = "text"
		case n.Type >= 3 && n.Type <= 11:
			key = "heading" + strconv.Itoa(n.Type-2)
		case n.Type == 12:
			key = "bullet"
		case n.Type == 13:
			key = "ordered"
		case n.Type == 17:
			key = "todo"
		case n.Type == 31:
			key = "table"
		case n.Type == 32:
			key = "table_cell"
		default:
			return fail, ErrUnsupported
		}
		// Unknown block payloads may contain additional evidence. Only known
		// structural metadata and the selected text payload are accepted.
		for k := range fields {
			switch k {
			case "block_id", "parent_id", "children", "block_type", "comment_ids":
			default:
				if k != key {
					return fail, ErrUnsupported
				}
			}
		}
		if n.Type == 31 {
			table, err := parseTable(fields[key], n.Children)
			if err != nil {
				return fail, err
			}
			n.table = table
		} else if n.Type == 32 {
			var cell map[string]json.RawMessage
			if json.Unmarshal(fields[key], &cell) != nil || cell == nil || len(cell) != 0 {
				return fail, ErrUnsupported
			}
		} else {
			body, err := plainText(fields[key])
			if err != nil {
				return fail, err
			}
			n.body = strings.TrimSpace(body)
		}
		if n.Type == 12 || n.Type == 13 || n.Type == 17 {
			if n.body == "" {
				return fail, ErrUnsupported
			}
			var payload struct {
				Style *struct {
					Done     *bool   `json:"done"`
					Sequence *string `json:"sequence"`
				} `json:"style"`
			}
			if json.Unmarshal(fields[key], &payload) != nil {
				return fail, ErrInvalid
			}
			switch n.Type {
			case 12:
				n.body = "• " + n.body
			case 13:
				if payload.Style == nil || payload.Style.Sequence == nil {
					return fail, ErrUnsupported
				}
				n.sequence = *payload.Style.Sequence
				if n.sequence != "auto" {
					number, err := strconv.Atoi(n.sequence)
					if err != nil || number < 1 || number > 1000000000 || strconv.Itoa(number) != n.sequence {
						return fail, ErrInvalid
					}
				}
			case 17:
				if payload.Style == nil || payload.Style.Done == nil {
					return fail, ErrUnsupported
				}
				if *payload.Style.Done {
					n.body = "[已完成] " + n.body
				} else {
					n.body = "[未完成] " + n.body
				}
			}
		}
		if len(n.body) > 16000 {
			return fail, ErrLimit
		}
		if n.Parent == "" {
			root = n.ID
		}
		nodes[n.ID] = n
	}
	// Resolve ordered numbering only from an explicit start and consecutive
	// ordered siblings. Missing historical sequence data is not guessed.
	for _, parent := range nodes {
		previous := 0
		for _, id := range parent.Children {
			n := nodes[id]
			if n.Type != 13 {
				previous = 0
				continue
			}
			if n.sequence == "auto" {
				if previous == 0 || previous == 1000000000 {
					return fail, ErrUnsupported
				}
				previous++
			} else {
				previous, _ = strconv.Atoi(n.sequence) // validated above
			}
			n.body = strconv.Itoa(previous) + ". " + n.body
			nodes[id] = n
		}
	}
	for id, n := range nodes {
		if n.Type == 32 && nodes[n.Parent].Type != 31 {
			return fail, ErrInvalid
		}
		if n.Type == 31 {
			body, err := tableText(n, nodes)
			if err != nil {
				return fail, err
			}
			n.body = body
			nodes[id] = n
		}
	}
	out := knowledge.Content{SourceID: source, DocumentID: snapshot.DocumentID,
		Revision: int64(snapshot.Revision), Title: snapshot.Title}
	// API pagination order is not document order. Traverse declared children
	// iteratively, preserving source order and avoiding attacker-controlled depth.
	type pending struct{ id, context string }
	stack := []pending{{id: root}}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		id := item.id
		n := nodes[id]
		if n.body != "" {
			body := item.context + n.body
			if len(body) > 16000 {
				return fail, ErrLimit
			}
			if len(out.Chunks) == 256 {
				return fail, ErrLimit
			}
			out.Chunks = append(out.Chunks, knowledge.Chunk{ID: id, Body: body,
				Anchor: "feishu-docx:" + snapshot.DocumentID + ":" + strconv.Itoa(snapshot.Revision) + ":" + id})
		}
		// The table is one evidence unit; detached cells would lose their
		// row/column relationships and must not be separately searchable.
		if n.Type == 31 {
			continue
		}
		prefix := item.context
		if n.Type == 12 || n.Type == 13 || n.Type == 17 {
			prefix += n.body + "\n"
		}
		for i := len(n.Children) - 1; i >= 0; i-- {
			stack = append(stack, pending{id: n.Children[i], context: prefix})
		}
	}
	if len(out.Chunks) == 0 {
		return fail, ErrUnsupported
	}
	return out, nil
}

func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func plainText(raw json.RawMessage) (string, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return "", ErrInvalid
	}
	for key := range payload {
		if key != "elements" && key != "style" {
			return "", ErrUnsupported
		}
	}
	var elements []map[string]json.RawMessage
	if json.Unmarshal(payload["elements"], &elements) != nil || elements == nil {
		return "", ErrInvalid
	}
	var body strings.Builder
	for _, element := range elements {
		if len(element) != 1 || element["text_run"] == nil {
			return "", ErrUnsupported
		}
		var run map[string]json.RawMessage
		if json.Unmarshal(element["text_run"], &run) != nil || run == nil {
			return "", ErrInvalid
		}
		for key := range run {
			if key != "content" && key != "text_element_style" {
				return "", ErrUnsupported
			}
		}
		var content *string
		if json.Unmarshal(run["content"], &content) != nil || content == nil || !cleanText(*content) {
			return "", ErrInvalid
		}
		// Links and struck-out text carry meaning beyond visible characters;
		// do not flatten them into an apparently ordinary assertion.
		if rawStyle, ok := run["text_element_style"]; ok {
			var style map[string]json.RawMessage
			if json.Unmarshal(rawStyle, &style) != nil || style == nil {
				return "", ErrInvalid
			}
			for key, value := range style {
				switch key {
				case "bold", "italic", "underline", "inline_code":
					var flag bool
					if json.Unmarshal(value, &flag) != nil {
						return "", ErrInvalid
					}
				case "strikethrough":
					if string(value) != "false" {
						return "", ErrUnsupported
					}
				case "text_color", "background_color":
					var color int
					if json.Unmarshal(value, &color) != nil {
						return "", ErrInvalid
					}
				default:
					return "", ErrUnsupported
				}
			}
		}
		body.WriteString(*content)
		if body.Len() > 16000 {
			return "", ErrLimit
		}
	}
	return body.String(), nil
}
