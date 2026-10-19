package operations

import (
	"agentos.local/core/internal/knowledge"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

func (a *API) knowledgeOperation(ctx context.Context, r *http.Request, w http.ResponseWriter) (any, error) {
	decode := func(v any) error {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024))
		d.DisallowUnknownFields()
		if r.URL.RawQuery != "" || d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
			return knowledge.ErrInvalid
		}
		return nil
	}
	switch r.URL.Path {
	case "/api/ops/knowledge/content":
		var in knowledge.Content
		if err := decode(&in); err != nil {
			return nil, err
		}
		if err := a.knowledge.PutContent(ctx, a.identity, in); err != nil {
			return nil, err
		}
	case "/api/ops/knowledge/acl":
		var in knowledge.ACL
		if err := decode(&in); err != nil {
			return nil, err
		}
		if err := a.knowledge.PutACL(ctx, a.identity, in); err != nil {
			return nil, err
		}
	case "/api/ops/knowledge/health":
		var in knowledge.HealthUpdate
		if err := decode(&in); err != nil {
			return nil, err
		}
		return a.knowledge.UpdateHealth(ctx, a.identity, in)
	case "/api/ops/knowledge/read":
		var in knowledge.Reference
		if err := decode(&in); err != nil {
			return nil, err
		}
		return a.knowledge.Read(ctx, a.identity, in)
	default:
		return nil, knowledge.ErrInvalid
	}
	return struct {
		Accepted bool `json:"accepted"`
	}{true}, nil
}
