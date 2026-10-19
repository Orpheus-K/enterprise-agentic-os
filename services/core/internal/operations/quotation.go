package operations

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/quotation"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type quotationDocument struct{ text, filename string }

func (a *API) quotationDocument(ctx context.Context, r *http.Request) (quotationDocument, error) {
	query, err := parseAudit(r)
	if err != nil || len(r.URL.Query()) != 1 {
		return quotationDocument{}, conversation.ErrInvalidApproval
	}
	approval, err := a.store.InspectApproval(ctx, a.identity, query.task)
	if err != nil {
		return quotationDocument{}, err
	}
	if approval.QuoteVersion == 0 {
		return quotationDocument{}, conversation.ErrTaskNotFound
	}
	var draft quotation.Draft
	if json.Unmarshal([]byte(approval.Response), &draft) != nil || draft.Version != approval.QuoteVersion || draft.ID != approval.TaskID {
		return quotationDocument{}, errors.New("invalid saved quotation")
	}
	text, err := quotation.RenderText(draft)
	if err != nil {
		return quotationDocument{}, err
	}
	return quotationDocument{text, fmt.Sprintf("quote-%s-v%d.txt", draft.ID, draft.Version)}, nil
}
