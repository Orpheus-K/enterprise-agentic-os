package retrievaleval

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"
)

func TestRecallSeparatesRelevanceAndAuthorization(t *testing.T) {
	c := Corpus{Documents: []Document{
		{ID: "private", Tenant: "a", Text: "设备维修流程", Members: []string{"owner"}, Published: true, Healthy: true},
		{ID: "public", Tenant: "a", Text: "设备维修操作流程", Members: []string{"reader"}, Published: true, Healthy: true},
	}, Queries: []Query{{ID: "maintenance", Tenant: "a", Member: "reader", Text: "设备维修流程", Relevant: []string{"private", "public"}}}}
	report, err := Evaluate(t.Context(), c, 1, "bigram-v1", Bigram)
	if err != nil || report.MacroRecall != 0.5 || report.MacroFilteredRecall == nil || *report.MacroFilteredRecall != 1 || report.Unauthorized != 0 {
		t.Fatal(report, err)
	}
	// Deliberately skip authorization: the evaluator must detect the leak.
	unsafe := func(ctx context.Context, docs []Document, q Query, k int, _ bool) ([]string, error) {
		return Bigram(ctx, docs, q, k, false)
	}
	report, err = Evaluate(t.Context(), c, 1, "unsafe-negative-control", unsafe)
	if err != nil || report.Unauthorized != 1 || *report.MacroFilteredRecall != 0 {
		t.Fatal("missed unauthorized candidate", report, err)
	}
	c.Queries[0].Member = "nobody"
	report, err = Evaluate(t.Context(), c, 1, "bigram-v1", Bigram)
	if err != nil || report.MacroFilteredRecall != nil || report.Queries[0].FilteredRecall != nil || len(report.Queries[0].Filtered) != 0 {
		t.Fatal("empty denominator fabricated recall", report, err)
	}
}

func TestInvalidBackendResultsFail(t *testing.T) {
	c := Corpus{Documents: []Document{{ID: "one", Tenant: "a", Text: "中文", Members: []string{"r"}, Published: true, Healthy: true}}, Queries: []Query{{ID: "q", Tenant: "a", Member: "r", Text: "中文", Relevant: []string{"one"}}}}
	for _, ids := range [][]string{{"unknown"}, {"one", "one"}, {"one", "one", "one"}} {
		backend := func(context.Context, []Document, Query, int, bool) ([]string, error) { return ids, nil }
		if _, err := Evaluate(t.Context(), c, 2, "bad", backend); err == nil {
			t.Fatal("invalid backend result accepted", ids)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Evaluate(ctx, c, 2, "bigram-v1", Bigram); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	report, err := Evaluate(t.Context(), c, 2, "bigram-v1", Bigram)
	if err != nil || math.IsNaN(report.MacroRecall) || report.MacroRecall != 1 {
		t.Fatal(report, err)
	}
}

func TestSyntheticChineseReportReproduces(t *testing.T) {
	raw, err := os.ReadFile("testdata/chinese-synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Corpus
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(t.Context(), c, 3, "synthetic-character-bigram-v1", Bigram)
	if err != nil || len(report.Queries) != 6 || report.CorpusSHA256 != "e2faae0967c9a217237a3b1fa41bce04f3bec3669802e657d3f3b22a7adaf7a6" || math.Abs(report.MacroRecall-37.0/60) > 1e-12 || report.MacroFilteredRecall == nil || math.Abs(*report.MacroFilteredRecall-.8) > 1e-12 || report.Unauthorized != 0 {
		t.Fatal(report, err)
	}
	if report.Queries[4].FilteredRecall != nil || len(report.Queries[4].Filtered) != 0 {
		t.Fatal("empty visibility fabricated recall")
	}
}

func TestBackendCannotRewriteJudgmentsOrACL(t *testing.T) {
	c := Corpus{Documents: []Document{{ID: "private", Tenant: "a", Text: "中文", Members: []string{"owner"}, Published: true, Healthy: true}}, Queries: []Query{{ID: "q", Tenant: "a", Member: "reader", Text: "中文", Relevant: []string{"private"}}}}
	malicious := func(_ context.Context, docs []Document, q Query, _ int, _ bool) ([]string, error) {
		if len(q.Relevant) != 0 {
			t.Fatal("backend received answer key")
		}
		docs[0].Members[0] = "reader"
		return []string{"private"}, nil
	}
	report, err := Evaluate(t.Context(), c, 1, "mutating-negative-control", malicious)
	if err != nil || report.Unauthorized != 1 || report.MacroFilteredRecall != nil || c.Documents[0].Members[0] != "owner" {
		t.Fatal("backend rewrote evaluation truth", report, err)
	}
}

func TestPostFilteringExposesRecallLoss(t *testing.T) {
	c := Corpus{Documents: []Document{
		{ID: "private", Tenant: "a", Text: "设备维修", Members: []string{"owner"}, Published: true, Healthy: true},
		{ID: "public", Tenant: "a", Text: "设备维修流程", Members: []string{"reader"}, Published: true, Healthy: true},
	}, Queries: []Query{{ID: "q", Tenant: "a", Member: "reader", Text: "设备维修", Relevant: []string{"private", "public"}}}}
	post := func(ctx context.Context, docs []Document, q Query, k int, filtered bool) ([]string, error) {
		hits, err := Bigram(ctx, docs, q, k, false)
		if err != nil || !filtered {
			return hits, err
		}
		out := []string{}
		for _, id := range hits {
			for _, d := range docs {
				if d.ID == id && Visible(d, q) {
					out = append(out, id)
				}
			}
		}
		return out, nil
	}
	report, err := Evaluate(t.Context(), c, 1, "postfilter-negative-control", post)
	if err != nil || report.Unauthorized != 0 || report.MacroFilteredRecall == nil || *report.MacroFilteredRecall != 0 {
		t.Fatal(report, err)
	}
}
