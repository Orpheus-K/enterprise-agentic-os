package feishudocs

import "encoding/json"

func completeTree(blocks []json.RawMessage) error {
	type block struct {
		ID       string   `json:"block_id"`
		Parent   string   `json:"parent_id"`
		Type     int      `json:"block_type"`
		Children []string `json:"children"`
	}
	index := map[string]block{}
	root := ""
	edges := 0
	for _, raw := range blocks {
		var b block
		if json.Unmarshal(raw, &b) != nil || !identifier(b.ID) || b.Type < 1 {
			return ErrRemote
		}
		if _, ok := index[b.ID]; ok {
			return ErrRemote
		}
		index[b.ID] = b
		edges += len(b.Children)
		if edges >= len(blocks) {
			return ErrRemote
		}
		if b.Parent == "" {
			if root != "" || b.Type != 1 {
				return ErrRemote
			}
			root = b.ID
		}
	}
	if root == "" {
		return ErrRemote
	}
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			return ErrRemote
		}
		seen[id] = true
		for _, child := range index[id].Children {
			b, ok := index[child]
			if !ok || b.Parent != id {
				return ErrRemote
			}
			queue = append(queue, child)
		}
	}
	if len(seen) != len(index) {
		return ErrRemote
	}
	return nil
}
