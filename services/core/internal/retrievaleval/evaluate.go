// Package retrievaleval measures synthetic retrieval, never production authorization.
package retrievaleval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode"
)

type Document struct {
	ID        string   `json:"id"`
	Tenant    string   `json:"tenant"`
	Text      string   `json:"text"`
	Members   []string `json:"members"`
	Published bool     `json:"published"`
	Healthy   bool     `json:"healthy"`
}
type Query struct {
	ID       string   `json:"id"`
	Tenant   string   `json:"tenant"`
	Member   string   `json:"member"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant"`
}
type Corpus struct {
	Documents []Document `json:"documents"`
	Queries   []Query    `json:"queries"`
}
type Retriever func(context.Context, []Document, Query, int, bool) ([]string, error)
type Score struct {
	Query          string   `json:"query"`
	Recall         float64  `json:"recall"`
	FilteredRecall *float64 `json:"filtered_recall"`
	Relevant       int      `json:"relevant"`
	Eligible       int      `json:"eligible"`
	Unauthorized   int      `json:"unauthorized"`
	Unfiltered     []string `json:"unfiltered"`
	Filtered       []string `json:"filtered"`
}
type Report struct {
	Format              string   `json:"format"`
	Backend             string   `json:"backend"`
	CorpusSHA256        string   `json:"corpus_sha256"`
	K                   int      `json:"k"`
	MacroRecall         float64  `json:"macro_recall"`
	MacroFilteredRecall *float64 `json:"macro_filtered_recall"`
	Unauthorized        int      `json:"unauthorized"`
	Queries             []Score  `json:"queries"`
}

func Visible(d Document, q Query) bool {
	return d.Tenant == q.Tenant && d.Published && d.Healthy && slices.Contains(d.Members, q.Member)
}

func Evaluate(ctx context.Context, c Corpus, k int, backend string, retrieve Retriever) (Report, error) {
	out := Report{Format: "agentos.retrieval-evaluation.v1", Backend: backend, K: k, Queries: []Score{}}
	if k < 1 || k > 100 || backend == "" || retrieve == nil || len(c.Documents) == 0 || len(c.Queries) == 0 {
		return out, errors.New("invalid evaluation")
	}
	docs := map[string]Document{}
	for _, d := range c.Documents {
		if d.ID == "" || d.Text == "" || d.Tenant == "" {
			return out, errors.New("invalid document")
		}
		if _, exists := docs[d.ID]; exists {
			return out, errors.New("duplicate document")
		}
		docs[d.ID] = d
	}
	queryIDs := map[string]bool{}
	raw, err := json.Marshal(c)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.CorpusSHA256 = hex.EncodeToString(sum[:])
	filteredSum := 0.0
	filteredCount := 0
	for _, q := range c.Queries {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if q.ID == "" || q.Tenant == "" || q.Member == "" || q.Text == "" || len(q.Relevant) == 0 || queryIDs[q.ID] {
			return Report{}, errors.New("invalid query")
		}
		queryIDs[q.ID] = true
		relevant := map[string]bool{}
		eligible := map[string]bool{}
		for _, id := range q.Relevant {
			d, exists := docs[id]
			if !exists || relevant[id] {
				return Report{}, errors.New("invalid relevance judgment")
			}
			relevant[id] = true
			if Visible(d, q) {
				eligible[id] = true
			}
		}
		s := Score{Query: q.ID, Relevant: len(relevant), Eligible: len(eligible)}
		for _, filtered := range []bool{false, true} {
			inputs := slices.Clone(c.Documents)
			for i := range inputs {
				inputs[i].Members = slices.Clone(inputs[i].Members)
			}
			queryInput := q
			// Ground-truth judgments belong to the evaluator, not the backend.
			queryInput.Relevant = nil
			hits, err := retrieve(ctx, inputs, queryInput, k, filtered)
			if err != nil {
				return Report{}, err
			}
			if len(hits) > k {
				return Report{}, errors.New("retriever exceeded k")
			}
			seen := map[string]bool{}
			matched := 0
			for _, id := range hits {
				d, exists := docs[id]
				if !exists || seen[id] {
					return Report{}, errors.New("unknown or duplicate result")
				}
				seen[id] = true
				if filtered {
					if !Visible(d, q) {
						s.Unauthorized++
					}
					if eligible[id] {
						matched++
					}
				} else if relevant[id] {
					matched++
				}
			}
			if filtered {
				s.Filtered = hits
				if len(eligible) > 0 {
					value := float64(matched) / float64(len(eligible))
					s.FilteredRecall = &value
					filteredSum += value
					filteredCount++
				}
			} else {
				s.Unfiltered = hits
				s.Recall = float64(matched) / float64(len(relevant))
			}
		}
		out.MacroRecall += s.Recall
		out.Unauthorized += s.Unauthorized
		out.Queries = append(out.Queries, s)
	}
	out.MacroRecall /= float64(len(c.Queries))
	if filteredCount > 0 {
		value := filteredSum / float64(filteredCount)
		out.MacroFilteredRecall = &value
	}
	return out, nil
}

// Bigram is a transparent character-overlap baseline, not BM25 or pg_search.
// Pre-filtering occurs before scoring and top-k; no embeddings or network calls.
func Bigram(ctx context.Context, docs []Document, q Query, k int, filtered bool) ([]string, error) {
	type candidate struct {
		id    string
		score float64
	}
	ranked := []candidate{}
	query := grams(q.Text)
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filtered && !Visible(d, q) {
			continue
		}
		terms := grams(d.Text)
		intersection := 0
		for term := range query {
			if terms[term] {
				intersection++
			}
		}
		if intersection > 0 {
			ranked = append(ranked, candidate{d.ID, float64(intersection) / float64(len(query)+len(terms)-intersection)})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	result := []string{}
	for i, item := range ranked {
		if i >= k {
			break
		}
		result = append(result, item.id)
	}
	return result, nil
}
func grams(text string) map[string]bool {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text))
	out := map[string]bool{}
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = true
	}
	return out
}
